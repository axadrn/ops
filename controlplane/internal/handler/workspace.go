package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/ui/pages"
)

type WorkspaceHandler struct {
	workspaces *service.WorkspaceService
}

func NewWorkspaceHandler(workspaces *service.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{workspaces: workspaces}
}

func (h *WorkspaceHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	user := ctxkeys.User(r.Context())
	if user == nil {
		redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}
	h.redirectToFirstWorkspace(w, r, user.ID)
}

func (h *WorkspaceHandler) Page(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	member := ctxkeys.WorkspaceMember(r.Context())
	members, err := h.workspaces.Members(member)
	if err != nil {
		slog.Error("workspace members could not be loaded", "error", err, "workspace_id", workspace.ID)
		w.WriteHeader(http.StatusInternalServerError)
		render(w, r, pages.ErrorPage(http.StatusInternalServerError, "Workspace unavailable", "The workspace members could not be loaded. Please try again."))
		return
	}
	var invitations []model.WorkspaceInvitation
	if member.Can(model.WorkspacePermissionManageMembers) {
		invitations, err = h.workspaces.PendingInvitations(workspace.ID)
		if err != nil {
			slog.Error("invitations could not be loaded", "error", err, "workspace_id", workspace.ID)
			w.WriteHeader(http.StatusInternalServerError)
			render(w, r, pages.ErrorPage(http.StatusInternalServerError, "Workspace unavailable", "The invitations could not be loaded. Please try again."))
			return
		}
	}
	render(w, r, pages.Workspace(members, invitations))
}

// ActivityPage shows what the team did, 50 events at a time. With after it
// renders only the next rows, for "Show older".
func (h *WorkspaceHandler) ActivityPage(w http.ResponseWriter, r *http.Request) {
	member := ctxkeys.WorkspaceMember(r.Context())
	query := r.URL.Query()
	after := query.Get("after")
	filter := model.ActivityFilter{App: query.Get("app"), Server: query.Get("server"), User: query.Get("user")}
	events, next, err := h.workspaces.ActivityPage(member, filter, after, 50)
	if errors.Is(err, service.ErrActivityPermission) {
		w.WriteHeader(http.StatusForbidden)
		render(w, r, pages.ErrorPage(http.StatusForbidden, "Access denied", "You do not have permission to view workspace activity."))
		return
	}
	var choices model.ActivityChoices
	if err == nil && after == "" {
		choices, err = h.workspaces.ActivityChoices(member.WorkspaceID)
	}
	if err != nil {
		slog.Error("activity could not be loaded", "error", err, "workspace_id", member.WorkspaceID)
		w.WriteHeader(http.StatusInternalServerError)
		render(w, r, pages.ErrorPage(http.StatusInternalServerError, "Activity unavailable", "Workspace activity could not be loaded. Please try again."))
		return
	}
	view := pages.ActivityView{Events: events, Next: next, Filter: filter, Choices: choices, Admin: member.Can(model.WorkspacePermissionManageMembers)}
	if after != "" {
		render(w, r, pages.ActivityRows(view))
		return
	}
	render(w, r, pages.Activity(view))
}

func (h *WorkspaceHandler) UpdateName(w http.ResponseWriter, r *http.Request) {
	if err := h.workspaces.UpdateName(ctxkeys.WorkspaceMember(r.Context()), ctxkeys.Workspace(r.Context()), r.FormValue("name"), service.RequestSource(r.Context())); err != nil {
		h.handleError(w, r, "Name not saved", err)
		return
	}
	h.renderWorkspaceAction(w, r, "Workspace name saved", "The sidebar and emails show the new name.")
}

func (h *WorkspaceHandler) Invite(w http.ResponseWriter, r *http.Request) {
	if err := h.workspaces.Invite(ctxkeys.WorkspaceMember(r.Context()), ctxkeys.Workspace(r.Context()), r.FormValue("email"), r.FormValue("role"), service.RequestSource(r.Context())); err != nil {
		h.handleError(w, r, "Invitation not sent", err)
		return
	}
	h.renderWorkspaceAction(w, r, "Invitation sent to "+strings.TrimSpace(r.FormValue("email")), "The link in it works for seven days.")
}

