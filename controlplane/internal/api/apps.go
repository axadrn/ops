package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/internal/dotenv"
	"github.com/axadrn/moin/protocol"
)

// AppsHandler serves apps, their deploys, logs and env. The web UI, the CLI
// and the TUI all use it.
type AppsHandler struct {
	apps *service.AppService
}

func NewAppsHandler(apps *service.AppService) *AppsHandler {
	return &AppsHandler{apps: apps}
}

// App is the public JSON shape of an app.
type App struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	ServerID     string   `json:"server_id"`
	ServerName   string   `json:"server_name"`
	LatestNumber int      `json:"latest_number"`
	LatestStatus string   `json:"latest_status"`
	LiveNumber   int      `json:"live_number"`
	Domains      []string `json:"domains"`
	Canonical    string   `json:"canonical"`
	Protected    bool     `json:"protected"`
	// ServiceDomains are the domains of the app's other services.
	ServiceDomains map[string][]string `json:"service_domains"`
	// Instant is the app's instant address, "" when it has none;
	// InstantProvider its provider or off; InstantOpen says no password
	// guards it.
	Instant         string `json:"instant"`
	InstantProvider string `json:"instant_provider"`
	InstantOpen     bool   `json:"instant_open"`
	// Stopped is set when the app was stopped; a deploy, restart or start
	// runs it again.
	Stopped bool `json:"stopped"`
	// ChangesWaiting counts the changes saved since the live deploy
	// started (env vars, domains, password, instant address, bucket); a
	// restart applies them. Only GET /apps/{app} fills it.
	ChangesWaiting int `json:"changes_waiting"`
	// Repo and Branch are the GitHub repo the app follows, if any.
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	AutoDeploy bool   `json:"auto_deploy"`
	// Pinned keeps the app above the others that run fine, in every list.
	Pinned bool `json:"pinned"`
	// Bucket is the bucket the app uploads and backs up to; empty uses
	// the workspace's default bucket.
	Bucket string `json:"bucket"`
	// Hook says the app has a deploy hook URL; the URL itself is shown
	// only once, when it is made.
	Hook bool `json:"hook"`
	// Catalog is the slug of the catalog entry the app was installed
	// from; empty for an app of your own.
	Catalog   string    `json:"catalog"`
	CreatedAt time.Time `json:"created_at"`
}

// AppList is the answer of GET /api/v1/apps and every event of GET
// /api/v1/apps/events.
type AppList struct {
	Items []App `json:"items"`
}

// Deploy is the public JSON shape of a deploy. Log is only set on the
// single-deploy answer.
type Deploy struct {
	Number     int    `json:"number"`
	Status     string `json:"status"`
	FromNumber int    `json:"from_number"`
	Image      string `json:"image"`
	// Ref and Commit name the GitHub commit the deploy came from.
	Ref           string             `json:"ref,omitempty"`
	Commit        string             `json:"commit,omitempty"`
	CommitMessage string             `json:"commit_message,omitempty"`
	Error         string             `json:"error"`
	Detected      *protocol.Detected `json:"detected"`
	CreatedAt     time.Time          `json:"created_at"`
	FinishedAt    *time.Time         `json:"finished_at"`
	Log           []string           `json:"log,omitempty"`
	// OnServer says whether the server still keeps the release, so it can
	// be rolled back to. Only in the deploys of an app's detail.
	OnServer *bool `json:"on_server,omitempty"`
}

// AppDetail is the answer of GET /api/v1/apps/{app}: the app and its newest deploys.
type AppDetail struct {
	App     App      `json:"app"`
	Deploys []Deploy `json:"deploys"`
}

// Env is the answer of GET /api/v1/apps/{app}/env.
type Env struct {
	Items map[string]string `json:"items"`
}

// Logs is the answer of GET /api/v1/apps/{app}/logs.
type Logs struct {
	Lines []string `json:"lines"`
}

type createAppRequest struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

// deployRequest is the body of a deploy: a source the CLI uploaded to the
// user's bucket with its SHA-256, an image built elsewhere, or a ref of the
// GitHub repo the app follows. An empty body deploys the linked branch.
type deployRequest struct {
	Upload string `json:"upload"`
	SHA256 string `json:"sha256"`
	Image  string `json:"image"`
	Ref    string `json:"ref"`
}

