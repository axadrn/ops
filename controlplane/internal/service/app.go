package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/secret"
	"github.com/axadrn/moin/internal/dotenv"
	"github.com/axadrn/moin/protocol"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	// deployTimeout ends a deploy the agent never reported on.
	deployTimeout     = 30 * time.Minute
	maxDeployLogLines = 5000
	maxEnvVars        = 200
	maxEnvValueSize   = 32 << 10
)

var (
	ErrAppName        = errors.New("app name must be 1 to 63 lowercase letters, digits or dashes, starting with a letter or digit")
	ErrDeployRunning  = errors.New("a deploy of this app is still running")
	ErrNoRollback     = errors.New("there is no earlier successful deploy to roll back to")
	ErrDeployGone     = errors.New("that deploy is no longer on the server")
	ErrNoSource       = errors.New("no deploy has a source yet")
	ErrNotLive        = errors.New("the app has no live deploy yet")
	ErrRunning        = errors.New("the app is already running")
	ErrImage          = errors.New("image must be a reference like ghcr.io/me/app:tag")
	ErrDeployHook     = errors.New("deploy hook is invalid")
	ErrRegistryHost   = errors.New("registry host must be a host name like ghcr.io")
	ErrRegistryLogin  = errors.New("registry username and password are required")
	ErrDomain         = errors.New("domains must be host names like blog.de")
	ErrCanonical      = errors.New("the canonical domain must be one of the domains")
	ErrComposeDomains = errors.New("the domains of this app come from x-moin.domains in compose.yml")
	ErrProtect        = errors.New("protection needs a password of 8 to 72 characters")
	ErrProtectUser    = errors.New("protection needs a username without spaces or colons")
	ErrEnvKey         = errors.New("env keys are letters, digits and underscores and do not start with a digit")
	ErrEnvValue       = errors.New("env values are at most 32 KB")
	ErrEnvLimit       = errors.New("an app has at most 200 env vars")
)

const deployHookMarker = "dph_"

var (
	imagePattern        = regexp.MustCompile(`^[a-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
	registryHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?(:[0-9]{1,5})?$`)
	appNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	domainPattern       = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	envKeyPattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// AppService manages apps, their deploys and their env. Deploys run on the
// agent; their log lines live in memory until the deploy ends.
type AppService struct {
	apps       *repository.Apps
	servers    *ServerService
	box        *secret.Box
	github     *GitHub
	cloudflare *Cloudflare
	now        func() time.Time
	// publicBucketsOnly refuses buckets on private or local addresses.
	publicBucketsOnly bool
	// proxyOnFirstConnect sends a server its config the first time its
	// agent connects, which starts the proxy; see ProxyOnFirstConnect.
	proxyOnFirstConnect bool
	uptime              *uptimeChecker
	metrics             *metricsStore

	mu          sync.Mutex
	live        map[string]*liveDeploy
	subscribers map[string]map[chan struct{}]struct{}
	// downloads are the copies being made or made for a download, by ID;
	// updates the catalog version each running update deploy brings.
	downloads map[string]*Download
	updates   map[string]string
	// waiting are the restores this control plane waits for the agent's
	// answer to; restoreMu keeps reserving and ending restores apart.
	waiting   map[string]bool
	restoreMu sync.Mutex
}

// liveDeploy is a running deploy: its lines so far and who follows them.
type liveDeploy struct {
	workspaceID string
	serverID    string
	lines       []string
	followers   map[chan struct{}]struct{}
}

func NewAppService(apps *repository.Apps, servers *ServerService, box *secret.Box) *AppService {
	service := &AppService{
		apps:        apps,
		servers:     servers,
		box:         box,
		cloudflare:  NewCloudflare(""),
		now:         time.Now,
		live:        map[string]*liveDeploy{},
		uptime:      newUptimeChecker(),
		metrics:     newMetricsStore(),
		subscribers: map[string]map[chan struct{}]struct{}{},
	}
	servers.OnAgentMessage(service.receive)
	servers.OnAgentConnect(func(serverID, workspaceID string) {
		_, err := apps.DefaultBucket(workspaceID)
		if err == nil {
			service.configureAgent(serverID, workspaceID)
		}
		service.configureServerOnConnect(serverID, workspaceID)
		service.reconcileRestores(serverID)
	})
	return service
}

// Create adds an app on one of the user's servers.
func (s *AppService) Create(actor Actor, name, serverIDOrName string) (*model.App, error) {
	name = strings.TrimSpace(name)
	if !appNamePattern.MatchString(name) {
		return nil, ErrAppName
	}
	server, err := s.servers.servers.ByIDOrNameForWorkspace(strings.TrimSpace(serverIDOrName), actor.WorkspaceID)
	if err != nil {
		return nil, err
	}
	app := &model.App{ID: uuid.New().String(), WorkspaceID: actor.WorkspaceID, ServerID: server.ID, Name: name, CreatedAt: s.now().UTC()}
	err = s.apps.Create(app)
	if err != nil {
		return nil, err
	}
	s.servers.record(actor, "app.created", "app", name, map[string]string{"server": server.Name})
	s.notify(actor.WorkspaceID)
	return s.apps.ByID(app.ID)
}

// List returns the user's apps with their newest and live deploy.
func (s *AppService) List(workspaceID string) ([]model.App, error) {
	return s.apps.ListByWorkspaceID(workspaceID)
}

// Get returns one app by ID or name.
func (s *AppService) Get(workspaceID, idOrName string) (*model.App, error) {
	return s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
}

// Delete stops the app on its server, then forgets it. Its data stays on
// the server. An offline server keeps the app, because its containers would
// keep running unseen. A server that never connected holds nothing of it.
func (s *AppService) Delete(ctx context.Context, actor Actor, idOrName string) error {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return err
	}
	server, err := s.servers.Get(actor.WorkspaceID, app.ServerID)
	if err != nil {
		return err
	}
	if server.AgentTokenHash != nil {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		err = s.servers.Request(ctx, app.ServerID, protocol.TypeAppRemove, protocol.AppRemove{App: app.Name}, nil)
		if err != nil {
			return err
		}
	}
	err = s.apps.DeleteForWorkspace(app.ID, actor.WorkspaceID)
	if err != nil {
		return err
	}
	s.servers.record(actor, "app.deleted", "app", app.Name, map[string]string{"server": server.Name})
	s.notify(actor.WorkspaceID)
	return nil
}

