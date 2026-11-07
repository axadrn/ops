package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/axadrn/moin/controlplane/internal/access"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
	"github.com/google/uuid"
)

const (
	installTokenMarker = "ins_"
	agentTokenMarker   = "agt_"
	// InstallTokenLifetime is how long an install command works.
	InstallTokenLifetime = time.Hour
)

var (
	ErrServerName        = errors.New("server name must be 1 to 63 lowercase letters, digits or dashes, starting with a letter or digit")
	ErrInstallTokenValid = errors.New("install token is invalid, used or expired")
	ErrAgentToken        = errors.New("agent token is invalid")
	ErrServerOffline     = errors.New("server is offline")
)

// AgentError is a request the agent answered with an error.
type AgentError struct{ Message string }

func (e *AgentError) Error() string { return "agent: " + e.Message }

// ServerService manages servers and the live sessions of their agents. The
// sessions live in memory: there is one control plane process.
type ServerService struct {
	activity *repository.Activity
	servers  *repository.Servers
	appURL   string
	now      func() time.Time
	// downloads holds the binaries agents update to.
	downloads string

	mu          sync.Mutex
	sessions    map[string]*agentSession
	subscribers map[string]map[chan struct{}]struct{}
	onMessage   func(serverID string, message protocol.Message)
	onConnect   func(serverID, workspaceID string)
	accessOf    func(workspaceID string) (access.Access, error)
	// autoVersion is the version agents update to by themselves; "" is
	// off. autoRunning and autoHalted are per workspace.
	autoVersion string
	autoRunning map[string]bool
	autoHalted  map[string]bool
	// alerts mails the owners and admins when a site or a server goes down
	// or comes back, or a deploy fails; nil sends nothing.
	alerts *AlertService
	// diskFull caches per server whether its disk is over the alert line,
	// read from the database the first time a sample comes.
	diskFull map[string]bool
}

type agentSession struct {
	workspaceID string
	// protocol is the protocol version the agent speaks.
	protocol int
	kick     func()
	send     func(protocol.Message) error
	// waiting maps request IDs to the requests waiting for their answer.
	waiting map[string]chan protocol.Message
	// streams maps request IDs to the requests whose answer comes in
	// pieces, such as a command's output.
	streams map[string]chan protocol.Message
	// gone closes when the session ends, so a stream stops waiting.
	gone     chan struct{}
	goneOnce sync.Once
}

func NewServerService(servers *repository.Servers, activity *repository.Activity, appURL string) *ServerService {
	return &ServerService{
		activity:    activity,
		servers:     servers,
		appURL:      strings.TrimRight(appURL, "/"),
		downloads:   DownloadsDir,
		now:         time.Now,
		sessions:    map[string]*agentSession{},
		subscribers: map[string]map[chan struct{}]struct{}{},
		autoRunning: map[string]bool{},
		autoHalted:  map[string]bool{},
	}
}

// Create adds a server that waits for its agent and returns the raw install
// token, which works once within InstallTokenLifetime.
func (s *ServerService) Create(actor Actor, name string) (*model.Server, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = s.freeName(actor.WorkspaceID)
	}
	if !validServerName(name) {
		return nil, "", ErrServerName
	}
	err := s.allowed(actor.WorkspaceID)
	if err != nil {
		return nil, "", err
	}
	raw := installTokenMarker + randomToken()
	hash := hashToken(raw)
	now := s.now().UTC()
	expiresAt := now.Add(InstallTokenLifetime)
	server := &model.Server{
		ID:               uuid.New().String(),
		WorkspaceID:      actor.WorkspaceID,
		Name:             name,
		InstallTokenHash: &hash,
		InstallExpiresAt: &expiresAt,
		CreatedAt:        now,
	}
	err = s.servers.Create(server)
	if err != nil {
		return nil, "", err
	}
	s.record(actor, "server.added", "server", name, nil)
	s.notify(actor.WorkspaceID)
	return server, raw, nil
}

// InstallCommand is what people run on the server.
func (s *ServerService) InstallCommand(installToken string) string {
	return fmt.Sprintf("curl -fsSL %s/install.sh | sh -s -- %s", s.appURL, installToken)
}

// validServerName follows the rule of app names: lowercase letters, digits
// and dashes. Servers added before the rule keep their names until renamed.
func validServerName(name string) bool { return protocol.ValidAppName(name) }

