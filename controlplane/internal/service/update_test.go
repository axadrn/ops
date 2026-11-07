package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/testutil"
)

func TestNewerVersion(t *testing.T) {
	for _, test := range []struct {
		candidate, current string
		newer              bool
	}{
		{"v0.3.0", "v0.2.9", true},
		{"v0.2.9", "v0.3.0", false},
		{"v0.3.0", "v0.3.0", false},
		{"v0.10.0", "v0.9.0", true},
		{"v1.0.0", "v1.0.0-beta.3", true},
		{"v1.0.0-beta.3", "v1.0.0", false},
		{"v1.0.0-beta.11", "v1.0.0-beta.3", true},
		{"v1.0.0-beta.3", "v1.0.0-alpha.9", true},
		{"v1.0.0-beta", "v1.0.0-beta.1", false},
		{"v1.0.0-rc.1", "v1.0.0-1", true},
		{"v1.0.1+build.5", "v1.0.0", true},
		{"v1.0.0", "dev", false},
		{"dev", "v1.0.0", false},
		{"v1.0", "v0.9.0", false},
		{"1.2.3", "v0.9.0", false},
		{"v01.2.3", "v0.9.0", false},
		{"v1.0.0", "main-abc1234", false},
	} {
		if got := NewerVersion(test.candidate, test.current); got != test.newer {
			t.Errorf("NewerVersion(%q, %q) = %v, want %v", test.candidate, test.current, got, test.newer)
		}
	}
}

const testReleaseBody = `{"tag_name":"v0.3.0","html_url":"https://github.com/axadrn/moin/releases/tag/v0.3.0","published_at":"2026-11-02T10:00:00Z",
"body":"**In short:** One-click updates for self-hosters.\n\n### New\n\n- **Update now**: one click.\n"}`

// TestUpdateCheck reads the newest release from GitHub, asks again with
// its ETag, and keeps the release on a 304.
func TestUpdateCheck(t *testing.T) {
	var requests atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.Write([]byte(testReleaseBody))
	}))
	defer github.Close()
	updates := NewUpdateService("v0.2.0", github.URL, t.TempDir(), true, true, []string{"ada@example.com"}, nil)
	for range 2 {
		if err := updates.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	info := updates.Info(nil)
	if info.Latest != "v0.3.0" || !info.UpdateAvailable || info.Summary != "One-click updates for self-hosters." ||
		info.NotesURL != "https://github.com/axadrn/moin/releases/tag/v0.3.0" || info.PublishedAt == nil || !strings.Contains(info.Notes, "### New") {
		t.Fatalf("info = %+v", info)
	}
	if info.CanUpdate || info.Admin {
		t.Fatalf("a visitor may update: %+v", info)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d", requests.Load())
	}

	// A development build never reports an update.
	dev := NewUpdateService("dev", github.URL, t.TempDir(), true, true, nil, nil)
	if err := dev.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if info := dev.Info(nil); info.UpdateAvailable || info.Latest != "v0.3.0" {
		t.Fatalf("dev info = %+v", info)
	}
}

