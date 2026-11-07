package service

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/axadrn/moin/controlplane/internal/access"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/internal/build"
	"github.com/google/uuid"
)

const (
	adminPageSize = 25
)

var (
	ErrAdminPermission       = errors.New("admin permission required")
	ErrAdminSelfAction       = errors.New("admin cannot perform this action on the current account")
	ErrAdminTargetIsOperator = errors.New("operator accounts cannot be impersonated")
	// ErrNoWorkspace means the user created no workspace that could be free
	// forever.
	ErrNoWorkspace = errors.New("this user has no workspace of their own")
	// ErrPaying means the workspace pays through the payment provider:
	// cancel the subscription before making it free forever.
	ErrPaying = errors.New("this workspace pays for its plan: cancel the subscription before making it free forever")
)

type AdminService struct {
	repository  *repository.Admin
	users       *repository.Users
	auth        *AuthService
	adminEmails map[string]struct{}
	// network, when set, counts what moin runs everywhere (UseNetwork).
	network func() model.Network
	// subscriptions and activity, when set, show and change who is free
	// forever.
	subscriptions *repository.Subscriptions
	activity      *repository.Activity
	// oldDomain counts what still arrives on old domains.
	oldDomain *OldDomainService
}

func NewAdminService(repo *repository.Admin, users *repository.Users, auth *AuthService, adminEmails []string) *AdminService {
	allowed := make(map[string]struct{}, len(adminEmails))
	for _, email := range adminEmails {
		allowed[strings.ToLower(strings.TrimSpace(email))] = struct{}{}
	}
	return &AdminService{repository: repo, users: users, auth: auth, adminEmails: allowed}
}

// UsePlans shows the plan of every user and lets admins make them free
// forever. Without billing there are no plans to show.
func (s *AdminService) UsePlans(subscriptions *repository.Subscriptions, activity *repository.Activity) {
	s.subscriptions = subscriptions
	s.activity = activity
}

func (s *AdminService) IsAdmin(user *model.User, session *model.Session) bool {
	if user == nil || user.EmailVerifiedAt == nil || session == nil || session.IsImpersonating() {
		return false
	}
	_, ok := s.adminEmails[strings.ToLower(user.Email)]
	return ok
}

// UseOldDomain has the overview show the traffic on the old domains.
func (s *AdminService) UseOldDomain(oldDomain *OldDomainService) {
	s.oldDomain = oldDomain
}

// OldDomain is the traffic on the old domains of the control plane, for
// admins.
func (s *AdminService) OldDomain(user *model.User, session *model.Session) (*model.OldDomain, error) {
	if !s.IsAdmin(user, session) {
		return nil, ErrAdminPermission
	}
	return s.oldDomain.Report()
}

func (s *AdminService) Overview(user *model.User, session *model.Session, query string, page int) (*model.AdminOverview, error) {
	if !s.IsAdmin(user, session) {
		return nil, ErrAdminPermission
	}
	if page < 1 {
		page = 1
	}
	now := time.Now()
	if err := s.repository.DeleteActivityEventsBefore(now.Add(-model.AdminActivityRetention)); err != nil {
		return nil, err
	}
	metrics, err := s.repository.Metrics(now)
	if err != nil {
		return nil, err
	}
	users, total, err := s.repository.ListUsers(query, adminPageSize, (page-1)*adminPageSize, now)
	if err != nil {
		return nil, err
	}
	for i := range users {
		_, users[i].IsAdmin = s.adminEmails[strings.ToLower(users[i].Email)]
		if err := s.describePlan(&users[i], now); err != nil {
			return nil, err
		}
	}
	totalPages := (total + adminPageSize - 1) / adminPageSize
	if totalPages == 0 {
		totalPages = 1
	}
	events, err := s.repository.ListActivityEvents(50)
	if err != nil {
		return nil, err
	}
	return &model.AdminOverview{
		Metrics: metrics,
		Users: model.AdminUserPage{
			Users:      users,
			Query:      strings.TrimSpace(query),
			Page:       page,
			TotalPages: totalPages,
			TotalUsers: total,
		},
		Events:  events,
		Network: s.networkCounts(),
	}, nil
}

// User is one user for its admin page: the same facts as the list.
func (s *AdminService) User(actor *model.User, actorSession *model.Session, userID string) (*model.AdminUser, error) {
	if !s.IsAdmin(actor, actorSession) {
		return nil, ErrAdminPermission
	}
	now := time.Now()
	user, err := s.repository.User(userID, now)
	if err != nil {
		return nil, err
	}
	_, user.IsAdmin = s.adminEmails[strings.ToLower(user.Email)]
	if err := s.describePlan(user, now); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *AdminService) RevokeUserSessions(actor *model.User, actorSession *model.Session, targetUserID string) error {
	if !s.IsAdmin(actor, actorSession) {
		return ErrAdminPermission
	}
	if actor.ID == targetUserID {
		return ErrAdminSelfAction
	}
	if _, err := s.users.ByID(targetUserID); err != nil {
		return err
	}
	err := s.auth.RevokeAllAccess(targetUserID)
	if err != nil {
		return err
	}
	return s.recordAdminAction(actor.ID, "admin.sessions_revoked", "user", targetUserID)
}

