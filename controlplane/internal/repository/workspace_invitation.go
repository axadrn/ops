package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/access"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/jmoiron/sqlx"
)

var (
	ErrWorkspaceInvitationNotFound = errors.New("invitation not found")
	ErrWorkspaceInvitationExpired  = errors.New("invitation expired")
	ErrWorkspaceInvitationRevoked  = errors.New("invitation revoked")
	ErrWorkspaceInvitationUsed     = errors.New("invitation already accepted")
	ErrWorkspaceInvitationEmail    = errors.New("invitation belongs to another email")
	ErrAlreadyMember               = errors.New("user is already a member")
	ErrSeatLimit                   = errors.New("workspace seat limit reached")
)

// WorkspaceInvitations stores the invitations to join a workspace.
type WorkspaceInvitations struct {
	db *sqlx.DB
}

func NewWorkspaceInvitations(db *sqlx.DB) *WorkspaceInvitations {
	return &WorkspaceInvitations{db: db}
}

func (r *WorkspaceInvitations) Create(invitation *model.WorkspaceInvitation, event *model.ActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := invitation.CreatedAt.Add(-model.WorkspaceInvitationRetention)
	if _, err := tx.Exec(`
		DELETE FROM workspace_invitations
		WHERE (accepted_at IS NOT NULL AND accepted_at < $1)
		   OR (revoked_at IS NOT NULL AND revoked_at < $1)
		   OR expires_at < $1
	`, cutoff); err != nil {
		return err
	}

	if _, err := tx.Exec(`UPDATE workspaces SET updated_at = updated_at WHERE id = $1`, invitation.WorkspaceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE workspace_invitations
		SET revoked_at = $1
		WHERE workspace_id = $2 AND email = $3 AND accepted_at IS NULL AND revoked_at IS NULL
	`, invitation.CreatedAt, invitation.WorkspaceID, invitation.Email); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO workspace_invitations (id, workspace_id, email, role, token_hash, invited_by, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, invitation.ID, invitation.WorkspaceID, invitation.Email, invitation.Role, invitation.TokenHash, invitation.InvitedBy, invitation.ExpiresAt, invitation.CreatedAt); err != nil {
		return err
	}
	if err := insertActivityEvent(tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *WorkspaceInvitations) ByIDForWorkspace(id, workspaceID string) (*model.WorkspaceInvitation, error) {
	invitation := &model.WorkspaceInvitation{}
	err := r.db.Get(invitation, `SELECT * FROM workspace_invitations WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err == sql.ErrNoRows {
		return nil, ErrWorkspaceInvitationNotFound
	}
	return invitation, err
}

func (r *WorkspaceInvitations) ByTokenHash(tokenHash string) (*model.WorkspaceInvitation, error) {
	invitation := &model.WorkspaceInvitation{}
	err := r.db.Get(invitation, `
		SELECT i.*, o.name AS workspace_name, o.slug AS workspace_slug, COALESCE(u.name, '') AS inviter_name
		FROM workspace_invitations i
		JOIN workspaces o ON o.id = i.workspace_id
		LEFT JOIN users u ON u.id = i.invited_by
		WHERE i.token_hash = $1
	`, tokenHash)
	if err == sql.ErrNoRows {
		return nil, ErrWorkspaceInvitationNotFound
	}
	return invitation, err
}

func (r *WorkspaceInvitations) ListPendingByWorkspace(workspaceID string, now time.Time) ([]model.WorkspaceInvitation, error) {
	cutoff := now.Add(-model.WorkspaceInvitationRetention)
	if _, err := r.db.Exec(`
		DELETE FROM workspace_invitations
		WHERE (accepted_at IS NOT NULL AND accepted_at < $1)
		   OR (revoked_at IS NOT NULL AND revoked_at < $1)
		   OR expires_at < $1
	`, cutoff); err != nil {
		return nil, err
	}
	var invitations []model.WorkspaceInvitation
	err := r.db.Select(&invitations, `
		SELECT * FROM workspace_invitations
		WHERE workspace_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at DESC, id DESC
	`, workspaceID, now)
	return invitations, err
}

func (r *WorkspaceInvitations) Revoke(workspaceID, invitationID string, revokedAt time.Time, event *model.ActivityEvent) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE workspaces SET updated_at = updated_at WHERE id = $1`, workspaceID); err != nil {
		return err
	}

	result, err := tx.Exec(`
		UPDATE workspace_invitations
		SET revoked_at = $1
		WHERE id = $2 AND workspace_id = $3 AND accepted_at IS NULL AND revoked_at IS NULL
	`, revokedAt, invitationID, workspaceID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrWorkspaceInvitationNotFound
	}
	if err := insertActivityEvent(tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

// Accept makes the user a member. event is the activity of it; Accept fills
// in its workspace and invitation.
func (r *WorkspaceInvitations) Accept(
	tokenHash, userID, email, memberID string,
	event *model.ActivityEvent,
	now time.Time,
) (*model.WorkspaceMember, error) {
	var workspaceID string
	if err := r.db.Get(&workspaceID, `SELECT workspace_id FROM workspace_invitations WHERE token_hash = $1`, tokenHash); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrWorkspaceInvitationNotFound
		}
		return nil, err
	}

	tx, err := r.db.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`UPDATE workspaces SET updated_at = updated_at WHERE id = $1`, workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, ErrWorkspaceNotFound
	}

	invitation := &model.WorkspaceInvitation{}
	if err := tx.Get(invitation, `SELECT * FROM workspace_invitations WHERE token_hash = $1`, tokenHash); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrWorkspaceInvitationNotFound
		}
		return nil, err
	}
	if invitation.AcceptedAt != nil {
		return nil, ErrWorkspaceInvitationUsed
	}
	if invitation.RevokedAt != nil {
		return nil, ErrWorkspaceInvitationRevoked
	}
	if !invitation.ExpiresAt.After(now) {
		return nil, ErrWorkspaceInvitationExpired
	}
	if !strings.EqualFold(invitation.Email, email) {
		return nil, ErrWorkspaceInvitationEmail
	}

	var existing int
	if err := tx.Get(&existing, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, ErrAlreadyMember
	}

	var members int
	if err := tx.Get(&members, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1`, workspaceID); err != nil {
		return nil, err
	}
	subscription := &model.Subscription{}
	if err := tx.Get(subscription, `SELECT * FROM subscriptions WHERE workspace_id = $1`, workspaceID); err != nil {
		return nil, err
	}
	if err := subscription.AccessAt(now).RequireWithin(access.LimitMembers, members); err != nil {
		return nil, ErrSeatLimit
	}

	member := &model.WorkspaceMember{
		ID:          memberID,
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        invitation.Role,
		CreatedAt:   now,
	}
	if _, err := tx.Exec(`
		INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, member.ID, member.WorkspaceID, member.UserID, member.Role, member.CreatedAt); err != nil {
		return nil, err
	}
	accepted, err := tx.Exec(`
		UPDATE workspace_invitations
		SET accepted_at = $1
		WHERE id = $2 AND accepted_at IS NULL AND revoked_at IS NULL
	`, now, invitation.ID)
	if err != nil {
		return nil, err
	}
	acceptedRows, err := accepted.RowsAffected()
	if err != nil {
		return nil, err
	}
	if acceptedRows != 1 {
		return nil, ErrWorkspaceInvitationUsed
	}
	event.WorkspaceID, event.TargetType, event.TargetID = workspaceID, "workspace_invitation", invitation.ID
	if err := insertActivityEvent(tx, event); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return member, nil
}

// HasPending reports whether a workspace invited the address and the
// invitation is still open.
func (r *WorkspaceInvitations) HasPending(email string, now time.Time) (bool, error) {
	var count int
	err := r.db.Get(&count, `
		SELECT COUNT(*) FROM workspace_invitations
		WHERE LOWER(email) = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > $2
	`, email, now)
	return count > 0, err
}
