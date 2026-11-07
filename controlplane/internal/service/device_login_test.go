package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

// deviceClock is a clock the tests move by hand.
type deviceClock struct{ now time.Time }

func (c *deviceClock) Now() time.Time { return c.now }

func (c *deviceClock) Add(d time.Duration) { c.now = c.now.Add(d) }

type deviceLoginEnvironment struct {
	logins   *DeviceLoginService
	tokens   *APITokenService
	clock    *deviceClock
	user     testutil.UserFixture
	activity *repository.Activity
}

func newDeviceLoginEnvironment(t *testing.T) deviceLoginEnvironment {
	t.Helper()
	environment := newAuthFlowEnvironment(t)
	user := testutil.SeedUser(t, environment.database, testutil.UserOptions{ID: "ada", Name: "Ada", Verified: true})
	clock := &deviceClock{now: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}
	tokens := NewAPITokenService(repository.NewAPITokens(environment.database), environment.workspaceService)
	tokens.now = clock.Now
	logins := NewDeviceLoginService(repository.NewDeviceLogins(environment.database), tokens)
	logins.now = clock.Now
	return deviceLoginEnvironment{logins: logins, tokens: tokens, clock: clock, user: user, activity: repository.NewActivity(environment.database)}
}

func (e deviceLoginEnvironment) start(t *testing.T) StartedDeviceLogin {
	t.Helper()
	started, err := e.logins.Start("host-1")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return started
}

// poll waits the interval and polls once.
func (e deviceLoginEnvironment) poll(started StartedDeviceLogin) (*model.APIToken, string, error) {
	e.clock.Add(time.Duration(started.Interval) * time.Second)
	return e.logins.Token(started.DeviceCode, Source{Via: ViaCLI})
}

func TestDeviceLoginGivesTheTokenOnceAfterApproval(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	started := e.start(t)
	if len(started.UserCode) != 9 || started.UserCode[4] != '-' || strings.ContainsAny(started.UserCode, "01OIL") {
		t.Fatalf("user code = %q, want ABCD-EFGH without look-alikes", started.UserCode)
	}
	if started.Interval != DeviceLoginInterval || !started.ExpiresAt.Equal(e.clock.now.Add(DeviceLoginLifetime)) {
		t.Fatalf("interval %d, expires %s", started.Interval, started.ExpiresAt)
	}

	_, _, err := e.poll(started)
	if !errors.Is(err, ErrDeviceLoginPending) {
		t.Fatalf("poll before approval = %v, want pending", err)
	}

	// The user types the code in any case, with or without the dash.
	pending, err := e.logins.Pending(strings.ToLower(strings.ReplaceAll(started.UserCode, "-", "")))
	if err != nil || pending.Hostname != "host-1" {
		t.Fatalf("Pending() = %+v, %v", pending, err)
	}
	_, err = e.logins.Approve(e.user.User.ID, started.UserCode, e.user.Workspace.ID)
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if _, err := e.logins.Approve(e.user.User.ID, started.UserCode, e.user.Workspace.ID); !errors.Is(err, ErrUserCodeNotFound) {
		t.Fatalf("second Approve() = %v, want the code used", err)
	}

	token, raw, err := e.poll(started)
	if err != nil {
		t.Fatalf("poll after approval error = %v", err)
	}
	if !strings.HasPrefix(raw, APITokenMarker) || token.Name != "CLI on host-1" || !token.IsDevice() || !token.CanWrite() || token.WorkspaceID != e.user.Workspace.ID {
		t.Fatalf("token = %+v, raw %q", token, raw)
	}
	if token.ExpiresAt == nil || !token.ExpiresAt.Equal(e.clock.now.Add(model.DeviceTokenIdle)) {
		t.Fatalf("expires_at = %v, want 90 days from now", token.ExpiresAt)
	}
	authenticated, err := e.tokens.Authenticate(raw)
	if err != nil || authenticated.ID != token.ID {
		t.Fatalf("Authenticate() = %+v, %v", authenticated, err)
	}

	if _, _, err := e.poll(started); !errors.Is(err, ErrDeviceLoginInvalid) {
		t.Fatalf("poll after the token = %v, want invalid", err)
	}
	events, err := e.activity.ListByWorkspace(e.user.Workspace.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "api_token.created" && strings.Contains(event.Metadata, `"kind":"device"`) && !strings.Contains(event.Metadata, raw) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no activity for the device token in %+v", events)
	}
}

