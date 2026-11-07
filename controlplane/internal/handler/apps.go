package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/ui/pages"
	"github.com/axadrn/moin/internal/dotenv"
	"github.com/axadrn/moin/protocol"
)

// AppsHandler is the apps pages of the web UI. Deploys come from `moin
// deploy`; the web UI follows them, rolls back, shows logs and keeps env vars.
type AppsHandler struct {
	apps     *service.AppService
	servers  *service.ServerService
	users    *service.UserService
	catalogs *service.CatalogService
}

func NewAppsHandler(apps *service.AppService, servers *service.ServerService, users *service.UserService, catalogs *service.CatalogService) *AppsHandler {
	return &AppsHandler{apps: apps, servers: servers, users: users, catalogs: catalogs}
}

// AppsPage is every app, in the one order.
func (h *AppsHandler) AppsPage(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	apps, err := h.apps.ListSorted(workspace.ID)
	if err != nil {
		renderError(w, "list apps failed", err)
		return
	}
	servers, err := h.servers.List(workspace.ID)
	if err != nil {
		renderError(w, "list servers failed", err)
		return
	}
	render(w, r, pages.Apps(apps, servers))
}

// NewPage is where an app starts: from a folder, GitHub or the catalog.
func (h *AppsHandler) NewPage(w http.ResponseWriter, r *http.Request) {
	servers, err := h.servers.List(ctxkeys.Workspace(r.Context()).ID)
	if err != nil {
		renderError(w, "list servers failed", err)
		return
	}
	render(w, r, pages.NewApp(servers))
}

// SetPinned pins or unpins the app (the switch on=1). The switch already
// shows the change, so a save answers nothing; a failed one flips it back
// as stored.
func (h *AppsHandler) SetPinned(w http.ResponseWriter, r *http.Request) {
	_, err := h.apps.SetPinned(actor(r), r.PathValue("app"), r.FormValue("on") == "1")
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	stored, getErr := h.apps.Get(ctxkeys.Workspace(r.Context()).ID, r.PathValue("app"))
	if getErr != nil {
		failed(w, r, "Pin not changed", err, nil)
		return
	}
	w.Header().Set("HX-Retarget", "#pin-form")
	w.Header().Set("HX-Reswap", "outerHTML")
	answerError(w, r, "Pin not changed", errorMessage(err))
	render(w, r, pages.PinSwitch(*stored))
}

// Create adds an app and answers the reset form. The event stream reloads the table.
// Create adds an app and opens its page.
func (h *AppsHandler) Create(w http.ResponseWriter, r *http.Request) {
	app, err := h.apps.Create(actor(r), r.FormValue("name"), r.FormValue("server"))
	if errors.Is(err, repository.ErrServerNotFound) {
		fieldError(w, "App not added", "app-server", "Choose a server.")
		return
	}
	if err != nil {
		failed(w, r, "App not added", err, map[string]string{"name": "app-name"})
		return
	}
	answerDone(w, r, app.Name+" added", "Link its repo, or run moin deploy "+app.Name+" in its folder.")
	redirect(w, r, ctxkeys.AppBasePath(r.Context())+"/apps/"+app.Name, http.StatusSeeOther)
}

// AppSection answers one section of an app (app, deploys, domains, logs
// or settings) as a whole page.
func (h *AppsHandler) AppSection(section string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.appPage(w, r, section) }
}

// DeployPage is the Deploys section with the log of one deploy.
func (h *AppsHandler) DeployPage(w http.ResponseWriter, r *http.Request) {
	h.appPage(w, r, "deploys")
}

