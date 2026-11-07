package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
)

func adminContext() context.Context {
	ctx := context.Background()
	ctx = ctxkeys.WithRenderConfig(ctx, config.RenderConfig{})
	ctx = ctxkeys.WithURLPath(ctx, "/admin")
	ctx = ctxkeys.WithCSRFToken(ctx, "csrf-token")
	return ctxkeys.WithUser(ctx, &model.User{ID: "operator", Email: "operator@example.com"})
}

func renderAdmin(t *testing.T, component templ.Component) string {
	t.Helper()
	var output bytes.Buffer
	if err := component.Render(adminContext(), &output); err != nil {
		t.Fatalf("render: %v", err)
	}
	return output.String()
}

func TestAdminOverviewListsUsersThatOpenTheirPage(t *testing.T) {
	now := time.Now()
	html := renderAdmin(t, AdminOverview(&model.AdminOverview{
		Metrics: model.AdminMetrics{Users: 2, VerifiedUsers: 1, Workspaces: 2, PaidWorkspaces: 1, ActiveSessions: 3},
		Users: model.AdminUserPage{
			Users: []model.AdminUser{
				{ID: "operator", Email: "operator@example.com", Name: "Operator", IsAdmin: true, CreatedAt: now, EmailVerifiedAt: &now},
				{ID: "target", Email: "target@example.com", Name: "Target", WorkspaceCount: 2, ActiveSessionCount: 1, CreatedAt: now, EmailVerifiedAt: &now},
			},
			Page:       1,
			TotalPages: 1,
			TotalUsers: 2,
		},
	}))
	for _, want := range []string{"noindex, nofollow", ">Admin</h1>", "target@example.com", `href="/admin/users/target"`, `href="/admin/activity"`, `href="/admin/old-address"`, "Operator", "2 users"} {
		if !strings.Contains(html, want) {
			t.Errorf("admin overview missing %q", want)
		}
	}
	if strings.Contains(html, "/impersonate") || strings.Contains(html, "/sessions/revoke") {
		t.Error("the list carries actions; they belong on the user's page")
	}
}

func TestAdminUserPageHasItsActions(t *testing.T) {
	now := time.Now()
	html := renderAdmin(t, AdminUser(&model.AdminUser{ID: "target", Email: "target@example.com", Name: "Target", CreatedAt: now, EmailVerifiedAt: &now, ActiveSessionCount: 1}))
	for _, want := range []string{`href="/admin"`, ">Target</h1>", "View as user", `hx-post="/admin/users/target/impersonate"`, `hx-post="/admin/users/target/sessions/revoke"`, "hx-confirm"} {
		if !strings.Contains(html, want) {
			t.Errorf("user page missing %q", want)
		}
	}

	self := renderAdmin(t, AdminUser(&model.AdminUser{ID: "operator", Email: "operator@example.com", IsAdmin: true, CreatedAt: now}))
	if strings.Contains(self, "/impersonate") || strings.Contains(self, "/sessions/revoke") {
		t.Error("an operator can view as or revoke their own account")
	}
	operator := renderAdmin(t, AdminUser(&model.AdminUser{ID: "other", Email: "other@example.com", IsAdmin: true, CreatedAt: now}))
	if strings.Contains(operator, "/impersonate") {
		t.Error("an operator can be viewed as")
	}
}

func TestAdminFreeForeverAsksForTheEmail(t *testing.T) {
	trialEnds := time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	trial := renderAdmin(t, AdminUser(&model.AdminUser{ID: "trial", Email: "trial@example.com", Plan: model.AdminPlanTrial, TrialEndsAt: &trialEnds}))
	for _, want := range []string{"Trial until Dec 2, 2026", `data-confirm-name="trial@example.com"`, "Make free forever", `&#34;forever_free&#34;: &#34;true&#34;`} {
		if !strings.Contains(trial, want) {
			t.Errorf("trial user page missing %q", want)
		}
	}
	free := renderAdmin(t, AdminUser(&model.AdminUser{ID: "free", Email: "free@example.com", Plan: model.AdminPlanFreeForever}))
	if !strings.Contains(free, `data-confirm-name="free@example.com"`) || !strings.Contains(free, "End free forever") {
		t.Error("a free forever user cannot end it")
	}
	payer := renderAdmin(t, AdminUser(&model.AdminUser{ID: "payer", Email: "payer@example.com", Plan: model.AdminPlanPaying}))
	if !strings.Contains(payer, "Paying") || strings.Contains(payer, "/forever-free") {
		t.Error("a paying user can be made free forever")
	}
}

func TestAdminActivityShowsEventsAndTheirKeep(t *testing.T) {
	workspaceID := "workspace-1"
	html := renderAdmin(t, AdminActivity([]model.AdminActivityEvent{{
		ID: "event-1", ActorEmail: "operator@example.com", WorkspaceID: &workspaceID,
		Action: "workspace.deleted", TargetType: "workspace", TargetID: workspaceID, CreatedAt: time.Now(),
	}, {
		ID: "event-2", ActorEmail: "operator@example.com",
		Action: "admin.sessions_revoked", TargetType: "user", TargetID: "target", CreatedAt: time.Now(),
	}}))
	for _, want := range []string{"Workspace deleted", "operator@example.com", "180 days", `href="/admin"`, `href="/admin/users/target"`} {
		if !strings.Contains(html, want) {
			t.Errorf("activity missing %q", want)
		}
	}
}
