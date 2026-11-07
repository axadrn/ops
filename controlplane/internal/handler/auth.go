package handler

import (
	"errors"
	"fmt"
	"github.com/a-h/templ"
	"log/slog"
	"net/http"
	"strings"

	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/model"
	"github.com/axadrn/moin/controlplane/internal/service"
	"github.com/axadrn/moin/controlplane/internal/ui/pages"
	"github.com/axadrn/moin/controlplane/internal/validation"
)

type authHandler struct {
	authService      *service.AuthService
	userService      *service.UserService
	workspaceService *service.WorkspaceService
	cfg              *config.Config
}

func NewAuthHandler(authService *service.AuthService, userService *service.UserService, workspaceService *service.WorkspaceService, cfg *config.Config) *authHandler {
	return &authHandler{
		authService:      authService,
		userService:      userService,
		workspaceService: workspaceService,
		cfg:              cfg,
	}
}

func (h *authHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if err := h.authService.EndSession(r); err != nil {
		slog.Error("failed to revoke session during logout", "error", err)
		if isAction(r) {
			answerFailed(w, r, "Not signed out", "Something went wrong. Please try again.")
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			render(w, r, pages.ErrorPage(http.StatusInternalServerError, "Sign out failed", "Please try again."))
		}
		return
	}
	h.authService.ClearSessionCookie(w)
	redirect(w, r, "/", http.StatusSeeOther)
}

func (h *authHandler) ForgotPasswordPage(w http.ResponseWriter, r *http.Request) {
	render(w, r, pages.AuthForgotPassword(""))
}

func (h *authHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))

	if email == "" {
		authFieldError(w, r, pages.AuthForgotPasswordForm("Enter your email."), "Link not sent", "email", "Enter your email.")
		return
	}

	err := validation.ValidateEmail(email)
	if err != nil {
		authFieldError(w, r, pages.AuthForgotPasswordForm("Enter a valid email address."), "Link not sent", "email", "Enter a valid email address.")
		return
	}

	err = h.authService.SendForgotPasswordLink(email)
	if err != nil {
		// Don't reveal specific errors to user
		slog.Warn("forgot password link send failed", "error", err)
	}

	// Always show success message to prevent email enumeration
	setAuthEmailCookie(w, r, email)
	redirect(w, r, "/auth/forgot-password-sent", http.StatusSeeOther)
}

func (h *authHandler) VerifyForgotPassword(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	user, err := h.authService.VerifyMagicLink(token)
	if err != nil {
		slog.Warn("forgot password verification failed", "error", err)
		render(w, r, pages.Auth("Invalid or expired link. Please try again."))
		return
	}

	err = h.authService.RevokeAllAccess(user.ID)
	if err != nil {
		slog.Error("failed to revoke access during forgot password flow", "error", err, "user_id", user.ID)
		render(w, r, pages.Auth("An error occurred. Please try again."))
		return
	}

	if user.HasPassword() {
		err = h.authService.RemovePassword(user.ID)
		if err != nil {
			slog.Error("failed to remove password during forgot password flow", "error", err, "user_id", user.ID)
			render(w, r, pages.Auth("An error occurred. Please try again."))
			return
		}
		slog.Info("password removed via forgot password flow", "user_id", user.ID)
	}

	_, err = h.authService.StartFreshSession(w, r, user.ID)
	if err != nil {
		slog.Error("failed to create session", "error", err, "user_id", user.ID)
		render(w, r, pages.Auth("An error occurred. Please try again."))
		return
	}

	slog.Info("user logged in via forgot password flow", "user_id", user.ID)

	members, listErr := h.workspaceService.ListForUser(user.ID)
	if listErr != nil || len(members) == 0 {
		redirect(w, r, "/app", http.StatusSeeOther)
		return
	}
	redirect(w, r, fmt.Sprintf("/app/%s/settings/account?tab=security&password_removed=1", members[0].Slug), http.StatusSeeOther)
}

