package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pressly/goose/v3"
)

// copiesKept is how many copies from before migrations stay in the
// data directory; older ones go.
const copiesKept = 3

// copyPrefix starts the name of every copy.
const copyPrefix = "backup-before-"

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Pending reports whether the database misses migrations of this build. A
// fresh database has nothing worth keeping and reports false.
func Pending(database *sql.DB) (bool, error) {
	if err := setupGoose(); err != nil {
		return false, err
	}
	var tables int
	err := database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).Scan(&tables)
	if err != nil || tables == 0 {
		return false, err
	}
	current, err := goose.GetDBVersion(database)
	if err != nil {
		return false, err
	}
	migrations, err := goose.CollectMigrations(".", 0, goose.MaxVersion)
	if err != nil {
		return false, err
	}
	last, err := migrations.Last()
	if errors.Is(err, goose.ErrNoNextVersion) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return current > 0 && current < last.Version, nil
}

// CopyBeforeMigrations copies the database into dir as
// backup-before-<version>.db when this build brings migrations it has not
// run yet, and keeps only the newest few of these copies. It returns the
// path of the copy, or "" when nothing was pending.
func CopyBeforeMigrations(database *sql.DB, dir, version string) (string, error) {
	pending, err := Pending(database)
	if err != nil || !pending {
		return "", err
	}
	name := unsafeName.ReplaceAllString(version, "_")
	if name == "" {
		name = "dev"
	}
	path := filepath.Join(dir, copyPrefix+name+".db")
	// VACUUM INTO refuses a file that exists, such as one of a dev build.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := database.Exec(`VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("copy the database before migrations: %w", err)
	}
	slog.Info("database copied before migrations", "path", path)
	pruneCopies(dir)
	return path, nil
}

// pruneCopies removes all but the newest copies.
func pruneCopies(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type copied struct {
		path    string
		modTime int64
	}
	var copies []copied
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), copyPrefix) || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		copies = append(copies, copied{filepath.Join(dir, entry.Name()), info.ModTime().UnixNano()})
	}
	slices.SortFunc(copies, func(a, b copied) int { return int(b.modTime - a.modTime) })
	for _, old := range copies[min(len(copies), copiesKept):] {
		if err := os.Remove(old.path); err != nil {
			slog.Warn("remove an old copy of the database", "path", old.path, "error", err)
		}
	}
}
