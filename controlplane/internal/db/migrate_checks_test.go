package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

// TestChecksMigrationRenamesTheDrill: 033 calls the daily check a check in
// the stored activity, the alert events and the backup status.
func TestChecksMigrationRenamesTheDrill(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "checks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := setupGoose(); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(database, ".", 32); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'Acme', 'acme')`,
		`INSERT INTO servers (id, workspace_id, name) VALUES ('s1', 'w1', 'web-1')`,
		`INSERT INTO apps (id, workspace_id, server_id, name) VALUES ('a1', 'w1', 's1', 'blog')`,
		`INSERT INTO backup_status (app_id, database, verified_at, drill_error) VALUES ('a1', 'data.db', '2026-11-01 03:00:00+00:00', 'role missing')`,
		`INSERT INTO alert_destinations (id, workspace_id, name, kind, target, events) VALUES ('d1', 'w1', 'ops', 'webhook', 'x', 'backup.failed,drill.failed,drill.recovered,disk.full')`,
		`INSERT INTO activity_events (id, workspace_id, action, target_type, target_id, metadata, created_at) VALUES ('e1', 'w1', 'drill.failed', 'app', 'blog', '{}', '2026-11-01 03:00:00+00:00')`,
		`INSERT INTO activity_events (id, workspace_id, action, target_type, target_id, metadata, created_at) VALUES ('e2', 'w1', 'drill.recovered', 'app', 'blog', '{}', '2026-11-02 03:00:00+00:00')`,
		`INSERT INTO activity_events (id, workspace_id, action, target_type, target_id, metadata, created_at) VALUES ('e3', 'w1', 'backup.drill_passed', 'app', 'blog', '{"database":"data.db","verified_at":"2026-11-03T03:00:00Z"}', '2026-11-03 03:00:00+00:00')`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := goose.UpTo(database, ".", 33); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][2]string{
		"e1": {"check.failed", "{}"},
		"e2": {"check.recovered", "{}"},
		"e3": {"backup.checked", `{"database":"data.db","checked_at":"2026-11-03T03:00:00Z"}`},
	} {
		var got [2]string
		if err := database.QueryRow(`SELECT action, metadata FROM activity_events WHERE id = ?`, id).Scan(&got[0], &got[1]); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %v, want %v", id, got, want)
		}
	}
	var events string
	if err := database.QueryRow(`SELECT events FROM alert_destinations WHERE id = 'd1'`).Scan(&events); err != nil || events != "backup.failed,check.failed,check.recovered,disk.full" {
		t.Errorf("events = %q, %v", events, err)
	}
	var checkError string
	var checkedAt sql.NullString
	if err := database.QueryRow(`SELECT check_error, checked_at FROM backup_status WHERE app_id = 'a1'`).Scan(&checkError, &checkedAt); err != nil || checkError != "role missing" || !checkedAt.Valid {
		t.Errorf("backup status = %q, %v, %v", checkError, checkedAt, err)
	}
}
