package routes

import (
	"io/fs"
	"net/http"
	"regexp"
	"time"

	"github.com/axadrn/moin/controlplane/assets"
	"github.com/axadrn/moin/controlplane/internal/api"
	"github.com/axadrn/moin/controlplane/internal/app"
	"github.com/axadrn/moin/controlplane/internal/content"
	"github.com/axadrn/moin/controlplane/internal/ctxkeys"
	"github.com/axadrn/moin/controlplane/internal/handler"
	"github.com/axadrn/moin/controlplane/internal/mcp"
	"github.com/axadrn/moin/controlplane/internal/middleware"
	"github.com/axadrn/moin/controlplane/internal/ui/pages"
	"github.com/axadrn/moin/internal/build"
	"github.com/axadrn/moin/protocol"
)

var immutableAssetPathPattern = regexp.MustCompile(`^/?js/.+\.[0-9a-f]{8}(?:\.min)?\.js$`)

// fontAssetPathPattern is the font files. Their names carry no hash, but a
// font changes so rarely that browsers may keep it for a year.
var fontAssetPathPattern = regexp.MustCompile(`^/?fonts/.+\.(?:woff2|ttf)$`)

// limiter wraps a handler in a rate limit.
type limiter = func(http.HandlerFunc) http.HandlerFunc

// SetupRoutes registers every route, area by area, and wraps them in the
// middleware every request goes through. The web, the API and MCP share
// their rate limits, so a sign-in or an invitation counts once wherever it
// comes from.
func SetupRoutes(app *app.App) http.Handler {
	clientIPs := middleware.NewClientIPResolver(app.Cfg.TrustedProxyCIDRs)
	rateLimiter := middleware.RateLimitAuth(clientIPs)
	authEmailRateLimiter := middleware.RateLimitAuthEmail(clientIPs)
	deviceLimiter := middleware.RateLimitDeviceLogin(clientIPs)
	emailChanges := middleware.RateLimitUserEmailChanges()
	// Web, API and MCP invitations share one budget.
	invitationLimiter := middleware.NewWorkspaceInvitationLimiter()

	home := handler.NewHomeHandler(app.TelemetryService, app.Cfg.Billing, content.Root)
	catalogPages := handler.NewCatalogHandler(app.CatalogService, app.AppService, app.ServerService, app.WorkspaceService, app.Cfg.AppURL)
	mux := http.NewServeMux()
	siteRoutes(mux, app, home, catalogPages)
	authRoutes(mux, app, rateLimiter, authEmailRateLimiter, deviceLimiter)
	workspaceRoutes(mux, app, emailChanges, middleware.RateLimitWorkspaceInvitations(invitationLimiter))
	appRoutes(mux, app, catalogPages)
	adminRoutes(mux, app)
	agentRoutes(mux, app, clientIPs)
	apiRoutes(mux, app, clientIPs, rateLimiter, authEmailRateLimiter, deviceLimiter, emailChanges, invitationLimiter)
	mcpRoutes(mux, app, invitationLimiter)
	mux.HandleFunc("/api/", middleware.APINotFound)
	mux.HandleFunc("/{path...}", home.NotFoundPage)

	securityHeaderConfig := middleware.SecurityHeadersConfig{
		RobotsNoIndex: app.Cfg.RobotsNoIndex,
		S3Endpoint:    app.Cfg.S3Endpoint,
		UmamiHost:     app.Cfg.UmamiHost,
		PlausibleHost: app.Cfg.PlausibleHost,
	}
	securityHeaders := func(next http.Handler) http.Handler {
		return middleware.SecurityHeaders(securityHeaderConfig, next)
	}
	return middleware.Chain(
		mux,
		middleware.RenderConfig(app.Cfg.Render()),
		middleware.NonceMiddleware,
		securityHeaders,
		middleware.RequestLogging(app.Cfg.LogHTTP, clientIPs),
		middleware.Client(clientIPs),
		middleware.OldAddress(app.OldAddressService.Hosts(), app.Cfg.AppURL, app.OldAddressService.Count),
		middleware.CORS(app.Cfg.CORSAllowedOrigins),
		middleware.CSRFProtection(app.Cfg.IsProduction(), clientIPs),
		middleware.AuthMiddleware(app.AuthService, app.UserService),
		middleware.APITokenAuth(app.APITokenService, app.UserService),
		middleware.BlockImpersonatedWrites,
		middleware.AdminContext(app.AdminService),
		middleware.WithURLPath,
	)
}