// Deploys returns the newest deploys of an app, without logs.
func (s *AppService) Deploys(workspaceID, idOrName string, limit int) (*model.App, []model.Deploy, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	deploys, err := s.apps.ListDeploys(app.ID, min(max(limit, 1), 100))
	if err != nil {
		return nil, nil, err
	}
	for index := range deploys {
		s.expire(&deploys[index])
	}
	app.ChangesWaiting, err = s.ChangesWaiting(workspaceID, app)
	if err != nil {
		return nil, nil, err
	}
	return app, deploys, nil
}

// Deploy deploys an image built elsewhere, such as in CI, as the main
// service; the server keeps the compose file of the live deploy.
func (s *AppService) Deploy(actor Actor, idOrName, image string) (*model.Deploy, error) {
	if !imagePattern.MatchString(image) {
		return nil, ErrImage
	}
	app, err := s.ready(actor.WorkspaceID, idOrName)
	if err != nil {
		return nil, err
	}
	deploy := s.newDeploy(app)
	deploy.Image = image
	return s.start(actor, "app.deployed", app, deploy, protocol.DeployStart{Image: image})
}

// newDeploy is a running deploy of app, not stored yet.
func (s *AppService) newDeploy(app *model.App) *model.Deploy {
	return &model.Deploy{ID: uuid.New().String(), AppID: app.ID, Status: model.DeployRunning, CreatedAt: s.now().UTC()}
}

// DeployHook deploys an image for the app whose deploy hook token this is.
// CI calls it after pushing the image.
func (s *AppService) DeployHook(token, image string) (*model.App, *model.Deploy, error) {
	if !strings.HasPrefix(token, deployHookMarker) {
		return nil, nil, ErrDeployHook
	}
	app, err := s.apps.ByDeployHookHash(hashToken(token))
	if errors.Is(err, repository.ErrAppNotFound) {
		return nil, nil, ErrDeployHook
	}
	if err != nil {
		return nil, nil, err
	}
	err = s.servers.RequireChanges(app.WorkspaceID)
	if err != nil {
		return app, nil, err
	}
	deploy, err := s.Deploy(Actor{WorkspaceID: app.WorkspaceID, Source: Source{Via: ViaHook}}, app.ID, image)
	return app, deploy, err
}

// RotateDeployHook gives the app a new deploy hook URL, shown once. The old
// one stops working.
func (s *AppService) RotateDeployHook(actor Actor, idOrName string) (string, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return "", err
	}
	token := deployHookMarker + randomToken()
	err = s.apps.SetDeployHookHash(app.ID, hashToken(token))
	if err != nil {
		return "", err
	}
	s.servers.record(actor, "app.hook_rotated", "app", app.Name, nil)
	return s.servers.appURL + "/webhooks/deploy/" + token, nil
}

// RemoveDeployHook turns the app's deploy hook off: its URL stops working
// and CI can no longer deploy. It reports false when there was none.
func (s *AppService) RemoveDeployHook(actor Actor, idOrName string) (bool, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return false, err
	}
	if app.DeployHookHash == nil {
		return false, nil
	}
	err = s.apps.RemoveDeployHook(app.ID)
	if err != nil {
		return false, err
	}
	s.servers.record(actor, "app.hook_removed", "app", app.Name, nil)
	return true, nil
}

// RegistryLogin is a registry login without its password.
type RegistryLogin struct {
	Host      string
	Username  string
	CreatedAt time.Time
}

// Registries lists the user's registry logins.
func (s *AppService) Registries(workspaceID string) ([]RegistryLogin, error) {
	registries, err := s.apps.Registries(workspaceID)
	if err != nil {
		return nil, err
	}
	logins := make([]RegistryLogin, 0, len(registries))
	for _, registry := range registries {
		logins = append(logins, RegistryLogin{Host: registry.Host, Username: registry.Username, CreatedAt: registry.CreatedAt})
	}
	return logins, nil
}