func (h *AppsHandler) appPage(w http.ResponseWriter, r *http.Request, section string) {
	workspace := ctxkeys.Workspace(r.Context())
	app, deploys, err := h.apps.Deploys(workspace.ID, r.PathValue("app"), 20)
	if errors.Is(err, repository.ErrAppNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		renderError(w, "load app failed", err)
		return
	}
	env, err := h.apps.Env(workspace.ID, app.ID)
	if err != nil {
		renderError(w, "load env failed", err)
		return
	}
	view := pages.AppPageView{App: *app, Deploys: api.NewDeploys(deploys, app.LiveNumber), EnvKeys: sortedKeys(env), Section: section}
	view.Selected, _ = strconv.Atoi(r.PathValue("number"))
	switch section {
	case "app":
		if h.catalogs != nil {
			view.Catalog = catalogUpdate(h.catalogs, workspace.ID, app)
		}
	case "deploys":
		repo, err := h.repoView(r, app, false)
		if err != nil {
			renderError(w, "load github installations failed", err)
			return
		}
		view.Repo = &repo
	case "logs":
		// The page comes with the logs, unless the agent is slow: then the
		// section asks for them itself.
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		lines, err := h.apps.Logs(ctx, workspace.ID, app.Name, logLines)
		cancel()
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			view.Logs = &pages.AppLogsView{Lines: lines}
			if err != nil {
				view.Logs.Message = errorMessage(err)
			}
		}
	case "domains":
		if domains, err := h.domainsView(r, ""); err == nil {
			view.Domains = &domains
		}
		if _, uptime, err := h.apps.Uptime(workspace.ID, app.Name); err == nil {
			view.Uptime = &pages.UptimeView{App: *app, Uptime: uptime, Now: time.Now()}
		}
	}
	render(w, r, pages.AppPage(view))
}

// Detail answers the header and deploys of an app, for the reload after a
// change. n is the deploy whose log shows (0 the newest); shown, when the
// page follows the newest, the one it shows now, so a new deploy takes the
// log. An app removed meanwhile sends the page to the list.
func (h *AppsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	app, deploys, err := h.apps.Deploys(workspace.ID, r.PathValue("app"), 20)
	if errors.Is(err, repository.ErrAppNotFound) {
		w.Header().Set("HX-Redirect", ctxkeys.AppBasePath(r.Context())+"/apps")
		return
	}
	if err != nil {
		renderError(w, "load app failed", err)
		return
	}
	selected, _ := strconv.Atoi(r.URL.Query().Get("n"))
	shown, err := strconv.Atoi(r.URL.Query().Get("shown"))
	follow := selected == 0 && err == nil
	render(w, r, pages.AppDetail(*app, api.NewDeploys(deploys, app.LiveNumber), selected, follow, shown, r.URL.Query().Get("in")))
}

// Rollback starts the release of an earlier deploy again. The page follows
// the new running deploy on its own.
func (h *AppsHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	number, _ := strconv.Atoi(r.FormValue("number"))
	deploy, err := h.apps.Rollback(actor(r), r.PathValue("app"), number)
	if err != nil {
		failed(w, r, "Rollback not started", err, nil)
		return
	}
	h.Detail(w, r)
	answerDone(w, r, "Rollback started", fmt.Sprintf("v%d starts v%d again.", deploy.Number, number))
}

// Redeploy builds the newest source again. The page follows the new deploy.
func (h *AppsHandler) Redeploy(w http.ResponseWriter, r *http.Request) {
	deploy, err := h.apps.Redeploy(actor(r), r.PathValue("app"))
	if err != nil {
		failed(w, r, "Redeploy not started", err, nil)
		return
	}
	h.Detail(w, r)
	answerDone(w, r, "Redeploy started", fmt.Sprintf("v%d builds the newest source.", deploy.Number))
}

// Restart starts the live deploy again with the env and domains as they
// are now, without a build. The page follows the new deploy.
func (h *AppsHandler) Restart(w http.ResponseWriter, r *http.Request) {
	deploy, err := h.apps.Restart(actor(r), r.PathValue("app"))
	if err != nil {
		failed(w, r, "Restart not started", err, nil)
		return
	}
	h.Detail(w, r)
	answerDone(w, r, "Restart started", fmt.Sprintf("v%d starts the live deploy again.", deploy.Number))
}

