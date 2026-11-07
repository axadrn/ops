-- +goose Up
-- A restore runs on its own on the server, longer than a request and
-- across sessions and restarts of the control plane: how each stands is
-- kept here, a week long. state is running, unknown (the server went
-- offline during it; it tells how it ended when it is back), done or
-- failed.
CREATE TABLE IF NOT EXISTS backup_restores (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    app_name TEXT NOT NULL,
    database TEXT NOT NULL DEFAULT '',
    to_label TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP,
    FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS backup_restores_app ON backup_restores (app_id, started_at);

-- +goose Down
DROP TABLE IF EXISTS backup_restores;
