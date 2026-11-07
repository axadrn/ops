package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationsCreateSelectedSchema(t *testing.T) {
	var database *sql.DB
	var err error
	database, err = sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if err := RunMigrations(database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	expected := map[string]bool{
		"users": true, "tokens": true, "sessions": true,
		"subscriptions": true, "admin_activity_events": true,
		"profiles": false,
		"files":    true, "newsletter_subscribers": false,
		"workspaces": true, "workspace_members": true, "workspace_invitations": true, "activity_events": true,
		"goals": false, "goal_entries": false,
	}
	for table, want := range expected {
		assertTableExists(t, database, table, want)
	}

	for _, column := range []string{"provider_subscription_created_at", "provider_state_updated_at", "provider_payment_updated_at"} {
		assertColumnExists(t, database, "subscriptions", column)
	}
	for _, column := range []string{"checkout_attempt_id", "checkout_provider", "checkout_plan_id", "checkout_interval", "checkout_url", "checkout_expires_at", "checkout_pending_until"} {
		assertColumnExists(t, database, "subscriptions", column)
	}
	assertColumnExists(t, database, "subscriptions", "workspace_id")
	assertColumnMissing(t, database, "subscriptions", "user_id")
	assertColumnExists(t, database, "sessions", "impersonator_user_id")
	assertColumnExists(t, database, "users", "name")
	assertColumnExists(t, database, "users", "updated_at")
	assertColumnExists(t, database, "tokens", "token_hash")
	assertColumnMissing(t, database, "tokens", "token")
	assertColumnMissing(t, database, "tokens", "used_at")
}

func assertColumnMissing(t *testing.T, database *sql.DB, table, column string) {
	t.Helper()
	exists, err := columnExists(database, table, column)
	if err != nil {
		t.Fatalf("query columns for %s: %v", table, err)
	}
	if exists {
		t.Fatalf("column %s.%s exists", table, column)
	}
}

func assertColumnExists(t *testing.T, database *sql.DB, table, column string) {
	t.Helper()
	exists, err := columnExists(database, table, column)
	if err != nil {
		t.Fatalf("query columns for %s: %v", table, err)
	}
	if !exists {
		t.Fatalf("column %s.%s does not exist", table, column)
	}
}

func columnExists(database *sql.DB, table, column string) (bool, error) {
	rows, err := database.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var position int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&position, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func assertTableExists(t *testing.T, database *sql.DB, table string, want bool) {
	t.Helper()
	var count int
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = $1",
		table,
	).Scan(&count); err != nil {
		t.Fatalf("query table %s: %v", table, err)
	}
	if got := count == 1; got != want {
		t.Fatalf("table %s exists = %t, want %t", table, got, want)
	}
}
