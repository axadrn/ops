package api

import "net/http"

// Route is one endpoint of the JSON API: a ServeMux pattern, its handler with
// every middleware applied, and what a personal API token may do there.
type Route struct {
	Pattern string
	Handler http.HandlerFunc
	// Tokens is TokenRead or TokenWrite for routes open to personal API
	// tokens. The zero value denies tokens.
	Tokens TokenAccess
}

// Wrap adds one middleware to a route handler.
type Wrap func(http.HandlerFunc) http.HandlerFunc

// Handlers are the API handlers the routes call.
type Handlers struct {
	Meta        *MetaHandler
	Auth        *AuthHandler
	Account     *AccountHandler
	Sessions    *SessionsHandler
	Tokens      *APITokensHandler
	Billing     *BillingHandler
	Admin       *AdminHandler
	Workspaces  *WorkspaceHandler
	Invitations *WorkspaceInvitationHandler
	Servers     *ServersHandler
	Apps        *AppsHandler
	Version     *VersionHandler
	Alerts      *AlertsHandler
	Catalog     *CatalogHandler
	Status      *StatusHandler
	Device      *DeviceHandler
}

// Middleware are the route middleware from internal/middleware. They are
// passed in because internal/middleware imports this package.
type Middleware struct {
	// RequireAuth answers 401 without a session.
	RequireAuth Wrap
	// RequireOnboarded also answers 403 onboarding_required without a name.
	RequireOnboarded Wrap
	// RateLimitAuth limits sign-in attempts per client IP.
	RateLimitAuth Wrap
	// RateLimitAuthEmail limits emails per client IP and recipient.
	RateLimitAuthEmail Wrap
	// RateLimitEmailChanges limits email change requests per user and
	// recipient.
	RateLimitEmailChanges Wrap
	// RequireAdmin answers 403 forbidden for users who are not admins.
	RequireAdmin Wrap
	// Workspace requires onboarding and scopes the route to the workspace
	// in its path.
	Workspace Wrap
	// RateLimitInvitations limits workspace invitation emails.
	RateLimitInvitations Wrap
	// InWorkspace requires onboarding and scopes a moin route to the
	// workspace of the API token, or for a session to the workspace its
	// Moin-Workspace header names.
	InWorkspace Wrap
	// RateLimitDevice limits the logins `moin login` starts per client IP.
	RateLimitDevice Wrap
	// RateLimitDevicePoll limits the polls of `moin login` per client IP.
	RateLimitDevicePoll Wrap
}