// TestHostedShowsNoUpdate: the hosted control plane updates with every
// release; it neither asks GitHub nor offers an update.
func TestHostedShowsNoUpdate(t *testing.T) {
	var asked atomic.Bool
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(true)
		w.Write([]byte(testReleaseBody))
	}))
	defer github.Close()
	database := testutil.NewDatabase(t)
	fixture := testutil.SeedUser(t, database, testutil.UserOptions{ID: "ada", Email: "ada@example.com", Verified: true})
	hosted := NewUpdateService("v0.2.0", github.URL, t.TempDir(), false, true, []string{"ada@example.com"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hosted.Run(ctx)
	info := hosted.Info(fixture.User)
	if asked.Load() || info.SelfHosted || info.Latest != "" || info.UpdateAvailable || info.CanUpdate || info.Admin {
		t.Fatalf("hosted info = %+v, asked %v", info, asked.Load())
	}
	if _, err := hosted.Start(Actor{WorkspaceID: fixture.Workspace.ID, UserID: "ada"}, fixture.User); !errors.Is(err, ErrUpdateHosted) {
		t.Fatalf("Start = %v, want ErrUpdateHosted", err)
	}
}

// TestUpdateStartAndSettle: an admin starts the update, which writes the
// request file for the updater; once the updater reports the end, the
// activity says how it went.
func TestUpdateStartAndSettle(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testReleaseBody))
	}))
	defer github.Close()
	database := testutil.NewDatabase(t)
	activity := repository.NewActivity(database)
	admin := testutil.SeedUser(t, database, testutil.UserOptions{ID: "ada", Email: "Ada@example.com", Verified: true})
	member := testutil.SeedUser(t, database, testutil.UserOptions{ID: "bob", Email: "bob@example.com", Verified: true})
	dir := t.TempDir()
	updates := NewUpdateService("v0.2.0", github.URL, dir, true, true, []string{"ada@example.com"}, activity)
	if err := updates.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	actor := Actor{WorkspaceID: admin.Workspace.ID, UserID: "ada", Source: Source{Via: ViaCLI, Token: "ci"}}

	if _, err := updates.Start(Actor{WorkspaceID: member.Workspace.ID, UserID: "bob"}, member.User); !errors.Is(err, ErrUpdateNotAllowed) {
		t.Fatalf("member Start = %v, want ErrUpdateNotAllowed", err)
	}
	if _, err := updates.Start(actor, admin.User); !errors.Is(err, ErrUpdaterMissing) {
		t.Fatalf("Start without updater = %v, want ErrUpdaterMissing", err)
	}
	// The updater writes its heartbeat.
	if err := os.WriteFile(filepath.Join(dir, "updater"), []byte("2026-11-03T10:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info := updates.Info(admin.User); !info.CanUpdate || !info.Admin || !info.Updater {
		t.Fatalf("admin info = %+v", info)
	}
	info, err := updates.Start(actor, admin.User)
	if err != nil {
		t.Fatal(err)
	}
	if info.Update == nil || info.Update.State != UpdateRequested || info.Update.Version != "v0.3.0" || info.CanUpdate {
		t.Fatalf("info after Start = %+v", info)
	}
	request, err := readKeyValues(filepath.Join(dir, "request"))
	if err != nil {
		t.Fatal(err)
	}
	if request["version"] != "v0.3.0" || request["from"] != "v0.2.0" || request["workspace"] != admin.Workspace.ID || request["via"] != ViaCLI {
		t.Fatalf("request = %v", request)
	}
	if _, err := updates.Start(actor, admin.User); !errors.Is(err, ErrUpdateRunning) {
		t.Fatalf("second Start = %v, want ErrUpdateRunning", err)
	}

	// The updater picks the request up, then rolls back.
	if err := os.Rename(filepath.Join(dir, "request"), filepath.Join(dir, "running")); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Format(time.RFC3339)
	status := "state=updating\nversion=v0.3.0\nfrom=v0.2.0\nat=" + at + "\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if info := updates.Info(admin.User); info.Update == nil || info.Update.State != UpdateRunning {
		t.Fatalf("running info = %+v", info.Update)
	}
	status = "state=rolled_back\nversion=v0.3.0\nfrom=v0.2.0\nreason=v0.3.0 did not answer /healthz within 2 minutes\nat=" + at + "\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "running"), filepath.Join(dir, "finished")); err != nil {
		t.Fatal(err)
	}
	updates.Settle()
	updates.Settle()
	if _, err := os.Stat(filepath.Join(dir, "finished")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished is still there: %v", err)
	}
	events, err := activity.ListByWorkspace(admin.Workspace.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	actions := []string{}
	for _, event := range events {
		actions = append(actions, event.Action)
		if event.Action == "controlplane.rolled_back" && (!strings.Contains(event.Summary(), "healthz") || event.Via != ViaCLI) {
			t.Fatalf("rolled back event = %q via %s", event.Summary(), event.Via)
		}
	}
	if !slices.Contains(actions, "controlplane.rolled_back") || !slices.Contains(actions, "controlplane.update_started") || len(actions) != 3 {
		t.Fatalf("actions = %v", actions)
	}
	if info := updates.Info(admin.User); info.Update == nil || info.Update.State != UpdateRolledBack || !info.CanUpdate {
		t.Fatalf("rolled back info = %+v", info)
	}
}

// TestAgentBehind: an agent built from a commit, not a release, updates to
// the release; a development control plane updates nobody.
func TestAgentBehind(t *testing.T) {
	for _, test := range []struct {
		release, agent string
		behind         bool
	}{
		{"v1.0.0-beta.6", "v1.0.0-beta.5", true},
		{"v1.0.0-beta.6", "v1.0.0-beta.6", false},
		{"v1.0.0-beta.6", "v1.0.0-beta.7", false},
		{"v1.0.0-beta.6", "b4be272", true},
		{"v1.0.0-beta.6", "dev", true},
		{"v1.0.0-beta.6", "", true},
		{"dev", "b4be272", false},
		{"b4be272", "v1.0.0", false},
	} {
		if got := AgentBehind(test.release, test.agent); got != test.behind {
			t.Errorf("AgentBehind(%q, %q) = %v, want %v", test.release, test.agent, got, test.behind)
		}
	}
}