// assetHandler serves the files under assets: from disk in development,
// without caching, else from the binary, hashed scripts and fonts cached
// for a year.
func assetHandler(app *app.App) http.Handler {
	if app.Cfg.IsDevelopment() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			http.FileServer(http.Dir("./assets")).ServeHTTP(w, r)
		})
	}
	sub, _ := fs.Sub(assets.AssetsFS, ".")
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case immutableAssetPathPattern.MatchString(r.URL.Path):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case fontAssetPathPattern.MatchString(r.URL.Path):
			w.Header().Set("Cache-Control", "public, max-age=31536000")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// siteRoutes are the public site: assets, the home page, docs, search, the
// changelog, the catalog and the legal pages.
func siteRoutes(mux *http.ServeMux, app *app.App, home *handler.HomeHandler, catalogPages *handler.CatalogHandler) {
	seo := handler.NewSEOHandler(app.DocsService, app.Cfg.AppURL, content.Root, app.Cfg.RobotsNoIndex, app.Cfg.Billing)
	legal := handler.NewLegalHandler(app.LegalService)
	docs := handler.NewDocsHandler(app.DocsService)
	assetHandler := assetHandler(app)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", middleware.Gzip(assetHandler)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// Browsers, link previews and analytics ask for /favicon.ico at the root.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		asset := r.Clone(r.Context())
		asset.URL.Path = "/img/favicon.ico"
		assetHandler.ServeHTTP(w, asset)
	})
	mux.HandleFunc("GET /robots.txt", seo.Robots)
	mux.HandleFunc("GET /sitemap.xml", seo.Sitemap)
	mux.HandleFunc("GET /llms.txt", seo.LLMs)
	mux.HandleFunc("GET /{$}", home.HomePage)
	mux.HandleFunc("GET /docs", docs.ShowDocs)
	mux.HandleFunc("GET /docs.md", docs.ShowDocs)
	mux.HandleFunc("GET /docs/", docs.ShowDocs)
	mux.HandleFunc("GET /search", handler.NewSiteSearchHandler(app.DocsService, app.CatalogService).Search)
	mux.HandleFunc("GET /changelog", handler.Changelog(content.Root))
	mux.HandleFunc("GET /changelog.xml", handler.ChangelogFeed(content.Root, app.Cfg.AppURL, build.Name))
	mux.HandleFunc("GET /catalog", catalogPages.List)
	mux.HandleFunc("GET /catalog/{slug}", catalogPages.Show)
	mux.HandleFunc("GET /catalog/{slug}/logo", catalogPages.Logo)
	mux.HandleFunc("GET /catalog/{slug}/install", middleware.RequireAuth(catalogPages.Start))
	// The legal pages describe the hosted service; a self-hosted control
	// plane answers them with not found.
	if app.Cfg.Billing {
		mux.HandleFunc("GET /legal/{page}", legal.ShowPage)
	}
}

