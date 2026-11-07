-- +goose Up
-- One word per thing: the activity is called activity in the schema too.
ALTER TABLE workspace_audit_events RENAME TO activity_events;
DROP INDEX IF EXISTS idx_workspace_audit_events_workspace_created;
DROP INDEX IF EXISTS idx_workspace_audit_events_app;
DROP INDEX IF EXISTS idx_workspace_audit_events_server;
DROP INDEX IF EXISTS idx_workspace_audit_events_actor;
CREATE INDEX IF NOT EXISTS idx_activity_events_workspace_created ON activity_events(workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_activity_events_app ON activity_events(workspace_id, app, created_at);
CREATE INDEX IF NOT EXISTS idx_activity_events_server ON activity_events(workspace_id, server, created_at);
CREATE INDEX IF NOT EXISTS idx_activity_events_actor ON activity_events(workspace_id, actor_user_id, created_at);

ALTER TABLE admin_audit_events RENAME TO admin_activity_events;
DROP INDEX IF EXISTS idx_admin_audit_events_created_at;
DROP INDEX IF EXISTS idx_admin_audit_events_workspace_id;
CREATE INDEX IF NOT EXISTS idx_admin_activity_events_created_at ON admin_activity_events(created_at);
CREATE INDEX IF NOT EXISTS idx_admin_activity_events_workspace_id ON admin_activity_events(workspace_id);

-- +goose Down
DROP INDEX IF EXISTS idx_admin_activity_events_workspace_id;
DROP INDEX IF EXISTS idx_admin_activity_events_created_at;
ALTER TABLE admin_activity_events RENAME TO admin_audit_events;
CREATE INDEX IF NOT EXISTS idx_admin_audit_events_created_at ON admin_audit_events(created_at);
CREATE INDEX IF NOT EXISTS idx_admin_audit_events_workspace_id ON admin_audit_events(workspace_id);

DROP INDEX IF EXISTS idx_activity_events_actor;
DROP INDEX IF EXISTS idx_activity_events_server;
DROP INDEX IF EXISTS idx_activity_events_app;
DROP INDEX IF EXISTS idx_activity_events_workspace_created;
ALTER TABLE activity_events RENAME TO workspace_audit_events;
CREATE INDEX IF NOT EXISTS idx_workspace_audit_events_workspace_created ON workspace_audit_events(workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_workspace_audit_events_app ON workspace_audit_events(workspace_id, app, created_at);
CREATE INDEX IF NOT EXISTS idx_workspace_audit_events_server ON workspace_audit_events(workspace_id, server, created_at);
CREATE INDEX IF NOT EXISTS idx_workspace_audit_events_actor ON workspace_audit_events(workspace_id, actor_user_id, created_at);