type rollbackRequest struct {
	Number int `json:"number"`
}

type envRequest struct {
	Set   map[string]string `json:"set"`
	Unset []string          `json:"unset"`
}

func NewApp(app model.App) App {
	return App{
		ID:              app.ID,
		Name:            app.Name,
		ServerID:        app.ServerID,
		ServerName:      app.ServerName,
		LatestNumber:    app.LatestNumber,
		LatestStatus:    app.LatestStatus,
		LiveNumber:      app.LiveNumber,
		Domains:         app.DomainList(),
		Canonical:       app.Canonical,
		Protected:       app.Protect != "",
		ServiceDomains:  app.ServiceDomainMap(),
		Instant:         instantURL(app.InstantHost),
		InstantProvider: app.Instant,
		InstantOpen:     app.InstantOpen,
		Stopped:         app.Stopped,
		ChangesWaiting:  app.ChangesWaiting,
		Repo:            app.Repo,
		Branch:          app.Branch,
		AutoDeploy:      app.AutoDeploy,
		Pinned:          app.Pinned,
		Hook:            app.DeployHookHash != nil,
		Bucket:          app.BucketName,
		Catalog:         app.Catalog,
		CreatedAt:       app.CreatedAt.UTC(),
	}
}

func instantURL(host string) string {
	if host == "" {
		return ""
	}
	return "https://" + host
}

// Instant is the instant address of an app: its URL, or why it has none,
// and the password in front of it when asked for.
type Instant struct {
	URL      string `json:"url"`
	Provider string `json:"provider"`
	Reason   string `json:"reason,omitempty"`
	Open     bool   `json:"open"`
	User     string `json:"user,omitempty"`
	// Password is the one moin made, with show=true; OwnPassword says the
	// app's own password, the one of its domains, guards it instead.
	Password    string `json:"password,omitempty"`
	OwnPassword bool   `json:"own_password"`
}

func NewInstant(instant service.Instant) Instant {
	return Instant{URL: instant.URL, Provider: instant.Provider, Reason: instant.Reason, Open: instant.Open, User: instant.User, Password: instant.Password, OwnPassword: instant.OwnPassword}
}

func NewDeploy(deploy model.Deploy) Deploy {
	return Deploy{
		Number:        deploy.Number,
		Status:        deploy.Status,
		FromNumber:    deploy.FromNumber,
		Image:         deploy.Image,
		Ref:           deploy.Ref,
		Commit:        deploy.CommitSHA,
		CommitMessage: deploy.CommitMessage,
		Error:         deploy.Error,
		Detected:      service.Detected(deploy),
		CreatedAt:     deploy.CreatedAt.UTC(),
		FinishedAt:    utcTime(deploy.FinishedAt),
	}
}

// NewDeploys converts the newest deploys of an app with live deploy live,
// newest first, and says which ones the server still keeps.
func NewDeploys(deploys []model.Deploy, live int) []Deploy {
	kept := service.KeptDeploys(deploys, live)
	out := make([]Deploy, 0, len(deploys))
	for _, deploy := range deploys {
		converted := NewDeploy(deploy)
		onServer := kept[deploy.Number]
		converted.OnServer = &onServer
		out = append(out, converted)
	}
	return out
}

func (h *AppsHandler) list(workspaceID string) (AppList, error) {
	apps, err := h.apps.ListSorted(workspaceID)
	if err != nil {
		return AppList{}, err
	}
	items := make([]App, 0, len(apps))
	for _, app := range apps {
		items = append(items, NewApp(app))
	}
	return AppList{Items: items}, nil
}

// List returns the apps in the order every client shows: what needs you,
// what deploys, pinned apps, then the rest by name.
func (h *AppsHandler) List(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	list, err := h.list(workspace.ID)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, list)
}

// Create adds an app on a server.
func (h *AppsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createAppRequest
	if !Decode(w, r, &request) {
		return
	}
	app, err := h.apps.Create(actor(r), request.Name, request.Server)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusCreated, NewApp(*app))
}

