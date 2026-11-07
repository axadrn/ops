package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

func TestAdminMakesAUserFreeForeverAndBack(t *testing.T) {
	database := testutil.NewDatabase(t)
	operator := testutil.SeedUser(t, database, testutil.UserOptions{ID: "operator", Email: "operator@example.com", Name: "Operator", Verified: true})
	target := testutil.SeedUser(t, database, testutil.UserOptions{ID: "target", Email: "target@example.com", Name: "Target"})
	payer := testutil.SeedUser(t, database, testutil.UserOptions{ID: "payer", Email: "payer@example.com", Name: "Payer"})
	subscriptions := repository.NewSubscriptions(database)
	adminRepository := repository.NewAdmin(database)

	trialEnd := time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)
	target.Workspace.CreatedAt = trialEnd.AddDate(0, 0, -30)
	if _, err := database.Exec(`UPDATE workspaces SET created_at = $1 WHERE id = $2`, target.Workspace.CreatedAt, target.Workspace.ID); err != nil {
		t.Fatal(err)
	}
	target.Subscription.PlanID, target.Subscription.Status, target.Subscription.CurrentPeriodEnd = model.SubscriptionPlanPro, model.SubscriptionStatusTrialing, &trialEnd
	providerID, paidUntil := "sub_123", time.Now().Add(30*24*time.Hour)
	payer.Subscription.PlanID, payer.Subscription.Status, payer.Subscription.CurrentPeriodEnd, payer.Subscription.ProviderSubscriptionID = model.SubscriptionPlanPro, model.SubscriptionStatusActive, &paidUntil, &providerID
	for _, subscription := range []*model.Subscription{target.Subscription, payer.Subscription} {
		if err := subscriptions.Update(subscription); err != nil {
			t.Fatal(err)
		}
	}

	auth := NewAuthService(nil, nil, repository.NewSessions(database), nil, nil, false, time.Hour, time.Hour, time.Hour, time.Hour, time.Hour)
	auth.UseAPITokens(&APITokenService{tokens: repository.NewAPITokens(database), now: time.Now})
	admin := NewAdminService(adminRepository, repository.NewUsers(database), auth, []string{"operator@example.com"})
	admin.UsePlans(subscriptions, repository.NewActivity(database))
	session, err := auth.StartSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth", nil), operator.User.ID)
	if err != nil {
		t.Fatal(err)
	}

	plans := func() map[string]model.AdminUser {
		t.Helper()
		overview, err := admin.Overview(operator.User, session, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		byEmail := map[string]model.AdminUser{}
		for _, user := range overview.Users.Users {
			byEmail[user.Email] = user
		}
		return byEmail
	}
	if user := plans()["target@example.com"]; user.Plan != model.AdminPlanTrial || user.TrialEndsAt == nil {
		t.Fatalf("before: %+v", user)
	}
	if user := plans()["payer@example.com"]; user.Plan != model.AdminPlanPaying {
		t.Fatalf("payer: %+v", user)
	}

	if err := admin.SetForeverFree(target.User, session, payer.User.ID, true); !errors.Is(err, ErrAdminPermission) {
		t.Fatalf("a non-admin: err = %v", err)
	}
	if err := admin.SetForeverFree(operator.User, session, payer.User.ID, true); !errors.Is(err, ErrPaying) {
		t.Fatalf("a paying workspace: err = %v", err)
	}

	if err := admin.SetForeverFree(operator.User, session, target.User.ID, true); err != nil {
		t.Fatal(err)
	}
	if user := plans()["target@example.com"]; user.Plan != model.AdminPlanFreeForever {
		t.Fatalf("after making free forever: %+v", user)
	}

	// Ended during the trial: the rest of the trial.
	if err := admin.SetForeverFree(operator.User, session, target.User.ID, false); err != nil {
		t.Fatal(err)
	}
	after, err := subscriptions.ByWorkspaceID(target.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.SubscriptionStatusTrialing || after.CurrentPeriodEnd == nil || !after.CurrentPeriodEnd.Equal(trialEnd) {
		t.Fatalf("ended during the trial: %+v, trial ends %s", after, trialEnd)
	}

	// Ended after the trial: the free plan, read-only.
	after.EndForeverFree(target.Workspace.CreatedAt, trialEnd.Add(time.Hour))
	if after.PlanID != model.SubscriptionPlanFree || after.Status != model.SubscriptionStatusActive || after.CurrentPeriodEnd != nil {
		t.Fatalf("ended after the trial: %+v", after)
	}

	events, err := adminRepository.ListActivityEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range events {
		actions[event.Action] = true
	}
	if !actions["admin.free_forever_started"] || !actions["admin.free_forever_ended"] {
		t.Fatalf("operator activity = %v", actions)
	}
}