// SetRegistry stores a login agents use to pull the user's images from host.
func (s *AppService) SetRegistry(actor Actor, host, username, password string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if !registryHostPattern.MatchString(host) {
		return ErrRegistryHost
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return ErrRegistryLogin
	}
	err := s.apps.SetRegistry(model.Registry{
		WorkspaceID: actor.WorkspaceID,
		Host:        host,
		Username:    strings.TrimSpace(username),
		Password:    s.box.Seal(password, actor.WorkspaceID+"/"+host),
		CreatedAt:   s.now().UTC(),
	})
	if err != nil {
		return err
	}
	s.servers.record(actor, "registry.set", "registry", host, map[string]string{"username": strings.TrimSpace(username)})
	return nil
}

// RemoveRegistry deletes a registry login.
func (s *AppService) RemoveRegistry(actor Actor, host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	err := s.apps.DeleteRegistry(actor.WorkspaceID, host)
	if err != nil {
		return err
	}
	s.servers.record(actor, "registry.removed", "registry", host, nil)
	return nil
}

// registry returns the user's login for the registry of image, if any.
func (s *AppService) registry(workspaceID, image string) (*protocol.Registry, error) {
	host := registryHost(image)
	registries, err := s.apps.Registries(workspaceID)
	if err != nil {
		return nil, err
	}
	for _, registry := range registries {
		if registry.Host != host {
			continue
		}
		password, err := s.box.Open(registry.Password, workspaceID+"/"+host)
		if err != nil {
			return nil, fmt.Errorf("registry %s: %w", host, err)
		}
		return &protocol.Registry{Host: host, Username: registry.Username, Password: password}, nil
	}
	return nil, nil
}

// registryHost is the registry of an image reference: its first part when
// that looks like a host, else Docker Hub.
func registryHost(image string) string {
	first, _, found := strings.Cut(image, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first
	}
	return "docker.io"
}

// Redeploy runs the newest deploy that had a source or an image again, with
// the env as it is now. Rollbacks are skipped.
func (s *AppService) Redeploy(actor Actor, idOrName string) (*model.Deploy, error) {
	app, err := s.ready(actor.WorkspaceID, idOrName)
	if err != nil {
		return nil, err
	}
	deploys, err := s.apps.ListDeploys(app.ID, 100)
	if err != nil {
		return nil, err
	}
	for _, previous := range deploys {
		if previous.Status != model.DeploySucceeded || !Builds(previous) {
			continue
		}
		deploy := &model.Deploy{
			ID:           uuid.New().String(),
			AppID:        app.ID,
			Status:       model.DeployRunning,
			SourceKey:    previous.SourceKey,
			SourceSHA256: previous.SourceSHA256,
			Image:        previous.Image,
			CreatedAt:    s.now().UTC(),
		}
		// The server builds the source it kept of that deploy again.
		start := protocol.DeployStart{Image: deploy.Image}
		if deploy.SourceKey != "" {
			start.SourceNumber = previous.Number
		}
		return s.start(actor, "app.redeployed", app, deploy, start)
	}
	return nil, ErrNoSource
}

// Restart starts the live deploy again with the env and domains as they
// are now, without a build.
func (s *AppService) Restart(actor Actor, idOrName string) (*model.Deploy, error) {
	return s.apply(actor, "app.restarted", idOrName)
}

// Start runs a stopped app again. Like a restart, it takes the env and
// domains as they are now. An app that runs already stays as it is.
func (s *AppService) Start(actor Actor, idOrName string) (*model.Deploy, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if app.LiveNumber > 0 && !app.Stopped {
		return nil, ErrRunning
	}
	return s.apply(actor, "app.started", idOrName)
}

// Stop stops the containers of the app on its server. They stay stopped,
// also after a reboot, until a deploy, a restart or a start. Data stays.
func (s *AppService) Stop(ctx context.Context, actor Actor, idOrName string) error {
	app, err := s.ready(actor.WorkspaceID, idOrName)
	if err != nil {
		return err
	}
	if app.LiveNumber == 0 {
		return ErrNotLive
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err = s.servers.Request(ctx, app.ServerID, protocol.TypeAppStop, protocol.AppStop{App: app.Name}, nil)
	if err != nil {
		return err
	}
	err = s.apps.SetStopped(app.ID, true)
	if err != nil {
		return err
	}
	s.servers.record(actor, "app.stopped", "app", app.Name, nil)
	s.notify(actor.WorkspaceID)
	return nil
}

func (s *AppService) apply(actor Actor, action, idOrName string) (*model.Deploy, error) {
	app, err := s.ready(actor.WorkspaceID, idOrName)
	if err != nil {
		return nil, err
	}
	if app.LiveNumber == 0 {
		return nil, ErrNotLive
	}
	deploy := &model.Deploy{ID: uuid.New().String(), AppID: app.ID, Status: model.DeployRunning, CreatedAt: s.now().UTC()}
	return s.start(actor, action, app, deploy, protocol.DeployStart{})
}

// SetDomains replaces the domains of one service of the app: the main one
// for an empty service, with the canonical one ("" for none), or another
// service of compose.yml, which has no canonical domain. The app takes them
// on its next restart, start or deploy (ChangesWaiting). changed is false
// when the service had these domains already.
func (s *AppService) SetDomains(actor Actor, idOrName, service string, domains []string, canonical string) (changed bool, err error) {
	if service != "" {
		if canonical != "" {
			return false, ErrServiceCanonical
		}
		return s.setServiceDomains(actor, idOrName, service, domains)
	}
	return s.setMainDomains(actor, idOrName, domains, canonical)
}

// setMainDomains replaces the domains of the main service and the
// canonical one.
func (s *AppService) setMainDomains(actor Actor, idOrName string, domains []string, canonical string) (changed bool, err error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return false, err
	}
	// x-moin wins, so a change here would never reach the app.
	if fromCompose, _ := s.composeDomains(app); len(fromCompose) > 0 {
		return false, ErrComposeDomains
	}
	clean, err := cleanDomains(domains)
	if err != nil {
		return false, err
	}
	for _, list := range app.ServiceDomainMap() {
		for _, domain := range clean {
			if slices.Contains(list, domain) {
				return false, ErrDomainTaken
			}
		}
	}
	canonical = strings.ToLower(strings.TrimSpace(canonical))
	if canonical != "" && !slices.Contains(clean, canonical) {
		return false, ErrCanonical
	}
	if slices.Equal(clean, app.DomainList()) && canonical == app.Canonical {
		return false, nil
	}
	before := strings.Join(app.DomainList(), ", ")
	body, _ := json.Marshal(clean)
	err = s.apps.SetDomains(app.ID, string(body), canonical)
	if err != nil {
		return false, err
	}
	app.Domains = string(body)
	s.syncTunnelOf(app)
	s.servers.record(actor, "app.domains_set", "app", app.Name, map[string]string{"from": before, "domains": strings.Join(clean, ", "), "canonical": canonical})
	s.notify(actor.WorkspaceID)
	return true, nil
}