// Start runs a stopped app again. The page follows the new deploy.
func (h *AppsHandler) Start(w http.ResponseWriter, r *http.Request) {
	deploy, err := h.apps.Start(actor(r), r.PathValue("app"))
	if errors.Is(err, service.ErrRunning) {
		// Started elsewhere: the page shows it running.
		h.Detail(w, r)
		answerDone(w, r, r.PathValue("app")+" is already running", "Nothing changed.")
		return
	}
	if err != nil {
		failed(w, r, "App not started", err, nil)
		return
	}
	h.Detail(w, r)
	answerDone(w, r, r.PathValue("app")+" starting", fmt.Sprintf("v%d runs it again.", deploy.Number))
}

// Stop stops the app's containers. Its data stays.
func (h *AppsHandler) Stop(w http.ResponseWriter, r *http.Request) {
	err := h.apps.Stop(r.Context(), actor(r), r.PathValue("app"))
	if err != nil {
		failed(w, r, "App not stopped", err, nil)
		return
	}
	h.Detail(w, r)
	answerDone(w, r, r.PathValue("app")+" stopped", "Its data and settings stay. Start runs it again.")
}

// DeployImage deploys an image built elsewhere. The page follows the new deploy.
func (h *AppsHandler) DeployImage(w http.ResponseWriter, r *http.Request) {
	deploy, err := h.apps.Deploy(actor(r), r.PathValue("app"), strings.TrimSpace(r.FormValue("image")))
	if err != nil {
		failed(w, r, "Image not deployed", err, map[string]string{"image": "image-ref"})
		return
	}
	h.Detail(w, r)
	answerDone(w, r, "Image deploy started", fmt.Sprintf("v%d pulls %s.", deploy.Number, deploy.Image))
}

// RotateHook shows a new deploy hook URL once.
func (h *AppsHandler) RotateHook(w http.ResponseWriter, r *http.Request) {
	before, err := h.apps.Get(ctxkeys.Workspace(r.Context()).ID, r.PathValue("app"))
	if err != nil {
		failed(w, r, "Deploy hook not created", err, nil)
		return
	}
	url, err := h.apps.RotateDeployHook(actor(r), before.Name)
	if err != nil {
		failed(w, r, "Deploy hook not created", err, nil)
		return
	}
	render(w, r, pages.AppHook(before.Name, url, true))
	if before.DeployHookHash == nil {
		answerDone(w, r, "Deploy hook created", "Copy the URL now: it shows only once.")
		return
	}
	answerDone(w, r, "Deploy hook created", "The old URL stopped working.")
}

// RemoveHook turns the deploy hook off; its URL stops working.
func (h *AppsHandler) RemoveHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("app")
	_, err := h.apps.RemoveDeployHook(actor(r), name)
	if err != nil {
		failed(w, r, "Deploy hook not turned off", err, nil)
		return
	}
	render(w, r, pages.AppHook(name, "", false))
	answerDone(w, r, "Deploy hook turned off", "CI can no longer deploy "+name+".")
}

// registryFields are the fields of the registry form, by API field name.
var registryFields = map[string]string{"host": "registry-host", "password": "registry-password"}

// SetRegistry saves a registry login from the settings form.
func (h *AppsHandler) SetRegistry(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.FormValue("host"))
	err := h.apps.SetRegistry(actor(r), host, r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		failed(w, r, "Login not saved", err, registryFields)
		return
	}
	h.renderRegistries(w, r)
	answerDone(w, r, "Login for "+host+" saved", "Your servers pull private images from it now.")
}

// DeleteRegistry removes a registry login.
func (h *AppsHandler) DeleteRegistry(w http.ResponseWriter, r *http.Request) {
	err := h.apps.RemoveRegistry(actor(r), r.PathValue("host"))
	if err != nil {
		failed(w, r, "Login not removed", err, nil)
		return
	}
	h.renderRegistries(w, r)
	answerDone(w, r, "Login for "+r.PathValue("host")+" removed", "")
}

func (h *AppsHandler) renderRegistries(w http.ResponseWriter, r *http.Request) {
	logins, err := h.apps.Registries(ctxkeys.Workspace(r.Context()).ID)
	if err != nil {
		renderError(w, "list registries failed", err)
		return
	}
	render(w, r, pages.SettingsRegistriesSection(logins))
}

// logLines is how many of the last lines of an app's containers the Logs
// section shows.
const logLines = 200

