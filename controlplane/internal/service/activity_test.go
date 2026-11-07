package service

import (
	"context"
	"strings"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/protocol"
)

// TestActivityNeverHoldsSecrets makes changes with secret values and user
// data in them and checks that none of it lands in the activity: moin keeps
// names, numbers and settings, never values, passwords, tokens or rows.
func TestActivityNeverHoldsSecrets(t *testing.T) {
	apps, servers, server, agent := newAppService(t)
	secrets := []string{"env-value-7f3a", "registry-password-9c1d", "protect-password-2b8e", "row-value-ada@private.example", "sql-literal-5e6f"}

	if _, err := apps.Create(owner, "blog", "web-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetEnv(owner, "blog", map[string]string{"DATABASE_URL": secrets[0]}, nil); err != nil {
		t.Fatal(err)
	}
	if err := apps.SetRegistry(owner, "ghcr.io", "ada", secrets[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetProtection(owner, "blog", "ada", secrets[2]); err != nil {
		t.Fatal(err)
	}
	hook, err := apps.RotateDeployHook(owner, "blog")
	if err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, hook[strings.LastIndex(hook, "/")+1:])

	deploy, err := deployUpload(apps)
	if err != nil {
		t.Fatal(err)
	}
	agentSays(t, servers, server.ID, protocol.TypeDeployDone, protocol.DeployDone{DeployID: deploy.ID, OK: true, Detected: protocol.Detected{Kind: "go", Service: "web", Port: 8080, Databases: []protocol.Database{{Path: "/data/app.db"}}}})
	go agentAnswers(servers, server.ID, agent, protocol.TypeDBQuery, protocol.TypeDBQueryResult, protocol.DBResult{Changes: 1})
	sql := "UPDATE users SET email = '" + secrets[3] + "', note = '" + secrets[4] + "' WHERE id = 1"
	if _, err := apps.Query(context.Background(), owner, "blog", "", sql, nil, true, 0); err != nil {
		t.Fatal(err)
	}

	events, err := servers.activity.ListPage(owner.WorkspaceID, model.ActivityFilter{}, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range events {
		actions[event.Action] = true
		stored := strings.Join([]string{event.Action, event.TargetType, event.TargetID, event.Metadata, event.Via, event.TokenName, event.App, event.Server}, "|")
		for _, value := range secrets {
			if strings.Contains(stored, value) {
				t.Errorf("%s holds the secret %q: %s", event.Action, value, stored)
			}
		}
	}
	for _, action := range []string{"env.set", "registry.set", "app.protected", "app.hook_rotated", "app.deployed", "deploy.succeeded", "data.sql"} {
		if !actions[action] {
			t.Errorf("no %s in the activity: %v", action, actions)
		}
	}
}

// TestActivityFiltersAndPages filters the activity by app, server and member
// and pages through it.
func TestActivityFiltersAndPages(t *testing.T) {
	apps, servers, _, _ := newAppService(t)
	for _, name := range []string{"blog", "shop"} {
		if _, err := apps.Create(owner, name, "web-1"); err != nil {
			t.Fatal(err)
		}
	}
	app, err := apps.Get(owner.WorkspaceID, "shop")
	if err != nil {
		t.Fatal(err)
	}
	// Data edits name the app by ID; the activity keeps its name.
	servers.record(owner, "data.row_deleted", "app", app.ID, map[string]string{"table": "orders", "row": "1"})

	page := func(filter model.ActivityFilter, after string, limit int) []model.ActivityEvent {
		t.Helper()
		events, err := servers.activity.ListPage(owner.WorkspaceID, filter, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	shop := page(model.ActivityFilter{App: "shop"}, "", 50)
	if len(shop) != 2 || shop[0].TargetID != "shop" || shop[0].Server != "web-1" {
		t.Fatalf("shop events = %+v", shop)
	}
	onServer := page(model.ActivityFilter{Server: "web-1"}, "", 50)
	if len(onServer) != 5 {
		t.Fatalf("web-1 has %d events, want added, installed, two apps and a row", len(onServer))
	}
	// The owner created the workspace, added the server and the apps and
	// deleted a row; moin installed the server.
	if mine := page(model.ActivityFilter{User: "owner@example.com"}, "", 50); len(mine) != 5 {
		t.Fatalf("owner has %d events, want 5 without the install by moin", len(mine))
	}
	all := page(model.ActivityFilter{}, "", 50)
	first := page(model.ActivityFilter{}, "", 2)
	rest := page(model.ActivityFilter{}, first[1].ID, 50)
	if len(first)+len(rest) != len(all) || rest[0].ID == first[1].ID {
		t.Fatalf("pages %d and %d do not add up", len(first), len(rest))
	}
	for _, event := range append(first, rest...) {
		if event.CreatedAt.Location().String() != "UTC" {
			t.Fatalf("%s is stored in %s", event.Action, event.CreatedAt.Location())
		}
	}
}
