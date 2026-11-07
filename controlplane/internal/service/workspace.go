package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/access"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/validation"
	"github.com/google/uuid"
)

var (
	ErrWorkspacePermission = errors.New("workspace permission denied")
	ErrInvalidWorkspace    = errors.New("invalid workspace")
	ErrInvalidRole         = errors.New("invalid workspace role")
	ErrInvalidInviteEmail  = errors.New("invalid invitation email")
	ErrActiveSubscription  = errors.New("finish checkout or end paid access before deleting the workspace")
	// ErrActivityPermission is returned to members who may not view
	// workspace activity.
	ErrActivityPermission = errors.New("workspace activity permission required")
)

type WorkspaceService struct {
	workspaces    *repository.Workspaces
	members       *repository.WorkspaceMembers
	invitations   *repository.WorkspaceInvitations
	activity      *repository.Activity
	users         *repository.Users
	subscriptions *SubscriptionService
	email         *EmailService
}

func NewWorkspaceService(
	workspaces *repository.Workspaces,
	members *repository.WorkspaceMembers,
	invitations *repository.WorkspaceInvitations,
	activity *repository.Activity,
	users *repository.Users,
	subscriptions *SubscriptionService,
	email *EmailService,
) *WorkspaceService {
	return &WorkspaceService{
		workspaces:    workspaces,
		members:       members,
		invitations:   invitations,
		activity:      activity,
		users:         users,
		subscriptions: subscriptions,
		email:         email,
	}
}

func (s *WorkspaceService) Create(userID, name string) (*model.Workspace, error) {
	return s.create(userID, name, name)
}

// create makes a workspace whose slug comes from slugFrom: the short form
// first, then with -2 to -9, then with a random suffix. The unique index on
// the slug decides, so two workspaces created at once never share one.
func (s *WorkspaceService) create(userID, name, slugFrom string) (*model.Workspace, error) {
	name = strings.TrimSpace(name)
	if err := validation.ValidateWorkspaceName(name); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWorkspace, err)
	}
	base := workspaceSlug(slugFrom)
	for attempt := 1; ; attempt++ {
		slug := base
		switch {
		case attempt > 12:
			return nil, fmt.Errorf("create workspace: %w", repository.ErrWorkspaceSlugTaken)
		case attempt > 9:
			slug = base + "-" + strings.ReplaceAll(uuid.New().String()[:6], "-", "")
		case attempt > 1:
			slug = base + "-" + strconv.Itoa(attempt)
		}
		workspace, err := s.insert(userID, name, slug)
		if errors.Is(err, repository.ErrWorkspaceSlugTaken) {
			continue
		}
		return workspace, err
	}
}

