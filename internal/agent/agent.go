// Package agent is `moin agent`: the process on a server that dials out to the
// control plane and holds one WebSocket session to it.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// DefaultStateDir is where a root agent keeps its state. MOIN_STATE_DIR
// overrides it, for example when an agent runs without root in development.
const DefaultStateDir = "/var/lib/" + build.Name

const (
	stateFile     = "agent.json"
	minReconnect  = time.Second
	maxReconnect  = 30 * time.Second
	registerLimit = 10 * time.Second
)

// ErrNotRegistered means the agent has no state yet and needs `moin agent register`.
var ErrNotRegistered = errors.New("agent is not registered: run `" + build.Name + " agent register --url <control plane> --token <install token>`")

// State is what the agent keeps between restarts.
type State struct {
	URL        string `json:"url"`
	ServerID   string `json:"server_id"`
	AgentToken string `json:"agent_token"`
}

// StateDir returns MOIN_STATE_DIR or DefaultStateDir.
func StateDir() string {
	if dir := build.Getenv("STATE_DIR"); dir != "" {
		return dir
	}
	return DefaultStateDir
}

// LoadState reads the state from dir.
func LoadState(dir string) (State, error) {
	body, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotRegistered
	}
	if err != nil {
		return State{}, err
	}
	var state State
	err = json.Unmarshal(body, &state)
	if err != nil {
		return State{}, fmt.Errorf("read %s: %w", stateFile, err)
	}
	if state.URL == "" || state.AgentToken == "" {
		return State{}, ErrNotRegistered
	}
	return state, nil
}

func saveState(dir string, state State) error {
	err := os.MkdirAll(dir, 0o700)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stateFile), body, 0o600)
}

// Hello describes this agent and server, with its rules.
func Hello() protocol.Hello {
	hostname, _ := os.Hostname()
	rules, _ := LoadRules(RulesFile())
	return protocol.Hello{
		Rules:           rules,
		ProtocolVersion: protocol.Version,
		AgentVersion:    build.Version,
		Hostname:        hostname,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Addresses:       publicAddresses(),
	}
}