func TestDeviceLoginDenied(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	started := e.start(t)
	if _, err := e.logins.Deny(e.user.User.ID, started.UserCode); err != nil {
		t.Fatalf("Deny() error = %v", err)
	}
	if _, err := e.logins.Approve(e.user.User.ID, started.UserCode, e.user.Workspace.ID); !errors.Is(err, ErrUserCodeNotFound) {
		t.Fatalf("Approve() after Deny() = %v, want not found", err)
	}
	if _, _, err := e.poll(started); !errors.Is(err, ErrDeviceLoginDenied) {
		t.Fatalf("poll = %v, want denied", err)
	}
	if _, _, err := e.poll(started); !errors.Is(err, ErrDeviceLoginInvalid) {
		t.Fatalf("poll after denied = %v, want invalid", err)
	}
}

func TestDeviceLoginExpires(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	started := e.start(t)
	e.clock.Add(DeviceLoginLifetime)
	if _, err := e.logins.Approve(e.user.User.ID, started.UserCode, e.user.Workspace.ID); !errors.Is(err, ErrUserCodeNotFound) {
		t.Fatalf("Approve() after expiry = %v, want not found", err)
	}
	if _, _, err := e.poll(started); !errors.Is(err, ErrDeviceLoginExpired) {
		t.Fatalf("poll after expiry = %v, want expired", err)
	}
}

func TestDeviceLoginSlowsDownFastPolls(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	started := e.start(t)
	if _, _, err := e.poll(started); !errors.Is(err, ErrDeviceLoginPending) {
		t.Fatalf("first poll = %v", err)
	}
	e.clock.Add(time.Second)
	if _, _, err := e.logins.Token(started.DeviceCode, Source{}); !errors.Is(err, ErrDeviceLoginSlowDown) {
		t.Fatalf("fast poll = %v, want slow_down", err)
	}
	// The interval grew to ten seconds: five are too few now.
	e.clock.Add(5 * time.Second)
	if _, _, err := e.logins.Token(started.DeviceCode, Source{}); !errors.Is(err, ErrDeviceLoginSlowDown) {
		t.Fatalf("poll after five seconds = %v, want slow_down", err)
	}
	e.clock.Add(15 * time.Second)
	if _, _, err := e.logins.Token(started.DeviceCode, Source{}); !errors.Is(err, ErrDeviceLoginPending) {
		t.Fatalf("poll after the longer interval = %v, want pending", err)
	}
}

func TestDeviceLoginRefusesWrongCodes(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	started := e.start(t)
	for _, code := range []string{"", "ABCD-EFGH", "short", started.UserCode + "X", strings.Repeat("A", 200)} {
		if _, err := e.logins.Approve(e.user.User.ID, code, e.user.Workspace.ID); !errors.Is(err, ErrUserCodeNotFound) {
			t.Errorf("Approve(%q) = %v, want not found", code, err)
		}
	}
	for _, code := range []string{"", "nope", started.DeviceCode + "0"} {
		if _, _, err := e.logins.Token(code, Source{}); !errors.Is(err, ErrDeviceLoginInvalid) {
			t.Errorf("Token(%q) = %v, want invalid", code, err)
		}
	}
	// Another user's workspace is not one of Ada's.
	if _, err := e.logins.Approve(e.user.User.ID, started.UserCode, "workspace-someone"); !errors.Is(err, ErrAPITokenWorkspace) {
		t.Fatalf("Approve() into a foreign workspace = %v, want ErrAPITokenWorkspace", err)
	}
	if _, err := e.logins.Pending(started.UserCode); err != nil {
		t.Fatalf("the code stays pending after a refused approval: %v", err)
	}
}