// SetProtection puts a password in front of the app, or takes it away when
// password is empty. The app takes it on its next restart. changed is false
// when the app had this password, or none, already.
func (s *AppService) SetProtection(actor Actor, idOrName, username, password string) (changed bool, err error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return false, err
	}
	protect := ""
	if password != "" {
		username = strings.TrimSpace(username)
		if username == "" || strings.ContainsAny(username, ": ") {
			return false, ErrProtectUser
		}
		if len(password) < 8 || len(password) > 72 {
			return false, ErrProtect
		}
		if user, hash, _ := strings.Cut(app.Protect, ":"); user == username && bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil {
			return false, nil
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return false, err
		}
		protect = username + ":" + string(hash)
	} else if app.Protect == "" {
		return false, nil
	}
	err = s.apps.SetProtect(app.ID, protect)
	if err != nil {
		return false, err
	}
	action := "app.protected"
	if protect == "" {
		action = "app.unprotected"
	}
	s.servers.record(actor, action, "app", app.Name, nil)
	s.notify(actor.WorkspaceID)
	return true, nil
}

// waitingActions are the changes an app takes only on its next restart,
// start or deploy: saving never restarts it (DESIGN.md, Feedback).
var waitingActions = []string{"env.set", "env.removed", "app.domains_set", "app.protected", "app.unprotected", "app.instant_set", "app.bucket_set"}

// ChangesWaiting counts the changes made to an app since its live deploy
// started, which its next restart applies. A stopped app or one never
// deployed has none waiting: it takes them when it starts.
func (s *AppService) ChangesWaiting(workspaceID string, app *model.App) (int, error) {
	if app.LiveNumber == 0 || app.Stopped {
		return 0, nil
	}
	live, err := s.apps.Deploy(app.ID, app.LiveNumber)
	if err != nil {
		return 0, err
	}
	return s.servers.activity.CountAppChanges(workspaceID, app.Name, waitingActions, live.CreatedAt)
}

// DomainCheck says whether a domain points at the app's server.
type DomainCheck struct {
	Domain    string
	Canonical bool
	// Address is the server's address the domain should point at: its
	// IPv4 when known, else its IPv6, or the tunnel.
	Address string
	// IPv4 and IPv6 are the server's addresses, empty when unknown. A
	// domain points here when it resolves to either.
	IPv4, IPv6 string
	// Advice is the DNS record to set, such as "A record to 203.0.113.7,
	// AAAA record to 2a01:4f8::1".
	Advice string
	Found  []string
	Points bool
	// Checked says the domain was looked up, so Found and Points hold.
	Checked bool
	// FromCompose says x-moin.domains in compose.yml sets the domain, so
	// it changes there, not in moin.
	FromCompose bool
	// Service is the service of compose.yml the domain reaches, empty for
	// the main one.
	Service string
}

// liveDetected is what the agent found in the live deploy, nil before the
// first deploy.
func (s *AppService) liveDetected(app *model.App) *protocol.Detected {
	if app.LiveNumber == 0 {
		return nil
	}
	deploy, err := s.apps.Deploy(app.ID, app.LiveNumber)
	if err != nil {
		return nil
	}
	return Detected(*deploy)
}

// composeDomains are the domains x-moin.domains sets for the main service in
// the live deploy, and its canonical one; none when compose.yml sets none
// for it.
func (s *AppService) composeDomains(app *model.App) (domains []string, canonical string) {
	detected := s.liveDetected(app)
	if detected == nil || detected.Config == nil || (len(detected.Config.Domains) == 0 && len(detected.Config.ServiceDomains[detected.Service]) == 0) {
		return nil, ""
	}
	return detected.Domains, detected.Canonical
}

// composeServiceDomains are the domains x-moin.domains sets for the other
// services in the live deploy, by service.
func (s *AppService) composeServiceDomains(app *model.App) map[string][]string {
	detected := s.liveDetected(app)
	if detected == nil || detected.Config == nil {
		return nil
	}
	set := map[string][]string{}
	for service, domains := range detected.Config.ServiceDomains {
		if service != detected.Service && len(domains) > 0 {
			set[service] = domains
		}
	}
	return set
}

