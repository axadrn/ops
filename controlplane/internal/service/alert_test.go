package service

import (
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

// TestAlertsGoToOwnersAndAdminsWhoWantThem mails each of the five events
// to the owners and admins with the switch on and a verified address, and
// nothing for any other action.
func TestAlertsGoToOwnersAndAdminsWhoWantThem(t *testing.T) {
	database := testutil.NewDatabase(t)
	testutil.SeedUser(t, database, testutil.UserOptions{ID: "owner", Name: "Ada", Verified: true})
	for _, user := range []struct {
		id, role       string
		verified, mute bool
	}{
		{"admin", model.WorkspaceRoleAdmin, true, false},
		{"member", model.WorkspaceRoleMember, true, false},
		{"muted", model.WorkspaceRoleAdmin, true, true},
		{"unverified", model.WorkspaceRoleOwner, false, false},
	} {
		testutil.SeedUser(t, database, testutil.UserOptions{ID: user.id, Verified: user.verified})
		if _, err := database.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ($1, 'workspace-owner', $2, $3, $4)`, "in-owner-"+user.id, user.id, user.role, time.Now()); err != nil {
			t.Fatal(err)
		}
		if user.mute {
			if err := repository.NewUsers(database).SetEmailAlerts(user.id, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	mails := testutil.NewMemoryMailer()
	apps := repository.NewApps(database, database)
	alerts := NewAlertService(NewEmailService(mails, "https://moin.example.com", "moin"), repository.NewWorkspaces(database), repository.NewWorkspaceMembers(database), apps, repository.NewAlertDestinations(database), repository.NewActivity(database), testBox(t))
	servers := NewServerService(repository.NewServers(database), repository.NewActivity(database), "https://moin.example.com")
	server, _, err := servers.Create(owner, "web-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := apps.Create(&model.App{ID: "app-shop", WorkspaceID: owner.WorkspaceID, ServerID: server.ID, Name: "shop", Domains: "[]", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC)
	for _, event := range []struct {
		action, target string
		details        map[string]string
		subject, body  string
	}{
		{"uptime.down", "shop", map[string]string{"domain": "shop.example.com", "reason": "HTTP 502"}, "🔴 shop.example.com is down", "Reason: HTTP 502"},
		{"uptime.up", "shop", map[string]string{"domain": "shop.example.com", "duration": "4m30s"}, "🟢 shop.example.com is back up", "It was down for 4 minutes 30 seconds."},
		{"server.down", "web-1", map[string]string{"since": "2026-11-03 14:00 UTC"}, "🔴 Server web-1 is offline", "Apps on it: shop."},
		{"server.up", "web-1", map[string]string{"duration": "12m0s"}, "🟢 Server web-1 is back online", "It was offline for 12 minutes."},
		{"deploy.failed", "shop", map[string]string{"number": "4", "error": "build: exit status 1"}, "🔴 Deploy of shop failed", "Error: build: exit status 1"},
		{"deploy.succeeded", "shop", map[string]string{"number": "5"}, "", ""},
		{"app.restarted", "shop", nil, "", ""},
	} {
		before := len(mails.Messages())
		alerts.Send(owner.WorkspaceID, event.action, event.target, event.details, at)
		sent := mails.Messages()[before:]
		if event.subject == "" {
			if len(sent) != 0 {
				t.Errorf("%s sent %d mails", event.action, len(sent))
			}
			continue
		}
		to := []string{}
		for _, mail := range sent {
			to = append(to, mail.To)
			if mail.Subject != event.subject {
				t.Errorf("%s subject = %q, want %q", event.action, mail.Subject, event.subject)
			}
			if !strings.Contains(mail.Text, event.body) || !strings.Contains(mail.Text, "Turn it off: https://moin.example.com/app/owner-workspace/settings") {
				t.Errorf("%s text = %q", event.action, mail.Text)
			}
		}
		if strings.Join(to, ",") != "admin@example.com,owner@example.com" {
			t.Errorf("%s went to %v", event.action, to)
		}
	}
	last := mails.Messages()[len(mails.Messages())-1]
	if !strings.Contains(last.Text, "Open in moin: https://moin.example.com/app/owner-workspace/apps/shop") || !strings.Contains(last.Text, "The app has no live version yet.") {
		t.Errorf("deploy.failed text = %q", last.Text)
	}
}

func TestSpokenDuration(t *testing.T) {
	for value, want := range map[string]string{
		"4m30s":      "4 minutes 30 seconds",
		"12m0s":      "12 minutes",
		"1h0m5s":     "1 hour",
		"26h3m0s":    "1 day 2 hours",
		"1s":         "1 second",
		"300ms":      "less than a second",
		"not a time": "not a time",
		"":           "-",
	} {
		if got := spokenDuration(value); got != want {
			t.Errorf("spokenDuration(%q) = %q, want %q", value, got, want)
		}
	}
}