// authRoutes sign people in and out, start a workspace, take an invitation
// and approve moin login in the browser.
func authRoutes(mux *http.ServeMux, app *app.App, rateLimiter, authEmailRateLimiter, deviceLimiter limiter) {
	auth := handler.NewAuthHandler(app.AuthService, app.UserService, app.WorkspaceService, app.Cfg)
	workspaceHandler := handler.NewWorkspaceHandler(app.WorkspaceService)
	workspaceInvitation := handler.NewWorkspaceInvitationHandler(app.WorkspaceService, app.Cfg)
	mux.HandleFunc("GET /auth", middleware.RequireGuest(auth.AuthPage))
	mux.HandleFunc("GET /auth/password", middleware.RequireGuest(auth.PasswordPage))
	mux.HandleFunc("GET /auth/forgot-password", middleware.RequireGuest(auth.ForgotPasswordPage))
	mux.HandleFunc("GET /auth/forgot-password-sent", middleware.RequireGuest(auth.ForgotPasswordSentPage))
	mux.HandleFunc("GET /auth/magic-link-sent", middleware.RequireGuest(auth.MagicLinkSentPage))
	mux.HandleFunc("GET /auth/onboarding", middleware.RequireAuth(auth.OnboardingPage))
	mux.HandleFunc("GET /auth/github", rateLimiter(middleware.RequireGuest(auth.GitHubAuth)))
	mux.HandleFunc("GET /auth/github/callback", rateLimiter(auth.GitHubCallback))
	mux.HandleFunc("GET /auth/magic-link/{token}", auth.VerifyMagicLink)
	mux.HandleFunc("GET /auth/forgot-password/{token}", auth.VerifyForgotPassword)
	mux.HandleFunc("GET /auth/verify-email-change/{token}", auth.VerifyEmailChange)
	mux.HandleFunc("POST /auth/magic-link", authEmailRateLimiter(middleware.RequireGuest(auth.SendMagicLink)))
	mux.HandleFunc("POST /auth/password", rateLimiter(middleware.RequireGuest(auth.PasswordAuth)))
	mux.HandleFunc("POST /auth/forgot-password", authEmailRateLimiter(middleware.RequireGuest(auth.ForgotPassword)))
	mux.HandleFunc("POST /auth/onboarding", middleware.RequireAuth(auth.CompleteOnboarding))
	mux.HandleFunc("GET /workspaces/new", middleware.RequireAuth(workspaceHandler.NewPage))
	mux.HandleFunc("POST /workspaces/new", middleware.RequireAuth(workspaceHandler.Create))
	mux.HandleFunc("POST /auth/logout", auth.Logout)
	mux.HandleFunc("GET /workspaces/invitations/{token}", workspaceInvitation.Show)
	mux.HandleFunc("POST /workspaces/invitations/{token}", middleware.RequireAuth(workspaceInvitation.Accept))
	// moin login in the browser: the page signs in first by itself.
	device := handler.NewDeviceHandler(app.DeviceLoginService, app.WorkspaceService, app.Cfg)
	mux.HandleFunc("GET /device", deviceLimiter(device.Show))
	mux.HandleFunc("POST /device/approve", deviceLimiter(middleware.RequireAuth(device.Approve)))
	mux.HandleFunc("POST /device/deny", deviceLimiter(middleware.RequireAuth(device.Deny)))
}

// inWorkspace is the middleware of every page under /app/{workspaceSlug}:
// signed in, and a member of the workspace.
func inWorkspace(app *app.App) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return middleware.RequireAuth(middleware.WithWorkspace(app.WorkspaceService, app.SubscriptionService)(next))
	}
}

