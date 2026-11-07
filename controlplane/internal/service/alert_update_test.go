package service

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/catalog"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

// TestUpdateAlertReachesEveryDestination: update.available says the new
// release, the one running and the command, in every destination's form.
func TestUpdateAlertReachesEveryDestination(t *testing.T) {
	f := newAlertFixture(t)
	f.add(t, model.AlertSlack, "slack", f.url+"/slack/T0/B0/XXXX", "")
	f.add(t, model.AlertDiscord, "discord", f.url+"/discord/api/webhooks/1/abc", "")
	f.add(t, model.AlertTelegram, "telegram", telegramToken, "-1001234")
	f.add(t, model.AlertWebhook, "hook", f.url+"/hook", "")
	f.add(t, model.AlertWebhook, "deploys-only", f.url+"/deploys", "", "deploy.failed")
	page := "https://moin.example.com/app/owner-workspace/apps/shop"
	f.alerts.Send(owner.WorkspaceID, "update.available", "shop", map[string]string{"catalog": "Umami", "version": "3.5.0", "current": "3.4.1"}, time.Now())

	line := "Umami 3.5.0 is out for shop (you run 3.4.1). Update in one click; moin backs up first."
	if text := decoded(t, f.chat.last(t, "/slack/T0/B0/XXXX").body)["text"]; text != "*⬆️ Umami 3.5.0 is out for shop*\n"+line+"\nOr run: moin update shop\n<"+page+"|Open in moin>" {
		t.Errorf("slack = %q", text)
	}
	if text := decoded(t, f.chat.last(t, "/discord/api/webhooks/1/abc").body)["content"]; text != "**⬆️ Umami 3.5.0 is out for shop**\n"+line+"\nOr run: moin update shop\nOpen in moin: <"+page+">" {
		t.Errorf("discord = %q", text)
	}
	if text := decoded(t, f.chat.last(t, "/telegram/bot"+telegramToken+"/sendMessage").body)["text"]; text != "<b>⬆️ Umami 3.5.0 is out for shop</b>\n"+line+"\nOr run: moin update shop\n<a href=\""+page+"\">Open in moin</a>" {
		t.Errorf("telegram = %q", text)
	}
	if body := string(f.chat.last(t, "/hook").body); body != `{"event":"update.available","workspace":"owner-workspace","app":"shop","server":"web-1","domain":"","reason":"Umami 3.5.0, runs 3.4.1","since":"","duration":"","url":"`+page+`"}` {
		t.Errorf("webhook = %s", body)
	}
	if f.chat.count("/deploys") != 0 {
		t.Error("a destination without update.available got it")
	}
}

// updateActions counts the update.available entries of the activity.
func updateActions(t *testing.T, catalogs *CatalogService) []string {
	t.Helper()
	events, err := catalogs.apps.servers.activity.ListByWorkspace(owner.WorkspaceID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, event := range events {
		if event.Action == "update.available" {
			found = append(found, event.Details()["version"])
		}
	}
	return found
}

// TestCheckUpdatesOncePerRelease: a newer release alerts once, a second
// check and a restart send nothing, the next release alerts again, and an
// app on the newest release gets nothing.
func TestCheckUpdatesOncePerRelease(t *testing.T) {
	catalogs, apps, _, _ := newCatalogService(t)
	app, _, err := catalogs.Install(context.Background(), owner, CatalogInstall{Slug: "umami", Server: "web-1", Version: "3.4.0"})
	if err != nil {
		t.Fatal(err)
	}
	finishDeploys(t, apps, app.ID)
	current, _, err := catalogs.Install(context.Background(), owner, CatalogInstall{Slug: "umami", Server: "web-1", Name: "stats-new"})
	if err != nil {
		t.Fatal(err)
	}
	finishDeploys(t, apps, current.ID)

	catalogs.CheckUpdates("umami")
	catalogs.CheckUpdates("umami")
	if got := updateActions(t, catalogs); len(got) != 1 || got[0] != "3.4.1" {
		t.Fatalf("after two checks: %v, want one for 3.4.1 (the app on 3.4.1 gets none)", got)
	}

	// A restart: a new service over the same database, as if it started again.
	restarted := NewCatalogService(catalogs.store, apps)
	restarted.CheckUpdates("umami")
	if got := updateActions(t, restarted); len(got) != 1 {
		t.Fatalf("after a restart: %v", got)
	}

	catalogs.store.Set("umami", []catalog.Version{{Tag: "3.5.0"}, {Tag: "3.4.1"}, {Tag: "3.4.0"}})
	catalogs.CheckUpdates("umami")
	got := updateActions(t, catalogs)
	if len(got) != 3 {
		t.Fatalf("after 3.5.0: %v, want one more for each app", got)
	}
}

// TestCatalogCacheServesARestart: the releases read once come back from
// history.db after a restart, without asking a registry.
func TestCatalogCacheServesARestart(t *testing.T) {
	cache := newTestCatalogCache(t)
	entries, err := catalog.Load(filepath.Join("..", "..", "content", "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	first := catalog.NewStore(entries, catalog.Registry{})
	first.UseCache(cache)
	first.Set("umami", []catalog.Version{{Tag: "3.4.1", Date: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}})

	// The registry of the restarted store answers nothing: every read fails.
	restarted := catalog.NewStore(entries, catalog.Registry{Client: failingClient(t), HubURL: "http://registry.invalid", GHCRURL: "http://registry.invalid"})
	restarted.UseCache(cache)
	latest, ok := restarted.Latest("umami")
	if !ok || latest.Tag != "3.4.1" || latest.Date.Year() != 2026 {
		t.Errorf("after a restart: %+v, %v", latest, ok)
	}
	if restarted.Checked("umami").IsZero() {
		t.Error("the cache lost when it was read")
	}
}

// newTestCatalogCache keeps catalog releases in the test database, which
// has the history tables.
func newTestCatalogCache(t *testing.T) catalog.Cache {
	t.Helper()
	cache := repository.NewCatalogCache(testutil.NewDatabase(t))
	return catalog.Cache{Load: cache.Load, Save: cache.Save}
}

// failingClient is an HTTP client every request of which fails the test.
func failingClient(t *testing.T) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Errorf("asked the registry: %s", request.URL)
		return nil, http.ErrHandlerTimeout
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
