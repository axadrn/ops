package layouts

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
)

// The panel has one quiet bar and no sidebar or menus: the wordmark, the
// search that opens the palette, and your picture, which opens Settings.
func TestAppLayoutHasTheBarAndNoSidebar(t *testing.T) {
	page := renderAppLayout(t, model.WorkspaceRoleOwner, 2)
	for _, hook := range []string{`id="app-bar"`, `id="bar-avatar"`, `href="/app/first-workspace/settings"`, "data-open-palette", `id="palette"`, `id="search"`, `id="search-results"`, "data-app-base=", `id="app-shortcuts"`} {
		if !strings.Contains(page, hook) {
			t.Fatalf("the bar lost %s", hook)
		}
	}
	for _, gone := range []string{`data-slot="sidebar`, "window.templ.sidebar", "app-command", "popovertarget=", "Second Workspace", "Billing"} {
		if strings.Contains(page, gone) {
			t.Fatalf("the panel still has %s", gone)
		}
	}
}

func TestBaseLayoutShowsReadOnlyImpersonationBanner(t *testing.T) {
	operatorID := "operator"
	ctx := context.Background()
	ctx = ctxkeys.WithRenderConfig(ctx, config.RenderConfig{})
	ctx = ctxkeys.WithCSRFToken(ctx, "csrf-token")
	ctx = ctxkeys.WithSession(ctx, &model.Session{ImpersonatorUserID: &operatorID})

	var output bytes.Buffer
	if err := Base().Render(ctx, &output); err != nil {
		t.Fatalf("render Base(): %v", err)
	}
	html := output.String()
	if !strings.Contains(html, "viewing the application as another user") || !strings.Contains(html, "/admin/impersonation/stop") || !strings.Contains(html, "csrf-token") {
		t.Fatal("impersonation banner is incomplete")
	}
}

func renderAppLayout(t *testing.T, role string, workspaceCount int) string {
	t.Helper()
	return renderApp(t, appContext(role, workspaceCount))
}

func appContext(role string, workspaceCount int) context.Context {
	workspace := &model.Workspace{ID: "workspace-1", Name: "First Workspace", Slug: "first-workspace"}
	member := &model.WorkspaceMember{ID: "member-1", WorkspaceID: workspace.ID, UserID: "user-1", Role: role}
	workspaces := []model.WorkspaceMembership{{Workspace: *workspace, Role: member.Role}}
	if workspaceCount > 1 {
		workspaces = append(workspaces, model.WorkspaceMembership{
			Workspace: model.Workspace{ID: "workspace-2", Name: "Second Workspace", Slug: "second-workspace"},
			Role:      model.WorkspaceRoleMember,
		})
	}

	ctx := context.Background()
	ctx = ctxkeys.WithRenderConfig(ctx, config.RenderConfig{SupportEmail: "support@example.com", Billing: true})
	ctx = ctxkeys.WithURLPath(ctx, "/app/first-workspace/overview")
	ctx = ctxkeys.WithUser(ctx, &model.User{ID: "user-1", Email: "user@example.com", Name: "Test User"})
	ctx = ctxkeys.WithWorkspace(ctx, workspace)
	ctx = ctxkeys.WithWorkspaceMember(ctx, member)
	return ctxkeys.WithWorkspaces(ctx, workspaces)
}

func renderApp(t *testing.T, ctx context.Context) string {
	t.Helper()
	var output bytes.Buffer
	if err := App("Overview").Render(ctx, &output); err != nil {
		t.Fatalf("render App(): %v", err)
	}
	return output.String()
}