// Rename gives a server a new name under the rules of Create. Apps follow,
// since they point at the server, not its name.
func (s *ServerService) Rename(actor Actor, idOrName, name string) (*model.Server, error) {
	name = strings.TrimSpace(name)
	server, err := s.servers.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if server.Name == name {
		return server, nil
	}
	if !validServerName(name) {
		return nil, ErrServerName
	}
	err = s.servers.Rename(server.ID, actor.WorkspaceID, name)
	if err != nil {
		return nil, err
	}
	s.record(actor, "server.renamed", "server", name, map[string]string{"from": server.Name, "to": name})
	s.notify(actor.WorkspaceID)
	server.Name = name
	server.Online = s.Online(server.ID)
	return server, nil
}

// NewInstallToken gives a server a new install token, for a lost install
// command or to move the agent to another machine. The old token stops
// working; the agent that registers with the new one takes over and the
// old agent's token stops working. It returns the raw token, which works
// once within InstallTokenLifetime.
func (s *ServerService) NewInstallToken(actor Actor, idOrName string) (*model.Server, string, error) {
	server, err := s.servers.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return nil, "", err
	}
	raw := installTokenMarker + randomToken()
	expiresAt := s.now().UTC().Add(InstallTokenLifetime)
	err = s.servers.SetInstallToken(server.ID, actor.WorkspaceID, hashToken(raw), expiresAt)
	if err != nil {
		return nil, "", err
	}
	s.record(actor, "server.install_command", "server", server.Name, nil)
	server.InstallExpiresAt = &expiresAt
	server.Online = s.Online(server.ID)
	return server, raw, nil
}

// UninstallCommand removes the agent from a server, run on it after the
// server is removed here.
func (s *ServerService) UninstallCommand() string {
	return protocol.UninstallCommand(build.Name)
}

// List returns the servers of a user with their live status.
func (s *ServerService) List(workspaceID string) ([]model.Server, error) {
	servers, err := s.servers.ListByWorkspaceID(workspaceID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range servers {
		_, servers[index].Online = s.sessions[servers[index].ID]
	}
	return servers, nil
}

// Delete removes a server by ID or name and disconnects its agent. The agent
// token stops working. It returns the command that removes the agent on the
// machine, empty when no agent ever connected, so none is installed there,
// and the apps that went with an offline server.
func (s *ServerService) Delete(actor Actor, idOrName string) (uninstall string, apps []string, err error) {
	server, err := s.servers.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return "", nil, err
	}
	// An online server keeps its apps until they are removed there, so
	// nothing runs on unseen. An offline one can't remove them, so they go
	// with it; they stay on the machine if it comes back.
	if s.Online(server.ID) {
		err = s.servers.DeleteForWorkspace(server.ID, actor.WorkspaceID)
	} else {
		apps, err = s.servers.DeleteWithApps(server.ID, actor.WorkspaceID)
	}
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	session := s.sessions[server.ID]
	s.mu.Unlock()
	if session != nil {
		session.kick()
	}
	for _, app := range apps {
		s.record(actor, "app.deleted", "app", app, map[string]string{"server": server.Name, "with": "server"})
	}
	s.record(actor, "server.removed", "server", server.Name, nil)
	s.notify(actor.WorkspaceID)
	if server.LastSeenAt == nil {
		return "", apps, nil
	}
	return s.UninstallCommand(), apps, nil
}

// Register trades a raw install token for an agent token. The install token
// stops working.
func (s *ServerService) Register(installToken string, hello protocol.Hello) (*model.Server, string, error) {
	if !strings.HasPrefix(installToken, installTokenMarker) {
		return nil, "", ErrInstallTokenValid
	}
	server, err := s.servers.ByInstallTokenHash(hashToken(installToken))
	if errors.Is(err, repository.ErrServerNotFound) {
		return nil, "", ErrInstallTokenValid
	}
	if err != nil {
		return nil, "", err
	}
	if server.InstallExpiresAt == nil || !s.now().Before(*server.InstallExpiresAt) {
		return nil, "", ErrInstallTokenValid
	}
	raw := agentTokenMarker + randomToken()
	reported := helloServer(hello, nil)
	err = s.servers.Register(server.ID, hashToken(raw), reported)
	if err != nil {
		return nil, "", err
	}
	// An agent that registers again, or on another machine, takes over.
	s.mu.Lock()
	session := s.sessions[server.ID]
	s.mu.Unlock()
	if session != nil {
		session.kick()
	}
	slog.Info("agent registered", "server_id", server.ID, "workspace_id", server.WorkspaceID)
	action := "server.installed"
	if server.AgentTokenHash != nil {
		action = "server.reinstalled"
	}
	s.record(System(server.WorkspaceID), action, "server", server.Name, map[string]string{"hostname": reported.Hostname, "os": reported.OS, "arch": reported.Arch, "version": reported.AgentVersion})
	s.notify(server.WorkspaceID)
	return server, raw, nil
}