// Get returns an app with its newest deploys.
func (h *AppsHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	app, deploys, err := h.apps.Deploys(workspace.ID, r.PathValue("app"), 20)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, AppDetail{App: NewApp(*app), Deploys: NewDeploys(deploys, app.LiveNumber)})
}

// Delete stops the app on its server and removes it. Its data stays there.
func (h *AppsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	err := h.apps.Delete(r.Context(), actor(r), r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}

// CreateDeploy starts a deploy. A gzipped source tarball as the body builds
// on the server, optionally with ?image= for the main service; a JSON body
// {"image": "..."} deploys an image built elsewhere.
func (h *AppsHandler) CreateDeploy(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		Error(w, http.StatusUnsupportedMediaType, "update_ops", "This moin is too old: sources go to your own bucket now. Update it with curl -fsSL <control plane>/cli.sh | sh.")
		return
	}
	var request deployRequest
	if !Decode(w, r, &request) {
		return
	}
	var deploy *model.Deploy
	var err error
	switch {
	case request.Upload != "":
		deploy, err = h.apps.DeployUpload(r.Context(), actor(r), r.PathValue("app"), request.Upload, request.SHA256)
	case request.Image != "":
		deploy, err = h.apps.Deploy(actor(r), r.PathValue("app"), request.Image)
	default:
		deploy, err = h.apps.DeployRef(r.Context(), actor(r), r.PathValue("app"), request.Ref)
	}
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusAccepted, NewDeploy(*deploy))
}

// UploadResponse is the answer of POST /api/v1/apps/{app}/uploads.
type UploadResponse struct {
	Upload string `json:"upload"`
	URL    string `json:"url"`
}

// CreateUpload signs a link the CLI uploads a source to, in the user's own
// bucket. moin never sees the code.
func (h *AppsHandler) CreateUpload(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	upload, err := h.apps.Upload(r.Context(), workspace.ID, r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusCreated, UploadResponse{Upload: upload.ID, URL: upload.URL})
}

// Redeploy builds the newest source again with the env as it is now.
func (h *AppsHandler) Redeploy(w http.ResponseWriter, r *http.Request) {
	deploy, err := h.apps.Redeploy(actor(r), r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusAccepted, NewDeploy(*deploy))
}

// Rollback starts an earlier successful release again.
func (h *AppsHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var request rollbackRequest
	if !Decode(w, r, &request) {
		return
	}
	deploy, err := h.apps.Rollback(actor(r), r.PathValue("app"), request.Number)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusAccepted, NewDeploy(*deploy))
}

// GetDeploy returns one deploy with its log.
func (h *AppsHandler) GetDeploy(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	number, ok := deployNumber(w, r)
	if !ok {
		return
	}
	deploy, lines, _, stop, err := h.apps.Follow(workspace.ID, r.PathValue("app"), number)
	if err != nil {
		ServiceError(w, err)
		return
	}
	stop()
	body := NewDeploy(*deploy)
	body.Log = lines()
	JSON(w, http.StatusOK, body)
}

// DeployEvents streams a deploy: a line event per log line, then one done
// event with the deploy. A finished deploy streams its whole log at once.
func (h *AppsHandler) DeployEvents(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	number, ok := deployNumber(w, r)
	if !ok {
		return
	}
	appName := r.PathValue("app")
	deploy, lines, changes, stop, err := h.apps.Follow(workspace.ID, appName, number)
	if err != nil {
		ServiceError(w, err)
		return
	}
	defer stop()
	events := startEvents(w)
	sent := 0
	flushLines := func() bool {
		all := lines()
		for _, line := range all[sent:] {
			if !events.send("line", map[string]string{"line": line}) {
				return false
			}
		}
		sent = len(all)
		return true
	}
	if !flushLines() {
		return
	}
	heartbeat := time.NewTicker(serverEventsHeartbeat)
	defer heartbeat.Stop()
	for changes != nil {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-changes:
			if !flushLines() {
				return
			}
			if !open {
				changes = nil
			}
		case <-heartbeat.C:
			if !events.comment("heartbeat") {
				return
			}
		}
	}
	finished, err := h.apps.DeployByNumber(workspace.ID, appName, deploy.Number)
	if err != nil {
		return
	}
	events.send("done", NewDeploy(*finished))
}