// Routes lists every endpoint under /api/v1. The router registers exactly
// these routes, and openapi_test.go checks them against openapi.yaml. Every
// handler is wrapped by the token gate of its route: GET routes open to tokens
// need the read scope, routes that change data need the write scope, and
// everything else answers 403 to tokens.
func Routes(h Handlers, m Middleware) []Route {
	routes := []Route{
		{"GET /api/v1/openapi.yaml", OpenAPI, TokenRead},
		{"GET /api/v1/csrf", CSRFToken, TokenRead},
		{"GET /api/v1/meta", h.Meta.Meta, TokenRead},
		{"GET /api/v1/version", m.RequireOnboarded(h.Version.Show), TokenRead},
		{"POST /api/v1/version/update", m.InWorkspace(h.Version.Update), TokenWrite},
		{"POST /api/v1/auth/password", m.RateLimitAuth(h.Auth.PasswordAuth), TokenDenied},
		{"POST /api/v1/auth/magic-link", m.RateLimitAuthEmail(h.Auth.SendMagicLink), TokenDenied},
		{"POST /api/v1/auth/magic-link/verify", h.Auth.VerifyMagicLink, TokenDenied},
		{"POST /api/v1/auth/forgot-password", m.RateLimitAuthEmail(h.Auth.ForgotPassword), TokenDenied},
		{"POST /api/v1/auth/forgot-password/verify", h.Auth.VerifyForgotPassword, TokenDenied},
		{"POST /api/v1/auth/email-change/verify", h.Auth.VerifyEmailChange, TokenDenied},
		{"POST /api/v1/auth/device", m.RateLimitDevice(h.Device.Start), TokenDenied},
		{"POST /api/v1/auth/device/token", m.RateLimitDevicePoll(h.Device.Token), TokenDenied},
		{"POST /api/v1/auth/onboarding", m.RequireAuth(h.Auth.CompleteOnboarding), TokenDenied},
		{"POST /api/v1/auth/logout", m.RequireAuth(h.Auth.Logout), TokenDenied},
		{"GET /api/v1/account", m.RequireAuth(h.Auth.Account), TokenRead},
		{"PATCH /api/v1/account/name", m.RequireOnboarded(h.Account.UpdateName), TokenWrite},
		{"PUT /api/v1/account/email-alerts", m.RequireOnboarded(h.Account.SetEmailAlerts), TokenWrite},
		{"PATCH /api/v1/account/email", m.RequireOnboarded(m.RateLimitEmailChanges(h.Account.ChangeEmail)), TokenDenied},
		{"POST /api/v1/account/password", m.RequireOnboarded(h.Account.ChangePassword), TokenDenied},
		{"POST /api/v1/account/password/set", m.RequireOnboarded(h.Account.SetPassword), TokenDenied},
		{"DELETE /api/v1/account/password", m.RequireOnboarded(h.Account.RemovePassword), TokenDenied},
		{"DELETE /api/v1/account", m.RequireOnboarded(h.Account.DeleteAccount), TokenDenied},
		{"GET /api/v1/account/sessions", m.RequireOnboarded(h.Sessions.List), TokenDenied},
		{"DELETE /api/v1/account/sessions/{id}", m.RequireOnboarded(h.Sessions.Revoke), TokenDenied},
		{"DELETE /api/v1/account/sessions", m.RequireOnboarded(h.Sessions.RevokeOthers), TokenDenied},
		{"GET /api/v1/account/tokens", m.RequireOnboarded(h.Tokens.List), TokenDenied},
		{"POST /api/v1/account/tokens", m.RequireOnboarded(h.Tokens.Create), TokenDenied},
		{"DELETE /api/v1/account/tokens/{id}", m.RequireOnboarded(h.Tokens.Revoke), TokenDenied},
		{"POST /api/v1/account/avatar", m.RequireOnboarded(h.Account.UploadAvatar), TokenWrite},
		{"DELETE /api/v1/account/avatar", m.RequireOnboarded(h.Account.DeleteAvatar), TokenWrite},
		{"GET /api/v1/workspaces", m.RequireOnboarded(h.Workspaces.List), TokenRead},
		{"POST /api/v1/workspaces", m.RequireOnboarded(h.Workspaces.Create), TokenDenied},
		{"GET /api/v1/workspaces/{workspaceSlug}", m.Workspace(h.Workspaces.Show), TokenRead},
		{"DELETE /api/v1/workspaces/{workspaceSlug}", m.Workspace(h.Workspaces.Delete), TokenDenied},
		{"PATCH /api/v1/workspaces/{workspaceSlug}/name", m.Workspace(h.Workspaces.UpdateName), TokenWrite},
		{"POST /api/v1/workspaces/{workspaceSlug}/invitations", m.Workspace(m.RateLimitInvitations(h.Workspaces.Invite)), TokenWrite},
		{"POST /api/v1/workspaces/{workspaceSlug}/invitations/{invitationID}/resend", m.Workspace(m.RateLimitInvitations(h.Workspaces.ResendInvitation)), TokenWrite},
		{"DELETE /api/v1/workspaces/{workspaceSlug}/invitations/{invitationID}", m.Workspace(h.Workspaces.RevokeInvitation), TokenWrite},
		{"PATCH /api/v1/workspaces/{workspaceSlug}/members/{memberID}/role", m.Workspace(h.Workspaces.ChangeRole), TokenDenied},
		{"DELETE /api/v1/workspaces/{workspaceSlug}/members/{memberID}", m.Workspace(h.Workspaces.RemoveMember), TokenDenied},
		{"POST /api/v1/workspaces/{workspaceSlug}/leave", m.Workspace(h.Workspaces.Leave), TokenDenied},
		{"GET /api/v1/workspaces/{workspaceSlug}/activity", m.Workspace(h.Workspaces.Activity), TokenRead},
		{"GET /api/v1/activity", m.InWorkspace(h.Workspaces.Activity), TokenRead},
		{"GET /api/v1/invitations/{token}", h.Invitations.Show, TokenRead},
		{"POST /api/v1/invitations/{token}/accept", m.RequireOnboarded(h.Invitations.Accept), TokenDenied},
		{"GET /api/v1/workspaces/{workspaceSlug}/billing", m.Workspace(billingOn(h.Billing, h.Billing.Billing)), TokenRead},
		{"POST /api/v1/workspaces/{workspaceSlug}/billing/checkout", m.Workspace(billingOn(h.Billing, h.Billing.CreateCheckout)), TokenDenied},
		{"GET /api/v1/workspaces/{workspaceSlug}/billing/manage", m.Workspace(billingOn(h.Billing, h.Billing.BillingManagement)), TokenDenied},
		{"GET /api/v1/servers", m.InWorkspace(h.Servers.List), TokenRead},
		{"POST /api/v1/servers", m.InWorkspace(h.Servers.Create), TokenWrite},
		{"DELETE /api/v1/servers/{id}", m.InWorkspace(h.Servers.Delete), TokenWrite},
		{"PATCH /api/v1/servers/{id}", m.InWorkspace(h.Servers.Rename), TokenWrite},
		{"POST /api/v1/servers/{id}/update", m.InWorkspace(h.Servers.Update), TokenWrite},
		{"POST /api/v1/servers/{id}/install", m.InWorkspace(h.Servers.NewInstall), TokenWrite},
		{"PUT /api/v1/servers/{id}/security", m.InWorkspace(h.Apps.SetSecurity), TokenWrite},
		{"GET /api/v1/servers/{id}/metrics", m.InWorkspace(h.Apps.ServerMetrics), TokenRead},
		{"GET /api/v1/integrations", m.InWorkspace(h.Apps.GetIntegrations), TokenRead},
		{"PUT /api/v1/integrations", m.InWorkspace(h.Apps.SetIntegrations), TokenWrite},
		{"GET /api/v1/agent-updates", m.InWorkspace(h.Version.AgentUpdates), TokenRead},
		{"PUT /api/v1/agent-updates", m.InWorkspace(h.Version.SetAgentUpdates), TokenWrite},
		{"GET /api/v1/servers/events", m.InWorkspace(streams.wrap(h.Servers.Events)), TokenRead},
		{"GET /api/v1/status", m.InWorkspace(h.Status.Get), TokenRead},
		{"GET /api/v1/catalog", m.InWorkspace(h.Catalog.List), TokenRead},
		{"GET /api/v1/catalog/{slug}", m.InWorkspace(h.Catalog.Get), TokenRead},
		{"POST /api/v1/catalog/{slug}/install", m.InWorkspace(h.Catalog.Install), TokenWrite},
		{"GET /api/v1/apps", m.InWorkspace(h.Apps.List), TokenRead},
		{"POST /api/v1/apps", m.InWorkspace(h.Apps.Create), TokenWrite},
		{"PUT /api/v1/apps/{app}/pin", m.InWorkspace(h.Apps.SetPinned), TokenWrite},
		{"GET /api/v1/apps/events", m.InWorkspace(streams.wrap(h.Apps.Events)), TokenRead},
		{"GET /api/v1/apps/{app}", m.InWorkspace(h.Apps.Get), TokenRead},
		{"DELETE /api/v1/apps/{app}", m.InWorkspace(h.Apps.Delete), TokenWrite},
		{"POST /api/v1/apps/{app}/deploys", m.InWorkspace(h.Apps.CreateDeploy), TokenWrite},
		{"POST /api/v1/apps/{app}/uploads", m.InWorkspace(h.Apps.CreateUpload), TokenWrite},
		{"GET /api/v1/apps/{app}/deploys/{number}", m.InWorkspace(h.Apps.GetDeploy), TokenRead},
		{"GET /api/v1/apps/{app}/deploys/{number}/events", m.InWorkspace(streams.wrap(h.Apps.DeployEvents)), TokenRead},
		{"POST /api/v1/apps/{app}/rollback", m.InWorkspace(h.Apps.Rollback), TokenWrite},
		{"POST /api/v1/apps/{app}/update", m.InWorkspace(h.Catalog.Update), TokenWrite},
		{"POST /api/v1/apps/{app}/redeploy", m.InWorkspace(h.Apps.Redeploy), TokenWrite},
		{"POST /api/v1/apps/{app}/hook", m.InWorkspace(h.Apps.RotateHook), TokenWrite},
		{"DELETE /api/v1/apps/{app}/hook", m.InWorkspace(h.Apps.RemoveHook), TokenWrite},
		{"POST /api/v1/apps/{app}/restart", m.InWorkspace(h.Apps.Restart), TokenWrite},
		{"POST /api/v1/apps/{app}/start", m.InWorkspace(h.Apps.Start), TokenWrite},
		{"POST /api/v1/apps/{app}/stop", m.InWorkspace(h.Apps.Stop), TokenWrite},
		{"GET /api/v1/apps/{app}/domains", m.InWorkspace(h.Apps.Domains), TokenRead},
		{"PUT /api/v1/apps/{app}/domains", m.InWorkspace(h.Apps.SetDomains), TokenWrite},
		{"GET /api/v1/apps/{app}/instant", m.InWorkspace(h.Apps.GetInstant), TokenRead},
		{"PUT /api/v1/apps/{app}/instant", m.InWorkspace(h.Apps.SetInstant), TokenWrite},
		{"PUT /api/v1/apps/{app}/protection", m.InWorkspace(h.Apps.SetProtection), TokenWrite},
		{"DELETE /api/v1/apps/{app}/protection", m.InWorkspace(h.Apps.DeleteProtection), TokenWrite},
		{"GET /api/v1/registries", m.InWorkspace(h.Apps.Registries), TokenRead},
		{"PUT /api/v1/registries/{host}", m.InWorkspace(h.Apps.SetRegistry), TokenWrite},
		{"DELETE /api/v1/registries/{host}", m.InWorkspace(h.Apps.DeleteRegistry), TokenWrite},
		{"GET /api/v1/apps/{app}/logs", m.InWorkspace(h.Apps.Logs), TokenRead},
		{"POST /api/v1/apps/{app}/exec", m.InWorkspace(h.Apps.Exec), TokenWrite},
		{"GET /api/v1/apps/{app}/databases", m.InWorkspace(h.Apps.Databases), TokenRead},
		{"GET /api/v1/apps/{app}/databases/tables", m.InWorkspace(h.Apps.Tables), TokenRead},
		{"GET /api/v1/apps/{app}/databases/rows", m.InWorkspace(h.Apps.Rows), TokenWrite},
		{"POST /api/v1/apps/{app}/databases/rows", m.InWorkspace(h.Apps.InsertRow), TokenWrite},
		{"PATCH /api/v1/apps/{app}/databases/rows", m.InWorkspace(h.Apps.UpdateRow), TokenWrite},
		{"DELETE /api/v1/apps/{app}/databases/rows", m.InWorkspace(h.Apps.DeleteRow), TokenWrite},
		{"POST /api/v1/apps/{app}/databases/query", m.InWorkspace(h.Apps.Query), TokenWrite},
		{"GET /api/v1/apps/{app}/backups", m.InWorkspace(h.Apps.Backups), TokenRead},
		{"POST /api/v1/apps/{app}/backups/downloads", m.InWorkspace(h.Apps.StartDownload), TokenWrite},
		{"GET /api/v1/apps/{app}/backups/downloads/{download}", m.InWorkspace(h.Apps.Download), TokenWrite},
		{"POST /api/v1/apps/{app}/restore", m.InWorkspace(h.Apps.Restore), TokenWrite},
		{"GET /api/v1/apps/{app}/uptime", m.InWorkspace(h.Apps.Uptime), TokenRead},
		{"PUT /api/v1/apps/{app}/uptime/{domain}", m.InWorkspace(h.Apps.SetUptimeCheck), TokenWrite},
		{"GET /api/v1/alert-destinations", m.InWorkspace(h.Alerts.List), TokenRead},
		{"POST /api/v1/alert-destinations", m.InWorkspace(h.Alerts.Create), TokenWrite},
		{"DELETE /api/v1/alert-destinations/{destination}", m.InWorkspace(h.Alerts.Delete), TokenWrite},
		{"POST /api/v1/alert-destinations/{destination}/test", m.InWorkspace(h.Alerts.Test), TokenWrite},
		{"GET /api/v1/apps/{app}/metrics", m.InWorkspace(h.Apps.AppMetrics), TokenRead},
		{"GET /api/v1/github", m.InWorkspace(h.Apps.GetGitHub), TokenRead},
		{"GET /api/v1/github/repos", m.InWorkspace(h.Apps.GetGitHubRepos), TokenRead},
		{"GET /api/v1/github/repos/{owner}/{repo}/branches", m.InWorkspace(h.Apps.GetGitHubBranches), TokenRead},
		{"PUT /api/v1/apps/{app}/repo", m.InWorkspace(h.Apps.SetRepo), TokenWrite},
		{"DELETE /api/v1/apps/{app}/repo", m.InWorkspace(h.Apps.DeleteRepo), TokenWrite},
		{"GET /api/v1/buckets", m.InWorkspace(h.Apps.Buckets), TokenRead},
		{"POST /api/v1/buckets", m.InWorkspace(h.Apps.AddBucket), TokenWrite},
		{"GET /api/v1/buckets/{name}", m.InWorkspace(h.Apps.GetBucket), TokenRead},
		{"PUT /api/v1/buckets/{name}", m.InWorkspace(h.Apps.EditBucket), TokenWrite},
		{"DELETE /api/v1/buckets/{name}", m.InWorkspace(h.Apps.DeleteBucket), TokenWrite},
		{"PUT /api/v1/apps/{app}/bucket", m.InWorkspace(h.Apps.SetAppBucket), TokenWrite},
		{"GET /api/v1/apps/{app}/env", m.InWorkspace(h.Apps.GetEnv), TokenWrite},
		{"PATCH /api/v1/apps/{app}/env", m.InWorkspace(h.Apps.UpdateEnv), TokenWrite},
		{"PUT /api/v1/apps/{app}/env", m.InWorkspace(h.Apps.ReplaceEnv), TokenWrite},
		{"GET /api/v1/admin/users", hostedOnly(h.Billing, m.RequireOnboarded(m.RequireAdmin(h.Admin.Users))), TokenDenied},
		{"GET /api/v1/admin/old-domain", hostedOnly(h.Billing, m.RequireOnboarded(m.RequireAdmin(h.Admin.OldDomain))), TokenDenied},
		{"POST /api/v1/admin/users/{userID}/sessions/revoke", hostedOnly(h.Billing, m.RequireOnboarded(m.RequireAdmin(h.Admin.RevokeUserSessions))), TokenDenied},
	}
	for index := range routes {
		routes[index].Handler = routes[index].Tokens.wrap(routes[index].Handler)
	}
	return routes
}

// hostedOnly answers 404 on a self-hosted control plane (billing off), for
// what only running moin as a service needs.
func hostedOnly(billing *BillingHandler, next http.HandlerFunc) http.HandlerFunc {
	if billing != nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		Error(w, http.StatusNotFound, "not_found", "Not found.")
	}
}

// billingOn answers 404 on a control plane with billing off.
func billingOn(billing *BillingHandler, next http.HandlerFunc) http.HandlerFunc {
	if billing != nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		Error(w, http.StatusNotFound, "not_found", "Billing is off on this control plane.")
	}
}
