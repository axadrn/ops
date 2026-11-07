package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/secret"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/testutil"
	"github.com/jmoiron/sqlx"
)

// switches is a workspace with a server and the app blog on it, and the
// handlers of every switch that saves itself.
type switches struct {
	t        *testing.T
	database *sqlx.DB
	ada      testutil.UserFixture
	apps     *service.AppService
	servers  *service.ServerService
	users    *service.UserService
	handler  *AppsHandler
}

func newSwitches(t *testing.T) *switches {
	t.Helper()
	database := testutil.NewDatabase(t)
	ada := testutil.SeedUser(t, database, testutil.UserOptions{ID: "ada", Name: "Ada", Verified: true})
	box, err := secret.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	servers := service.NewServerService(repository.NewServers(database), repository.NewActivity(database), "https://moin.example.com")
	apps := service.NewAppService(repository.NewApps(database, database), servers, box)
	users := service.NewUserService(repository.NewUsers(database), nil, nil, nil)
	s := &switches{t: t, database: database, ada: ada, apps: apps, servers: servers, users: users, handler: NewAppsHandler(apps, servers, users, nil)}
	server, _, err := servers.Create(s.actor(), "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Create(s.actor(), "blog", server.ID); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *switches) actor() service.Actor {
	return service.Actor{WorkspaceID: s.ada.Workspace.ID, UserID: s.ada.User.ID, Source: service.Source{Via: service.ViaWeb}}
}

// post sends the form of a switch as the page does and answers the body.
func (s *switches) post(pattern, path string, form url.Values, run http.HandlerFunc) string {
	s.t.Helper()
	request := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("HX-Request", "true")
	user, err := repository.NewUsers(s.database).ByID(s.ada.User.ID)
	if err != nil {
		s.t.Fatal(err)
	}
	ctx := context.Background()
	ctx = ctxkeys.WithRenderConfig(ctx, config.RenderConfig{SupportEmail: "support@example.com"})
	ctx = ctxkeys.WithURLPath(ctx, path)
	ctx = ctxkeys.WithCSRFToken(ctx, "csrf-token")
	ctx = ctxkeys.WithUser(ctx, user)
	ctx = ctxkeys.WithWorkspace(ctx, s.ada.Workspace)
	ctx = ctxkeys.WithWorkspaceMember(ctx, &model.WorkspaceMember{WorkspaceID: s.ada.Workspace.ID, UserID: "ada", Role: model.WorkspaceRoleOwner})
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+pattern, run)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request.WithContext(ctx))
	if recorder.Code != http.StatusOK {
		s.t.Fatalf("POST %s = %d: %s", path, recorder.Code, recorder.Body.String())
	}
	return recorder.Header().Get("HX-Reswap") + recorder.Body.String()
}

func (s *switches) app() *model.App {
	s.t.Helper()
	app, err := s.apps.Get(s.ada.Workspace.ID, "blog")
	if err != nil {
		s.t.Fatal(err)
	}
	return app
}

// switchOn says whether the answer renders the switch with the ID on: a
// real checkbox, so its checked attribute is the whole truth.
func switchOn(t *testing.T, body, id string) bool {
	t.Helper()
	start := strings.Index(body, `id="`+id+`"`)
	if start < 0 {
		t.Fatalf("the answer has no switch %s: %s", id, body)
	}
	input := body[strings.LastIndex(body[:start], "<input"):]
	input = input[:strings.Index(input, ">")]
	if !strings.Contains(input, `role="switch"`) {
		t.Fatalf("the switch %s is not a real checkbox: %s", id, input)
	}
	// Only the attributes count, not the classes such as checked:bg-primary.
	attributes := regexp.MustCompile(`class="[^"]*"`).ReplaceAllString(input, "")
	return regexp.MustCompile(`\bchecked\b`).MatchString(attributes)
}