// Logs returns the last lines of the app's containers from its server.
func (h *AppsHandler) Logs(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	tail := 200
	if raw := r.URL.Query().Get("tail"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			ValidationError(w, map[string]string{"tail": "Choose between 1 and 1000 lines."})
			return
		}
		tail = parsed
	}
	lines, err := h.apps.Logs(r.Context(), workspace.ID, r.PathValue("app"), tail)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, Logs{Lines: lines})
}

// GetEnv returns the env vars in plain text. Tokens need the write scope:
// reading secrets is as strong as writing them.
func (h *AppsHandler) GetEnv(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	env, err := h.apps.Env(workspace.ID, r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, Env{Items: env})
}

// UpdateEnv sets and removes env vars. The next deploy gets them.
func (h *AppsHandler) UpdateEnv(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	var request envRequest
	if !Decode(w, r, &request) {
		return
	}
	_, err := h.apps.SetEnv(actor(r), r.PathValue("app"), request.Set, request.Unset)
	if err != nil {
		ServiceError(w, err)
		return
	}
	env, err := h.apps.Env(workspace.ID, r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, Env{Items: env})
}

// replaceEnvRequest is every env var of an app, by key.
type replaceEnvRequest struct {
	Env map[string]string `json:"env"`
}

// ReplacedEnv is the env vars after a replace and what changed.
type ReplacedEnv struct {
	Items map[string]string `json:"items"`
	dotenv.Change
}

// ReplaceEnv makes the sent env vars the app's, all at once: keys not sent
// are removed. The next deploy gets them.
func (h *AppsHandler) ReplaceEnv(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	var request replaceEnvRequest
	if !Decode(w, r, &request) {
		return
	}
	if request.Env == nil {
		request.Env = map[string]string{}
	}
	change, err := h.apps.ReplaceEnv(actor(r), r.PathValue("app"), request.Env)
	if err != nil {
		ServiceError(w, err)
		return
	}
	env, err := h.apps.Env(workspace.ID, r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, ReplacedEnv{Items: env, Change: change})
}

// Events streams the app list: once on connect and again after every change.
func (h *AppsHandler) Events(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	changes, unsubscribe := h.apps.Subscribe(workspace.ID)
	defer unsubscribe()
	events := startEvents(w)
	send := func() bool {
		list, err := h.list(workspace.ID)
		if err != nil {
			slog.Error("failed to list apps", "error", err, "workspace_id", workspace.ID)
			return false
		}
		return events.send("apps", list)
	}
	if !send() {
		return
	}
	heartbeat := time.NewTicker(serverEventsHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if !events.comment("heartbeat") {
				return
			}
		}
	}
}

func deployNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || number < 1 {
		Error(w, http.StatusNotFound, "not_found", "The deploy was not found.")
		return 0, false
	}
	return number, true
}

// eventStream writes server-sent events.
type eventStream struct {
	w          http.ResponseWriter
	controller *http.ResponseController
}

func startEvents(w http.ResponseWriter) eventStream {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return eventStream{w: w, controller: controller}
}

func (e eventStream) send(event string, value any) bool {
	body, err := json.Marshal(value)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, body)
	return err == nil && e.controller.Flush() == nil
}

func (e eventStream) comment(text string) bool {
	_, err := fmt.Fprintf(e.w, ": %s\n\n", text)
	return err == nil && e.controller.Flush() == nil
}

// DeployHook is the answer of POST /api/v1/apps/{app}/hook. The URL is shown once.
type DeployHook struct {
	URL string `json:"url"`
}

// RotateHook gives the app a new deploy hook URL; the old one stops working.
func (h *AppsHandler) RotateHook(w http.ResponseWriter, r *http.Request) {
	url, err := h.apps.RotateDeployHook(actor(r), r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, DeployHook{URL: url})
}