func TestDeviceTokenUseExtendsItsExpiry(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	_, raw, err := e.tokens.CreateForDevice(e.user.User.ID, e.user.Workspace.ID, "host-1", Source{Via: ViaCLI})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Add(80 * 24 * time.Hour)
	token, err := e.tokens.Authenticate(raw)
	if err != nil {
		t.Fatalf("Authenticate() after 80 days = %v", err)
	}
	if !token.ExpiresAt.Equal(e.clock.now.Add(model.DeviceTokenIdle)) {
		t.Fatalf("expires_at = %s, want 90 days after this use", token.ExpiresAt)
	}
	e.clock.Add(80 * 24 * time.Hour)
	if _, err := e.tokens.Authenticate(raw); err != nil {
		t.Fatalf("Authenticate() 80 days after the last use = %v", err)
	}
	e.clock.Add(model.DeviceTokenIdle)
	if _, err := e.tokens.Authenticate(raw); !errors.Is(err, repository.ErrAPITokenNotFound) {
		t.Fatalf("Authenticate() 90 days without use = %v, want not found", err)
	}

	// A personal token keeps its expiry when used.
	personal, rawPersonal, err := e.tokens.Create(e.user.User.ID, CreateAPITokenInput{Name: "ci", Scopes: []string{"read"}, ExpiresInDays: 7, WorkspaceID: e.user.Workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Add(time.Hour)
	used, err := e.tokens.Authenticate(rawPersonal)
	if err != nil || !used.ExpiresAt.Equal(*personal.ExpiresAt) {
		t.Fatalf("personal token after use = %+v, %v", used, err)
	}
}

func TestRevokedDeviceTokenIsRejected(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	token, raw, err := e.tokens.CreateForDevice(e.user.User.ID, e.user.Workspace.ID, "host-1", Source{Via: ViaCLI})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.tokens.Revoke(e.user.User.ID, token.ID, Source{Via: ViaWeb}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tokens.Authenticate(raw); !errors.Is(err, repository.ErrAPITokenNotFound) {
		t.Fatalf("Authenticate() after revoke = %v, want not found", err)
	}
}

func TestDeviceTokenName(t *testing.T) {
	for hostname, want := range map[string]string{
		"host-1":                 "CLI on host-1",
		"  ":                     "CLI",
		"bad\x00\nhost":          "CLI on badhost",
		strings.Repeat("a", 100): "CLI on " + strings.Repeat("a", 57),
	} {
		if got := DeviceTokenName(hostname); got != want {
			t.Errorf("DeviceTokenName(%q) = %q, want %q", hostname, got, want)
		}
	}
}

func TestDeviceLoginReplacesTheComputersLastLogin(t *testing.T) {
	e := newDeviceLoginEnvironment(t)
	login := func() (*model.APIToken, string) {
		t.Helper()
		started := e.start(t)
		if _, err := e.logins.Approve(e.user.User.ID, started.UserCode, e.user.Workspace.ID); err != nil {
			t.Fatalf("Approve() error = %v", err)
		}
		token, raw, err := e.poll(started)
		if err != nil {
			t.Fatalf("poll error = %v", err)
		}
		return token, raw
	}
	_, firstRaw := login()
	personal, personalRaw, err := e.tokens.Create(e.user.User.ID, CreateAPITokenInput{Name: "CLI on host-1", WorkspaceID: e.user.Workspace.ID})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	second, secondRaw := login()

	if _, err := e.tokens.Authenticate(firstRaw); err == nil {
		t.Fatal("the first login still works after logging in again")
	}
	if token, err := e.tokens.Authenticate(secondRaw); err != nil || token.ID != second.ID {
		t.Fatalf("the new login = %+v, %v", token, err)
	}
	if token, err := e.tokens.Authenticate(personalRaw); err != nil || token.ID != personal.ID {
		t.Fatalf("a personal token with the same name was revoked: %+v, %v", token, err)
	}
	tokens, err := e.tokens.List(e.user.User.ID)
	if err != nil || len(tokens) != 2 {
		t.Fatalf("List() = %d tokens, %v, want the new login and the personal token", len(tokens), err)
	}
}
