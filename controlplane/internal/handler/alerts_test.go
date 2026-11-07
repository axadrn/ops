package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/secret"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

// The alerts page adds a webhook from its form with the events checked,
// shows the signing secret once, sends a test and removes it; a member
// gets no further than the list.
func TestAlertsPageForm(t *testing.T) {
	database := testutil.NewDatabase(t)
	ada := testutil.SeedUser(t, database, testutil.UserOptions{ID: "ada", Name: "Ada", Verified: true})
	box, err := secret.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	alerts := service.NewAlertService(service.NewEmailService(testutil.NewMemoryMailer(), "https://moin.example.com", "moin"), repository.NewWorkspaces(database), repository.NewWorkspaceMembers(database), repository.NewApps(database, database), repository.NewAlertDestinations(database), repository.NewActivity(database), box)
	handler := NewAlertsHandler(alerts)
	var got string
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = string(body)
	}))
	defer hook.Close()

	send := func(role, method, path string, form url.Values, run http.HandlerFunc) string {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("HX-Request", "true")
		ctx := context.Background()
		ctx = ctxkeys.WithRenderConfig(ctx, config.RenderConfig{SupportEmail: "support@example.com"})
		ctx = ctxkeys.WithURLPath(ctx, "/app/"+ada.Workspace.Slug+"/alerts")
		ctx = ctxkeys.WithCSRFToken(ctx, "csrf-token")
		ctx = ctxkeys.WithUser(ctx, ada.User)
		ctx = ctxkeys.WithWorkspace(ctx, ada.Workspace)
		ctx = ctxkeys.WithWorkspaceMember(ctx, &model.WorkspaceMember{WorkspaceID: ada.Workspace.ID, UserID: "ada", Role: role})
		mux := http.NewServeMux()
		mux.HandleFunc(method+" /app/{workspaceSlug}/alerts/destinations/{destination}/test", run)
		mux.HandleFunc(method+" /app/{workspaceSlug}/alerts/destinations/{destination}", run)
		mux.HandleFunc(method+" /app/{workspaceSlug}/alerts/destinations", run)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request.WithContext(ctx))
		return recorder.Header().Get("HX-Trigger") + recorder.Body.String()
	}

	base := "/app/" + ada.Workspace.Slug + "/alerts/destinations"
	page := send(model.WorkspaceRoleOwner, "POST", base, url.Values{"kind": {"webhook"}, "name": {"ci"}, "url": {hook.URL + "/in"}, "events": {"deploy.failed", "uptime.down"}}, handler.Add)
	for _, want := range []string{`data-alert-destination="ci"`, "data-signing-secret", "Site down, Deploy failed", "ci added"} {
		if !strings.Contains(page, want) {
			t.Errorf("after add, the page has no %q", want)
		}
	}
	if page := send(model.WorkspaceRoleOwner, "POST", base, url.Values{"kind": {"slack"}, "name": {"team"}, "url": {"https://example.com"}}, handler.Add); !strings.Contains(page, "alert-url") {
		t.Errorf("a wrong Slack URL does not mark the field: %s", page)
	}
	if page := send(model.WorkspaceRoleOwner, "POST", base+"/ci/test", nil, handler.Test); !strings.Contains(page, "Test sent") || !strings.Contains(got, `"event":"test"`) {
		t.Errorf("test: page %q, webhook got %q", page, got)
	}
	if page := send(model.WorkspaceRoleMember, "POST", base, url.Values{"kind": {"webhook"}, "name": {"mine"}, "url": {hook.URL}}, handler.Add); !strings.Contains(page, "Your workspace role does not allow this action.") {
		t.Errorf("a member added a destination: %s", page)
	}
	if page := send(model.WorkspaceRoleOwner, "DELETE", base+"/ci", nil, handler.Remove); !strings.Contains(page, "ci removed") || strings.Contains(page, `data-alert-destination="ci"`) {
		t.Errorf("remove: %s", page)
	}
}