// Authenticate returns the server of a raw agent token.
func (s *ServerService) Authenticate(agentToken string) (*model.Server, error) {
	if !strings.HasPrefix(agentToken, agentTokenMarker) {
		return nil, ErrAgentToken
	}
	server, err := s.servers.ByAgentTokenHash(hashToken(agentToken))
	if errors.Is(err, repository.ErrServerNotFound) {
		return nil, ErrAgentToken
	}
	return server, err
}

// Connect marks the server online and remembers the address the agent
// connected from, which the DNS check compares domains with. kick ends the session; Connect calls it
// for an older session of the same server, and Delete calls it too. send
// writes one message to the agent. The session calls the returned disconnect
// when it ends.
func (s *ServerService) Connect(server *model.Server, hello protocol.Hello, address string, kick func(), send func(protocol.Message) error) (disconnect func()) {
	now := s.now().UTC()
	update := helloServer(hello, &now)
	update.Address = clip(address)
	err := s.servers.UpdateHello(server.ID, update)
	if err != nil {
		slog.Error("store agent hello failed", "error", err, "server_id", server.ID)
	}
	session := &agentSession{workspaceID: server.WorkspaceID, protocol: hello.ProtocolVersion, kick: kick, send: send, waiting: map[string]chan protocol.Message{}, streams: map[string]chan protocol.Message{}, gone: make(chan struct{})}
	s.mu.Lock()
	previous := s.sessions[server.ID]
	s.sessions[server.ID] = session
	s.mu.Unlock()
	if previous != nil {
		// Its requests get no answer on the new session: they end now.
		previous.goneOnce.Do(func() { close(previous.gone) })
		previous.kick()
	}
	s.notify(server.WorkspaceID)
	s.mu.Lock()
	onConnect := s.onConnect
	s.mu.Unlock()
	if onConnect != nil {
		go onConnect(server.ID, server.WorkspaceID)
	}
	go s.autoUpdate(server.WorkspaceID)

	return func() {
		session.goneOnce.Do(func() { close(session.gone) })
		s.mu.Lock()
		current := s.sessions[server.ID] == session
		if current {
			delete(s.sessions, server.ID)
		}
		s.mu.Unlock()
		if !current {
			return
		}
		err := s.servers.TouchLastSeen(server.ID, s.now().UTC())
		if err != nil {
			slog.Error("store last seen failed", "error", err, "server_id", server.ID)
		}
		s.notify(server.WorkspaceID)
	}
}

// ErrAgentGone is a request whose agent went offline before it answered.
// What it did may still finish on the server.
var ErrAgentGone = errors.New("the server went offline before it answered; what it did may still finish there")

// ErrAgentOutdated answers a request the agent of a server is too old
// for, instead of sending it.
var ErrAgentOutdated = errors.New("the agent on this server is too old for this: update it under Servers, or with moin servers update")

// ErrSubscriptionRequired means the account needs the moin plan to add a
// server.
var ErrSubscriptionRequired = errors.New("subscribe to moin to add servers")

// ErrTrialEnded means the workspace has no plan with changes, such as after
// its trial: it is read-only until it subscribes.
var ErrTrialEnded = errors.New("the trial has ended: subscribe to make changes")

// RequireChanges says ErrTrialEnded when the workspace's plan allows no
// changes.
func (s *ServerService) RequireChanges(workspaceID string) error {
	if s.accessOf == nil {
		return nil
	}
	plan, err := s.accessOf(workspaceID)
	if err != nil {
		return err
	}
	if !plan.Has(access.FeatureChanges) {
		return ErrTrialEnded
	}
	return nil
}

// RequirePlanFor makes adding a server check the account's plan. Wiring
// sets it; without it every account may add servers.
func (s *ServerService) RequirePlanFor(accessOf func(workspaceID string) (access.Access, error)) {
	s.accessOf = accessOf
}