// workspaceRoutes are the pages of a workspace around its servers and apps:
// the overview, settings, the account, billing, the team and alerts.
func workspaceRoutes(mux *http.ServeMux, app *app.App, emailChanges, limitInvitations limiter) {
	workspace := inWorkspace(app)
	settings := handler.NewSettingsHandler(app.AuthService, app.APITokenService, app.AppService)
	apiTokens := handler.NewAPITokensHandler(app.APITokenService)
	profile := handler.NewProfileHandler(app.UserService)
	account := handler.NewAccountHandler(app.AuthService, app.UserService, app.FileService)
	workspaceHandler := handler.NewWorkspaceHandler(app.WorkspaceService)
	mux.HandleFunc("GET /app", middleware.RequireAuth(workspaceHandler.Redirect))
	// Sign-ins and invitations lead to the overview, and a workspace's own
	// path too.
	overview := handler.NewOverviewHandler(app.StatusService, app.AppService, app.ServerService)
	toOverview := workspace(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ctxkeys.AppBasePath(r.Context())+"/overview", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /app/{workspaceSlug}", toOverview)
	mux.HandleFunc("GET /app/{workspaceSlug}/overview", workspace(overview.Page))
	mux.HandleFunc("GET /app/{workspaceSlug}/search", workspace(handler.NewCommandHandler(app.AppService, app.ServerService, app.DocsService, app.CatalogService).Search))
	mux.HandleFunc("GET /app/{workspaceSlug}/settings", workspace(settings.Home))
	mux.HandleFunc("GET /app/{workspaceSlug}/settings/account", workspace(settings.SettingsPage))
	mux.HandleFunc("GET /app/{workspaceSlug}/settings/github", workspace(settings.GitHub))
	if app.Cfg.IsDevelopment() {
		// The gallery of the kit (DESIGN.md, Geometry), never in production.
		mux.HandleFunc("GET /app/{workspaceSlug}/dev/kit", workspace(func(w http.ResponseWriter, r *http.Request) {
			_ = pages.DevKit().Render(r.Context(), w)
		}))
	}
	mux.HandleFunc("DELETE /app/{workspaceSlug}/settings/sessions/{id}", workspace(settings.RevokeSession))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/settings/sessions", workspace(settings.RevokeAllSessions))
	mux.HandleFunc("POST /app/{workspaceSlug}/settings/api-tokens", workspace(apiTokens.Create))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/settings/api-tokens/{id}", workspace(apiTokens.Revoke))
	mux.HandleFunc("PATCH /app/{workspaceSlug}/profile/name", workspace(profile.UpdateName))
	mux.HandleFunc("POST /app/{workspaceSlug}/profile/email-alerts", workspace(profile.EmailAlerts))
	mux.HandleFunc("PATCH /app/{workspaceSlug}/account/email", workspace(emailChanges(account.ChangeEmail)))
	mux.HandleFunc("POST /app/{workspaceSlug}/account/password", workspace(account.ChangePassword))
	mux.HandleFunc("POST /app/{workspaceSlug}/account/avatar", workspace(account.UploadAvatar))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/account/avatar", workspace(account.DeleteAvatar))
	mux.HandleFunc("POST /app/{workspaceSlug}/account/password/set", workspace(account.SetPassword))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/account/password", workspace(account.RemovePassword))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/account", workspace(account.DeleteAccount))
	if app.Cfg.Billing {
		billing := handler.NewBillingHandler(app.PaymentService, app.WorkspaceService)
		mux.HandleFunc("GET /app/{workspaceSlug}/billing", workspace(billing.BillingPage))
		mux.HandleFunc("POST /app/{workspaceSlug}/billing/checkout", workspace(billing.CreateCheckout))
		mux.HandleFunc("GET /app/{workspaceSlug}/billing/manage", workspace(billing.BillingManagement))
		mux.HandleFunc("POST /webhooks/payment", billing.Webhook)
	}
	mux.HandleFunc("GET /app/{workspaceSlug}/workspace", workspace(workspaceHandler.Page))
	mux.HandleFunc("GET /app/{workspaceSlug}/activity", workspace(workspaceHandler.ActivityPage))
	alerts := handler.NewAlertsHandler(app.AlertService)
	mux.HandleFunc("GET /app/{workspaceSlug}/alerts", workspace(alerts.Page))
	mux.HandleFunc("POST /app/{workspaceSlug}/alerts/destinations", workspace(alerts.Add))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/alerts/destinations/{destination}", workspace(alerts.Remove))
	mux.HandleFunc("POST /app/{workspaceSlug}/alerts/destinations/{destination}/test", workspace(alerts.Test))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/name", workspace(workspaceHandler.UpdateName))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/invitations", workspace(limitInvitations(workspaceHandler.Invite)))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/invitations/{invitationID}/resend", workspace(limitInvitations(workspaceHandler.ResendInvitation)))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/invitations/{invitationID}/revoke", workspace(workspaceHandler.RevokeInvitation))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/members/{memberID}/role", workspace(workspaceHandler.ChangeRole))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/members/{memberID}/remove", workspace(workspaceHandler.RemoveMember))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/leave", workspace(workspaceHandler.Leave))
	mux.HandleFunc("POST /app/{workspaceSlug}/workspace/delete", workspace(workspaceHandler.Delete))
}

