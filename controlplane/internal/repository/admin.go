package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/jmoiron/sqlx"
)

// Admin reads what the operator pages show and stores their activity events.
type Admin struct {
	db *sqlx.DB
}

func NewAdmin(db *sqlx.DB) *Admin {
	return &Admin{db: db}
}

func (r *Admin) Metrics(now time.Time) (model.AdminMetrics, error) {
	var metrics model.AdminMetrics
	queries := []struct {
		target *int
		query  string
		args   []any
	}{
		{&metrics.Users, `SELECT COUNT(*) FROM users`, nil},
		{&metrics.VerifiedUsers, `SELECT COUNT(*) FROM users WHERE email_verified_at IS NOT NULL`, nil},
		{&metrics.Workspaces, `SELECT COUNT(*) FROM workspaces`, nil},
		{&metrics.PaidWorkspaces, `
			SELECT COUNT(*) FROM subscriptions
			WHERE plan_id <> 'free'
			  AND (status IN ('active', 'trialing', 'past_due') OR (status = 'cancelled' AND current_period_end > $1))
		`, []any{now}},
		{&metrics.ActiveSessions, `SELECT COUNT(*) FROM sessions WHERE expires_at > $1`, []any{now}},
	}
	for _, item := range queries {
		if err := r.db.Get(item.target, item.query, item.args...); err != nil {
			return model.AdminMetrics{}, err
		}
	}
	return metrics, nil
}

func (r *Admin) ListUsers(query string, limit, offset int, now time.Time) ([]model.AdminUser, int, error) {
	pattern := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	where := `WHERE ($1 = '%%' OR LOWER(u.email) LIKE $1 OR LOWER(u.name) LIKE $1)`

	var total int
	if err := r.db.Get(&total, `
		SELECT COUNT(*)
		FROM users u
		`+where, pattern); err != nil {
		return nil, 0, err
	}

	users := []model.AdminUser{}
	err := r.db.Select(&users, adminUserSelect+where+`
		GROUP BY u.id, u.email, u.name, u.email_verified_at, u.created_at
		ORDER BY u.created_at DESC, u.id
		LIMIT $3 OFFSET $4
	`, pattern, now, limit, offset)
	return users, total, err
}

// adminUserSelect is a user with its counts; $2 is now.
const adminUserSelect = `
		SELECT
			u.id,
			u.email,
			u.name,
			u.email_verified_at,
			u.created_at,
			COUNT(DISTINCT m.id) AS workspace_count,
			COUNT(DISTINCT CASE WHEN s.expires_at > $2 THEN s.id END) AS active_session_count
		FROM users u
		LEFT JOIN workspace_members m ON m.user_id = u.id
		LEFT JOIN sessions s ON s.user_id = u.id
		`

// User is one user as the list shows it; ErrUserNotFound when there is
// none.
func (r *Admin) User(id string, now time.Time) (*model.AdminUser, error) {
	var user model.AdminUser
	err := r.db.Get(&user, adminUserSelect+`
		WHERE u.id = $1
		GROUP BY u.id, u.email, u.name, u.email_verified_at, u.created_at
	`, id, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Admin) CreateActivityEvent(event *model.AdminActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM admin_activity_events WHERE created_at < $1`, event.CreatedAt.Add(-model.AdminActivityRetention)); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO admin_activity_events (
			id, actor_user_id, workspace_id, action, target_type, target_id, metadata, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO NOTHING
	`, event.ID, event.ActorUserID, event.WorkspaceID, event.Action, event.TargetType, event.TargetID, event.Metadata, event.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Admin) StopImpersonationSession(sessionID, targetUserID, operatorUserID string, event *model.AdminActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		DELETE FROM sessions
		WHERE id = $1 AND user_id = $2 AND impersonator_user_id = $3
	`, sessionID, targetUserID, operatorUserID)
	if err != nil {
		return err
	}
	if err := requireSessionRow(result); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM admin_activity_events WHERE created_at < $1`, event.CreatedAt.Add(-model.AdminActivityRetention)); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO admin_activity_events (
			id, actor_user_id, workspace_id, action, target_type, target_id, metadata, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, event.ID, event.ActorUserID, event.WorkspaceID, event.Action, event.TargetType, event.TargetID, event.Metadata, event.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Admin) ListActivityEvents(limit int) ([]model.AdminActivityEvent, error) {
	events := []model.AdminActivityEvent{}
	err := r.db.Select(&events, `
		SELECT e.*, COALESCE(u.email, '') AS actor_email
		FROM admin_activity_events e
		LEFT JOIN users u ON u.id = e.actor_user_id
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $1
	`, limit)
	return events, err
}

func (r *Admin) DeleteActivityEventsBefore(cutoff time.Time) error {
	_, err := r.db.Exec(`DELETE FROM admin_activity_events WHERE created_at < $1`, cutoff)
	return err
}

// FirstWorkspace returns the first workspace the user created, nil when
// there is none.
func (r *Admin) FirstWorkspace(userID string) (*model.Workspace, error) {
	var workspace model.Workspace
	err := r.db.Get(&workspace, `SELECT * FROM workspaces WHERE created_by = $1 ORDER BY created_at, id LIMIT 1`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &workspace, nil
}
