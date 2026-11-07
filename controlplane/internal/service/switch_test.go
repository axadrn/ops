package service

import (
	"context"
	"errors"
	"testing"

	"github.com/axadrn/moin/controlplane/internal/model"
)

// Deploy on push changes by itself, without GitHub: the switch, and a link
// of the repo and branch the app follows already, from any surface.
func TestSetAutoDeployNeedsNoGitHub(t *testing.T) {
	apps, servers, server, _ := newAppService(t)
	if _, err := apps.Create(owner, "blog", server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetAutoDeploy(owner, "blog", true); !errors.Is(err, ErrNoRepo) {
		t.Fatalf("without a repo: err = %v", err)
	}
	app, _ := apps.Get(owner.WorkspaceID, "blog")
	if err := apps.apps.SetRepo(app.ID, "axadrn/blog", "main", true, 7); err != nil {
		t.Fatal(err)
	}
	app, err := apps.SetAutoDeploy(owner, "blog", false)
	if err != nil || app.AutoDeploy || app.Repo != "axadrn/blog" || app.Branch != "main" || app.InstallationID != 7 {
		t.Fatalf("off = %+v, %v", app, err)
	}
	if _, err := apps.SetAutoDeploy(other, "blog", true); err == nil {
		t.Fatal("another workspace turned it on")
	}
	// No GitHub is set up: a link of the same repo and branch only flips it.
	app, err = apps.LinkRepo(context.Background(), owner, "blog", "AxAdrn/Blog", "main", true)
	if err != nil || !app.AutoDeploy {
		t.Fatalf("link the same repo = %+v, %v", app, err)
	}
	if _, err := apps.LinkRepo(context.Background(), owner, "blog", "axadrn/blog", "dev", true); err == nil {
		t.Fatal("another branch linked without GitHub")
	}
	if _, err := apps.SetAutoDeploy(owner, "blog", true); err != nil {
		t.Fatal(err)
	}
	events, _ := servers.activity.ListPage(owner.WorkspaceID, model.ActivityFilter{}, "", 50)
	var summaries []string
	for _, event := range events {
		if event.Action == "repo.auto_deploy" {
			summaries = append(summaries, event.Summary())
		}
	}
	if len(summaries) != 2 || summaries[0] != "turned deploy on push on for blog" || summaries[1] != "turned deploy on push off for blog" {
		t.Fatalf("activity = %q", summaries)
	}
}

// A check that finishes after the user paused it stores what it found, but
// never the pause from before.
func TestUptimeStateKeepsThePause(t *testing.T) {
	apps, _, server, _ := newAppService(t)
	if _, err := apps.Create(owner, "blog", server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.SetDomains(owner, "blog", "", []string{"blog.de"}, ""); err != nil {
		t.Fatal(err)
	}
	app, _ := apps.Get(owner.WorkspaceID, "blog")
	stale := model.UptimeCheck{AppID: app.ID, Domain: "blog.de", State: model.UptimeUp}
	paused := true
	if _, _, err := apps.SetUptimeCheck(owner, "blog", "blog.de", nil, &paused); err != nil {
		t.Fatal(err)
	}
	if err := apps.apps.SaveUptimeState(stale); err != nil {
		t.Fatal(err)
	}
	checks, err := apps.apps.UptimeChecks()
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || !checks[0].Paused {
		t.Fatalf("checks = %+v", checks)
	}
}