// Logs answers the last lines of the app's containers, or why they could
// not be read: the panel is their output, an error included.
func (h *AppsHandler) Logs(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	lines, err := h.apps.Logs(r.Context(), workspace.ID, r.PathValue("app"), logLines)
	if err != nil {
		render(w, r, pages.AppLogs(nil, errorMessage(err)))
		return
	}
	render(w, r, pages.AppLogs(lines, ""))
}

// Env answers the env table; with show=1 the values are visible, with
// text=1 they are one .env text to edit.
func (h *AppsHandler) Env(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("text") == "1" {
		name := r.PathValue("app")
		env, err := h.apps.Env(ctxkeys.Workspace(r.Context()).ID, name)
		if err != nil {
			renderError(w, "load env failed", err)
			return
		}
		render(w, r, pages.AppEnvText(name, dotenv.Format(env), ""))
		return
	}
	h.renderEnv(w, r, r.URL.Query().Get("show") == "1")
}

// EnvText takes the env vars as one .env text: first it shows what changes,
// with confirm=1 it replaces them, with back=1 it goes back to the text.
func (h *AppsHandler) EnvText(w http.ResponseWriter, r *http.Request) {
	name, text := r.PathValue("app"), r.FormValue("text")
	next, err := dotenv.Parse(text)
	if err != nil || r.FormValue("back") == "1" {
		problem := ""
		if err != nil {
			problem = strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + ". Nothing changed."
		}
		render(w, r, pages.AppEnvText(name, text, problem))
		return
	}
	if r.FormValue("confirm") != "1" {
		current, err := h.apps.Env(ctxkeys.Workspace(r.Context()).ID, name)
		if err != nil {
			renderError(w, "load env failed", err)
			return
		}
		change := dotenv.Diff(current, next)
		if change.None() {
			h.renderEnv(w, r, false)
			answerDone(w, r, "Nothing changed", "The text has the env vars there are.")
			return
		}
		render(w, r, pages.AppEnvReview(name, text, change))
		return
	}
	change, err := h.apps.ReplaceEnv(actor(r), name, next)
	if err != nil {
		render(w, r, pages.AppEnvText(name, text, errorMessage(err)+" Nothing changed."))
		return
	}
	h.renderEnv(w, r, false)
	answerDone(w, r, "Env vars replaced", "It "+change.String()+". The next deploy or restart hands them to the app.")
}

// SetEnv sets one env var from the add form.
func (h *AppsHandler) SetEnv(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.FormValue("key"))
	_, err := h.apps.SetEnv(actor(r), r.PathValue("app"), map[string]string{key: r.FormValue("value")}, nil)
	if errors.Is(err, service.ErrEnvValue) {
		fieldError(w, key+" not saved", "env-value", errorMessage(err))
		return
	}
	if err != nil {
		failed(w, r, "Variable not saved", err, map[string]string{"set": "env-key"})
		return
	}
	h.renderEnv(w, r, false)
	answerDone(w, r, key+" saved", "The next deploy or restart hands it to the app.")
}

// UnsetEnv removes one env var.
func (h *AppsHandler) UnsetEnv(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	_, err := h.apps.SetEnv(actor(r), r.PathValue("app"), nil, []string{key})
	if err != nil {
		failed(w, r, key+" not removed", err, nil)
		return
	}
	h.renderEnv(w, r, false)
	answerDone(w, r, key+" removed", "The next deploy or restart drops it.")
}

func (h *AppsHandler) renderEnv(w http.ResponseWriter, r *http.Request, show bool) {
	name := r.PathValue("app")
	env, err := h.apps.Env(ctxkeys.Workspace(r.Context()).ID, name)
	if err != nil {
		renderError(w, "load env failed", err)
		return
	}
	values := map[string]string{}
	if show {
		values = env
	}
	render(w, r, pages.AppEnv(name, sortedKeys(env), values))
}

