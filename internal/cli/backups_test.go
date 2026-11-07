package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBackupsList shows how each database stands and the moments to go
// back to, with the commands for both.
func TestBackupsList(t *testing.T) {
	api := &fakeAPI{answers: map[string]string{"GET /api/v1/apps/shop/backups": `{"bucket":"main","older_buckets":[],"databases":[
		{"database":"/data/app.db","since":"2026-11-01T10:00:00Z","checked_at":"` + time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339) + `","moments":[
			{"id":"1h","label":"1 hour ago","at":"2026-11-07T13:00:00Z","kind":"ago"},
			{"id":"before-v12","label":"Before v12","at":"2026-11-07T09:30:00Z","kind":"deploy"}]},
		{"database":"postgres-db","kind":"postgres","error":"dial tcp: i/o timeout","problem":"The bucket can't be reached.","moments":[]}]}`}}
	code, stdout, stderr := runCLI(t, api, "backups", "shop")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{"backed up · checked 2h ago", "1h", "1 hour ago", "before-v12", "Before v12", "Nov 7 09:30 UTC", "The bucket can't be reached.", "dial tcp: i/o timeout", "nothing to go back to yet", "moin restore shop --to <moment>", "moin backup download shop"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("no %q in\n%s", want, stdout)
		}
	}
	for _, gone := range []string{"snapshot", "point in time"} {
		if strings.Contains(strings.ToLower(stdout), gone) {
			t.Errorf("still says %q", gone)
		}
	}
}

// TestRestoreNeedsAMoment: without --to nothing reaches the control plane.
func TestRestoreNeedsAMoment(t *testing.T) {
	api := &fakeAPI{}
	code, _, stderr := runCLI(t, api, "restore", "shop", "--yes")
	if code == 0 || !strings.Contains(stderr, "--to 1h") || len(api.requests) > 0 {
		t.Fatalf("code %d, stderr %q, requests %v", code, stderr, api.requests)
	}
	if moment, at := restoreTarget("14:01", time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)); moment != "" || at == nil || !at.Equal(time.Date(2026, 11, 7, 14, 1, 0, 0, time.UTC)) {
		t.Errorf("14:01 = %q %v", moment, at)
	}
	if moment, at := restoreTarget("2026-11-07T14:01:42Z", time.Now()); at == nil || at.Second() != 0 || moment != "" {
		t.Errorf("a time goes to the minute: %v", at)
	}
	if moment, at := restoreTarget(" before-v12 ", time.Now()); moment != "before-v12" || at != nil {
		t.Errorf("a moment = %q %v", moment, at)
	}
}

// TestBackupDownload waits for the copy, then saves it from the bucket
// into the current folder; --link prints the link instead.
func TestBackupDownload(t *testing.T) {
	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("X-Amz-Signature") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("the copy"))
	}))
	defer bucket.Close()
	link := bucket.URL + "/ws/shop/id/downloads/shop-data-app.db-20261107T140200Z.db.gz?X-Amz-Signature=x"
	api := &fakeAPI{answers: map[string]string{
		"POST /api/v1/apps/shop/backups/downloads":   `{"id":"d1","app":"shop","database":"","state":"running","size":0}`,
		"GET /api/v1/apps/shop/backups/downloads/d1": `{"id":"d1","app":"shop","database":"/data/app.db","state":"ready","size":8,"link":"` + link + `"}`,
	}}
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	code, stdout, stderr := runCLI(t, api, "backup", "download", "shop")
	if code != 0 || !strings.Contains(stdout, "Saved shop-data-app.db-20261107T140200Z.db.gz") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	body, err := os.ReadFile(filepath.Join(dir, "shop-data-app.db-20261107T140200Z.db.gz"))
	if err != nil || string(body) != "the copy" {
		t.Fatalf("saved %q %v", body, err)
	}
	code, stdout, _ = runCLI(t, api, "backup", "download", "shop", "--link")
	if code != 0 || !strings.Contains(stdout, link) {
		t.Fatalf("--link: code %d, stdout %q", code, stdout)
	}
	code, _, stderr = runCLI(t, api, "backup", "shop")
	if code == 0 || !strings.Contains(stderr, "backup download") {
		t.Fatalf("backup without download: code %d, stderr %q", code, stderr)
	}
}

// TestRestoreWaitsForTheAgent: a restore runs on its own on the control
// plane; the CLI follows it until it is done, and says why it failed.
func TestRestoreWaitsForTheAgent(t *testing.T) {
	api := &fakeAPI{answers: map[string]string{
		"POST /api/v1/apps/shop/restores":   `{"id":"r1","state":"running","to":"1 hour ago"}`,
		"GET /api/v1/apps/shop/restores/r1": `{"id":"r1","state":"failed","to":"1 hour ago","error":"swap the data: disk full. The copy from before is undo-20261107T140200Z"}`,
	}}
	code, _, stderr := runCLI(t, api, "restore", "shop", "--to", "1h", "--yes")
	if code == 0 || !strings.Contains(stderr, "disk full") || !strings.Contains(stderr, "undo-20261107T140200Z") || !api.seen("GET /api/v1/apps/shop/restores/r1") {
		t.Fatalf("code %d, stderr %q, requests %v", code, stderr, api.requests)
	}
}
