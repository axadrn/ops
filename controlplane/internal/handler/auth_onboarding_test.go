package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

type onboardingTest struct {
	handler    *authHandler
	workspaces *service.WorkspaceService
	owner      testutil.UserFixture
	mailer     *testutil.MemoryMailer
	users      *repository.Users
}

func newOnboardingTest(t *testing.T) onboardingTest {
	t.Helper()
	database, workspaces, owner, mailer := invitationHandlerTestSetup(t)
	email := service.NewEmailService(testutil.NewMemoryMailer(), "https://app.example.com", "Test App")
	users := repository.NewUsers(database)
	auth := service.NewAuthService(users, nil, nil, workspaces, email, false, time.Hour, time.Hour, time.Hour, time.Hour, time.Hour)
	handler := NewAuthHandler(auth, nil, workspaces, &config.Config{})
	return onboardingTest{handler: handler, workspaces: workspaces, owner: owner, mailer: mailer, users: users}
}

func (o onboardingTest) newUser(t *testing.T, id string) *model.User {
	t.Helper()
	now := time.Now()
	user := &model.User{ID: id, Email: id + "@example.com", CreatedAt: now}
	if err := o.users.Create(user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func (o onboardingTest) invite(t *testing.T, user *model.User) string {
	t.Helper()
	if err := o.workspaces.Invite(o.owner.WorkspaceMember, o.owner.Workspace, user.Email, model.WorkspaceRoleMember, service.Source{Via: "web"}); err != nil {
		t.Fatalf("Invite() error = %v", err)
	}
	return invitationToken(t, o.mailer)
}

func onboardingRequest(method string, user *model.User, form url.Values, cookies ...*http.Cookie) *http.Request {
	request := httptest.NewRequest(method, "/auth/onboarding", strings.NewReader(form.Encode()))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	ctx := ctxkeys.WithRenderConfig(request.Context(), config.RenderConfig{})
	return request.WithContext(ctxkeys.WithUser(ctx, user))
}

// githubCookie is the cookie the GitHub callback sets for user.
func githubCookie(t *testing.T, userID, name string) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	setGitHubNameCookie(recorder, httptest.NewRequest(http.MethodGet, "/auth/github/callback", nil).WithContext(ctxkeys.WithRenderConfig(t.Context(), config.RenderConfig{})), userID, name)
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != "/auth" {
		t.Fatalf("GitHub name cookie = %#v", cookies)
	}
	return cookies[0]
}

func TestOnboardingPageFillsInTheGitHubName(t *testing.T) {
	test := newOnboardingTest(t)
	user := test.newUser(t, "github-user")

	recorder := httptest.NewRecorder()
	test.handler.OnboardingPage(recorder, onboardingRequest(http.MethodGet, user, nil, githubCookie(t, user.ID, "Grace Hopper")))

	body := recorder.Body.String()
	for _, want := range []string{`name="name"`, `value="Grace Hopper"`, `name="workspace"`, `value="Grace Hopper&#39;s Workspace"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("page misses %s", want)
		}
	}
}

func TestOnboardingPageIsEmptyForEmailAndForeignCookies(t *testing.T) {
	test := newOnboardingTest(t)
	user := test.newUser(t, "email-user")

	for name, cookies := range map[string][]*http.Cookie{
		"email":                 nil,
		"another user's cookie": {githubCookie(t, "someone-else", "Mallory")},
	} {
		recorder := httptest.NewRecorder()
		test.handler.OnboardingPage(recorder, onboardingRequest(http.MethodGet, user, nil, cookies...))
		body := recorder.Body.String()
		if !strings.Contains(body, `name="workspace"`) || strings.Contains(body, "Mallory") || strings.Contains(body, `&#39;s Workspace"`) {
			t.Fatalf("%s: page is not empty with both fields", name)
		}
	}
}

func TestOnboardingPageAsksInvitedPeopleOnlyForTheirName(t *testing.T) {
	test := newOnboardingTest(t)
	user := test.newUser(t, "invited-user")
	token := test.invite(t, user)

	recorder := httptest.NewRecorder()
	test.handler.OnboardingPage(recorder, onboardingRequest(http.MethodGet, user, nil, &http.Cookie{Name: "pending_invitation", Value: token}))

	body := recorder.Body.String()
	if !strings.Contains(body, `name="name"`) || strings.Contains(body, `name="workspace"`) {
		t.Fatal("invited page must ask only for the name")
	}
}

func TestOnboardingCreatesTheNamedWorkspace(t *testing.T) {
	test := newOnboardingTest(t)
	user := test.newUser(t, "named-user")

	recorder := httptest.NewRecorder()
	test.handler.CompleteOnboarding(recorder, onboardingRequest(http.MethodPost, user, url.Values{"name": {"Grace Hopper"}, "workspace": {"Acme Labs"}}, githubCookie(t, user.ID, "Grace Hopper")))

	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/app/acme-labs/overview" {
		t.Fatalf("status = %d, location = %q", recorder.Code, recorder.Header().Get("Location"))
	}
	cleared := false
	for _, cookie := range recorder.Result().Cookies() {
		cleared = cleared || (cookie.Name == githubNameCookie && cookie.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("the GitHub name cookie stays after onboarding")
	}
	saved, err := test.users.ByID(user.ID)
	if err != nil || saved.Name != "Grace Hopper" {
		t.Fatalf("name = %q, %v", saved.Name, err)
	}
}

func TestOnboardingNeedsBothFieldsWithoutInvitation(t *testing.T) {
	test := newOnboardingTest(t)
	user := test.newUser(t, "empty-user")

	for _, form := range []struct {
		values url.Values
		field  string
	}{
		{url.Values{"name": {""}, "workspace": {"Acme"}}, "name"},
		{url.Values{"name": {"Grace"}, "workspace": {""}}, "workspace"},
		{url.Values{"name": {"Grace"}, "workspace": {"x"}}, "workspace"},
	} {
		request := onboardingRequest(http.MethodPost, user, form.values)
		request.Header.Set("HX-Request", "true")
		recorder := httptest.NewRecorder()
		test.handler.CompleteOnboarding(recorder, request)
		if !strings.Contains(recorder.Header().Get("HX-Trigger"), `"field":"`+form.field+`"`) {
			t.Fatalf("%v: HX-Trigger = %q, want field %s", form.values, recorder.Header().Get("HX-Trigger"), form.field)
		}
	}
	saved, err := test.users.ByID(user.ID)
	if err != nil || saved.Name != "" {
		t.Fatalf("name = %q, %v, want nothing saved", saved.Name, err)
	}
}

func TestGitHubNameFallsBackToTheLogin(t *testing.T) {
	for profile, want := range map[string]string{
		`{"email":"a@example.com","name":"Grace Hopper","login":"grace"}`: "Grace Hopper",
		`{"email":"a@example.com","name":null,"login":"grace"}`:           "grace",
		`{"email":"a@example.com","name":"  ","login":"grace"}`:           "grace",
	} {
		client := &http.Client{Transport: fakeGitHubAPI{
			"https://api.github.com/user":        profile,
			"https://api.github.com/user/emails": `[{"email":"a@example.com","primary":true,"verified":true}]`,
		}}
		_, name, err := githubAccount(client, "https://api.github.com")
		if err != nil || name != want {
			t.Errorf("githubAccount(%s) name = %q, %v, want %q", profile, name, err, want)
		}
	}
}