// Delete stops the app on its server and removes it, then goes to the list.
func (h *AppsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	err := h.apps.Delete(r.Context(), actor(r), r.PathValue("app"))
	if err != nil {
		failed(w, r, "App not removed", err, nil)
		return
	}
	w.Header().Set("HX-Redirect", ctxkeys.AppBasePath(r.Context())+"/apps")
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// DomainsDetail answers the domains of an app, its instant address and its
// password. It looks up no DNS, but the domain in ?check= (its Check
// button).
func (h *AppsHandler) DomainsDetail(w http.ResponseWriter, r *http.Request) {
	h.renderDomains(w, r, r.URL.Query().Get("check"))
}

// AddDomain adds one domain, to the main service or the one named, and
// applies it.
func (h *AppsHandler) AddDomain(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimSpace(r.FormValue("domain"))
	service := strings.TrimSpace(r.FormValue("service"))
	h.changeDomains(w, r, service, domain, domain+" added", "Domain not added", func(domains []string, canonical string) ([]string, string) {
		return append(domains, domain), canonical
	})
}

// RemoveDomain removes one domain and applies it.
func (h *AppsHandler) RemoveDomain(w http.ResponseWriter, r *http.Request) {
	domain := r.FormValue("domain")
	h.changeDomains(w, r, r.FormValue("service"), "", domain+" removed", domain+" not removed", func(domains []string, canonical string) ([]string, string) {
		if canonical == domain {
			canonical = ""
		}
		return slices.DeleteFunc(domains, func(d string) bool { return d == domain }), canonical
	})
}

// MakeCanonical lets the other domains redirect to one.
func (h *AppsHandler) MakeCanonical(w http.ResponseWriter, r *http.Request) {
	domain := r.FormValue("domain")
	done := domain + " is canonical"
	if domain == "" {
		done = "No canonical domain"
	}
	h.changeDomains(w, r, "", "", done, "Canonical domain not changed", func(domains []string, _ string) ([]string, string) {
		return domains, domain
	})
}

// changeDomains saves the domains of a service, the main one for "";
// check is a domain to look up for the answer, the one added.
func (h *AppsHandler) changeDomains(w http.ResponseWriter, r *http.Request, service, check, done, notDone string, change func([]string, string) ([]string, string)) {
	workspace := ctxkeys.Workspace(r.Context())
	app, err := h.apps.Get(workspace.ID, r.PathValue("app"))
	if err != nil {
		renderError(w, "load app failed", err)
		return
	}
	domains, canonical := change(app.DomainsOf(service))
	changed, err := h.apps.SetDomains(actor(r), app.ID, service, domains, canonical)
	if err != nil {
		failed(w, r, notDone, err, map[string]string{"domains": "domain-name", "service": "domain-service"})
		return
	}
	h.domainsChanged(w, r, done, changed, check)
}

// SetInstant picks the provider of the instant address, or off. Its
// switch (switch=1) turns it on with the first provider, or off; a failed
// switch flips back.
func (h *AppsHandler) SetInstant(w http.ResponseWriter, r *http.Request) {
	provider := r.FormValue("provider")
	if r.FormValue("switch") == "1" {
		provider = "off"
		if r.FormValue("on") == "1" {
			provider = protocol.InstantProviders[0]
		}
	}
	changed, err := h.apps.SetInstant(actor(r), r.PathValue("app"), provider, nil)
	if err != nil {
		if _, ok := h.renderDomains(w, r, ""); ok {
			answerError(w, r, "Instant address not changed", errorMessage(err))
		}
		return
	}
	done := "Instant address via " + provider
	if provider == "off" {
		done = "Instant address off"
	}
	h.domainsChanged(w, r, done, changed, "")
}

// SetInstantPassword is the switch that puts the password in front of the
// instant address, or takes it away. It answers the panel as stored, so a
// failed save flips the switch back.
func (h *AppsHandler) SetInstantPassword(w http.ResponseWriter, r *http.Request) {
	open := r.FormValue("on") != "1"
	changed, err := h.apps.SetInstant(actor(r), r.PathValue("app"), "", &open)
	done := "Password on the instant address"
	if open {
		done = "Instant address open to everyone"
	}
	switch {
	case err != nil:
		if _, ok := h.renderDomains(w, r, ""); ok {
			answerError(w, r, "Password not changed", errorMessage(err))
		}
	default:
		h.domainsChanged(w, r, done, changed, "")
	}
}

// Protect puts a password in front of the app. A wrong field says the
// rule of its hint line again, in red.
func (h *AppsHandler) Protect(w http.ResponseWriter, r *http.Request) {
	changed, err := false, service.ErrProtect
	if r.FormValue("password") != "" {
		changed, err = h.apps.SetProtection(actor(r), r.PathValue("app"), r.FormValue("username"), r.FormValue("password"))
	}
	switch {
	case errors.Is(err, service.ErrProtectUser):
		fieldError(w, "Password not set", "protect-user", "No spaces or colons.")
		return
	case errors.Is(err, service.ErrProtect):
		fieldError(w, "Password not set", "protect-password", "Use 8 to 72 characters.")
		return
	case err != nil:
		failed(w, r, "Password not set", err, nil)
		return
	}
	h.domainsChanged(w, r, "Password set", changed, "")
}

// Unprotect makes the app public again.
func (h *AppsHandler) Unprotect(w http.ResponseWriter, r *http.Request) {
	changed, err := h.apps.SetProtection(actor(r), r.PathValue("app"), "", "")
	if err != nil {
		failed(w, r, "Password not removed", err, nil)
		return
	}
	h.domainsChanged(w, r, "Password removed", changed, "")
}

// domainsChanged answers a saved change of the domains or the password: the
// panel as it is now and a check on the button. The app takes it on its
// next restart; its head says so (changes waiting). check is the domain
// added: the answer says whether it points at the server, or the record
// to set.
func (h *AppsHandler) domainsChanged(w http.ResponseWriter, r *http.Request, done string, changed bool, check string) {
	view, ok := h.renderDomains(w, r, check)
	if !ok {
		return
	}
	if !changed {
		answerDone(w, r, "Nothing changed", "")
		return
	}
	answerDone(w, r, done, pointsNote(view.Checks, check))
}

// pointsNote says whether the domain checked points at the server, or the
// record it needs.
func pointsNote(checks []service.DomainCheck, domain string) string {
	for _, check := range checks {
		if check.Domain != domain || !check.Checked {
			continue
		}
		if check.Points {
			return "It points at the server."
		}
		return "Set its " + check.Advice + "."
	}
	return ""
}

// renderDomains renders the domains, the instant address and the password,
// looking up only the domain check.
func (h *AppsHandler) renderDomains(w http.ResponseWriter, r *http.Request, check string) (pages.DomainsView, bool) {
	view, err := h.domainsView(r, check)
	if err != nil {
		renderError(w, "load domains failed", err)
		return view, false
	}
	render(w, r, pages.AppDomains(view))
	return view, true
}

// domainsView is what the Domains section shows. It looks up no DNS but
// the domain check, so opening the page waits on no lookup.
func (h *AppsHandler) domainsView(r *http.Request, check string) (pages.DomainsView, error) {
	workspace := ctxkeys.Workspace(r.Context())
	app, err := h.apps.Get(workspace.ID, r.PathValue("app"))
	if err != nil {
		return pages.DomainsView{}, err
	}
	var checks []service.DomainCheck
	if check != "" {
		checks, err = h.apps.CheckDomain(r.Context(), workspace.ID, app.ID, check)
	} else {
		checks, err = h.apps.Domains(workspace.ID, app.ID)
	}
	if err != nil {
		return pages.DomainsView{}, err
	}
	instant, err := h.apps.Instant(workspace.ID, app.ID, r.URL.Query().Get("show") == "true", ctxkeys.APIToken(r.Context()))
	if err != nil {
		return pages.DomainsView{}, err
	}
	view := pages.DomainsView{App: *app, Checks: checks, Instant: instant}
	if server, err := h.servers.Get(workspace.ID, app.ServerID); err == nil {
		view.Server = *server
	}
	return view, nil
}

// actor is the member who sends the request, in the workspace of its path.
func actor(r *http.Request) service.Actor {
	return service.Actor{WorkspaceID: ctxkeys.Workspace(r.Context()).ID, UserID: ctxkeys.User(r.Context()).ID, Source: service.RequestSource(r.Context())}
}