// appRoutes are the pages of servers and apps: deploys, domains, data,
// backups, uptime, metrics, buckets, env, GitHub and the catalog.
func appRoutes(mux *http.ServeMux, app *app.App, catalogPages *handler.CatalogHandler) {
	workspace := inWorkspace(app)
	servers := handler.NewServersHandler(app.ServerService, app.UserService)
	mux.HandleFunc("GET /app/{workspaceSlug}/servers", workspace(servers.ServersPage))
	mux.HandleFunc("GET /app/{workspaceSlug}/servers/list", workspace(servers.List))
	mux.HandleFunc("POST /app/{workspaceSlug}/servers", workspace(servers.Create))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/servers/{id}", workspace(servers.Delete))
	mux.HandleFunc("POST /app/{workspaceSlug}/servers/{id}/update", workspace(servers.UpdateAgent))
	mux.HandleFunc("POST /app/{workspaceSlug}/servers/agent-updates", workspace(servers.AgentUpdates))
	updates := handler.NewUpdateHandler(app.UpdateService)
	mux.HandleFunc("GET /app/{workspaceSlug}/update", workspace(updates.Notice))
	mux.HandleFunc("POST /app/{workspaceSlug}/update", workspace(updates.Start))
	mux.HandleFunc("POST /app/{workspaceSlug}/servers/{id}/install", workspace(servers.NewInstall))
	mux.HandleFunc("GET /app/{workspaceSlug}/servers/{id}/install", workspace(servers.Install))
	apps := handler.NewAppsHandler(app.AppService, app.ServerService, app.UserService, app.CatalogService)
	mux.HandleFunc("GET /app/{workspaceSlug}/catalog", workspace(catalogPages.InApp))
	mux.HandleFunc("GET /app/{workspaceSlug}/catalog/{slug}", workspace(catalogPages.InstallPage))
	mux.HandleFunc("POST /app/{workspaceSlug}/catalog/{slug}/install", workspace(catalogPages.Install))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/catalog", workspace(catalogPages.UpdatePanel))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/update", workspace(catalogPages.Update))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps", workspace(apps.AppsPage))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/new", workspace(apps.NewPage))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/pin", workspace(apps.SetPinned))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps", workspace(apps.Create))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}", workspace(apps.AppSection("app")))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/deploys", workspace(apps.AppSection("deploys")))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/deploys/{number}", workspace(apps.DeployPage))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/deploys/{number}/log", workspace(apps.DeployLog))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/domains", workspace(apps.AppSection("domains")))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/settings", workspace(apps.AppSection("settings")))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/detail", workspace(apps.Detail))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/apps/{app}", workspace(apps.Delete))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/rollback", workspace(apps.Rollback))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/redeploy", workspace(apps.Redeploy))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/restart", workspace(apps.Restart))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/start", workspace(apps.Start))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/stop", workspace(apps.Stop))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/image", workspace(apps.DeployImage))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/hook", workspace(apps.RotateHook))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/apps/{app}/hook", workspace(apps.RemoveHook))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/domains/detail", workspace(apps.DomainsDetail))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/domains/add", workspace(apps.AddDomain))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/domains/remove", workspace(apps.RemoveDomain))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/domains/canonical", workspace(apps.MakeCanonical))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/domains/protect", workspace(apps.Protect))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/domains/unprotect", workspace(apps.Unprotect))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/domains/instant", workspace(apps.SetInstant))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/domains/instant-password", workspace(apps.SetInstantPassword))
	mux.HandleFunc("POST /app/{workspaceSlug}/settings/registries", workspace(apps.SetRegistry))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/settings/registries/{host}", workspace(apps.DeleteRegistry))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/logs", workspace(apps.AppSection("logs")))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/logs/lines", workspace(apps.Logs))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/data", workspace(apps.Data))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/data/cell", workspace(apps.DataCell))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/data/delete", workspace(apps.DataDelete))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/data/query", workspace(apps.DataQuery))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/data/insert", workspace(apps.DataInsert))
	mux.HandleFunc("GET /app/{workspaceSlug}/servers/{id}/security", workspace(apps.ServerSecurity))
	mux.HandleFunc("POST /app/{workspaceSlug}/servers/{id}/security", workspace(apps.SetServerSecurity))
	mux.HandleFunc("POST /app/{workspaceSlug}/servers/{id}/name", workspace(apps.RenameServer))
	mux.HandleFunc("POST /app/{workspaceSlug}/integrations", workspace(apps.SetIntegrations))
	mux.HandleFunc("GET /app/{workspaceSlug}/github/install", workspace(apps.GitHubInstall))
	mux.HandleFunc("GET /app/github/setup", middleware.RequireAuth(apps.GitHubSetup))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/repo", workspace(apps.Repo))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/repo", workspace(apps.LinkRepo))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/repo/branches", workspace(apps.RepoBranches))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/repo/auto-deploy", workspace(apps.SetAutoDeploy))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/repo/unlink", workspace(apps.UnlinkRepo))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/repo/deploy", workspace(apps.DeployRef))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/backups/restore", workspace(apps.BackupRestore))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/backups/download", workspace(apps.BackupDownload))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/uptime", workspace(apps.Uptime))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/uptime/check", workspace(apps.UptimeCheck))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/uptime/on", workspace(apps.UptimeOn))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/metrics", workspace(apps.AppMetrics))
	mux.HandleFunc("GET /app/{workspaceSlug}/servers/{id}/metrics", workspace(apps.ServerMetrics))
	mux.HandleFunc("GET /app/{workspaceSlug}/bucket", workspace(apps.Bucket))
	mux.HandleFunc("POST /app/{workspaceSlug}/bucket", workspace(apps.AddBucket))
	mux.HandleFunc("POST /app/{workspaceSlug}/bucket/{name}", workspace(apps.EditBucket))
	mux.HandleFunc("POST /app/{workspaceSlug}/bucket/{name}/default", workspace(apps.DefaultBucket))
	mux.HandleFunc("POST /app/{workspaceSlug}/bucket/{name}/remove", workspace(apps.RemoveBucket))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/bucket", workspace(apps.SetAppBucket))
	mux.HandleFunc("GET /app/{workspaceSlug}/apps/{app}/env", workspace(apps.Env))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/env", workspace(apps.SetEnv))
	mux.HandleFunc("POST /app/{workspaceSlug}/apps/{app}/env/text", workspace(apps.EnvText))
	mux.HandleFunc("DELETE /app/{workspaceSlug}/apps/{app}/env/{key}", workspace(apps.UnsetEnv))
}

