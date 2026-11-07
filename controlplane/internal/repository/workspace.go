package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/jmoiron/sqlx"
)

var (
	ErrWorkspaceNotFound      = errors.New("workspace not found")
	ErrWorkspaceBillingActive = errors.New("workspace has active billing")
	// ErrWorkspaceSlugTaken means another workspace has the slug.
	ErrWorkspaceSlugTaken = errors.New("workspace slug taken")
)

// Workspaces stores the workspaces that own everything else.
type Workspaces struct {
	db *sqlx.DB
}

func NewWorkspaces(db *sqlx.DB) *Workspaces {
	return &Workspaces{db: db}
}

func (r *Workspaces) CreateWithOwner(
	workspace *model.Workspace,
	member *model.WorkspaceMember,
	subscription *model.Subscription,
	event *model.ActivityEvent,
) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO workspaces (id, name, slug, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, workspace.ID, workspace.Name, workspace.Slug, workspace.CreatedBy, workspace.CreatedAt, workspace.UpdatedAt); err != nil {
		if isSlugConflict(err) {
			return ErrWorkspaceSlugTaken
		}
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, member.ID, member.WorkspaceID, member.UserID, member.Role, member.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO subscriptions (id, workspace_id, plan_id, status, current_period_end, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, subscription.ID, subscription.WorkspaceID, subscription.PlanID, subscription.Status, subscription.CurrentPeriodEnd, subscription.CreatedAt, subscription.UpdatedAt); err != nil {
		return err
	}
	if err := insertActivityEvent(tx, event); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *Workspaces) ByID(id string) (*model.Workspace, error) {
	workspace := &model.Workspace{}
	if err := r.db.Get(workspace, `SELECT * FROM workspaces WHERE id = $1`, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrWorkspaceNotFound
		}
		return nil, err
	}
	return workspace, nil
}

func (r *Workspaces) BySlugForUser(slug, userID string) (*model.Workspace, error) {
	workspace := &model.Workspace{}
	err := r.db.Get(workspace, `
		SELECT o.*
		FROM workspaces o
		JOIN workspace_members m ON m.workspace_id = o.id
		WHERE o.slug = $1 AND m.user_id = $2
	`, slug, userID)
	if err == sql.ErrNoRows {
		return nil, ErrWorkspaceNotFound
	}
	return workspace, err
}

func (r *Workspaces) ListForUser(userID string) ([]model.WorkspaceMembership, error) {
	var workspaces []model.WorkspaceMembership
	err := r.db.Select(&workspaces, `
		SELECT o.*, m.role
		FROM workspace_members m
		JOIN workspaces o ON o.id = m.workspace_id
		WHERE m.user_id = $1
		ORDER BY o.name, o.id
	`, userID)
	return workspaces, err
}

func (r *Workspaces) UpdateName(id, name string, updatedAt time.Time, event *model.ActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE workspaces SET name = $1, updated_at = $2 WHERE id = $3`, name, updatedAt, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrWorkspaceNotFound
	}
	if err := insertActivityEvent(tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Workspaces) DeleteIfBillingInactive(id string, now time.Time, adminEvent *model.AdminActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE workspaces SET updated_at = updated_at WHERE id = $1`, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrWorkspaceNotFound
	}
	subscription := &model.Subscription{}
	if err := tx.Get(subscription, `SELECT * FROM subscriptions WHERE workspace_id = $1`, id); err != nil {
		return err
	}
	if subscription.BlocksWorkspaceDeletion(now) {
		return ErrWorkspaceBillingActive
	}
	if _, err := tx.Exec(`DELETE FROM workspaces WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM admin_activity_events WHERE created_at < $1`, now.Add(-model.AdminActivityRetention)); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO admin_activity_events (
			id, actor_user_id, workspace_id, action, target_type, target_id, metadata, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, adminEvent.ID, adminEvent.ActorUserID, adminEvent.WorkspaceID, adminEvent.Action, adminEvent.TargetType, adminEvent.TargetID, adminEvent.Metadata, adminEvent.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// isSlugConflict reports whether err is the unique index on the slug, in
// SQLite or Postgres.
func isSlugConflict(err error) bool {
	message := err.Error()
	return strings.Contains(message, "workspaces.slug") || strings.Contains(message, "workspaces_slug_key")
}
