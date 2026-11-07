package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// TestCopyBeforeMigrations copies a database that misses migrations,
// keeps only the newest copies, and leaves fresh and current databases alone.
func TestCopyBeforeMigrations(t *testing.T) {
	dir := t.TempDir()
	database, err := sql.Open("sqlite", filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	path, err := CopyBeforeMigrations(database, dir, "v1.0.0")
	if err != nil || path != "" {
		t.Fatalf("fresh database: path %q, error %v; want no copy", path, err)
	}
	if err := setupGoose(); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(database, ".", 13); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'ada@example.com')`); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"v0.1.0", "v0.2.0", "v0.3.0"} {
		file := filepath.Join(dir, copyPrefix+old+".db")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-time.Hour)
		_ = os.Chtimes(file, past, past)
	}

	path, err = CopyBeforeMigrations(database, dir, "v1.0.0-beta.3")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "backup-before-v1.0.0-beta.3.db") {
		t.Fatalf("path = %q", path)
	}
	copied, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var email string
	if err := copied.QueryRow(`SELECT email FROM users WHERE id = 'u1'`).Scan(&email); err != nil || email != "ada@example.com" {
		t.Fatalf("copied user =%q, %v", email, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, copyPrefix+"*.db"))
	if len(matches) != copiesKept {
		t.Fatalf("copies = %v, want %d", matches, copiesKept)
	}

	if err := RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	path, err = CopyBeforeMigrations(database, dir, "v1.0.0")
	if err != nil || path != "" {
		t.Fatalf("current database: path %q, error %v; want no copy", path, err)
	}
}