// adminRoutes are the operator's Admin. Admin is for running moin as a
// service: only the hosted control plane has it. A self-hosted one manages
// its people under Team.
func adminRoutes(mux *http.ServeMux, app *app.App) {
	if !app.Cfg.Billing {
		return
	}
	admin := handler.NewAdminHandler(app.AdminService)
	adminRoute := func(next http.HandlerFunc) http.HandlerFunc {
		return middleware.RequireAuth(middleware.RequireAdmin(next))
	}
	mux.HandleFunc("GET /admin", adminRoute(admin.Overview))
	mux.HandleFunc("GET /admin/old-address", adminRoute(admin.OldAddress))
	mux.HandleFunc("GET /admin/activity", adminRoute(admin.Activity))
	mux.HandleFunc("GET /admin/users/{userID}", adminRoute(admin.User))
	mux.HandleFunc("POST /admin/users/{userID}/sessions/revoke", adminRoute(admin.RevokeUserSessions))
	mux.HandleFunc("POST /admin/users/{userID}/impersonate", adminRoute(admin.StartImpersonation))
	mux.HandleFunc("POST /admin/users/{userID}/forever-free", adminRoute(admin.ForeverFree))
	mux.HandleFunc("POST /admin/impersonation/stop", admin.StopImpersonation)
}

// agentRoutes are what servers and CI call: the install scripts and
// binaries, the agent's register and session, deploy hooks and the
// webhooks of GitHub and telemetry.
func agentRoutes(mux *http.ServeMux, app *app.App, clientIPs middleware.ClientIPResolver) {
	agent := handler.NewAgentHandler(app.ServerService, app.Cfg.AppURL, clientIPs, app.OldAddressService.Hosts())
	mux.HandleFunc("GET /install.sh", agent.InstallScript)
	mux.HandleFunc("GET /cli.sh", agent.CLIScript)
	mux.Handle("GET /download/", http.StripPrefix("/download/", http.FileServer(http.Dir("downloads"))))
	// Agents get their own budget: many servers behind one NAT must not lock
	// out sign-ins, and install tokens are too long to guess.
	registerLimiter := middleware.RateLimit(clientIPs, 30, time.Minute)
	mux.HandleFunc("POST "+protocol.RegisterPath, registerLimiter(agent.Register))
	// CI calls deploy hooks; the token is the credential, the limit stops guessing.
	hooks := handler.NewHooksHandler(app.AppService)
	// The hosted control plane counts what self-hosted ones report.
	if app.Cfg.Billing {
		mux.HandleFunc("POST /webhooks/telemetry", registerLimiter(handler.NewTelemetryHandler(app.TelemetryService).Receive))
	}
	mux.HandleFunc("POST /webhooks/deploy/{token}", registerLimiter(hooks.Deploy))
	mux.HandleFunc("POST /webhooks/github", hooks.GitHubWebhook)
	mux.HandleFunc("GET "+protocol.SessionPath, agent.Session)
}

