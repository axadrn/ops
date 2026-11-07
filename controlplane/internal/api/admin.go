package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
)

// AdminHandler serves the admin dashboard data and session revocation.
// Impersonation stays in the HTML admin.
type AdminHandler struct {
	admin *service.AdminService
}

func NewAdminHandler(admin *service.AdminService) *AdminHandler {
	return &AdminHandler{admin: admin}
}

// AdminMetrics are the counters of the admin dashboard.
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

// AdminDashboard is the JSON shape of model.AdminDashboard.
type AdminDashboard struct {
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

// NewAdminDashboard converts the admin dashboard into its JSON shape.
func NewAdminDashboard(dashboard *model.AdminDashboard) AdminDashboard {
	users := make([]AdminUser, 0, len(dashboard.Users.Users))
	for _, user := range dashboard.Users.Users {
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
	events := make([]AdminActivityEvent, 0, len(dashboard.Events))
	for _, event := range dashboard.Events {
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
	if n := dashboard.Network; n != nil {
		network = &AdminNetwork{Deploys: n.Deploys, Apps: n.Apps, Servers: n.Servers, HostedWorkspaces: n.HostedWorkspaces, SelfHosted: n.SelfHosted}
	}
	return AdminDashboard{
		Network: network,
		Metrics: AdminMetrics{
			Users:          dashboard.Metrics.Users,
			VerifiedUsers:  dashboard.Metrics.VerifiedUsers,
			Workspaces:     dashboard.Metrics.Workspaces,
			PaidWorkspaces: dashboard.Metrics.PaidWorkspaces,
			ActiveSessions: dashboard.Metrics.ActiveSessions,
		},
		Users: AdminUserPage{
			Items:      users,
			Query:      dashboard.Users.Query,
			Page:       dashboard.Users.Page,
			TotalPages: dashboard.Users.TotalPages,
			TotalUsers: dashboard.Users.TotalUsers,
		},
		Events: events,
	}
}

// Users returns the admin dashboard: metrics, one page of users filtered by
// the query parameter, and the latest activity events.
func (h *AdminHandler) Users(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	dashboard, err := h.admin.Dashboard(
		ctxkeys.User(r.Context()),
		ctxkeys.Session(r.Context()),
		r.URL.Query().Get("query"),
		page,
	)
	if err != nil {
		ServiceError(w, err)
		return
	}
	JSON(w, http.StatusOK, NewAdminDashboard(dashboard))
}

// OldAddress returns what still arrives on the old addresses of the
// control plane over the last days.
func (h *AdminHandler) OldAddress(w http.ResponseWriter, r *http.Request) {
	report, err := h.admin.OldAddress(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()))
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