func activity(t *testing.T, s *switches, action string) int {
	t.Helper()
	events, err := repository.NewActivity(s.database).ListPage(s.ada.Workspace.ID, model.ActivityFilter{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Action == action {
			count++
		}
	}
	return count
}

// The switch for deploy on push saves only that, without GitHub, and
// answers itself as stored; an app without a repo keeps it off.
func TestAutoDeploySwitch(t *testing.T) {
	s := newSwitches(t)
	path := "/app/" + s.ada.Workspace.Slug + "/apps/blog/repo/auto-deploy"
	pattern := "/app/{workspaceSlug}/apps/{app}/repo/auto-deploy"

	body := s.post(pattern, path, url.Values{"on": {"1"}}, s.handler.SetAutoDeploy)
	if switchOn(t, body, "repo-auto-deploy") || !strings.Contains(body, "Deploy on push not changed") || s.app().AutoDeploy {
		t.Fatalf("without a repo the switch went on: %s", body)
	}

	if _, err := s.apps.LinkRepo(context.Background(), s.actor(), "blog", "axadrn/blog", "main", true); err == nil {
		t.Fatal("linked without GitHub")
	}
	// No GitHub is set up here: linking needs it, the switch must not.
	linkWithoutGitHub(t, s, "axadrn/blog", "main")

	body = s.post(pattern, path, url.Values{}, s.handler.SetAutoDeploy)
	if switchOn(t, body, "repo-auto-deploy") || strings.Contains(body, "not changed") {
		t.Fatalf("off: %s", body)
	}
	if app := s.app(); app.AutoDeploy || app.Repo != "axadrn/blog" || app.Branch != "main" {
		t.Fatalf("after off the app is %+v", app)
	}
	body = s.post(pattern, path, url.Values{"on": {"1"}}, s.handler.SetAutoDeploy)
	if !switchOn(t, body, "repo-auto-deploy") || !s.app().AutoDeploy {
		t.Fatalf("on: %s", body)
	}
	// The same again changes nothing and records nothing.
	s.post(pattern, path, url.Values{"on": {"1"}}, s.handler.SetAutoDeploy)
	if got := activity(t, s, "repo.auto_deploy"); got != 2 {
		t.Fatalf("repo.auto_deploy recorded %d times, want 2", got)
	}
}

// linkWithoutGitHub stores a linked repo as GitHub would have confirmed it.
func linkWithoutGitHub(t *testing.T, s *switches, repo, branch string) {
	t.Helper()
	if err := repository.NewApps(s.database, s.database).SetRepo(s.app().ID, repo, branch, true, 7); err != nil {
		t.Fatal(err)
	}
}

// The email alerts switch answers itself as stored.
func TestEmailAlertsSwitch(t *testing.T) {
	s := newSwitches(t)
	handler := NewProfileHandler(s.users)
	path := "/app/" + s.ada.Workspace.Slug + "/profile/email-alerts"
	for _, on := range []bool{false, true} {
		form := url.Values{}
		if on {
			form.Set("on", "1")
		}
		body := s.post("/app/{workspaceSlug}/profile/email-alerts", path, form, handler.EmailAlerts)
		user, err := repository.NewUsers(s.database).ByID("ada")
		if err != nil {
			t.Fatal(err)
		}
		if switchOn(t, body, "email-alerts") != on || user.EmailAlerts != on || strings.Contains(body, "data-feedback") {
			t.Fatalf("on %v: stored %v, answer %s", on, user.EmailAlerts, body)
		}
	}
}

// The agent updates switch answers itself as stored and records the change.
func TestAgentUpdatesSwitch(t *testing.T) {
	s := newSwitches(t)
	handler := NewServersHandler(s.servers, s.users)
	path := "/app/" + s.ada.Workspace.Slug + "/servers/agent-updates"
	for _, on := range []bool{false, true} {
		form := url.Values{}
		if on {
			form.Set("on", "1")
		}
		body := s.post("/app/{workspaceSlug}/servers/agent-updates", path, form, handler.AgentUpdates)
		stored, err := s.servers.AutoUpdateAgents(s.ada.Workspace.ID)
		if err != nil {
			t.Fatal(err)
		}
		if switchOn(t, body, "agent-updates") != on || stored != on {
			t.Fatalf("on %v: stored %v, answer %s", on, stored, body)
		}
	}
	if got := activity(t, s, "workspace.agent_updates"); got != 2 {
		t.Fatalf("workspace.agent_updates recorded %d times, want 2", got)
	}
}

// The uptime switch of a domain pauses and resumes only its check and
// answers itself as stored; a domain the app does not have stays as it is.
func TestUptimeSwitch(t *testing.T) {
	s := newSwitches(t)
	if _, err := s.apps.SetDomains(s.actor(), "blog", "", []string{"blog.example.com"}, ""); err != nil {
		t.Fatal(err)
	}
	path := "/app/" + s.ada.Workspace.Slug + "/apps/blog/uptime/on"
	pattern := "/app/{workspaceSlug}/apps/{app}/uptime/on"
	for _, on := range []bool{false, true, false} {
		form := url.Values{"domain": {"blog.example.com"}}
		if on {
			form.Set("on", "1")
		}
		body := s.post(pattern, path, form, s.handler.UptimeOn)
		_, uptime, err := s.apps.Uptime(s.ada.Workspace.ID, "blog")
		if err != nil {
			t.Fatal(err)
		}
		if switchOn(t, body, "uptime-blog.example.com-on") != on || uptime.Domains[0].Paused == on {
			t.Fatalf("on %v: stored %+v, answer %s", on, uptime.Domains[0], body)
		}
	}
	if got := activity(t, s, "uptime.paused") + activity(t, s, "uptime.resumed"); got != 3 {
		t.Fatalf("uptime switch recorded %d times, want 3", got)
	}
	if body := s.post(pattern, path, url.Values{"domain": {"other.example.com"}, "on": {"1"}}, s.handler.UptimeOn); !strings.Contains(body, "Check of other.example.com not saved") {
		t.Fatalf("another domain: %s", body)
	}
}

// The instant password switch answers the routing panel as stored.
func TestInstantPasswordSwitch(t *testing.T) {
	s := newSwitches(t)
	path := "/app/" + s.ada.Workspace.Slug + "/apps/blog/routing/instant-password"
	pattern := "/app/{workspaceSlug}/apps/{app}/routing/instant-password"
	for _, on := range []bool{false, true} {
		form := url.Values{"password": {"1"}}
		if on {
			form.Set("on", "1")
		}
		s.post(pattern, path, form, s.handler.SetInstantPassword)
		if s.app().InstantOpen == on {
			t.Fatalf("password on %v: open %v", on, s.app().InstantOpen)
		}
	}
}
