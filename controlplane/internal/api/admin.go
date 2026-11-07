package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
)

// AdminHandler serves the admin overview data and session revocation.
// Impersonation stays in the HTML admin.
type AdminHandler struct {
	admin *service.AdminService
}

func NewAdminHandler(admin *service.AdminService) *AdminHandler {
	return &AdminHandler{admin: admin}
}

// AdminMetrics are the counters of the admin overview.
type AdminMetrics struct {
	Users          int `json:"users"`
	VerifiedUsers  int `json:"verified_users"`
	Workspaces     int `json:"workspaces"`
	PaidWorkspaces int `json:"paid_workspaces"`
	ActiveSessions int `json:"active_sessions"`
}

// AdminUser is one row of the admin user list.
type AdminUser struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	Name               string     `json:"name"`
	EmailVerifiedAt    *time.Time `json:"email_verified_at"`
	CreatedAt          time.Time  `json:"created_at"`
	WorkspaceCount     int        `json:"workspace_count"`
	ActiveSessionCount int        `json:"active_session_count"`
	IsAdmin            bool       `json:"is_admin"`
}

// AdminUserPage is one page of the admin user list.
type AdminUserPage struct {
	Items      []AdminUser `json:"items"`
	Query      string      `json:"query"`
	Page       int         `json:"page"`
	TotalPages int         `json:"total_pages"`
	TotalUsers int         `json:"total_users"`
}

// AdminActivityEvent is one entry of the admin activity.
type AdminActivityEvent struct {
	ID          string    `json:"id"`
	ActorUserID *string   `json:"actor_user_id"`
	ActorEmail  string    `json:"actor_email"`
	WorkspaceID *string   `json:"workspace_id"`
	Action      string    `json:"action"`
	TargetType  string    `json:"target_type"`
	TargetID    string    `json:"target_id"`
	Metadata    string    `json:"metadata"`
	CreatedAt   time.Time `json:"created_at"`
}

// AdminOverview is the JSON shape of model.AdminOverview.
type AdminOverview struct {
	Metrics AdminMetrics         `json:"metrics"`
	Users   AdminUserPage        `json:"users"`
	Events  []AdminActivityEvent `json:"events"`
	// Network is only on the hosted control plane.
	Network *AdminNetwork `json:"network,omitempty"`
}

// AdminNetwork is everything moin runs: hosted and self-hosted.
type AdminNetwork struct {
	Deploys          int `json:"deploys"`
	Apps             int `json:"apps"`
	Servers          int `json:"servers"`
	HostedWorkspaces int `json:"hosted_workspaces"`
	SelfHosted       int `json:"self_hosted"`
}

// NewAdminOverview converts the admin overview into its JSON shape.
func NewAdminOverview(overview *model.AdminOverview) AdminOverview {
	users := make([]AdminUser, 0, len(overview.Users.Users))
	for _, user := range overview.Users.Users {
		users = append(users, AdminUser{
			ID:                 user.ID,
			Email:              user.Email,
			Name:               user.Name,
			EmailVerifiedAt:    utcTime(user.EmailVerifiedAt),
			CreatedAt:          user.CreatedAt.UTC(),
			WorkspaceCount:     user.WorkspaceCount,
			ActiveSessionCount: user.ActiveSessionCount,
			IsAdmin:            user.IsAdmin,
		})
	}
	events := make([]AdminActivityEvent, 0, len(overview.Events))
	for _, event := range overview.Events {
		events = append(events, AdminActivityEvent{
			ID:          event.ID,
			ActorUserID: event.ActorUserID,
			ActorEmail:  event.ActorEmail,
			WorkspaceID: event.WorkspaceID,
			Action:      event.Action,
			TargetType:  event.TargetType,
			TargetID:    event.TargetID,
			Metadata:    event.Metadata,
			CreatedAt:   event.CreatedAt.UTC(),
		})
	}
	var network *AdminNetwork
	if n := overview.Network; n != nil {
		network = &AdminNetwork{Deploys: n.Deploys, Apps: n.Apps, Servers: n.Servers, HostedWorkspaces: n.HostedWorkspaces, SelfHosted: n.SelfHosted}
	}
	return AdminOverview{
		Network: network,
		Metrics: AdminMetrics{
			Users:          overview.Metrics.Users,
			VerifiedUsers:  overview.Metrics.VerifiedUsers,
			Workspaces:     overview.Metrics.Workspaces,
			PaidWorkspaces: overview.Metrics.PaidWorkspaces,
			ActiveSessions: overview.Metrics.ActiveSessions,
		},
		Users: AdminUserPage{
			Items:      users,
			Query:      overview.Users.Query,
			Page:       overview.Users.Page,
			TotalPages: overview.Users.TotalPages,
			TotalUsers: overview.Users.TotalUsers,
		},
		Events: events,
	}
}

// Users returns the admin overview: metrics, one page of users filtered by
// the query parameter, and the latest activity events.
func (h *AdminHandler) Users(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	overview, err := h.admin.Overview(
		ctxkeys.User(r.Context()),
		ctxkeys.Session(r.Context()),
		r.URL.Query().Get("query"),
		page,
	)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, NewAdminOverview(overview))
}

// OldDomain returns what still arrives on the old domains of the
// control plane over the last days.
func (h *AdminHandler) OldDomain(w http.ResponseWriter, r *http.Request) {
	report, err := h.admin.OldDomain(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()))
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, report)
}

// RevokeUserSessions signs a user out everywhere.
func (h *AdminHandler) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	err := h.admin.RevokeUserSessions(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), r.PathValue("userID"))
	if err != nil {
		ServiceError(w, err)
		return
	}
	NoContent(w)
}
