package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var (
	ErrWorkspaceMemberNotFound   = errors.New("member not found")
	ErrWorkspaceMemberPermission = errors.New("member permission denied")
	ErrLastOwner                 = errors.New("workspace must keep at least one owner")
)

func (r *WorkspaceMembers) ByIDForWorkspace(id, workspaceID string) (*model.WorkspaceMember, error) {
	member := &model.WorkspaceMember{}
	err := r.db.Get(member, `SELECT * FROM workspace_members WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err == sql.ErrNoRows {
		return nil, ErrWorkspaceMemberNotFound
	}
	return member, err
}

// WorkspaceMembers stores who belongs to a workspace and with which role.
type WorkspaceMembers struct {
	db *sqlx.DB
}

func NewWorkspaceMembers(db *sqlx.DB) *WorkspaceMembers {
	return &WorkspaceMembers{db: db}
}

func (r *WorkspaceMembers) ByWorkspaceAndUser(workspaceID, userID string) (*model.WorkspaceMember, error) {
	member := &model.WorkspaceMember{}
	err := r.db.Get(member, `SELECT * FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
	if err == sql.ErrNoRows {
		return nil, ErrWorkspaceMemberNotFound
	}
	return member, err
}

// AlertEmails returns the addresses that get the email alerts of a
// workspace: its owners and admins with the switch on and a verified address.
func (r *WorkspaceMembers) AlertEmails(workspaceID string) ([]string, error) {
	emails := []string{}
	err := r.db.Select(&emails, `
		SELECT u.email FROM workspace_members m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.role IN ($2, $3) AND u.email_alerts = 1 AND u.email_verified_at IS NOT NULL
		ORDER BY u.email`, workspaceID, model.WorkspaceRoleOwner, model.WorkspaceRoleAdmin)
	return emails, err
}

func (r *WorkspaceMembers) ListByWorkspace(workspaceID string) ([]model.WorkspaceMember, error) {
	var members []model.WorkspaceMember
	err := r.db.Select(&members, `
		SELECT m.*, u.email, u.name
		FROM workspace_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1
		ORDER BY CASE m.role WHEN 'owner' THEN 1 WHEN 'admin' THEN 2 ELSE 3 END, u.name, u.email
	`, workspaceID)
	return members, err
}

func (r *WorkspaceMembers) CountByWorkspace(workspaceID string) (int, error) {
	var count int
	err := r.db.Get(&count, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1`, workspaceID)
	return count, err
}

func (r *WorkspaceMembers) CountOwners(workspaceID string) (int, error) {
	var count int
	err := r.db.Get(&count, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'owner'`, workspaceID)
	return count, err
}

func (r *WorkspaceMembers) UpdateRole(workspaceID, actorUserID, memberID, role string, event *model.ActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE workspaces SET updated_at = updated_at WHERE id = $1`, workspaceID); err != nil {
		return err
	}
	actor := &model.WorkspaceMember{}
	if err := tx.Get(actor, `SELECT * FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, actorUserID); err != nil {
		if err == sql.ErrNoRows {
			return ErrWorkspaceMemberPermission
		}
		return err
	}
	current := &model.WorkspaceMember{}
	if err := tx.Get(current, `SELECT * FROM workspace_members WHERE id = $1 AND workspace_id = $2`, memberID, workspaceID); err != nil {
		if err == sql.ErrNoRows {
			return ErrWorkspaceMemberNotFound
		}
		return err
	}
	if !actor.Can(model.WorkspacePermissionManageMembers) ||
		((current.Role == model.WorkspaceRoleOwner || role == model.WorkspaceRoleOwner) && !actor.Can(model.WorkspacePermissionManageOwners)) {
		return ErrWorkspaceMemberPermission
	}
	if current.Role == model.WorkspaceRoleOwner && role != model.WorkspaceRoleOwner {
		var owners int
		if err := tx.Get(&owners, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'owner'`, workspaceID); err != nil {
			return err
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	result, err := tx.Exec(`
		UPDATE workspace_members
		SET role = $1
		WHERE id = $2 AND workspace_id = $3
	`, role, memberID, workspaceID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrWorkspaceMemberNotFound
	}
	event.Metadata = activityMetadata(map[string]string{"email": userEmail(tx, current.UserID), "from": current.Role, "to": role})
	if err := insertActivityEvent(tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *WorkspaceMembers) Delete(workspaceID, actorUserID, memberID string, event *model.ActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE workspaces SET updated_at = updated_at WHERE id = $1`, workspaceID); err != nil {
		return err
	}
	actor := &model.WorkspaceMember{}
	if err := tx.Get(actor, `SELECT * FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, actorUserID); err != nil {
		if err == sql.ErrNoRows {
			return ErrWorkspaceMemberPermission
		}
		return err
	}
	current := &model.WorkspaceMember{}
	if err := tx.Get(current, `SELECT * FROM workspace_members WHERE id = $1 AND workspace_id = $2`, memberID, workspaceID); err != nil {
		if err == sql.ErrNoRows {
			return ErrWorkspaceMemberNotFound
		}
		return err
	}
	isLeaving := actor.ID == current.ID
	if !isLeaving && (!actor.Can(model.WorkspacePermissionManageMembers) ||
		(current.Role == model.WorkspaceRoleOwner && !actor.Can(model.WorkspacePermissionManageOwners))) {
		return ErrWorkspaceMemberPermission
	}
	if current.Role == model.WorkspaceRoleOwner {
		var owners int
		if err := tx.Get(&owners, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1 AND role = 'owner'`, workspaceID); err != nil {
			return err
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	result, err := tx.Exec(`DELETE FROM workspace_members WHERE id = $1 AND workspace_id = $2`, memberID, workspaceID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrWorkspaceMemberNotFound
	}
	if !isLeaving {
		event.Metadata = activityMetadata(map[string]string{"user_id": current.UserID, "email": userEmail(tx, current.UserID), "role": current.Role})
	}
	if err := insertActivityEvent(tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// userEmail is the email of a user, empty for one that is gone.
func userEmail(tx *sqlx.Tx, userID string) string {
	var email string
	_ = tx.Get(&email, `SELECT email FROM users WHERE id = $1`, userID)
	return email
}

func activityMetadata(values map[string]string) string {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func (r *WorkspaceMembers) DeleteUserIfOwnershipSafe(userID string) error {
	var workspaceIDs []string
	if err := r.db.Select(&workspaceIDs, `
		SELECT workspace_id
		FROM workspace_members
		WHERE user_id = $1
		ORDER BY workspace_id
	`, userID); err != nil {
		return err
	}

	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, workspaceID := range workspaceIDs {
		if _, err := tx.Exec(`UPDATE workspaces SET updated_at = updated_at WHERE id = $1`, workspaceID); err != nil {
			return err
		}
	}

	var soleOwnerCount int
	if err := tx.Get(&soleOwnerCount, `
		SELECT COUNT(*)
		FROM workspace_members mine
		WHERE mine.user_id = $1
		  AND mine.role = 'owner'
		  AND NOT EXISTS (
		      SELECT 1 FROM workspace_members other
		      WHERE other.workspace_id = mine.workspace_id
		        AND other.role = 'owner'
		        AND other.user_id != mine.user_id
		  )
	`, userID); err != nil {
		return err
	}
	if soleOwnerCount > 0 {
		return ErrLastOwner
	}

	// Each workspace keeps in its activity that the member's account went;
	// the event keeps the email after the user is gone.
	now := time.Now().UTC()
	for _, workspaceID := range workspaceIDs {
		actor := userID
		err := insertActivityEvent(tx, &model.ActivityEvent{
			ID:          uuid.New().String(),
			WorkspaceID: workspaceID,
			ActorUserID: &actor,
			Action:      "workspace.member.account_deleted",
			TargetType:  "user",
			TargetID:    userID,
			Metadata:    "{}",
			CreatedAt:   now,
		})
		if err != nil {
			return err
		}
	}

	result, err := tx.Exec(`DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrUserNotFound
	}
	return tx.Commit()
}