// ServerAddresses are the IPv4 and IPv6 of a server: the address its agent
// connects from and the ones it reports.
func ServerAddresses(server model.Server) (ipv4, ipv6 string) {
	for _, text := range append([]string{server.Address}, strings.Fields(server.Addresses)...) {
		address, err := netip.ParseAddr(text)
		if err != nil {
			continue
		}
		address = address.Unmap()
		// An agent on the control plane's own machine or network connects
		// from a loopback or private address, which no domain can point at.
		if !address.IsGlobalUnicast() || address.IsPrivate() {
			continue
		}
		switch {
		case address.Is4() && ipv4 == "":
			ipv4 = address.String()
		case address.Is6() && ipv6 == "":
			ipv6 = address.String()
		}
	}
	return ipv4, ipv6
}

// DNSAdvice is the record a domain needs to point at a server.
func DNSAdvice(ipv4, ipv6 string) string {
	switch {
	case ipv4 != "" && ipv6 != "":
		return "A record to " + ipv4 + ", AAAA record to " + ipv6
	case ipv4 != "":
		return "A record to " + ipv4
	case ipv6 != "":
		return "AAAA record to " + ipv6
	}
	return "A record to the server's IPv4"
}

// resolvesTo reports whether one of the addresses found is ipv4 or ipv6.
func resolvesTo(found []string, ipv4, ipv6 string) bool {
	for _, text := range found {
		address, err := netip.ParseAddr(text)
		if err != nil {
			continue
		}
		address = address.Unmap()
		if (ipv4 != "" && address.String() == ipv4) || (ipv6 != "" && address == netip.MustParseAddr(ipv6)) {
			return true
		}
	}
	return false
}

// CheckDomains looks up every domain of an app and compares it with the
// address its agent connects from.
func (s *AppService) CheckDomains(ctx context.Context, workspaceID, idOrName string) ([]DomainCheck, error) {
	return s.domainChecks(ctx, workspaceID, idOrName, func(string) bool { return true })
}

// Domains lists the domains of an app like CheckDomains, with the record
// each needs, but looks none up: what the web shows when it opens.
func (s *AppService) Domains(workspaceID, idOrName string) ([]DomainCheck, error) {
	return s.domainChecks(context.Background(), workspaceID, idOrName, func(string) bool { return false })
}

// CheckDomain lists the domains of an app like Domains and looks up only
// the one named.
func (s *AppService) CheckDomain(ctx context.Context, workspaceID, idOrName, domain string) ([]DomainCheck, error) {
	return s.domainChecks(ctx, workspaceID, idOrName, func(name string) bool { return name == domain })
}

// domainChecks lists the domains of an app and looks up those lookup says.
func (s *AppService) domainChecks(ctx context.Context, workspaceID, idOrName string, lookup func(string) bool) ([]DomainCheck, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	server, err := s.servers.servers.ByIDOrNameForWorkspace(app.ServerID, workspaceID)
	if err != nil {
		return nil, err
	}
	ipv4, ipv6 := ServerAddresses(*server)
	address := ipv4
	if address == "" {
		address = ipv6
	}
	// check looks one domain up: does it point at the server?
	check := func(domain DomainCheck) DomainCheck {
		domain.Address, domain.IPv4, domain.IPv6, domain.Advice = address, ipv4, ipv6, DNSAdvice(ipv4, ipv6)
		if server.Tunnel {
			domain.Address = server.TunnelID + ".cfargotunnel.com"
			domain.Advice = "CNAME record to " + domain.Address
		}
		if !lookup(domain.Domain) {
			return domain
		}
		domain.Checked = true
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		domain.Found, _ = net.DefaultResolver.LookupHost(lookupCtx, domain.Domain)
		cancel()
		domain.Points = resolvesTo(domain.Found, ipv4, ipv6)
		if server.Tunnel {
			// moin points the domain at the tunnel itself; Cloudflare proxies
			// it, so it resolves to Cloudflare's addresses.
			domain.Points = len(domain.Found) > 0
		}
		return domain
	}
	checks := []DomainCheck{}
	domains, canonical := app.DomainList(), app.Canonical
	fromCompose, composeCanonical := s.composeDomains(app)
	if len(fromCompose) > 0 {
		domains, canonical = fromCompose, composeCanonical
	}
	for _, domain := range domains {
		checks = append(checks, check(DomainCheck{Domain: domain, Canonical: domain == canonical, FromCompose: len(fromCompose) > 0}))
	}
	// Then the domains of the other services, by service: compose.yml's
	// where it sets them, else moin'.
	services := app.ServiceDomainMap()
	fromComposeServices := s.composeServiceDomains(app)
	maps.Copy(services, fromComposeServices)
	main := s.mainService(app)
	for _, service := range slices.Sorted(maps.Keys(services)) {
		if service == main {
			continue
		}
		for _, domain := range services[service] {
			checks = append(checks, check(DomainCheck{Domain: domain, Service: service, FromCompose: len(fromComposeServices[service]) > 0}))
		}
	}
	return checks, nil
}

