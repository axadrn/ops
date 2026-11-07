# AGENTS.md

## Custom rules

moin stands on its own: it started from a starter template, but it follows only its own rules now and never keeps code just to stay easy to merge with one.

- This is the moin control plane. How moin works is in `content/docs`, the only source of truth.
- The agent, CLI, TUI and the protocol package live at the root of this repo, one Go module for all. The repo is public: never commit secrets, `.env` files or anything private.
- Every feature works in the web UI, the JSON API (with `openapi.yaml`), the CLI and an MCP tool; it is done when each one works end to end. The TUI covers the daily work and opens the web for setup. `content/docs/reference/surfaces.md` ("Where things work") lists what each surface does; update it with every feature.
- Everything belongs to a workspace: moin services take the workspace ID to read and a `service.Actor` (workspace, user, how it came in) to change. Every change records itself in the activity with `record` (`internal/service/activity.go`), never with secret values or user data (see Activity in `DESIGN.md`), so the team sees who did what on the web, in `moin activity`, the TUI, the API and MCP.
- Every feature is in the docs under `content/docs`. Change the docs in the same commit as the feature: the guide, the CLI and TUI reference, the API table and the MCP tool table. The docs are the only source of truth; READMEs stay short and point to them.

## Stack

- Go 1.26 with the standard library `net/http` router.
- templ, htmx 4.0, vanilla JavaScript, and Tailwind CSS.
- sqlx with raw SQL and SQLite.
- Cloudflare Email Sending for transactional email. Without credentials in development, emails go to the log.
- Stripe for subscription billing.
- S3-compatible object storage.

## House rules

- Keep handlers thin. Put business rules in services and SQL in repositories.
- Keep product access provider-agnostic. Stripe or Polar updates local subscription state. `internal/access` decides what that state unlocks.
- Templates may mirror access for presentation. Services must enforce every protected operation with explicit early returns.
- Enforce resource, seat, and quota increments in the same database transaction as the protected write.
- Use `$1, $2` SQL placeholders. sqlx rebinds them for the selected database.
- Prefer explicit constructor injection and direct control flow.
- Add dependencies only when the standard library does not solve the problem well.
- Keep secrets out of templates. Request context contains only `config.RenderConfig`, the small public value templates need.
- Keep brand identity in `internal/brand` and architecture choices in Go source. Env is only for deployment coordinates, external IDs, operator switches, and secrets.
- Never edit generated `*_templ.go` files. Edit `.templ` files and let `task dev` regenerate them.

## Architecture

```text
HTTP request
  -> Handler
  -> Service
  -> Repository
  -> Database
```

Application wiring lives in `internal/app/app.go`. Route registration and middleware order live in `internal/routes/routes.go`.
Full configuration stays in startup wiring. Do not put secrets, database settings, operator allowlists, or trusted proxy ranges in request context.
Tenant-owned data is scoped by `workspace_id`. Load workspace context with `middleware.WithWorkspace` and enforce capabilities with `WorkspaceMember.Can`.

## JSON API

- JSON handlers live in `internal/api` and call the same services as the HTML handlers. Never duplicate a rule or a permission check there.
- Every route under `/api/v1` is listed once in `api.Routes` in `internal/api/routes.go`. `internal/api/openapi.yaml` documents each one and is served at `GET /api/v1/openapi.yaml`. `internal/api/openapi_test.go` fails when the two drift.
- Clients sign in with an `/api/v1/auth` endpoint and send the returned token as `Authorization: Bearer <token>`. Cookie requests that change state send `X-CSRF-Token` with the token from `GET /api/v1/csrf`.
- Personal API tokens (`pat_...`) let scripts and agents act for a user. Each route in `api.Routes` declares `TokenRead`, `TokenWrite` or the default `TokenDenied`. Open a new route to tokens only when an agent should reach it, and update its `apiTokenAuth` entry in `openapi.yaml`. A token is bound to one workspace; `WithWorkspaceAPI` answers 404 for every other workspace.
- Every non-2xx answer is the envelope `{"error":{"code","message","fields"}}`. Codes are stable lowercase snake case. `fields` appears only for `validation_failed`.
- `CORS_ALLOWED_ORIGINS` lists the browser origins that may call `/api/` with credentials. Origins match exactly, without wildcards.
- Magic link, password reset, and email change emails link to the HTML routes under `APP_URL/auth/...`. An API client that receives the token another way calls the matching `/api/v1/auth/.../verify` endpoint.

## MCP server

- `internal/mcp` serves an MCP server at `/mcp`: Streamable HTTP, stateless, `POST` only. It is a third transport next to the HTML handlers and the JSON API. Tools call the same services and never repeat a rule or a permission check.
- Agents authenticate with a personal API token as `Authorization: Bearer pat_...`. `/mcp` answers 401 `unauthenticated` to sessions, cookies, and unknown, revoked or expired tokens.
- Built-in tools: `whoami`, `list_workspaces`, `get_workspace`, `list_activity`, `get_billing`, `invite_member`, `rename_workspace`. `registerTools` in `internal/mcp/tools.go` registers them in one place.
- Wrap a tool body in `mcp.Read` when it only reads and in `mcp.Write` when it changes data. A read-only token gets `insufficient_scope` from a `Write` tool. Workspace tools take an optional `workspace` slug, default to the token's workspace, and answer `not_found` for every other workspace.
- Failed calls return the JSON API error envelope as the tool result. Return service errors unchanged; `api.ErrorFor` maps them to the same codes as the API. Use `mcp.InvalidField` for a `validation_failed` field.
- Tools are a curated list for agents, not a copy of the JSON API. Never add tools for checkout, billing management, account or workspace deletion, role changes, member removal, admin, impersonation, passwords, or email.
- `mcp.Services` in `internal/mcp/server.go` lists the services tools may call. Add your service there and pass it where `internal/routes/routes.go` calls `mcp.NewServer`.
- Raise `mcp.Version` when the tool list or a tool's schema changes.