func (s *WorkspaceService) insert(userID, name, slug string) (*model.Workspace, error) {
	workspaceID := uuid.New().String()
	now := time.Now()
	workspace := &model.Workspace{
		ID:        workspaceID,
		Name:      name,
		Slug:      slug,
		CreatedBy: &userID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	member := &model.WorkspaceMember{
		ID:          uuid.New().String(),
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        model.WorkspaceRoleOwner,
		CreatedAt:   now,
	}
	initial := access.Initial(now)
	// The trial comes once per person: a second workspace of its creator
	// starts on the free plan.
	if initial.Status == access.StatusTrialing && s.createdWorkspace(userID) {
		initial = access.InitialSubscription{PlanID: access.PlanFree, Status: access.StatusActive}
	}
	subscription := &model.Subscription{
		ID:               uuid.New().String(),
		WorkspaceID:      workspaceID,
		PlanID:           initial.PlanID,
		Status:           initial.Status,
		CurrentPeriodEnd: initial.CurrentPeriodEnd,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	event := activityEvent(workspaceID, userID, Source{}, "workspace.created", "workspace", workspaceID, nil, now)
	err := s.workspaces.CreateWithOwner(workspace, member, subscription, event)
	if errors.Is(err, repository.ErrWorkspaceSlugTaken) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	return workspace, nil
}

// createdWorkspace says whether the user created a workspace before.
func (s *WorkspaceService) createdWorkspace(userID string) bool {
	workspaces, err := s.ListForUser(userID)
	if err != nil {
		return true
	}
	for _, workspace := range workspaces {
		if workspace.CreatedBy != nil && *workspace.CreatedBy == userID {
			return true
		}
	}
	return false
}

func (s *WorkspaceService) CreateDefault(userID, userName string) (*model.Workspace, error) {
	// The slug is the person's name: ada-lovelace, not
	// ada-lovelace-s-workspace.
	return s.create(userID, DefaultWorkspaceName(userName), userName)
}

// DefaultWorkspaceName is the name the first workspace of a person gets
// unless they choose another: "Ada Lovelace's Workspace".
func DefaultWorkspaceName(userName string) string {
	name := strings.TrimSpace(userName) + "'s Workspace"
	if len(name) > 80 {
		return "My Workspace"
	}
	return name
}

// EnsureFirst returns the first workspace of the user or creates it, named
// workspaceName. Without a name, or with the default one, it is the default
// workspace of CreateDefault.
func (s *WorkspaceService) EnsureFirst(userID, userName, workspaceName string) (*model.Workspace, error) {
	members, err := s.workspaces.ListForUser(userID)
	if err != nil {
		return nil, err
	}
	if len(members) > 0 {
		return s.workspaces.ByID(members[0].ID)
	}
	workspaceName = strings.TrimSpace(workspaceName)
	if workspaceName == "" || workspaceName == DefaultWorkspaceName(userName) {
		return s.CreateDefault(userID, userName)
	}
	return s.Create(userID, workspaceName)
}

func (s *WorkspaceService) BySlugForUser(slug, userID string) (*model.Workspace, *model.WorkspaceMember, error) {
	workspace, err := s.workspaces.BySlugForUser(slug, userID)
	if err != nil {
		return nil, nil, err
	}
	member, err := s.members.ByWorkspaceAndUser(workspace.ID, userID)
	if err != nil {
		return nil, nil, err
	}
	return workspace, member, nil
}

func (s *WorkspaceService) ListForUser(userID string) ([]model.WorkspaceMembership, error) {
	return s.workspaces.ListForUser(userID)
}

func (s *WorkspaceService) Members(viewer *model.WorkspaceMember) ([]model.WorkspaceMember, error) {
	if viewer == nil {
		return nil, ErrWorkspacePermission
	}
	members, err := s.members.ListByWorkspace(viewer.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if !viewer.Can(model.WorkspacePermissionManageMembers) {
		for i := range members {
			members[i].Email = ""
		}
	}
	return members, nil
}

func (s *WorkspaceService) PendingInvitations(workspaceID string) ([]model.WorkspaceInvitation, error) {
	return s.invitations.ListPendingByWorkspace(workspaceID, time.Now())
}

func (s *WorkspaceService) Activity(workspaceID string) ([]model.ActivityEvent, error) {
	return s.activity.ListByWorkspace(workspaceID, 100)
}

// ActivityPage returns one page of the activity of the member's
// workspace, newest first, and the event ID to continue after, empty on the
// last page. Members who may not view it get ErrActivityPermission.
func (s *WorkspaceService) ActivityPage(member *model.WorkspaceMember, filter model.ActivityFilter, afterID string, limit int) ([]model.ActivityEvent, string, error) {
	if member == nil || !member.Can(model.WorkspacePermissionViewActivity) {
		return nil, "", ErrActivityPermission
	}
	events, err := s.activity.ListPage(member.WorkspaceID, filter, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	if len(events) <= limit {
		return events, "", nil
	}
	events = events[:limit]
	return events, events[limit-1].ID, nil
}

// ActivityChoices returns what the activity of a workspace can be filtered
// by.
func (s *WorkspaceService) ActivityChoices(workspaceID string) (model.ActivityChoices, error) {
	return s.activity.Choices(workspaceID)
}

func (s *WorkspaceService) UpdateName(actor *model.WorkspaceMember, workspace *model.Workspace, name string, source Source) error {
	if actor == nil || workspace == nil || actor.WorkspaceID != workspace.ID || !actor.Can(model.WorkspacePermissionManageWorkspace) {
		return ErrWorkspacePermission
	}
	if err := validation.ValidateWorkspaceName(name); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkspace, err)
	}
	name = strings.TrimSpace(name)
	now := time.Now()
	event := activityEvent(workspace.ID, actor.UserID, source, "workspace.updated", "workspace", workspace.ID, map[string]string{"from": workspace.Name, "name": name}, now)
	if err := s.workspaces.UpdateName(workspace.ID, name, now, event); err != nil {
		return fmt.Errorf("update workspace: %w", err)
	}
	return nil
}

// The changes of the team take source, how the change came in, for the
// activity.
func (s *WorkspaceService) Invite(actor *model.WorkspaceMember, workspace *model.Workspace, email, role string, source Source) error {
	if actor == nil || workspace == nil || actor.WorkspaceID != workspace.ID || !actor.Can(model.WorkspacePermissionManageMembers) {
		return ErrWorkspacePermission
	}
	if role != model.WorkspaceRoleAdmin && role != model.WorkspaceRoleMember {
		return ErrInvalidRole
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if err := validation.ValidateEmail(email); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInviteEmail, err)
	}
	user, err := s.users.ByEmail(email)
	if err != nil && !errors.Is(err, repository.ErrUserNotFound) {
		return fmt.Errorf("look up invited user: %w", err)
	}
	if err == nil {
		if _, err := s.members.ByWorkspaceAndUser(workspace.ID, user.ID); err == nil {
			return repository.ErrAlreadyMember
		} else if !errors.Is(err, repository.ErrWorkspaceMemberNotFound) {
			return fmt.Errorf("check existing member: %w", err)
		}
	}
	currentAccess, err := s.subscriptions.Access(workspace.ID)
	if err != nil {
		return fmt.Errorf("load access for invitation: %w", err)
	}
	members, err := s.members.CountByWorkspace(workspace.ID)
	if err != nil {
		return fmt.Errorf("count members for invitation: %w", err)
	}
	if err := currentAccess.RequireWithin(access.LimitMembers, members); err != nil {
		return repository.ErrSeatLimit
	}

	token, tokenHash, err := newInvitationToken()
	if err != nil {
		return fmt.Errorf("create invitation token: %w", err)
	}
	now := time.Now()
	invitation := &model.WorkspaceInvitation{
		ID:          uuid.New().String(),
		WorkspaceID: workspace.ID,
		Email:       email,
		Role:        role,
		TokenHash:   tokenHash,
		InvitedBy:   actor.UserID,
		ExpiresAt:   now.Add(7 * 24 * time.Hour),
		CreatedAt:   now,
	}
	metadata := map[string]string{"email": email, "role": role}
	event := activityEvent(workspace.ID, actor.UserID, source, "workspace.invitation.created", "workspace_invitation", invitation.ID, metadata, now)
	if err := s.invitations.Create(invitation, event); err != nil {
		return fmt.Errorf("create invitation: %w", err)
	}
	inviter, _ := s.users.ByID(actor.UserID)
	inviterName := "A workspace member"
	if inviter != nil {
		inviterName = inviter.Email
	}
	if err := s.email.SendWorkspaceInvitationEmail(email, token, workspace.Name, inviterName); err != nil {
		return fmt.Errorf("send invitation: %w", err)
	}
	if err := s.activity.Create(activityEvent(workspace.ID, actor.UserID, source, "workspace.invitation.sent", "workspace_invitation", invitation.ID, metadata, time.Now())); err != nil {
		slog.Warn("failed to record invitation delivery", "error", err, "workspace_id", workspace.ID, "invitation_id", invitation.ID)
	}
	return nil
}

func (s *WorkspaceService) ResendInvitation(actor *model.WorkspaceMember, workspace *model.Workspace, invitationID string, source Source) error {
	if actor == nil || workspace == nil || actor.WorkspaceID != workspace.ID || !actor.Can(model.WorkspacePermissionManageMembers) {
		return ErrWorkspacePermission
	}
	invitation, err := s.invitations.ByIDForWorkspace(invitationID, workspace.ID)
	if err != nil {
		return err
	}
	if invitation.AcceptedAt != nil || invitation.RevokedAt != nil {
		return repository.ErrWorkspaceInvitationNotFound
	}
	return s.Invite(actor, workspace, invitation.Email, invitation.Role, source)
}

func (s *WorkspaceService) RevokeInvitation(actor *model.WorkspaceMember, workspaceID, invitationID string, source Source) error {
	if actor == nil || actor.WorkspaceID != workspaceID || !actor.Can(model.WorkspacePermissionManageMembers) {
		return ErrWorkspacePermission
	}
	now := time.Now()
	var metadata map[string]string
	if invitation, err := s.invitations.ByIDForWorkspace(invitationID, workspaceID); err == nil {
		metadata = map[string]string{"email": invitation.Email, "role": invitation.Role}
	}
	event := activityEvent(workspaceID, actor.UserID, source, "workspace.invitation.revoked", "workspace_invitation", invitationID, metadata, now)
	return s.invitations.Revoke(workspaceID, invitationID, now, event)
}

func (s *WorkspaceService) WorkspaceInvitation(token string) (*model.WorkspaceInvitation, error) {
	invitation, err := s.invitations.ByTokenHash(hashInvitationToken(token))
	if err != nil {
		return nil, err
	}
	if invitation.AcceptedAt != nil {
		return nil, repository.ErrWorkspaceInvitationUsed
	}
	if invitation.RevokedAt != nil {
		return nil, repository.ErrWorkspaceInvitationRevoked
	}
	if !invitation.ExpiresAt.After(time.Now()) {
		return nil, repository.ErrWorkspaceInvitationExpired
	}
	return invitation, nil
}

func (s *WorkspaceService) AcceptInvitation(token string, user *model.User, source Source) (*model.Workspace, error) {
	invitation, err := s.WorkspaceInvitation(token)
	if err != nil {
		return nil, err
	}
	if _, err := s.invitations.Accept(
		hashInvitationToken(token),
		user.ID,
		user.Email,
		uuid.New().String(),
		activityEvent("", user.ID, source, "workspace.invitation.accepted", "", "", map[string]string{"email": user.Email, "role": invitation.Role}, time.Now()),
		time.Now(),
	); err != nil {
		return nil, err
	}
	return s.workspaces.ByID(invitation.WorkspaceID)
}

func (s *WorkspaceService) ChangeRole(actor *model.WorkspaceMember, workspaceID, memberID, role string, source Source) error {
	if actor == nil || actor.WorkspaceID != workspaceID || !actor.Can(model.WorkspacePermissionManageMembers) {
		return ErrWorkspacePermission
	}
	if role != model.WorkspaceRoleOwner && role != model.WorkspaceRoleAdmin && role != model.WorkspaceRoleMember {
		return ErrInvalidRole
	}
	now := time.Now()
	event := activityEvent(workspaceID, actor.UserID, source, "workspace.member.role_changed", "workspace_member", memberID, nil, now)
	err := s.members.UpdateRole(workspaceID, actor.UserID, memberID, role, event)
	if errors.Is(err, repository.ErrWorkspaceMemberPermission) {
		return ErrWorkspacePermission
	}
	return err
}

func (s *WorkspaceService) RemoveMember(actor *model.WorkspaceMember, workspaceID, memberID string, source Source) error {
	if actor == nil || actor.WorkspaceID != workspaceID || !actor.Can(model.WorkspacePermissionManageMembers) {
		return ErrWorkspacePermission
	}
	now := time.Now()
	event := activityEvent(workspaceID, actor.UserID, source, "workspace.member.removed", "workspace_member", memberID, nil, now)
	err := s.members.Delete(workspaceID, actor.UserID, memberID, event)
	if errors.Is(err, repository.ErrWorkspaceMemberPermission) {
		return ErrWorkspacePermission
	}
	return err
}

func (s *WorkspaceService) Leave(workspaceID, userID string, source Source) error {
	member, err := s.members.ByWorkspaceAndUser(workspaceID, userID)
	if err != nil {
		return err
	}
	now := time.Now()
	event := activityEvent(workspaceID, userID, source, "workspace.member.left", "workspace_member", member.ID, nil, now)
	return s.members.Delete(workspaceID, userID, member.ID, event)
}

func (s *WorkspaceService) Delete(actor *model.WorkspaceMember, workspace *model.Workspace) error {
	if actor == nil || workspace == nil || actor.WorkspaceID != workspace.ID || !actor.Can(model.WorkspacePermissionDeleteWorkspace) {
		return ErrWorkspacePermission
	}
	now := time.Now()
	metadata, err := json.Marshal(map[string]string{"workspace_name": workspace.Name})
	if err != nil {
		return err
	}
	workspaceID := workspace.ID
	actorUserID := actor.UserID
	adminEvent := &model.AdminActivityEvent{
		ID:          uuid.New().String(),
		ActorUserID: &actorUserID,
		WorkspaceID: &workspaceID,
		Action:      "workspace.deleted",
		TargetType:  "workspace",
		TargetID:    workspace.ID,
		Metadata:    string(metadata),
		CreatedAt:   now,
	}
	err = s.workspaces.DeleteIfBillingInactive(workspace.ID, now, adminEvent)
	if errors.Is(err, repository.ErrWorkspaceBillingActive) {
		return ErrActiveSubscription
	}
	return err
}

func (s *WorkspaceService) DeleteUserIfOwnershipSafe(userID string) error {
	return s.members.DeleteUserIfOwnershipSafe(userID)
}

func (s *WorkspaceService) RecordBillingAction(workspaceID, userID, action string, source Source) error {
	event := activityEvent(workspaceID, userID, source, action, "subscription", workspaceID, nil, time.Now())
	return s.activity.Create(event)
}

func newInvitationToken() (string, string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	return token, hashInvitationToken(token), nil
}

func hashInvitationToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// reservedSlugs are names that read like a page of the app, not a workspace.
var reservedSlugs = map[string]bool{
	"admin": true, "api": true, "app": true, "assets": true, "auth": true, "billing": true, "dashboard": true,
	"docs": true, "github": true, "new": true, "settings": true, "workspace": true, "workspaces": true,
}

// slugLetters spells common accented Latin letters in ASCII.
var slugLetters = strings.NewReplacer(
	"ä", "a", "ö", "o", "ü", "u", "ß", "ss", "à", "a", "á", "a", "â", "a", "ã", "a", "å", "a", "æ", "ae",
	"ç", "c", "č", "c", "è", "e", "é", "e", "ê", "e", "ë", "e", "ě", "e", "ì", "i", "í", "i", "î", "i",
	"ï", "i", "ı", "i", "ł", "l", "ñ", "n", "ò", "o", "ó", "o", "ô", "o", "õ", "o", "ø", "o", "œ", "oe",
	"ř", "r", "š", "s", "ş", "s", "ù", "u", "ú", "u", "û", "u", "ý", "y", "ÿ", "y", "ž", "z", "ğ", "g",
)

// workspaceSlug is the short URL form of a name: lower case ASCII letters,
// digits and dashes, at most 40 characters. A name without any, or one that
// reads like a page of the app, becomes "team".
func workspaceSlug(name string) string {
	var slug strings.Builder
	lastDash := false
	for _, character := range slugLetters.Replace(strings.ToLower(name)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			slug.WriteRune(character)
			lastDash = false
		} else if !lastDash && slug.Len() > 0 {
			slug.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(slug.String(), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if len(base) < 2 || reservedSlugs[base] {
		return "team"
	}
	return base
}

// withVia adds how the change came in to the details of an event.
func activityEvent(workspaceID, actorUserID string, source Source, action, targetType, targetID string, metadata map[string]string, now time.Time) *model.ActivityEvent {
	encoded := "{}"
	if len(metadata) > 0 {
		if value, err := json.Marshal(metadata); err == nil {
			encoded = string(value)
		}
	}
	actor := actorUserID
	return &model.ActivityEvent{
		ID:          uuid.New().String(),
		WorkspaceID: workspaceID,
		ActorUserID: &actor,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    encoded,
		Via:         source.Via,
		TokenName:   source.Token,
		IP:          source.IP,
		UserAgent:   clipText(source.UserAgent, 300),
		CreatedAt:   now.UTC(),
	}
}