func (s *AdminService) StartImpersonation(w http.ResponseWriter, r *http.Request, actor *model.User, actorSession *model.Session, targetUserID string) error {
	if !s.IsAdmin(actor, actorSession) {
		return ErrAdminPermission
	}
	if actor.ID == targetUserID {
		return ErrAdminSelfAction
	}
	target, err := s.users.ByID(targetUserID)
	if err != nil {
		return err
	}
	if _, isAdmin := s.adminEmails[strings.ToLower(target.Email)]; isAdmin {
		return ErrAdminTargetIsOperator
	}
	impersonationSession, err := s.auth.StartImpersonation(w, r, actor.ID, target.ID)
	if err != nil {
		return err
	}
	if err := s.recordAdminAction(actor.ID, "admin.impersonation_started", "user", target.ID); err != nil {
		if revokeErr := s.auth.RevokeSession(target.ID, impersonationSession.ID); revokeErr != nil {
			slog.Error("failed to roll back impersonation session", "error", revokeErr, "target_user_id", target.ID)
		}
		s.auth.ClearImpersonationCookie(w)
		return err
	}
	return nil
}

func (s *AdminService) StopImpersonation(w http.ResponseWriter, user *model.User, session *model.Session) error {
	if user == nil || session == nil || !session.IsImpersonating() {
		return ErrAdminPermission
	}
	actorUserID := *session.ImpersonatorUserID
	event := s.adminActionEvent(actorUserID, "admin.impersonation_stopped", "user", user.ID)
	if err := s.repository.StopImpersonationSession(session.ID, user.ID, actorUserID, event); err != nil {
		return err
	}
	s.auth.ClearImpersonationCookie(w)
	return nil
}

// describePlan says what the first workspace the user created is on.
func (s *AdminService) describePlan(user *model.AdminUser, now time.Time) error {
	if s.subscriptions == nil {
		return nil
	}
	user.Plan = model.AdminPlanNoWorkspace
	workspace, err := s.repository.FirstWorkspace(user.ID)
	if err != nil || workspace == nil {
		return err
	}
	subscription, err := s.subscriptions.ByWorkspaceID(workspace.ID)
	if err != nil {
		return err
	}
	current := subscription.AccessAt(now)
	switch {
	case subscription.IsForeverFree():
		user.Plan = model.AdminPlanFreeForever
	case subscription.HasProviderSubscription() && current.State != access.StateFree:
		user.Plan = model.AdminPlanPaying
	case current.State == access.StateTrial:
		user.Plan = model.AdminPlanTrial
		user.TrialEndsAt = current.TrialEndsAt
	default:
		user.Plan = model.AdminPlanTrialEnded
	}
	return nil
}

// SetForeverFree makes the first workspace a user created free forever, or
// puts it back: the rest of its trial, or the free plan once that is over.
// A paying workspace is not made free forever.
func (s *AdminService) SetForeverFree(actor *model.User, actorSession *model.Session, targetUserID string, on bool) error {
	if !s.IsAdmin(actor, actorSession) {
		return ErrAdminPermission
	}
	if s.subscriptions == nil {
		return ErrNoWorkspace
	}
	if _, err := s.users.ByID(targetUserID); err != nil {
		return err
	}
	workspace, err := s.repository.FirstWorkspace(targetUserID)
	if err != nil {
		return err
	}
	if workspace == nil {
		return ErrNoWorkspace
	}
	subscription, err := s.subscriptions.ByWorkspaceID(workspace.ID)
	if err != nil {
		return err
	}
	if subscription.IsForeverFree() == on {
		return nil
	}
	before := *subscription
	now := time.Now().UTC()
	action := "admin.free_forever_ended"
	if on {
		if subscription.HasProviderSubscription() {
			return ErrPaying
		}
		subscription.MakeForeverFree(now)
		action = "admin.free_forever_started"
	} else {
		subscription.EndForeverFree(workspace.CreatedAt, now)
	}
	if err := s.subscriptions.Update(subscription); err != nil {
		return err
	}
	recordActivity(s.activity, now, System(workspace.ID), "billing.plan_changed", "subscription", workspace.ID, map[string]string{
		"from": before.PlanID, "to": subscription.PlanID, "status_from": before.Status, "status": subscription.Status, "provider": build.Name,
	})
	return s.recordAdminAction(actor.ID, action, "workspace", workspace.ID)
}

func (s *AdminService) recordAdminAction(actorUserID, action, targetType, targetID string) error {
	return s.repository.CreateActivityEvent(s.adminActionEvent(actorUserID, action, targetType, targetID))
}

func (s *AdminService) adminActionEvent(actorUserID, action, targetType, targetID string) *model.AdminActivityEvent {
	return &model.AdminActivityEvent{
		ID:          uuid.New().String(),
		ActorUserID: &actorUserID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    "{}",
		CreatedAt:   time.Now(),
	}
}

// UseNetwork shows on the overview what moin runs everywhere: the hosted
// control plane sets it, a self-hosted one has no network to show.
func (s *AdminService) UseNetwork(network func() model.Network) {
	s.network = network
}

func (s *AdminService) networkCounts() *model.Network {
	if s.network == nil {
		return nil
	}
	network := s.network()
	return &network
}