func (h *WorkspaceHandler) ResendInvitation(w http.ResponseWriter, r *http.Request) {
	if err := h.workspaces.ResendInvitation(ctxkeys.WorkspaceMember(r.Context()), ctxkeys.Workspace(r.Context()), r.PathValue("invitationID"), service.RequestSource(r.Context())); err != nil {
		h.handleError(w, r, "Invitation not sent", err)
		return
	}
	h.renderWorkspaceAction(w, r, "Invitation sent again", "The link in it works for seven days.")
}

func (h *WorkspaceHandler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	if err := h.workspaces.RevokeInvitation(ctxkeys.WorkspaceMember(r.Context()), workspace.ID, r.PathValue("invitationID"), service.RequestSource(r.Context())); err != nil {
		h.handleError(w, r, "Invitation not revoked", err)
		return
	}
	h.renderWorkspaceAction(w, r, "Invitation revoked", "Its link no longer works.")
}

func (h *WorkspaceHandler) ChangeRole(w http.ResponseWriter, r *http.Request) {
	member := ctxkeys.WorkspaceMember(r.Context())
	if err := h.workspaces.ChangeRole(member, member.WorkspaceID, r.PathValue("memberID"), r.FormValue("role"), service.RequestSource(r.Context())); err != nil {
		h.handleError(w, r, "Role not changed", err)
		return
	}
	h.renderWorkspaceAction(w, r, "Role changed", "")
}

func (h *WorkspaceHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	member := ctxkeys.WorkspaceMember(r.Context())
	memberID := r.PathValue("memberID")
	if err := h.workspaces.RemoveMember(member, member.WorkspaceID, memberID, service.RequestSource(r.Context())); err != nil {
		h.handleError(w, r, "Member not removed", err)
		return
	}
	if member.ID == memberID {
		h.redirectToFirstWorkspace(w, r, member.UserID)
		return
	}
	h.renderWorkspaceAction(w, r, "Member removed", "They no longer reach this workspace.")
}

func (h *WorkspaceHandler) Leave(w http.ResponseWriter, r *http.Request) {
	workspace := ctxkeys.Workspace(r.Context())
	user := ctxkeys.User(r.Context())
	if err := h.workspaces.Leave(workspace.ID, user.ID, service.RequestSource(r.Context())); err != nil {
		h.handleError(w, r, "You did not leave", err)
		return
	}
	h.redirectToFirstWorkspace(w, r, user.ID)
}

func (h *WorkspaceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user := ctxkeys.User(r.Context())
	workspace := ctxkeys.Workspace(r.Context())
	if err := h.workspaces.Delete(ctxkeys.WorkspaceMember(r.Context()), workspace); err != nil {
		h.handleError(w, r, "Workspace not deleted", err)
		return
	}
	slog.Info("workspace deleted", "workspace_id", workspace.ID, "user_id", user.ID)
	h.redirectToFirstWorkspace(w, r, user.ID)
}

func (h *WorkspaceHandler) NewPage(w http.ResponseWriter, r *http.Request) {
	render(w, r, pages.NewWorkspace(""))
}

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := ctxkeys.User(r.Context())
	workspace, err := h.workspaces.Create(user.ID, r.FormValue("name"))
	if errors.Is(err, service.ErrInvalidWorkspace) {
		authFieldError(w, r, pages.NewWorkspaceForm("Use 2 to 80 characters."), "Workspace not created", "workspace-name", "Use 2 to 80 characters.")
		return
	}
	if err != nil {
		slog.Error("create workspace failed", "error", err, "user_id", user.ID)
		answerFailed(w, r, "Workspace not created", "Something went wrong. Please try again.")
		return
	}
	redirect(w, r, fmt.Sprintf("/app/%s/overview", workspace.Slug), http.StatusSeeOther)
}

func (h *WorkspaceHandler) redirectToWorkspace(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, fmt.Sprintf("/app/%s/workspace", ctxkeys.Workspace(r.Context()).Slug), http.StatusSeeOther)
}

