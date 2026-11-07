package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/repository"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/ui/pages"
)

type AdminHandler struct {
	admin *service.AdminService
}

func NewAdminHandler(admin *service.AdminService) *AdminHandler {
	return &AdminHandler{admin: admin}
}

func (h *AdminHandler) Overview(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	overview, err := h.admin.Overview(
		ctxkeys.User(r.Context()),
		ctxkeys.Session(r.Context()),
		r.URL.Query().Get("q"),
		page,
	)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	render(w, r, pages.AdminOverview(overview))
}

// OldAddress shows what still arrives on the old addresses.
func (h *AdminHandler) OldAddress(w http.ResponseWriter, r *http.Request) {
	report, err := h.admin.OldAddress(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()))
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	render(w, r, pages.AdminOldAddress(report))
}

// User is one user's page: the facts and what an operator can do.
func (h *AdminHandler) User(w http.ResponseWriter, r *http.Request) {
	user, err := h.admin.User(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), r.PathValue("userID"))
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	render(w, r, pages.AdminUser(user))
}

// Activity is what operators did, the last 50 events.
func (h *AdminHandler) Activity(w http.ResponseWriter, r *http.Request) {
	overview, err := h.admin.Overview(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), "", 1)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	render(w, r, pages.AdminActivity(overview.Events))
}

func (h *AdminHandler) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	err := h.admin.RevokeUserSessions(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), userID)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if !isAction(r) {
		redirect(w, r, "/admin/users/"+url.PathEscape(userID), http.StatusSeeOther)
		return
	}
	user, err := h.admin.User(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), userID)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	answerDone(w, r, "Sessions revoked", "Signed out everywhere.")
	render(w, r, pages.AdminUserFacts(user))
}

// ForeverFree makes the first workspace of a user free forever, or ends it.
func (h *AdminHandler) ForeverFree(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	on := r.FormValue("forever_free") == "true"
	err := h.admin.SetForeverFree(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), userID, on)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if !isAction(r) {
		redirect(w, r, "/admin/users/"+url.PathEscape(userID), http.StatusSeeOther)
		return
	}
	user, err := h.admin.User(ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), userID)
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	if on {
		answerDone(w, r, "Free forever", "")
	} else {
		answerDone(w, r, "Free forever ended", "")
	}
	render(w, r, pages.AdminUserPlan(user))
}

func (h *AdminHandler) StartImpersonation(w http.ResponseWriter, r *http.Request) {
	err := h.admin.StartImpersonation(w, r, ctxkeys.User(r.Context()), ctxkeys.Session(r.Context()), r.PathValue("userID"))
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	redirect(w, r, "/app", http.StatusSeeOther)
}

func (h *AdminHandler) StopImpersonation(w http.ResponseWriter, r *http.Request) {
	user := ctxkeys.User(r.Context())
	err := h.admin.StopImpersonation(w, user, ctxkeys.Session(r.Context()))
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	// Back to the page of the user you viewed as.
	redirect(w, r, "/admin/users/"+url.PathEscape(user.ID), http.StatusSeeOther)
}

func (h *AdminHandler) handleError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	title := "Admin action failed"
	description := "The admin action could not be completed. Please try again."
	switch {
	case errors.Is(err, repository.ErrUserNotFound), errors.Is(err, repository.ErrSessionNotFound):
		status = http.StatusNotFound
		title = "User not found"
		description = "The requested user or session no longer exists."
	case errors.Is(err, service.ErrAdminPermission):
		status = http.StatusNotFound
		title = "Not found"
		description = "The requested admin resource was not found."
	case errors.Is(err, service.ErrAdminSelfAction), errors.Is(err, service.ErrAdminTargetIsOperator),
		errors.Is(err, service.ErrNoWorkspace), errors.Is(err, service.ErrPaying):
		status = http.StatusConflict
		title = "Action not allowed"
		description = err.Error()
	default:
		slog.Error("admin action failed", "error", err)
	}
	if isAction(r) {
		answerFailed(w, r, title, description)
		return
	}
	w.WriteHeader(status)
	render(w, r, pages.ErrorPage(status, title, description))
}