// Rollback starts the images of an earlier successful deploy again, with
// the env it had. Number 0 means the one before the live deploy.
func (s *AppService) Rollback(actor Actor, idOrName string, number int) (*model.Deploy, error) {
	app, err := s.ready(actor.WorkspaceID, idOrName)
	if err != nil {
		return nil, err
	}
	if number == 0 {
		deploys, err := s.apps.ListDeploys(app.ID, 100)
		if err != nil {
			return nil, err
		}
		for _, deploy := range deploys {
			if deploy.Status == model.DeploySucceeded && deploy.Number < app.LiveNumber {
				number = deploy.Number
				break
			}
		}
		if number == 0 {
			return nil, ErrNoRollback
		}
	}
	// A deploy number that does not exist is not found.
	target, err := s.apps.Deploy(app.ID, number)
	if err != nil {
		return nil, err
	}
	if target.Status != model.DeploySucceeded {
		return nil, ErrNoRollback
	}
	deploys, err := s.apps.ListDeploys(app.ID, 100)
	if err != nil {
		return nil, err
	}
	if !KeptDeploys(deploys, app.LiveNumber)[target.Number] {
		return nil, ErrDeployGone
	}
	deploy := &model.Deploy{
		ID:         uuid.New().String(),
		AppID:      app.ID,
		Status:     model.DeployRunning,
		FromNumber: target.Number,
		CreatedAt:  s.now().UTC(),
	}
	return s.start(actor, "app.rolled_back", app, deploy, protocol.DeployStart{FromNumber: target.Number})
}

// Builds reports whether a deploy brought something new, a source build or
// an image. Restarts, domain changes and rollbacks do not.
func Builds(deploy model.Deploy) bool {
	return deploy.SourceKey != "" || deploy.Image != ""
}

// KeptDeploys returns the numbers of the deploys whose release the server
// still keeps, by the rule of protocol.KeptReleases, from deploys newest
// first and the live number. Only succeeded deploys keep a release.
func KeptDeploys(deploys []model.Deploy, live int) map[int]bool {
	releases := make([]protocol.Release, 0, len(deploys))
	for _, deploy := range deploys {
		if deploy.Status == model.DeploySucceeded {
			releases = append(releases, protocol.Release{Number: deploy.Number, Build: Builds(deploy)})
		}
	}
	return protocol.KeptReleases(releases, live)
}

// ready returns an app whose server is online and that has no running deploy.
func (s *AppService) ready(workspaceID, idOrName string) (*model.App, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	if !s.servers.Online(app.ServerID) {
		return nil, ErrServerOffline
	}
	if app.LatestStatus == model.DeployRunning {
		latest, err := s.apps.Deploy(app.ID, app.LatestNumber)
		if err != nil {
			return nil, err
		}
		if !s.expire(latest) {
			return nil, ErrDeployRunning
		}
	}
	return app, nil
}

// start stores the deploy, sends it to the agent and records action in the
// activity; an empty action records nothing.
func (s *AppService) start(actor Actor, action string, app *model.App, deploy *model.Deploy, start protocol.DeployStart) (*model.Deploy, error) {
	env, err := s.Env(app.WorkspaceID, app.ID)
	if err != nil {
		return nil, err
	}
	if start.Image != "" {
		start.Registry, err = s.registry(app.WorkspaceID, start.Image)
		if err != nil {
			return nil, err
		}
	}
	err = s.apps.CreateDeploy(deploy)
	if err != nil {
		return nil, err
	}
	start.DeployID, start.App, start.AppID, start.Number = deploy.ID, app.Name, app.ID, deploy.Number
	start.Domains = &protocol.Domains{Domains: app.DomainList(), Canonical: app.Canonical, Protect: app.Protect, Services: serviceDomains(app)}
	err = s.fillInstant(app, start.Domains)
	if err != nil {
		return nil, err
	}
	if start.FromNumber == 0 {
		start.Env = env
	}
	s.mu.Lock()
	s.live[deploy.ID] = &liveDeploy{workspaceID: app.WorkspaceID, serverID: app.ServerID, followers: map[chan struct{}]struct{}{}}
	s.mu.Unlock()
	s.notify(app.WorkspaceID)

	err = s.servers.Send(app.ServerID, protocol.TypeDeployStart, start)
	if err != nil {
		s.finish(deploy.ID, protocol.DeployDone{DeployID: deploy.ID, Error: "the server went offline before the deploy started"})
		return nil, ErrServerOffline
	}
	slog.Info("deploy started", "app_id", app.ID, "number", deploy.Number, "from_number", deploy.FromNumber)
	// Every deploy runs the app, so it is no longer stopped.
	if app.Stopped {
		err = s.apps.SetStopped(app.ID, false)
		if err != nil {
			slog.Error("app started, but it is still marked stopped", "app_id", app.ID, "error", err)
		}
	}
	if action != "" {
		details := map[string]string{"number": strconv.Itoa(deploy.Number)}
		for key, value := range map[string]string{"image": deploy.Image, "ref": deploy.Ref, "commit": clipText(deploy.CommitSHA, 7), "message": deploy.CommitMessage} {
			if value != "" {
				details[key] = value
			}
		}
		if deploy.FromNumber > 0 {
			details["from"] = strconv.Itoa(deploy.FromNumber)
		}
		s.servers.record(actor, action, "app", app.Name, details)
	}
	return deploy, nil
}