// RemoveHook turns the app's deploy hook off; its URL stops working.
func (h *AppsHandler) RemoveHook(w http.ResponseWriter, r *http.Request) {
	_, err := h.apps.RemoveDeployHook(actor(r), r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}

// Registry is a registry login without its password.
type Registry struct {
	Host      string    `json:"host"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// RegistryList is the answer of GET /api/v1/registries.
type RegistryList struct {
	Items []Registry `json:"items"`
}

type registryRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AppsHandler) registryList(workspaceID string) (RegistryList, error) {
	logins, err := h.apps.Registries(workspaceID)
	if err != nil {
		return RegistryList{}, err
	}
	list := RegistryList{Items: make([]Registry, 0, len(logins))}
	for _, login := range logins {
		list.Items = append(list.Items, Registry{Host: login.Host, Username: login.Username, CreatedAt: login.CreatedAt.UTC()})
	}
	return list, nil
}

// Registries lists the registry logins agents use to pull private images.
func (h *AppsHandler) Registries(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	list, err := h.registryList(workspace.ID)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, list)
}

// SetRegistry stores the login for one registry host.
func (h *AppsHandler) SetRegistry(w http.ResponseWriter, r *http.Request) {
	var request registryRequest
	if !Decode(w, r, &request) {
		return
	}
	err := h.apps.SetRegistry(actor(r), r.PathValue("host"), request.Username, request.Password)
	if err != nil {
		ServiceError(w, err)
		return
	}
	h.Registries(w, r)
}

// DeleteRegistry removes the login for one registry host.
func (h *AppsHandler) DeleteRegistry(w http.ResponseWriter, r *http.Request) {
	err := h.apps.RemoveRegistry(actor(r), r.PathValue("host"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}

// Change is the answer to a change of the domains: the app, and the deploy that
// applies it to the live deploy, or null when the next deploy will.
// Unchanged is set when the app had it so already; nothing restarts.
type Change struct {
	App       App     `json:"app"`
	Deploy    *Deploy `json:"deploy"`
	Unchanged bool    `json:"unchanged,omitempty"`
}

type domainsRequest struct {
	Domains   []string `json:"domains"`
	Canonical string   `json:"canonical"`
	// Service sets the domains of another service than the main one.
	Service string `json:"service"`
}

type instantRequest struct {
	Provider string `json:"provider"`
	Open     *bool  `json:"open"`
}

type protectionRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// DomainCheck says whether a domain points at the app's server.
type DomainCheck struct {
	Domain    string `json:"domain"`
	Canonical bool   `json:"canonical"`
	Address   string `json:"address"`
	// IPv4 and IPv6 are the server's addresses, empty when unknown.
	IPv4 string `json:"ipv4"`
	IPv6 string `json:"ipv6"`
	// Advice is the DNS record to set, such as "A record to 203.0.113.7".
	Advice string   `json:"advice"`
	Found  []string `json:"found"`
	Points bool     `json:"points"`
	// FromCompose says x-moin.domains in compose.yml sets the domain.
	FromCompose bool `json:"from_compose"`
	// Service is the service of compose.yml the domain reaches, empty for
	// the main one.
	Service string `json:"service,omitempty"`
}

// DomainChecks is the answer of GET /api/v1/apps/{app}/domains.
type DomainChecks struct {
	Items []DomainCheck `json:"items"`
}

func NewDomainChecks(checks []service.DomainCheck) DomainChecks {
	out := DomainChecks{Items: make([]DomainCheck, 0, len(checks))}
	for _, check := range checks {
		found := check.Found
		if found == nil {
			found = []string{}
		}
		out.Items = append(out.Items, DomainCheck{Domain: check.Domain, Canonical: check.Canonical, Address: check.Address, IPv4: check.IPv4, IPv6: check.IPv6, Advice: check.Advice, Found: found, Points: check.Points, FromCompose: check.FromCompose, Service: check.Service})
	}
	return out
}

// NewChange is the answer to a change of app: the app as it is now, and
// the deploy that applies it, if one does.
func NewChange(apps *service.AppService, workspaceID, app string, deploy *model.Deploy, changed bool) (Change, error) {
	current, err := apps.Get(workspaceID, app)
	if err != nil {
		return Change{}, err
	}
	answer := Change{App: NewApp(*current), Unchanged: !changed}
	if deploy != nil {
		applied := NewDeploy(*deploy)
		answer.Deploy = &applied
	}
	return answer, nil
}

func (h *AppsHandler) change(w http.ResponseWriter, workspaceID, app string, deploy *model.Deploy, changed bool) {
	answer, err := NewChange(h.apps, workspaceID, app, deploy, changed)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, answer)
}

// Domains checks the DNS of every domain of the app.
func (h *AppsHandler) Domains(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	checks, err := h.apps.CheckDomains(r.Context(), workspace.ID, r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, NewDomainChecks(checks))
}

// SetDomains replaces the domains and applies them to the live deploy.
func (h *AppsHandler) SetDomains(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	var request domainsRequest
	if !Decode(w, r, &request) {
		return
	}
	changed, err := h.apps.SetDomains(actor(r), r.PathValue("app"), request.Service, request.Domains, request.Canonical)
	if err != nil {
		ServiceError(w, err)
		return
	}
	h.change(w, workspace.ID, r.PathValue("app"), nil, changed)
}

// GetInstant answers the app's instant address; with show=true the
// password moin made for it, which needs a token that may write.
func (h *AppsHandler) GetInstant(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	show := r.URL.Query().Get("show") == "true"
	instant, err := h.apps.Instant(workspace.ID, r.PathValue("app"), show, ctxkeys.APIToken(r.Context()))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, NewInstant(instant))
}

// SetInstant picks the provider of the app's instant address, or off, and
// whether a password guards it.
func (h *AppsHandler) SetInstant(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	var request instantRequest
	if !Decode(w, r, &request) {
		return
	}
	changed, err := h.apps.SetInstant(actor(r), r.PathValue("app"), request.Provider, request.Open)
	if err != nil {
		ServiceError(w, err)
		return
	}
	h.change(w, workspace.ID, r.PathValue("app"), nil, changed)
}

// SetProtection puts a password in front of the app.
func (h *AppsHandler) SetProtection(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	var request protectionRequest
	if !Decode(w, r, &request) {
		return
	}
	if request.Password == "" {
		ValidationError(w, map[string]string{"password": "Enter a password of at least 8 characters."})
		return
	}
	changed, err := h.apps.SetProtection(actor(r), r.PathValue("app"), request.Username, request.Password)
	if err != nil {
		ServiceError(w, err)
		return
	}
	h.change(w, workspace.ID, r.PathValue("app"), nil, changed)
}

// DeleteProtection takes the password away.
func (h *AppsHandler) DeleteProtection(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	changed, err := h.apps.SetProtection(actor(r), r.PathValue("app"), "", "")
	if err != nil {
		ServiceError(w, err)
		return
	}
	h.change(w, workspace.ID, r.PathValue("app"), nil, changed)
}

// Restart starts the live deploy again with the env and domains as they
// are now.
func (h *AppsHandler) Restart(w http.ResponseWriter, r *http.Request) {
	deploy, err := h.apps.Restart(actor(r), r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusAccepted, NewDeploy(*deploy))
}

// Start runs a stopped app again.
func (h *AppsHandler) Start(w http.ResponseWriter, r *http.Request) {
	deploy, err := h.apps.Start(actor(r), r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusAccepted, NewDeploy(*deploy))
}

// Stop stops the app's containers. Its data stays.
func (h *AppsHandler) Stop(w http.ResponseWriter, r *http.Request) {
	err := h.apps.Stop(r.Context(), actor(r), r.PathValue("app"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}

// actor is who sends the request.
func actor(r *http.Request) service.Actor {
	return service.Actor{WorkspaceID: ctxkeys.Workspace(r.Context()).ID, UserID: ctxkeys.User(r.Context()).ID, Source: service.RequestSource(r.Context())}
}

// source is how a request came in: the web, the CLI, the TUI or the API.
func source(r *http.Request) service.Source { return service.RequestSource(r.Context()) }

// SetPinned pins or unpins an app: {"pinned": true}.
func (h *AppsHandler) SetPinned(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pinned *bool `json:"pinned"`
	}
	if !Decode(w, r, &body) {
		return
	}
	if body.Pinned == nil {
		ValidationError(w, map[string]string{"pinned": "Say true or false."})
		return
	}
	app, err := h.apps.SetPinned(actor(r), r.PathValue("app"), *body.Pinned)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, NewApp(*app))
}
