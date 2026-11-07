package handler

import (
	"errors"
	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/testutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const tokenRevocationPassword = "correct horse battery staple"

// tokenRevocationTest holds the services of one HTML handler test and a user
// with a password and one personal API token.
type tokenRevocationTest struct {
	auth     *service.AuthService
	tokens   *service.APITokenService
	mailer   *testutil.MemoryMailer
	account  *AccountHandler
	login    *authHandler
	user     testutil.UserFixture
	rawToken string
}

func newTokenRevocationTest(t *testing.T) *tokenRevocationTest {
	t.Helper()
	database := testutil.NewDatabase(t)
	users := repository.NewUsers(database)
	mailer := testutil.NewMemoryMailer()
	email := service.NewEmailService(mailer, "https://app.example.com", "Test App")
	test := &tokenRevocationTest{mailer: mailer}
	subscriptions := service.NewSubscriptionService(repository.NewSubscriptions(database), repository.NewAdmin(database))
	workspaces := service.NewWorkspaceService(
		repository.NewWorkspaces(database),
		repository.NewWorkspaceMembers(database),
		repository.NewWorkspaceInvitations(database),
		repository.NewActivity(database),
		users,
		subscriptions,
		email,
	)
	test.auth = service.NewAuthService(users, repository.NewTokens(database), repository.NewSessions(database), workspaces, email, false, time.Hour, time.Hour, time.Hour, time.Hour, time.Hour)
	userService := service.NewUserService(users, service.NewFileService(repository.NewFiles(database), testutil.NewMemoryStorage()), email, workspaces)
	test.tokens = service.NewAPITokenService(repository.NewAPITokens(database), workspaces)
	test.login = NewAuthHandler(test.auth, userService, workspaces, &config.Config{})
	test.auth.UseAPITokens(test.tokens)
	test.account = NewAccountHandler(test.auth, userService, nil)
	test.user = testutil.SeedUser(t, database, testutil.UserOptions{ID: "revoked", Email: "revoked@example.com", Name: "Revoked", Password: tokenRevocationPassword, Verified: true})
	test.newToken(t)
	return test
}

func (test *tokenRevocationTest) newToken(t *testing.T) {
	t.Helper()
	_, raw, err := test.tokens.Create(test.user.User.ID, service.CreateAPITokenInput{
		Name:        "Script",
		WorkspaceID: test.user.Workspace.ID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	test.rawToken = raw
}

func (test *tokenRevocationTest) expectTokenRevoked(t *testing.T) {
	t.Helper()
	_, err := test.tokens.Authenticate(test.rawToken)
	if !errors.Is(err, repository.ErrAPITokenNotFound) {
		t.Fatalf("Authenticate() error = %v, want the token revoked", err)
	}
}

// signedInRequest sends form values as the user with a fresh session.
func (test *tokenRevocationTest) signedInRequest(t *testing.T, method, path string, form url.Values) *http.Request {
	t.Helper()
	session, _, err := test.auth.CreateSession(httptest.NewRequest(http.MethodPost, "/auth/password", nil), test.user.User.ID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := ctxkeys.WithSession(request.Context(), session)
	ctx = ctxkeys.WithWorkspace(ctx, test.user.Workspace)
	return request.WithContext(ctxkeys.WithUser(ctx, test.user.User))
}

func TestForgotPasswordPageRevokesAPITokens(t *testing.T) {
	test := newTokenRevocationTest(t)
	err := test.auth.SendForgotPasswordLink(test.user.User.Email)
	if err != nil {
		t.Fatalf("SendForgotPasswordLink() error = %v", err)
	}
	messages := test.mailer.Messages()
	match := regexp.MustCompile(`/auth/forgot-password/([0-9a-f]{64})`).FindStringSubmatch(messages[len(messages)-1].Text)
	if match == nil {
		t.Fatal("no forgot password link was sent")
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/forgot-password/"+match[1], nil)
	request.SetPathValue("token", match[1])
	recorder := httptest.NewRecorder()

	test.login.VerifyForgotPassword(recorder, request)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	test.expectTokenRevoked(t)
}

func TestPasswordPagesRevokeAPITokens(t *testing.T) {
	test := newTokenRevocationTest(t)
	for _, change := range []struct {
		name    string
		handler http.HandlerFunc
		method  string
		form    url.Values
	}{
		{name: "change", handler: test.account.ChangePassword, method: http.MethodPost, form: url.Values{"current_password": {tokenRevocationPassword}, "new_password": {"another long passphrase"}, "confirm_password": {"another long passphrase"}}},
		{name: "remove", handler: test.account.RemovePassword, method: http.MethodDelete},
		{name: "set", handler: test.account.SetPassword, method: http.MethodPost, form: url.Values{"new_password": {tokenRevocationPassword}, "confirm_password": {tokenRevocationPassword}}},
	} {
		test.newToken(t)
		recorder := httptest.NewRecorder()

		change.handler(recorder, test.signedInRequest(t, change.method, "/app/account/password", change.form))

		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "Error") {
			t.Fatalf("%s: status = %d, body %s", change.name, recorder.Code, recorder.Body.String())
		}
		test.expectTokenRevoked(t)
	}
}