// expire fails a running deploy the agent stopped reporting on, and reports
// whether the deploy is over.
func (s *AppService) expire(deploy *model.Deploy) bool {
	if deploy.Status != model.DeployRunning {
		return true
	}
	if s.now().Sub(deploy.CreatedAt) < deployTimeout {
		return false
	}
	s.finish(deploy.ID, protocol.DeployDone{DeployID: deploy.ID, Error: "the agent stopped reporting on this deploy"})
	deploy.Status = model.DeployFailed
	return true
}

// receive takes the deploy messages of an agent. A server can only report on
// deploys of its own apps.
func (s *AppService) receive(serverID string, message protocol.Message) {
	switch message.Type {
	case protocol.TypeDeployLog:
		var line protocol.DeployLog
		if message.Decode(&line) != nil {
			return
		}
		s.mu.Lock()
		live := s.live[line.DeployID]
		if live != nil && live.serverID == serverID && len(live.lines) < maxDeployLogLines {
			live.lines = append(live.lines, line.Line)
			for follower := range live.followers {
				select {
				case follower <- struct{}{}:
				default:
				}
			}
		}
		s.mu.Unlock()
	case protocol.TypeMetrics:
		var sample protocol.Metrics
		if message.Decode(&sample) == nil {
			s.receiveMetrics(serverID, sample)
		}
	case protocol.TypeBackupStatus:
		var backups protocol.Backups
		if message.Decode(&backups) == nil {
			s.receiveBackupStatus(serverID, backups)
		}
	case protocol.TypeDeployDone:
		var done protocol.DeployDone
		if message.Decode(&done) != nil {
			return
		}
		deploy, err := s.apps.DeployByID(done.DeployID)
		if err != nil {
			return
		}
		app, err := s.apps.ByID(deploy.AppID)
		if err != nil || app.ServerID != serverID {
			slog.Warn("agent reported on a deploy of another server", "server_id", serverID, "deploy_id", done.DeployID)
			return
		}
		s.rememberDeploy(done.DeployID, done)
		s.finish(done.DeployID, done)
	}
}

// finish stores how a deploy ended with its log and wakes its followers.
func (s *AppService) finish(deployID string, done protocol.DeployDone) {
	s.mu.Lock()
	live := s.live[deployID]
	delete(s.live, deployID)
	s.mu.Unlock()

	status := model.DeployFailed
	if done.OK {
		status = model.DeploySucceeded
	}
	detected := ""
	if done.Detected.Kind != "" {
		body, err := json.Marshal(done.Detected)
		if err == nil {
			detected = string(body)
		}
	}
	log := ""
	if live != nil {
		log = strings.Join(live.lines, "\n")
	}
	err := s.apps.FinishDeploy(deployID, status, clipText(done.Error, 2000), log, detected, s.now().UTC())
	if err != nil {
		slog.Error("store deploy result failed", "error", err, "deploy_id", deployID)
	}
	s.recordFinished(deployID, status, done.Error)
	if live != nil {
		s.mu.Lock()
		for follower := range live.followers {
			close(follower)
		}
		s.mu.Unlock()
		s.notify(live.workspaceID)
	}
	s.reportFinished(deployID, status, done.Error)
	if status == model.DeploySucceeded {
		deploy, err := s.apps.DeployByID(deployID)
		if err == nil {
			app, err := s.apps.ByID(deploy.AppID)
			if err == nil {
				// The instant address the release serves, which x-moin may
				// have changed or turned off.
				if app.InstantHost != done.Detected.Instant {
					_ = s.apps.SetInstantHost(app.ID, done.Detected.Instant)
				}
				if len(done.Detected.Domains) > 0 {
					s.syncTunnelOf(app)
				}
			}
		}
	}
}

// recordFinished writes how a deploy ended to the activity, as moin itself:
// its number, how long it took and, for a failed one, why.
func (s *AppService) recordFinished(deployID, status, failure string) {
	deploy, err := s.apps.DeployByID(deployID)
	if err != nil {
		return
	}
	app, err := s.apps.ByID(deploy.AppID)
	if err != nil {
		return
	}
	details := map[string]string{
		"number":   strconv.Itoa(deploy.Number),
		"duration": s.now().Sub(deploy.CreatedAt).Round(time.Second).String(),
	}
	action := "deploy.succeeded"
	if status != model.DeploySucceeded {
		action = "deploy.failed"
		details["error"] = clipText(failure, 300)
	}
	s.servers.record(System(app.WorkspaceID), action, "app", app.Name, details)
}

// reportFinished writes the commit status of a finished deploy that came
// from GitHub.
func (s *AppService) reportFinished(deployID, status, failure string) {
	deploy, err := s.apps.DeployByID(deployID)
	if err != nil || deploy.CommitSHA == "" {
		return
	}
	app, err := s.apps.ByID(deploy.AppID)
	if err != nil {
		return
	}
	state, description := "success", fmt.Sprintf("v%d is live", deploy.Number)
	if status != model.DeploySucceeded {
		state, description = "failure", fmt.Sprintf("v%d failed: %s", deploy.Number, failure)
	}
	go s.reportStatus(app, deploy, state, description)
}