// apiRoutes are the JSON API: every route comes from api.Routes.
func apiRoutes(mux *http.ServeMux, app *app.App, clientIPs middleware.ClientIPResolver, rateLimiter, authEmailRateLimiter, deviceLimiter, emailChanges limiter, invitationLimiter *middleware.RateLimiter) {
	var apiBilling *api.BillingHandler
	if app.Cfg.Billing {
		apiBilling = api.NewBillingHandler(app.PaymentService, app.WorkspaceService)
	}
	apiHandlers := api.Handlers{
		Meta:        api.NewMetaHandler(app.Cfg.SupportEmail),
		Auth:        api.NewAuthHandler(app.AuthService, app.UserService, app.WorkspaceService),
		Account:     api.NewAccountHandler(app.AuthService, app.UserService, app.FileService),
		Sessions:    api.NewSessionsHandler(app.AuthService),
		Tokens:      api.NewAPITokensHandler(app.APITokenService),
		Billing:     apiBilling,
		Admin:       api.NewAdminHandler(app.AdminService),
		Workspaces:  api.NewWorkspaceHandler(app.WorkspaceService),
		Invitations: api.NewWorkspaceInvitationHandler(app.WorkspaceService),
		Servers:     api.NewServersHandler(app.ServerService),
		Apps:        api.NewAppsHandler(app.AppService),
		Version:     api.NewVersionHandler(app.UpdateService, app.ServerService),
		Alerts:      api.NewAlertsHandler(app.AlertService),
		Catalog:     api.NewCatalogHandler(app.CatalogService),
		Status:      api.NewStatusHandler(app.StatusService),
		Device:      api.NewDeviceHandler(app.DeviceLoginService, app.Cfg.AppURL),
	}
	apiMiddleware := api.Middleware{
		RequireAuth:           middleware.RequireAuthAPI,
		RequireOnboarded:      middleware.RequireOnboardedAPI,
		RateLimitAuth:         rateLimiter,
		RateLimitAuthEmail:    authEmailRateLimiter,
		RateLimitEmailChanges: emailChanges,
		RequireAdmin:          middleware.RequireAdminAPI,
		Workspace: func(next http.HandlerFunc) http.HandlerFunc {
			return middleware.RequireOnboardedAPI(middleware.WithWorkspaceAPI(app.WorkspaceService, app.SubscriptionService)(next))
		},
		RateLimitInvitations: middleware.RateLimitWorkspaceInvitations(invitationLimiter),
		InWorkspace: func(next http.HandlerFunc) http.HandlerFunc {
			return middleware.RequireOnboardedAPI(middleware.WithTokenWorkspace(app.WorkspaceService, app.SubscriptionService)(next))
		},
		RateLimitDevice:     deviceLimiter,
		RateLimitDevicePoll: middleware.RateLimitDevicePoll(clientIPs),
	}
	for _, route := range api.Routes(apiHandlers, apiMiddleware) {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
}

// mcpRoutes serve the MCP server at /mcp.
func mcpRoutes(mux *http.ServeMux, app *app.App, invitationLimiter *middleware.RateLimiter) {
	mcpServer := mcp.NewServer(mcp.Services{
		Users:         app.UserService,
		Workspaces:    app.WorkspaceService,
		Subscriptions: app.SubscriptionService,
		Invitations:   invitationLimiter,
		Servers:       app.ServerService,
		Apps:          app.AppService,
		Updates:       app.UpdateService,
		Alerts:        app.AlertService,
		Catalog:       app.CatalogService,
		Status:        app.StatusService,
		BillingOff:    !app.Cfg.Billing,
	})
	mux.Handle("/mcp", mcp.Handler(mcpServer))
}