// CanAdd says whether the user's plan allows another server.
func (s *ServerService) CanAdd(workspaceID string) bool {
	return s.allowed(workspaceID) == nil
}

func (s *ServerService) allowed(workspaceID string) error {
	if s.accessOf == nil {
		return nil
	}
	plan, err := s.accessOf(workspaceID)
	if err != nil {
		return err
	}
	servers, err := s.servers.ListByWorkspaceID(workspaceID)
	if err != nil {
		return err
	}
	if plan.RequireWithin(access.LimitServers, len(servers)) != nil {
		return ErrSubscriptionRequired
	}
	return nil
}

// Get returns one of the user's servers by ID or name.
func (s *ServerService) Get(workspaceID, idOrName string) (*model.Server, error) {
	server, err := s.servers.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	server.Online = s.Online(server.ID)
	return server, nil
}

// UserOf returns the user of a connected server.
func (s *ServerService) UserOf(serverID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[serverID]
	if session == nil {
		return "", false
	}
	return session.workspaceID, true
}

// OnAgentConnect sets who learns that an agent connected, to send it what
// it needs to know. Wiring calls it once.
func (s *ServerService) OnAgentConnect(handle func(serverID, workspaceID string)) {
	s.mu.Lock()
	s.onConnect = handle
	s.mu.Unlock()
}

// OnAgentMessage sets who gets the agent messages that answer no request,
// such as deploy logs. Wiring calls it once.
func (s *ServerService) OnAgentMessage(handle func(serverID string, message protocol.Message)) {
	s.mu.Lock()
	s.onMessage = handle
	s.mu.Unlock()
}

// Online reports whether the server holds a session.
func (s *ServerService) Online(serverID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[serverID] != nil
}

// Send writes one message to the agent of a server.
func (s *ServerService) Send(serverID string, messageType protocol.MessageType, data any) error {
	message, err := protocol.NewMessage(messageType, data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	session := s.sessions[serverID]
	s.mu.Unlock()
	if session == nil {
		return ErrServerOffline
	}
	return session.send(message)
}

// Request sends a message to the agent and decodes its answer into result,
// which may be nil. An error answer returns an *AgentError.
func (s *ServerService) Request(ctx context.Context, serverID string, messageType protocol.MessageType, data, result any) error {
	message, err := protocol.NewMessage(messageType, data)
	if err != nil {
		return err
	}
	message.RequestID = randomToken()[:20]
	answer := make(chan protocol.Message, 1)
	s.mu.Lock()
	session := s.sessions[serverID]
	old := session != nil && session.protocol < protocol.Needs(messageType)
	if session != nil && !old {
		session.waiting[message.RequestID] = answer
	}
	s.mu.Unlock()
	if session == nil {
		return ErrServerOffline
	}
	if old {
		return ErrAgentOutdated
	}
	defer func() {
		s.mu.Lock()
		delete(session.waiting, message.RequestID)
		s.mu.Unlock()
	}()
	err = session.send(message)
	if err != nil {
		return ErrServerOffline
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("the agent did not answer: %w", ctx.Err())
	case <-session.gone:
		return ErrAgentGone
	case reply := <-answer:
		if reply.Type == protocol.TypeError {
			var agentErr protocol.Error
			_ = reply.Decode(&agentErr)
			return &AgentError{Message: agentErr.Message}
		}
		if result == nil {
			return nil
		}
		return reply.Decode(result)
	}
}

// Stream sends a request whose answer comes in pieces: every message the
// agent sends with the request's ID goes to receive, until receive says
// done, the context ends or the agent goes. An error answer returns an
// *AgentError.
func (s *ServerService) Stream(ctx context.Context, serverID string, messageType protocol.MessageType, data any, receive func(protocol.Message) (done bool, err error)) error {
	message, err := protocol.NewMessage(messageType, data)
	if err != nil {
		return err
	}
	message.RequestID = randomToken()[:20]
	pieces := make(chan protocol.Message, 256)
	s.mu.Lock()
	session := s.sessions[serverID]
	if session != nil {
		session.streams[message.RequestID] = pieces
	}
	s.mu.Unlock()
	if session == nil {
		return ErrServerOffline
	}
	defer func() {
		s.mu.Lock()
		delete(session.streams, message.RequestID)
		s.mu.Unlock()
	}()
	if err := session.send(message); err != nil {
		return ErrServerOffline
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("the agent did not finish: %w", ctx.Err())
		case <-session.gone:
			return ErrServerOffline
		case reply := <-pieces:
			if reply.Type == protocol.TypeError {
				var agentErr protocol.Error
				_ = reply.Decode(&agentErr)
				return &AgentError{Message: agentErr.Message}
			}
			done, err := receive(reply)
			if err != nil || done {
				return err
			}
		}
	}
}