func (h *authHandler) VerifyEmailChange(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	user, err := h.authService.VerifyEmailChange(token)
	if err != nil {
		slog.Warn("email change verification failed", "error", err)
		render(w, r, pages.VerifyEmailError("It expired or was used already."))
		return
	}

	_, err = h.authService.StartFreshSession(w, r, user.ID)
	if err != nil {
		slog.Error("failed to create session after email change", "error", err, "user_id", user.ID)
		render(w, r, pages.VerifyEmailError("Something went wrong. Open the link again."))
		return
	}

	slog.Info("email changed", "user_id", user.ID)
	render(w, r, pages.VerifyEmailSuccess())
}

func (h *authHandler) SendMagicLink(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))

	if email == "" {
		authFieldError(w, r, pages.AuthMagicLinkForm("Enter your email."), "Link not sent", "email", "Enter your email.")
		return
	}

	err := validation.ValidateEmail(email)
	if err != nil {
		authFieldError(w, r, pages.AuthMagicLinkForm("Enter a valid email address."), "Link not sent", "email", "Enter a valid email address.")
		return
	}

	err = h.authService.SendMagicLink(email)
	if errors.Is(err, service.ErrPrivate) {
		renderAuthPage(w, r, pages.AuthPrivate())
		return
	}
	if err != nil {
		// Don't reveal specific errors to prevent email enumeration
		slog.Warn("magic link send failed", "error", err)
	}

	if r.URL.Query().Get("resend") == "true" {
		render(w, r, pages.AuthMagicLinkResend(email, true))
		return
	}

	setAuthEmailCookie(w, r, email)
	redirect(w, r, "/auth/magic-link-sent", http.StatusSeeOther)
}

func (h *authHandler) VerifyMagicLink(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	user, err := h.authService.VerifyMagicLink(token)
	if err != nil {
		slog.Warn("magic link verification failed", "error", err)
		render(w, r, pages.Auth("Invalid or expired magic link. Please try again."))
		return
	}

	_, err = h.authService.StartSession(w, r, user.ID)
	if err != nil {
		slog.Error("failed to create session", "error", err, "user_id", user.ID)
		render(w, r, pages.Auth("An error occurred. Please try again."))
		return
	}

	needsOnboarding, err := h.authService.NeedsOnboarding(user.ID)
	if err != nil {
		slog.Warn("failed to check onboarding status", "error", err, "user_id", user.ID)
	}

	if needsOnboarding {
		slog.Info("new user needs onboarding", "user_id", user.ID)
		redirect(w, r, "/auth/onboarding", http.StatusSeeOther)
		return
	}

	slog.Info("user logged in via magic link", "user_id", user.ID)
	h.redirectAfterAuth(w, r, user)
}

func (h *authHandler) AuthPage(w http.ResponseWriter, r *http.Request) {
	render(w, r, pages.Auth(""))
}

func (h *authHandler) PasswordPage(w http.ResponseWriter, r *http.Request) {
	render(w, r, pages.AuthPassword(""))
}

func (h *authHandler) OnboardingPage(w http.ResponseWriter, r *http.Request) {
	user := ctxkeys.User(r.Context())
	if user != nil && user.Name != "" {
		h.redirectAfterAuth(w, r, user)
		return
	}
	_, invited := h.pendingInvitationForUser(w, r, user)
	form := pages.OnboardingForm{AskWorkspace: !invited}
	// After GitHub sign-in both fields come filled in, so it is one click.
	if name := githubName(r, user); name != "" {
		form.Name = name
		form.Workspace = service.DefaultWorkspaceName(name)
	}
	render(w, r, pages.AuthOnboarding(form))
}