// Register trades a one-time install token for an agent token and saves it
// in dir. Running it again with a new install token moves the agent to that
// server.
func Register(ctx context.Context, dir, baseURL, installToken string) (State, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	ctx, cancel := context.WithTimeout(ctx, registerLimit)
	defer cancel()

	body, err := json.Marshal(protocol.RegisterRequest{Hello: Hello()})
	if err != nil {
		return State{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+protocol.RegisterPath, bytes.NewReader(body))
	if err != nil {
		return State{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+installToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return State{}, fmt.Errorf("register: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return State{}, fmt.Errorf("register: %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var registered protocol.RegisterResponse
	err = json.NewDecoder(response.Body).Decode(&registered)
	if err != nil {
		return State{}, fmt.Errorf("register: %w", err)
	}
	state := State{URL: baseURL, ServerID: registered.ServerID, AgentToken: registered.AgentToken}
	err = saveState(dir, state)
	if err != nil {
		return State{}, err
	}
	// The firewall and tunnel of an earlier server entry must not outlive it.
	resetCtx, resetCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer resetCancel()
	resetNetwork(resetCtx, dir)
	return state, nil
}

// Run holds the session until ctx ends and reconnects when it drops. A
// rejected agent token ends Run, because retrying cannot fix it. Deploys
// keep running across reconnects.
func Run(ctx context.Context, dir string, state State) error {
	rulesFile := RulesFile()
	if _, err := LoadRules(rulesFile); err != nil {
		return err
	}
	agent := &runner{dir: dir, state: state, rulesFile: rulesFile, pending: map[string]protocol.DeployDone{}}
	agent.apps = newApps(dir, agent.send)
	agent.backups = newBackups(agent.apps, agent.send)
	agent.apps.backups = agent.backups
	agent.security = newSecurity(dir, agent.apps)
	agent.apps.proxyMode = agent.security.proxyMode
	agent.updater = newUpdater(state.URL)
	// A restore the agent stopped in the middle of ends first: the app
	// runs again, on the data before or the restored data, both whole.
	agent.backups.recoverRestores(ctx)
	// On a stop, a deploy or a restore that runs gets a moment to end
	// before backups stop; one that does not is ended by the next start.
	defer func() {
		agent.waitIdle(stopWait)
		agent.backups.configure(context.Background(), nil, nil)
	}()
	go agent.backups.recordLoop(ctx)
	go agent.backups.checks(ctx)
	go agent.backups.health(ctx)
	go agent.apps.cleanups(ctx)
	go agent.backups.sweepLoop(ctx)
	go agent.metrics(ctx)
	wait := minReconnect
	for {
		connected, err := agent.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRejected) {
			return err
		}
		if connected {
			wait = minReconnect
		}
		slog.Warn("session ended, reconnecting", "error", err, "in", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, maxReconnect)
	}
}

var errRejected = errors.New("the control plane rejected the agent token: register this server again")

// runner is one agent process. conn is the current session, nil between
// sessions.
type runner struct {
	dir string
	// state is read and changed under mu: agent.url moves it.
	state State
	// rulesFile is read for every request, so a change to it counts at once.
	rulesFile string
	apps      *apps
	backups   *backups
	security  *security
	updater   *updater
	// work counts the deploys and restores that run now, and updating is
	// set once an update starts: an update waits for the work, and no
	// work starts while the agent replaces itself. See beginWork.
	workMu   sync.Mutex
	work     int
	updating bool

	mu   sync.Mutex
	conn *websocket.Conn
	// pending holds the deploy.done messages no session took yet. They go
	// out on the next one, so a deploy never ends unseen.
	pending map[string]protocol.DeployDone
}

// send writes a message to the current session. Log lines without a session
// are dropped; a deploy.done waits for the next session.
func (r *runner) send(messageType protocol.MessageType, requestID string, data any) {
	message, err := protocol.NewMessage(messageType, data)
	if err != nil {
		slog.Error("encode message failed", "type", messageType, "error", err)
		return
	}
	message.RequestID = requestID
	r.mu.Lock()
	conn := r.conn
	if done, ok := data.(protocol.DeployDone); ok {
		r.pending[done.DeployID] = done
	}
	r.mu.Unlock()
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = wsjson.Write(ctx, conn, message)
	if err != nil {
		return
	}
	if done, ok := data.(protocol.DeployDone); ok {
		r.mu.Lock()
		delete(r.pending, done.DeployID)
		r.mu.Unlock()
	}
}

// session holds one WebSocket session. connected reports whether the dial
// succeeded, so Run can reset its backoff.
func (r *runner) session(ctx context.Context) (connected bool, err error) {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	sessionURL, err := websocketURL(state.URL + protocol.SessionPath)
	if err != nil {
		return false, err
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+state.AgentToken)
	conn, response, err := websocket.Dial(ctx, sessionURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if response != nil && response.StatusCode == http.StatusUnauthorized {
			return false, errRejected
		}
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	hello, err := protocol.NewMessage(protocol.TypeHello, Hello())
	if err != nil {
		return true, err
	}
	err = wsjson.Write(ctx, conn, hello)
	if err != nil {
		return true, err
	}
	slog.Info("connected", "url", state.URL, "server_id", state.ServerID)

	r.mu.Lock()
	r.conn = conn
	pending := make([]protocol.DeployDone, 0, len(r.pending))
	for _, done := range r.pending {
		pending = append(pending, done)
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.conn == conn {
			r.conn = nil
		}
		r.mu.Unlock()
	}()
	for _, done := range pending {
		r.send(protocol.TypeDeployDone, "", done)
	}

	// Reading also answers the control plane's pings.
	for {
		var message protocol.Message
		err = wsjson.Read(ctx, conn, &message)
		if err != nil {
			return true, err
		}
		go r.handle(ctx, message)
	}
}

// handle runs one request from the control plane.
func (r *runner) handle(ctx context.Context, message protocol.Message) {
	// A rules file with a mistake refuses everything until it is fixed.
	rules, err := LoadRules(r.rulesFile)
	if err != nil {
		slog.Error("rules", "error", err)
		if message.Type == protocol.TypeDeployStart {
			var start protocol.DeployStart
			_ = message.Decode(&start)
			r.send(protocol.TypeDeployDone, "", protocol.DeployDone{DeployID: start.DeployID, Error: err.Error()})
		} else if message.RequestID != "" {
			r.fail(message, err)
		}
		return
	}
	switch message.Type {
	case protocol.TypeDeployStart:
		r.deployStart(ctx, message, rules)
	case protocol.TypeLogs:
		r.sendLogs(ctx, message, rules)
	case protocol.TypeAppRemove:
		r.removeApp(ctx, message)
	case protocol.TypeAppStop:
		r.stopApp(ctx, message)
	case protocol.TypeDBTables:
		r.dbTables(ctx, message, rules)
	case protocol.TypeDBQuery:
		r.dbQuery(ctx, message, rules)
	case protocol.TypeServerConfig:
		r.serverConfig(ctx, message, rules)
	case protocol.TypeBackupConfig:
		r.backupConfig(ctx, message, rules)
	case protocol.TypeBackupList:
		r.backupList(ctx, message)
	case protocol.TypeBackupRestore:
		r.backupRestore(ctx, message)
	case protocol.TypeBackupRestores:
		r.backupRestores(message)
	case protocol.TypeBackupDownload:
		r.backupDownload(ctx, message)
	case protocol.TypeExecStart:
		r.execStart(ctx, message, rules)
	case protocol.TypeAgentURL:
		r.agentURL(ctx, message)
	case protocol.TypeAgentUpdate:
		r.agentUpdate(ctx, message, rules)
	default:
		if message.RequestID != "" {
			r.fail(message, fmt.Errorf("this agent does not know %q: update it", message.Type))
		}
	}
}

// fail answers a request with the error that stopped it.
func (r *runner) fail(message protocol.Message, err error) {
	r.send(protocol.TypeError, message.RequestID, protocol.Error{Message: err.Error()})
}

// url is the control plane's address.
func (r *runner) url() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.URL
}

// agentURL moves to the control plane's new address (AgentURL). It has no
// answer.
func (r *runner) agentURL(ctx context.Context, message protocol.Message) {
	var moved protocol.AgentURL
	if err := message.Decode(&moved); err != nil {
		slog.Error("bad agent.url", "error", err)
		return
	}
	r.move(ctx, moved.URL)
}

// moveCheck is how long the new address has to answer.
const moveCheck = 10 * time.Second

// move dials the control plane at a new address from now on, when the
// control plane says the one this agent dialed is old. It only moves when
// the new address answers, never from https to http, and keeps it in the
// state, so it survives a restart. The session ends, and the next one
// dials the new address.
func (r *runner) move(ctx context.Context, to string) {
	to = strings.TrimRight(to, "/")
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	if to == "" || to == state.URL {
		return
	}
	parsed, err := url.Parse(to)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && (parsed.Scheme != "http" || strings.HasPrefix(state.URL, "https://"))) {
		slog.Warn("not moving to the control plane's new address: it is not a valid https address", "url", to)
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, moveCheck)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, to+"/healthz", nil)
	if err != nil {
		return
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		slog.Warn("not moving to the control plane's new address yet: it does not answer", "url", to, "error", err)
		return
	}
	response.Body.Close()
	if response.StatusCode/100 != 2 {
		slog.Warn("not moving to the control plane's new address yet: it does not answer", "url", to, "status", response.Status)
		return
	}
	state.URL = to
	if err := saveState(r.dir, state); err != nil {
		slog.Error("keep the control plane's new address", "error", err)
		return
	}
	r.mu.Lock()
	r.state = state
	conn := r.conn
	r.mu.Unlock()
	r.updater.setURL(to)
	slog.Info("the control plane has a new address: moving", "url", to)
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "moving to "+to)
	}
}

func websocketURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("control plane URL must start with http:// or https://, got %q", raw)
	}
	return parsed.String(), nil
}

// stopWait is how long a stopping agent waits for the deploys and restores
// that run, within systemd's default stop timeout of 90 seconds. A
// restore changes no data in place, so one cut off is harmless.
const stopWait = time.Minute

// beginWork starts a deploy or a restore, unless the agent is updating
// itself. endWork ends it.
func (r *runner) beginWork() bool {
	r.workMu.Lock()
	defer r.workMu.Unlock()
	if r.updating {
		return false
	}
	r.work++
	return true
}

func (r *runner) endWork() {
	r.workMu.Lock()
	defer r.workMu.Unlock()
	r.work--
}

// beginUpdate starts an update when no deploy or restore runs; from then
// on none starts. endUpdate ends an update that failed.
func (r *runner) beginUpdate() bool {
	r.workMu.Lock()
	defer r.workMu.Unlock()
	if r.work > 0 || r.updating {
		return false
	}
	r.updating = true
	return true
}

func (r *runner) endUpdate() {
	r.workMu.Lock()
	defer r.workMu.Unlock()
	r.updating = false
}

// waitIdle waits until no deploy or restore runs, at most limit.
func (r *runner) waitIdle(limit time.Duration) {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(time.Second) {
		r.workMu.Lock()
		work := r.work
		r.workMu.Unlock()
		if work == 0 {
			return
		}
		slog.Info("waiting for a deploy or a restore to end before stopping", "running", work)
	}
}