// Receive takes one message the agent of a server sent.
func (s *ServerService) Receive(serverID string, message protocol.Message) {
	s.mu.Lock()
	var answer, stream chan protocol.Message
	var gone chan struct{}
	if session := s.sessions[serverID]; session != nil && message.RequestID != "" {
		answer = session.waiting[message.RequestID]
		stream = session.streams[message.RequestID]
		gone = session.gone
	}
	handle := s.onMessage
	s.mu.Unlock()
	if stream != nil {
		// A piece of a stream is never dropped while its reader keeps up;
		// one that stopped reading for long loses the rest.
		select {
		case stream <- message:
		case <-gone:
		case <-time.After(10 * time.Second):
		}
		return
	}
	if answer != nil {
		select {
		case answer <- message:
		default:
		}
		return
	}
	if handle != nil && message.RequestID == "" {
		handle(serverID, message)
	}
}

// Subscribe returns a channel that receives a value whenever a server of the
// user changes. Changes that arrive while one is pending fold into it.
func (s *ServerService) Subscribe(workspaceID string) (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	s.mu.Lock()
	if s.subscribers[workspaceID] == nil {
		s.subscribers[workspaceID] = map[chan struct{}]struct{}{}
	}
	s.subscribers[workspaceID][changes] = struct{}{}
	s.mu.Unlock()
	return changes, func() {
		s.mu.Lock()
		delete(s.subscribers[workspaceID], changes)
		if len(s.subscribers[workspaceID]) == 0 {
			delete(s.subscribers, workspaceID)
		}
		s.mu.Unlock()
	}
}

func (s *ServerService) notify(workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for changes := range s.subscribers[workspaceID] {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
}

func helloServer(hello protocol.Hello, seenAt *time.Time) model.Server {
	return model.Server{
		Hostname:     clip(hello.Hostname),
		OS:           clip(hello.OS),
		Arch:         clip(hello.Arch),
		AgentVersion: clip(hello.AgentVersion),
		Rules:        rulesJSON(hello.Rules),
		Addresses:    reportedAddresses(hello.Addresses),
		LastSeenAt:   seenAt,
	}
}

// reportedAddresses keeps the valid IP addresses an agent reports, at most
// four, space separated.
func reportedAddresses(addresses []string) string {
	var clean []string
	for _, text := range addresses {
		address, err := netip.ParseAddr(strings.TrimSpace(text))
		if err != nil || len(clean) == 4 {
			continue
		}
		clean = append(clean, address.Unmap().String())
	}
	return strings.Join(clean, " ")
}

// rulesJSON keeps what an agent reports about its rules short.
func rulesJSON(rules *protocol.Rules) string {
	if rules == nil {
		return ""
	}
	clipped := *rules
	clipped.BackupsTo = clip(rules.BackupsTo)
	clipped.DeployFrom = nil
	for _, source := range rules.DeployFrom {
		if len(clipped.DeployFrom) == 50 {
			break
		}
		clipped.DeployFrom = append(clipped.DeployFrom, clip(source))
	}
	body, _ := json.Marshal(clipped)
	return string(body)
}

// ServerRules are the rules the server's agent last reported, nil without.
func ServerRules(server model.Server) *protocol.Rules {
	if server.Rules == "" {
		return nil
	}
	var rules protocol.Rules
	if json.Unmarshal([]byte(server.Rules), &rules) != nil {
		return nil
	}
	return &rules
}

// clip keeps what an agent reports short.
// clip cleans a value an agent reports: at most 128 bytes and no control
// characters, so it cannot move the cursor or recolor a terminal that
// shows it in the CLI or the TUI.
func clip(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
	if len(value) > 128 {
		return strings.ToValidUTF8(value[:128], "")
	}
	return value
}

func randomToken() string {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return strings.ToLower(apiTokenEncoding.EncodeToString(secret))
}