Add a tool next to its service. This example is tested in `internal/mcp/example_test.go`. Inside `internal/mcp`, drop the `mcp.` prefixes and call it from `registerTools`:

```go
type nameInput struct {
	Name string `json:"name" jsonschema:"the new display name"`
}

func registerNameTool(server *mcpsdk.Server, services mcp.Services) {
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        "set_display_name",
		Description: "Changes the display name of the user this token acts for. Needs a token with read and write access.",
	}, mcp.Write(func(ctx context.Context, caller mcp.Caller, input nameInput) (api.UserResponse, error) {
		err := validation.ValidateName(input.Name)
		if err != nil {
			return api.UserResponse{}, mcp.InvalidField("name", api.FieldMessage(err))
		}
		user, err := services.Users.UpdateName(caller.User.ID, input.Name)
		if err != nil {
			return api.UserResponse{}, err
		}
		return api.UserResponse{User: api.NewUser(user)}, nil
	}))
}
```

The description tells the agent what the tool returns or changes and which scope it needs. Add a test with the in-memory transport like `TestAddedToolRunsWithTheTokenScopes`.

Client setup, with a token from Settings, Tokens and the public URL of this server:

- Claude Code: `claude mcp add --transport http app https://example.com/mcp --header "Authorization: Bearer pat_..."`
- Claude Desktop and claude.ai: Customize, Connectors, Add custom connector, the `/mcp` URL, Authentication "No sign in", and the request header `Authorization: Bearer pat_...`. Claude connects from Anthropic's cloud, so the URL must be public. Request headers on custom connectors are in beta and not available to every organization yet. For a local server, or when your organization has no Request headers field, use the local `mcp-remote` bridge: run `npx mcp-remote http://localhost:8090/mcp --header Authorization:${AUTH_HEADER}` from `claude_desktop_config.json` with `AUTH_HEADER` set to `Bearer pat_...`.
- Cursor: `.cursor/mcp.json` with `"mcpServers": {"app": {"url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${env:API_TOKEN}"}}}`.
- VS Code: `.vscode/mcp.json` with `"servers": {"app": {"type": "http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${input:api-token}"}}}` and a `promptString` input `api-token` with `"password": true`.

## Commands

- `task dev` runs templ, the application, and Tailwind watchers.
- `task dev` selects available app and templ proxy ports automatically. Pass `APP_PORT`, `DEV_PORT`, or `OPEN_BROWSER=true` to override its development defaults.
- No foreign UI library, not even for icons or charts: every part comes from our own kit (`internal/ui/kit`, gallery at `/app/<workspace>/dev/kit` in development). Icons are templ components in `kit/icons.templ`, drawn on one 24 grid with `currentColor`; only brand marks keep their official shapes.
- `go test ./...` runs the test suite.
- `go test -race ./...` checks concurrency behavior.
- `go vet ./...` and `go build ./...` run static and build checks.

## Code map

- `cmd/server` contains application startup and graceful shutdown.
- `internal/config` owns typed environment configuration.
- `internal/handler` parses requests and renders responses.
- `internal/handler/htmx.go` contains the selected frontend's native response behavior.
- `internal/api` parses JSON requests and writes JSON responses.
- `internal/mcp` serves the MCP tools for agents.
- `internal/service` owns business rules.
- `internal/repository` owns raw SQL.
- `internal/ui` has three flat folders, each answering one question. `kit/`: what pages are built from (rows, buttons, fields, dialog, icons, chart, wordmark); it knows looks, not moin's data. `layouts/`: what surrounds every page (the HTML frame, the app bar and palette, the site's navbar, footer and search). `pages/`: what one page shows, one file per page or topic. A part goes to `kit/` once two pages need it, to `layouts/` if it surrounds every page, else into its page. No fourth folder without a good reason, no subfolders.
- `internal/db/migrations` contains the Goose schema.
- `internal/service/payment` contains the selected billing adapter.
- `internal/access` contains signup policy, plans, features, limits, quotas, and lifecycle rules.
- `internal/service/mailer` contains the selected mail adapter.
- `internal/service/workspace.go` owns workspaces, roles, invitations, seats, and the activity.
- `internal/storage` and `internal/service/file.go` own file storage.
- `content/docs` and `internal/service/docs.go` contain the Docs module.

## Included product surface
This project includes authentication with email links and passwords, email verification, password recovery, revocable database sessions, account settings, legal pages, SEO basics, security headers, structured logging, subscription billing, provider-agnostic product access, and a global operator Admin panel.
OAuth provider: GitHub.
It also includes workspaces with Owner, Admin, and Member roles, invitations, seat limits, workspace billing, and the activity.
