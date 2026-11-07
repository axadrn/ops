package db

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
)

// historySchema is the whole of history.db: everything that is a chart over
// time and grows back by itself. Metrics: a row per server or app and minute,
// and one per hour. Uptime: a row per domain and hour. The releases of the
// catalog apps, so a restart serves them at once. The file has no
// migrations and no backup: lost, the charts start over and the releases
// are read again, nothing else.
const historySchema = `
CREATE TABLE IF NOT EXISTS metrics_minutes (
    target TEXT NOT NULL,
    minute INTEGER NOT NULL,
    cpu INTEGER NOT NULL DEFAULT 0,
    memory INTEGER NOT NULL DEFAULT 0,
    disk INTEGER NOT NULL DEFAULT 0,
    net_rx INTEGER NOT NULL DEFAULT 0,
    net_tx INTEGER NOT NULL DEFAULT 0,
    load INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (target, minute)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_metrics_minutes_minute ON metrics_minutes(minute);
CREATE TABLE IF NOT EXISTS metrics_hours (
    target TEXT NOT NULL,
    hour INTEGER NOT NULL,
    cpu INTEGER NOT NULL DEFAULT 0,
    memory INTEGER NOT NULL DEFAULT 0,
    disk INTEGER NOT NULL DEFAULT 0,
    net_rx INTEGER NOT NULL DEFAULT 0,
    net_tx INTEGER NOT NULL DEFAULT 0,
    load INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (target, hour)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_metrics_hours_hour ON metrics_hours(hour);
CREATE TABLE IF NOT EXISTS uptime_hours (
    app_id TEXT NOT NULL,
    domain TEXT NOT NULL,
    hour TIMESTAMP NOT NULL,
    checks INTEGER NOT NULL DEFAULT 0,
    up INTEGER NOT NULL DEFAULT 0,
    response_ms INTEGER NOT NULL DEFAULT 0,
    max_ms INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (app_id, domain, hour)
);
CREATE INDEX IF NOT EXISTS idx_uptime_hours_hour ON uptime_hours(hour);
CREATE TABLE IF NOT EXISTS old_domain_days (
    day TEXT NOT NULL,
    host TEXT NOT NULL,
    kind TEXT NOT NULL,
    requests INTEGER NOT NULL DEFAULT 0,
    last_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, host, kind)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS catalog_versions (
    slug TEXT PRIMARY KEY,
    versions TEXT NOT NULL,
    checked_at INTEGER NOT NULL
);
`

// HistoryConnection is history.db next to the database of connection. A
// database in memory gets its history in memory, too.
func HistoryConnection(connection string) string {
	file, _, _ := strings.Cut(strings.TrimPrefix(connection, "file:"), "?")
	if file == "" || file == ":memory:" || strings.Contains(file, "://") {
		return ":memory:"
	}
	return filepath.Join(filepath.Dir(file), "history.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

// OpenHistory opens history.db and creates its tables.
func OpenHistory(connection string) (*sqlx.DB, error) {
	database, err := Init(connection)
	if err != nil {
		return nil, fmt.Errorf("history database: %w", err)
	}
	if strings.HasPrefix(connection, ":memory:") {
		// Every connection to :memory: is its own database.
		database.SetMaxOpenConns(1)
	}
	if err := CreateHistory(database); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// CreateHistory creates the history tables in database if they are missing.
func CreateHistory(database *sqlx.DB) error {
	if _, err := database.Exec(historySchema); err != nil {
		return fmt.Errorf("create history tables: %w", err)
	}
	return nil
}