func (h *authHandler) CompleteOnboarding(w http.ResponseWriter, r *http.Request) {
	user := ctxkeys.User(r.Context())
	if user == nil {
		redirect(w, r, "/auth", http.StatusSeeOther)
		return
	}

	pendingInvitation, hasPendingInvitation := h.pendingInvitationForUser(w, r, user)
	form := pages.OnboardingForm{
		Name:         r.FormValue("name"),
		Workspace:    r.FormValue("workspace"),
		AskWorkspace: !hasPendingInvitation,
	}
	if validation.ValidateName(form.Name) != nil {
		onboardingFieldError(w, r, form, "name", "Enter your name, up to 100 characters.")
		return
	}
	workspaceName := ""
	if form.AskWorkspace {
		workspaceName = form.Workspace
		if validation.ValidateWorkspaceName(workspaceName) != nil {
			onboardingFieldError(w, r, form, "workspace", "Enter a name of 2 to 80 characters.")
			return
		}
	}

	workspace, err := h.authService.CompleteOnboarding(user.ID, form.Name, workspaceName, !hasPendingInvitation)
	if err != nil {
		slog.Error("onboarding failed", "error", err, "user_id", user.ID)
		answerFailed(w, r, "Not saved", "Something went wrong. Please try again.")
		return
	}
	clearGitHubNameCookie(w, r)

	slog.Info("onboarding completed", "user_id", user.ID)
	if hasPendingInvitation {
		redirect(w, r, "/workspaces/invitations/"+pendingInvitation, http.StatusSeeOther)
		return
	}
	if device, ok := pendingDevice(r); ok {
		redirect(w, r, device, http.StatusSeeOther)
		return
	}
	redirect(w, r, fmt.Sprintf("/app/%s/overview", workspace.Slug), http.StatusSeeOther)
}

func onboardingFieldError(w http.ResponseWriter, r *http.Request, form pages.OnboardingForm, field, message string) {
	form.Field, form.Error = field, message
	authFieldError(w, r, pages.AuthOnboardingForm(form), "Not saved", field, message)
}

func (h *authHandler) PasswordAuth(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	if email == "" {
		authFieldError(w, r, pages.AuthPasswordForm(email, "Enter your email."), "Not signed in", "email", "Enter your email.")
		return
	}
	if password == "" {
		authFieldError(w, r, pages.AuthPasswordForm(email, "Enter your password."), "Not signed in", "password", "Enter your password.")
		return
	}

	err := validation.ValidateEmail(email)
	if err != nil {
		authFieldError(w, r, pages.AuthPasswordForm(email, "Enter a valid email address."), "Not signed in", "email", "Enter a valid email address.")
		return
	}

	user, err := h.authService.Login(email, password)
	if err != nil {
		slog.Warn("password login failed", "error", err)
		authFieldError(w, r, pages.AuthPasswordForm(email, "Wrong email or password."), "Not signed in", "password", "Wrong email or password.")
		return
	}

	_, err = h.authService.StartSession(w, r, user.ID)
	if err != nil {
		slog.Error("failed to create session", "error", err, "user_id", user.ID)
		answerFailed(w, r, "Not signed in", "Something went wrong. Please try again.")
		return
	}

	slog.Info("user logged in with password", "user_id", user.ID)
	h.redirectAfterAuth(w, r, user)
}

func (h *authHandler) redirectAfterAuth(w http.ResponseWriter, r *http.Request, user *model.User) {
	if invitation, ok := h.pendingInvitationForUser(w, r, user); ok {
		redirect(w, r, "/workspaces/invitations/"+invitation, http.StatusSeeOther)
		return
	}
	if device, ok := pendingDevice(r); ok {
		redirect(w, r, device, http.StatusSeeOther)
		return
	}
	members, err := h.workspaceService.ListForUser(user.ID)
	if err != nil || len(members) == 0 {
		redirect(w, r, "/workspaces/new", http.StatusSeeOther)
		return
	}
	redirect(w, r, fmt.Sprintf("/app/%s/overview", members[0].Slug), http.StatusSeeOther)
}

func (h *authHandler) pendingInvitationForUser(w http.ResponseWriter, r *http.Request, user *model.User) (string, bool) {
	cookie, err := r.Cookie("pending_invitation")
	if err != nil || cookie.Value == "" {
		return "", false
	}
	invitation, err := h.workspaceService.WorkspaceInvitation(cookie.Value)
	if err != nil || user == nil || !strings.EqualFold(invitation.Email, user.Email) {
		clearPendingInvitationCookie(w, h.cfg)
		return "", false
	}
	return cookie.Value, true
}

// authFieldError answers a sign-in form with a wrong field: the field is
// marked and the message shows in its hint line, and what was typed stays
// (DESIGN.md, Feedback). Without htmx the form renders again with it.
func authFieldError(w http.ResponseWriter, r *http.Request, form templ.Component, title, field, message string) {
	if isAction(r) {
		fieldError(w, title, field, message)
		return
	}
	render(w, r, form)
}