func (h *WorkspaceHandler) renderWorkspaceAction(w http.ResponseWriter, r *http.Request, title, description string) {
	if !isAction(r) {
		h.redirectToWorkspace(w, r)
		return
	}

	user := ctxkeys.User(r.Context())
	current := ctxkeys.Workspace(r.Context())
	workspace, member, err := h.workspaces.BySlugForUser(current.Slug, user.ID)
	if err != nil {
		h.handleError(w, r, "Team page not refreshed", err)
		return
	}
	members, err := h.workspaces.Members(member)
	if err != nil {
		h.handleError(w, r, "Team page not refreshed", err)
		return
	}
	var invitations []model.WorkspaceInvitation
	if member.Can(model.WorkspacePermissionManageMembers) {
		invitations, err = h.workspaces.PendingInvitations(workspace.ID)
		if err != nil {
			h.handleError(w, r, "Team page not refreshed", err)
			return
		}
	}

	workspaces, err := h.workspaces.ListForUser(user.ID)
	if err != nil {
		h.handleError(w, r, "Team page not refreshed", err)
		return
	}
	ctx := ctxkeys.WithWorkspace(r.Context(), workspace)
	ctx = ctxkeys.WithWorkspaceMember(ctx, member)
	ctx = ctxkeys.WithWorkspaces(ctx, workspaces)
	r = r.WithContext(ctx)

	render(w, r, pages.WorkspaceContent(members, invitations))
	answerDone(w, r, title, description)
}

func (h *WorkspaceHandler) redirectToFirstWorkspace(w http.ResponseWriter, r *http.Request, userID string) {
	members, err := h.workspaces.ListForUser(userID)
	if err != nil || len(members) == 0 {
		redirect(w, r, "/workspaces/new", http.StatusSeeOther)
		return
	}
	redirect(w, r, fmt.Sprintf("/app/%s/overview", members[0].Slug), http.StatusSeeOther)
}

// handleError answers a workspace action that failed: title says what did
// not happen, the error why. A wrong field of a form is marked.
func (h *WorkspaceHandler) handleError(w http.ResponseWriter, r *http.Request, title string, err error) {
	status := http.StatusInternalServerError
	description := "The workspace action could not be completed. Please try again."
	field := ""
	switch {
	case errors.Is(err, service.ErrWorkspacePermission):
		status = http.StatusForbidden
		description = "You do not have permission to perform this workspace action."
	case errors.Is(err, repository.ErrLastOwner):
		status = http.StatusUnprocessableEntity
		description = "A workspace must always have at least one owner."
	case errors.Is(err, repository.ErrSeatLimit):
		status = http.StatusUnprocessableEntity
		description = "Upgrade the workspace or remove a member before inviting someone new."
	case errors.Is(err, repository.ErrAlreadyMember):
		status = http.StatusUnprocessableEntity
		description = "That user already belongs to this workspace."
		field = "invite-email"
	case errors.Is(err, service.ErrInvalidRole):
		status = http.StatusUnprocessableEntity
		description = "Choose Owner, Admin, or Member where allowed."
	case errors.Is(err, service.ErrInvalidWorkspace):
		status = http.StatusUnprocessableEntity
		description = "Use 2 to 80 printable characters."
		field = "workspace-name"
	case errors.Is(err, service.ErrInvalidInviteEmail):
		status = http.StatusUnprocessableEntity
		description = "Enter a valid email address."
		field = "invite-email"
	case errors.Is(err, service.ErrActiveSubscription):
		status = http.StatusUnprocessableEntity
		description = "Finish or wait for checkout to expire. Cancel paid subscriptions and wait for access to end before deleting this workspace."
	case errors.Is(err, repository.ErrWorkspaceMemberNotFound), errors.Is(err, repository.ErrWorkspaceInvitationNotFound):
		status = http.StatusNotFound
		description = "The requested member or invitation no longer exists."
	default:
		slog.Error("workspace action failed", "error", err)
	}

	switch {
	case isAction(r) && field != "":
		fieldError(w, title, field, description)
	case isAction(r):
		answerFailed(w, r, title, description)
	default:
		w.WriteHeader(status)
		render(w, r, pages.ErrorPage(status, title, description))
	}
}
