package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/protocol"
)

// The buckets page shows every bucket with the apps that use it, marks
// the default and offers to make another one the default.
func TestBucketsPage(t *testing.T) {
	view := BucketView{Buckets: []service.Bucket{
		{Name: "default", Default: true, Bucket: "acme", Folder: "workspace-1/", Apps: []string{"blog"}, Older: []string{"shop"}},
		{Name: "client-a", Bucket: "client", Folder: "workspace-1/", Apps: []string{"shop"}},
	}}
	var html strings.Builder
	if err := Bucket(view).Render(projectPageContext("/app/tiny-rocket-co/bucket"), &html); err != nil {
		t.Fatal(err)
	}
	page := html.String()
	for _, want := range []string{`data-bucket="default"`, `data-bucket="client-a"`, "data-default-bucket", `data-make-default="client-a"`, "/bucket/client-a/remove", ">shop</span>", "data-bucket-older", "Another bucket"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	if strings.Contains(page, `data-make-default="default"`) {
		t.Error("the default bucket offers to become the default")
	}
}

// Data picks the app's bucket among several, says in one row how the
// backup stands, lists the moments to go back to and offers a copy.
func TestBackupsPickTheBucket(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	since, checked := now.Add(-3*time.Hour), now.Add(-2*time.Hour)
	current := service.Bucket{Name: "client-a", Bucket: "client", Folder: "workspace-1/"}
	view := BackupsView{
		App:     model.App{ID: "app-1", Name: "shop", BucketName: "client-a"},
		Bucket:  &current,
		Buckets: []service.Bucket{{Name: "default", Default: true}, current},
		Older:   []service.Bucket{{Name: "default"}},
		Now:     now,
		Databases: []service.DatabaseBackups{{
			DatabaseBackup: protocol.DatabaseBackup{Database: "/data/app.db", Since: &since, CheckedAt: &checked},
			Moments: []service.Moment{
				{ID: "1h", Label: "1 hour ago", At: now.Add(-time.Hour), Kind: service.MomentAgo},
				{ID: "before-v12", Label: "Before v12", At: now.Add(-26 * time.Hour), Kind: "deploy"},
			},
		}},
	}
	var html strings.Builder
	ctx := projectPageContext("/app/tiny-rocket-co/apps/shop/data")
	if err := BackupSettings(view).Render(ctx, &html); err != nil {
		t.Fatal(err)
	}
	if err := DatabaseBackups(view, "/data/app.db").Render(ctx, &html); err != nil {
		t.Fatal(err)
	}
	page := html.String()
	for _, want := range []string{`id="app-bucket-form"`, "The default (default)", "data-bucket-older", `data-confirm-name="shop"`,
		"Backed up · checked 2h ago", `data-moment="1h"`, `data-moment="before-v12"`, "Nov 2, 10:00 UTC", "11:00 UTC", "Download a copy", "Go back to"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	for _, gone := range []string{"snapshot", "Snapshot", "point in time", "Back up now", "type=\"date\""} {
		if strings.Contains(page, gone) {
			t.Errorf("the page still has %q", gone)
		}
	}

	// A broken backup says why in one sentence, the details under it.
	view.Databases[0].Problem, view.Databases[0].Error = "The bucket can't be reached.", "dial tcp: i/o timeout"
	html.Reset()
	if err := DatabaseBackups(view, "/data/app.db").Render(ctx, &html); err != nil {
		t.Fatal(err)
	}
	if page := html.String(); !strings.Contains(page, "The bucket can&#39;t be reached.") || !strings.Contains(page, "Show details") || !strings.Contains(page, "dial tcp: i/o timeout") {
		t.Errorf("a broken backup: %s", page)
	}

	// A restore runs on its own: the page says so and follows it, then
	// says how it ended.
	view.Restore = &service.Restore{State: service.RestoreRunning, To: "1 hour ago"}
	html.Reset()
	DatabaseBackups(view, "/data/app.db").Render(ctx, &html)
	if !strings.Contains(html.String(), "Restoring app.db to 1 hour ago") || !strings.Contains(html.String(), `hx-select="#db-backups"`) {
		t.Errorf("a running restore: %s", html.String())
	}
	finished := now.Add(-time.Minute)
	view.Restore = &service.Restore{State: service.RestoreFailed, To: "Before v12", Error: "swap the data: disk full", FinishedAt: &finished}
	html.Reset()
	DatabaseBackups(view, "/data/app.db").Render(ctx, &html)
	if !strings.Contains(html.String(), "The restore to before v12 failed") || !strings.Contains(html.String(), "disk full") {
		t.Errorf("a failed restore: %s", html.String())
	}
	view.Restore = &service.Restore{State: service.RestoreDone, To: "1 hour ago", FinishedAt: &finished}
	html.Reset()
	DatabaseBackups(view, "/data/app.db").Render(ctx, &html)
	if !strings.Contains(html.String(), "Restored to 1 hour ago") {
		t.Errorf("a done restore: %s", html.String())
	}
	view.Restore = nil

	// A copy being made says so and follows the page; a ready one links.
	view.Download = &service.Download{State: service.DownloadRunning}
	html.Reset()
	DatabaseBackups(view, "/data/app.db").Render(ctx, &html)
	if !strings.Contains(html.String(), "Making a copy") || !strings.Contains(html.String(), `hx-trigger="apps from:body"`) {
		t.Errorf("a running copy: %s", html.String())
	}
	view.Download = &service.Download{State: service.DownloadReady, Link: "https://client.example.com/x.db.gz?X-Amz-Signature=1", Size: 2048}
	html.Reset()
	DatabaseBackups(view, "/data/app.db").Render(ctx, &html)
	if !strings.Contains(html.String(), `href="https://client.example.com/x.db.gz?X-Amz-Signature=1"`) || !strings.Contains(html.String(), "2.0 KB") {
		t.Errorf("a ready copy: %s", html.String())
	}
}