// Follow returns the lines of a deploy so far. While it runs, changes gets a
// value after new lines and closes when it ends; for a finished deploy
// changes is nil.
func (s *AppService) Follow(workspaceID, idOrName string, number int) (deploy *model.Deploy, lines func() []string, changes <-chan struct{}, stop func(), err error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	deploy, err = s.apps.Deploy(app.ID, number)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	s.expire(deploy)
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.live[deploy.ID]
	if live == nil {
		// It may have ended a moment ago; read it again for the log.
		deploy, err = s.apps.Deploy(app.ID, number)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		all := splitLines(deploy.Log)
		return deploy, func() []string { return all }, nil, func() {}, nil
	}
	follower := make(chan struct{}, 1)
	live.followers[follower] = struct{}{}
	lines = func() []string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return slices.Clone(live.lines)
	}
	stop = func() {
		s.mu.Lock()
		delete(live.followers, follower)
		s.mu.Unlock()
	}
	return deploy, lines, follower, stop, nil
}

// DeployByNumber returns one deploy with its log.
func (s *AppService) DeployByNumber(workspaceID, idOrName string, number int) (*model.Deploy, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	deploy, err := s.apps.Deploy(app.ID, number)
	if err != nil {
		return nil, err
	}
	s.expire(deploy)
	return deploy, nil
}

// Logs asks the agent for the last lines of the app's containers.
func (s *AppService) Logs(ctx context.Context, workspaceID, idOrName string, tail int) ([]string, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result protocol.LogsResult
	err = s.servers.Request(ctx, app.ServerID, protocol.TypeLogs, protocol.LogsRequest{App: app.Name, Tail: tail}, &result)
	return result.Lines, err
}

// Env returns the app's env vars in plain text. The next deploy gets them.
func (s *AppService) Env(workspaceID, idOrName string) (map[string]string, error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, workspaceID)
	if err != nil {
		return nil, err
	}
	sealed, err := s.apps.Env(app.ID)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(sealed))
	for key, value := range sealed {
		env[key], err = s.box.Open(value, app.ID+"/"+key)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", key, err)
		}
	}
	return env, nil
}

// SetEnv sets and removes env vars. They take effect with the next deploy.
// removed are the keys of unset the app had; the others were not set.
func (s *AppService) SetEnv(actor Actor, idOrName string, set map[string]string, unset []string) (removed []string, err error) {
	app, err := s.apps.ByIDOrNameForWorkspace(idOrName, actor.WorkspaceID)
	if err != nil {
		return nil, err
	}
	current, err := s.apps.Env(app.ID)
	if err != nil {
		return nil, err
	}
	sealed := make(map[string]string, len(set))
	for key, value := range set {
		if !envKeyPattern.MatchString(key) {
			return nil, ErrEnvKey
		}
		if len(value) > maxEnvValueSize {
			return nil, ErrEnvValue
		}
		sealed[key] = s.box.Seal(value, app.ID+"/"+key)
		current[key] = ""
	}
	for _, key := range unset {
		if !envKeyPattern.MatchString(key) {
			return nil, ErrEnvKey
		}
		if _, ok := current[key]; ok {
			removed = append(removed, key)
		}
		delete(current, key)
	}
	if len(current) > maxEnvVars {
		return nil, ErrEnvLimit
	}
	err = s.apps.SetEnv(app.ID, sealed, unset, s.now().UTC())
	if err != nil {
		return nil, err
	}
	// The activity names the keys, never the values.
	keys := slices.Sorted(maps.Keys(set))
	if len(keys) > 0 {
		s.servers.record(actor, "env.set", "app", app.Name, map[string]string{"keys": strings.Join(keys, ", ")})
	}
	if len(removed) > 0 {
		s.servers.record(actor, "env.removed", "app", app.Name, map[string]string{"keys": strings.Join(removed, ", ")})
	}
	s.notify(actor.WorkspaceID)
	return removed, nil
}

// ReplaceEnv makes env the app's env vars: it sets what is new or changed
// and removes the keys env does not have. It returns what changed.
func (s *AppService) ReplaceEnv(actor Actor, idOrName string, env map[string]string) (dotenv.Change, error) {
	current, err := s.Env(actor.WorkspaceID, idOrName)
	if err != nil {
		return dotenv.Change{}, err
	}
	if len(env) > maxEnvVars {
		return dotenv.Change{}, ErrEnvLimit
	}
	change := dotenv.Diff(current, env)
	if change.None() {
		return change, nil
	}
	set := make(map[string]string, len(change.Added)+len(change.Changed))
	for _, key := range append(slices.Clone(change.Added), change.Changed...) {
		set[key] = env[key]
	}
	_, err = s.SetEnv(actor, idOrName, set, change.Removed)
	return change, err
}

// Subscribe returns a channel that receives a value whenever an app of the
// user or one of its deploys changes.
func (s *AppService) Subscribe(workspaceID string) (<-chan struct{}, func()) {
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

func (s *AppService) notify(workspaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for changes := range s.subscribers[workspaceID] {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
}

// Detected decodes what the agent reported for a deploy.
func Detected(deploy model.Deploy) *protocol.Detected {
	if deploy.Detected == "" {
		return nil
	}
	var detected protocol.Detected
	if json.Unmarshal([]byte(deploy.Detected), &detected) != nil {
		return nil
	}
	return &detected
}

func splitLines(text string) []string {
	if text == "" {
		return []string{}
	}
	return strings.Split(text, "\n")
}

func clipText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
