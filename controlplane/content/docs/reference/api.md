---
title: "API"
description: "The JSON API under /api/v1: every endpoint, by area"
group: "Reference"
order: 5
---

The CLI, the terminal UI and your own scripts use this API; its OpenAPI 3.1 document is at [`/api/v1/openapi.yaml`](/api/v1/openapi.yaml).

## Basics

| | |
| --- | --- |
| Token | a personal API token as `Authorization: Bearer pat_…`. It belongs to one workspace; every endpoint below works in it |
| Token column | **read**: a read token is enough. **write**: needs a token with read and write. **session**: a signed-in browser only, never a token. **none**: needs no login |
| Session | sign-in and two-step flows are under `/api/v1/auth`; `GET /api/v1/csrf` gives the CSRF token a session sends with every change; the `Moin-Workspace` header names the workspace |
| Errors | `{"error":{"code","message","fields"}}`, with stable codes such as `server_offline`, `deploy_running` and `no_rollback` |
| Streams | `…/events` endpoints are server-sent events. A user holds at most 20 streams at once; one more answers `429` `rate_limited`, closing one frees its place |

Session-only endpoints keep a leaked token from making new tokens, changing roles, removing people or deleting a workspace. [Where things work](/docs/reference/surfaces) shows the same per feature.

## Control plane

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/meta` | none | the name, version and support address of this control plane |
| `GET /api/v1/openapi.yaml` | none | this API as OpenAPI 3.1 |
| `GET /api/v1/csrf` | read | the CSRF token of a session |
| `GET /api/v1/version` | read | the version and, on a self-hosted control plane, the newest version, its "In short" line, the notes and how the last update went |
| `POST /api/v1/version/update` | write | updates a self-hosted control plane to the newest version (admins in `ADMIN_EMAILS`); follow it with `GET /api/v1/version` |
| `GET /api/v1/status` | read | whether everything is OK: `ok`, the `issues` that need you with the `path` to fix them, catalog `updates` and `counts` ([Overview](/docs/guides/overview)) |

## Sign in

| Endpoint | Token | What it does |
| --- | --- | --- |
| `POST /api/v1/auth/device` | none | starts `moin login` through the browser (RFC 8628) with `{"hostname"}` |
| `POST /api/v1/auth/device/token` | none | polls it with `{"device_code"}` every `interval` seconds until it answers `access_token`. See [Log in the CLI](/docs/guides/account#log-in-the-cli) |
| `POST /api/v1/auth/password` | session | signs in with email and password |
| `POST /api/v1/auth/magic-link` · `POST /auth/magic-link/verify` | session | sends a sign-in link; signs in with it |
| `POST /api/v1/auth/forgot-password` · `POST /auth/forgot-password/verify` | session | emails a reset link; signs in with it, which removes the password, signs out every other session and revokes every API token. Set a new one with `POST /account/password/set` |
| `POST /api/v1/auth/email-change/verify` | session | confirms a new email |
| `POST /api/v1/auth/onboarding` | session | the name of a new account and its first workspace, or none with an `invitation_token`; every other endpoint answers `onboarding_required` until it is done |
| `POST /api/v1/auth/logout` | session | signs this browser out |

## Account

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/account` | read | the signed-in account, with `email_alerts` |
| `PATCH /api/v1/account/name` | write | your name |
| `POST` · `DELETE /api/v1/account/avatar` | write | uploads or removes your picture |
| `PUT /api/v1/account/email-alerts` | write | your [email alerts](/docs/guides/alerts) on or off: `{"on": false}` |
| `PATCH /api/v1/account/email` | session | changes your email |
| `POST /api/v1/account/password` | session | changes your password |
| `POST /api/v1/account/password/set` · `DELETE /account/password` | session | sets a first password; removes it |
| `DELETE /api/v1/account` | session | deletes your account |
| `GET` · `POST /api/v1/account/tokens` · `DELETE /account/tokens/{id}` | session | personal API tokens and the computers logged in with `moin login`, `kind` `personal` or `device`; makes one; revokes one |
| `GET /api/v1/account/sessions` · `DELETE /account/sessions/{id}` · `DELETE /account/sessions` | session | signed-in browsers; signs one, or all others, out |

## Workspace and team

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/workspaces` | read | the workspaces you are in |
| `POST /api/v1/workspaces` | session | makes a workspace |
| `GET /api/v1/workspaces/{slug}` | read | the team: who is in it, their roles, open invitations |
| `PATCH /api/v1/workspaces/{slug}/name` | write | renames the workspace (admin) |
| `DELETE /api/v1/workspaces/{slug}` | session | deletes the workspace (owner) |
| `POST /api/v1/workspaces/{slug}/invitations` | write | invites someone by email (admin) |
| `POST /api/v1/workspaces/{slug}/invitations/{id}/resend` · `DELETE /invitations/{id}` | write | sends an invitation again; revokes it (admin) |
| `PATCH /api/v1/workspaces/{slug}/members/{id}/role` | session | changes a role (admin; only an owner makes or changes an owner) |
| `DELETE /api/v1/workspaces/{slug}/members/{id}` | session | removes someone from the team (admin; only an owner removes an owner) |
| `POST /api/v1/workspaces/{slug}/leave` | session | leaves the workspace |
| `GET /api/v1/invitations/{token}` | read | an invitation, from its email link |
| `POST /api/v1/invitations/{token}/accept` | session | accepts it |
| `GET /api/v1/activity` | read | who did what, newest first, with `next_cursor`; `app`, `server` and `user` filter it |
| `GET /api/v1/workspaces/{slug}/activity` | read | the same, for the workspace in the path |
| `GET /api/v1/workspaces/{slug}/billing` | read | the plan and the subscription, where billing is on |
| `POST /api/v1/workspaces/{slug}/billing/checkout` · `GET /billing/manage` | session | a checkout link; the billing portal (owner) |

## Servers

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/servers` | read | servers, with their `security` (what the agent set up) and `rules` from `/etc/moin/rules` |
| `GET /api/v1/servers/events` | read | the server list as server-sent events |
| `POST /api/v1/servers` | write | adds a server and answers its install command once; its token connects one agent within an hour. An empty name gets a generated one |
| `POST /api/v1/servers/{id}/install` | write | a new one-time install command; the old one stops working |
| `PATCH /api/v1/servers/{id}` | write | renames a server: `{"name": "…"}` |
| `DELETE /api/v1/servers/{id}` | write | removes a server; answers the `uninstall_command` that removes its agent (empty when no agent ever connected) and the `apps` that went with an offline server |
| `POST /api/v1/servers/{id}/update` | write | updates the agent to the control plane's version |
| `GET` · `PUT /api/v1/agent-updates` | read · write | whether agents update by themselves, `{"auto": true}`; on by default |
| `GET /api/v1/servers/{id}/metrics?range=1h` | read | CPU, memory, disk, network and load now and over `1h`, `24h`, `7d` or `30d`, and its apps now, the most memory first |
| `PUT /api/v1/servers/{id}/security` | write | Cloudflare Tunnel, SSH on Tailscale, firewall ([Security](/docs/guides/security)) |
| `GET` · `PUT /api/v1/integrations` | read · write | the Cloudflare API token and the Tailscale auth key; an empty key removes it |

## Apps

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/apps` · `POST /api/v1/apps` | read · write | lists apps; adds one |
| `GET /api/v1/apps/{app}` · `DELETE /api/v1/apps/{app}` | read · write | one app, with `changes_waiting`: the changes the next restart applies; removes it, its data stays on the server |
| `GET /api/v1/apps/events` | read | the app list as server-sent events |
| `PUT /api/v1/apps/{app}/pin` | write | `{"pinned": true}` keeps the app above the others that run fine; `false` unpins it |
| `POST /api/v1/apps/{app}/restart` | write | starts the live deploy again with the current settings |
| `POST /api/v1/apps/{app}/stop` · `POST /apps/{app}/start` | write | takes the app offline, its data stays; runs it again |
| `GET /api/v1/apps/{app}/logs` | read | container logs |
| `POST /api/v1/apps/{app}/exec` | write | [runs a command](/docs/guides/exec), answered as server-sent events: `output` pieces, then `done` with `exit_code`, `timed_out` and `truncated`. Takes `{"command": [...]}` or `{"line": "..."}`, `service` and `timeout` in seconds (owner or admin) |
| `GET /api/v1/apps/{app}/metrics?range=1h` | read | CPU, memory and network now and over `1h`, `24h`, `7d` or `30d`, with the deploys that went live |

## Deploys

| Endpoint | Token | What it does |
| --- | --- | --- |
| `POST /api/v1/apps/{app}/uploads` | write | a link that takes the source straight into your bucket |
| `POST /api/v1/apps/{app}/deploys` | write | deploys an upload `{"upload": "…", "sha256": "…"}`, an image built in CI `{"image": "…"}`, or a GitHub ref `{"ref": "main"}` |
| `GET /api/v1/apps/{app}/deploys/{number}` | read | one deploy with its log |
| `GET /api/v1/apps/{app}/deploys/{number}/events` | read | one deploy as server-sent events |
| `POST /api/v1/apps/{app}/redeploy` | write | builds the newest source again, or runs the newest image again |
| `POST /api/v1/apps/{app}/rollback` | write | starts an earlier deploy again |
| `POST /api/v1/apps/{app}/hook` · `DELETE /apps/{app}/hook` | write | a new deploy hook URL, shown once; turns the hook off |
| `POST /webhooks/deploy/{token}` | the hook | what CI calls, with `?image=` or the body `{"image": "…"}` |
| `GET /api/v1/registries` | read | registry logins for private images |
| `PUT` · `DELETE /api/v1/registries/{host}` | write | saves a login for a host like `ghcr.io`, `{"username", "password"}`, stored encrypted; removes it |
| `GET /api/v1/github` · `GET /api/v1/github/repos` | read | GitHub installations with their `settings_url` and the install link; the repos moin may read |
| `GET /api/v1/github/repos/{owner}/{repo}/branches` | read | the branches of a repo, the default first |
| `PUT` · `DELETE /api/v1/apps/{app}/repo` | write | links a GitHub repo and branch; unlinks it |

## Catalog

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/catalog` · `GET /api/v1/catalog/{slug}` | read | the [catalog](/docs/guides/catalog) with each app's newest version; one app with every version |
| `POST /api/v1/catalog/{slug}/install` | write | installs a catalog app: `{"server": "web-1", "version": "3.4.1", "name": "stats", "domain": "stats.example.com"}`; answers the app and its deploy |
| `POST /api/v1/apps/{app}/update` | write | sets a catalog app to a version, `{"version": "3.4.1"}`, the newest when empty, and deploys it |

## Domains

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET` · `PUT /api/v1/apps/{app}/domains` | read · write | the domains and canonical; with `service`, those of another service. `GET` answers DNS checks with the server's `ipv4`, `ipv6` and the record to set as `advice` |
| `GET` · `PUT /api/v1/apps/{app}/instant` | read · write | the instant address, its provider and password (`?show=true` with a write token); picks the provider or `off`, and `open` |
| `PUT` · `DELETE /api/v1/apps/{app}/protection` | write | puts a password in front of the app; takes it away |
| `GET /api/v1/apps/{app}/uptime` | read | uptime per domain: state, 24 hours, 7 and 30 days, the newest check, the last outages |
| `PUT /api/v1/apps/{app}/uptime/{domain}` | write | the path of a domain's check, `{"path": "/health"}`, or `{"paused": true}` |

## Env vars

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/apps/{app}/env` | write | the env vars, values included |
| `PATCH /api/v1/apps/{app}/env` | write | sets and removes some; changes take effect with the next deploy, a rollback keeps the env of its deploy |
| `PUT /api/v1/apps/{app}/env` | write | replaces them all at once; answers the keys `added`, `changed` and `removed` |

## Data

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/apps/{app}/databases` | read | the databases of the live deploy: SQLite files and Postgres services |
| `GET /api/v1/apps/{app}/databases/tables` | read | tables, columns, row counts, schema and migration |
| `GET` · `POST` · `PATCH` · `DELETE /api/v1/apps/{app}/databases/rows` | write | browses, adds, edits and deletes rows |
| `POST /api/v1/apps/{app}/databases/query` | write | runs SQL; `write: true` for changes |

## Backups and bucket

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/apps/{app}/backups` | read | how each database is backed up, `problem` in one sentence when it does not work, and the `moments` to go back to |
| `POST /api/v1/apps/{app}/restores` | write | goes back to a moment, `{"moment": "before-v12"}` (its id or label), or a time, `{"at": "2026-11-07T14:01:00Z"}`; `database` when the app has several. `202` with the restore; `409` `restore_running` while one runs |
| `GET /api/v1/apps/{app}/restores/{restore}` | read | how a restore stands: `running`, `unknown` while its server is offline, then `done` or `failed` with `error`; kept a week. `POST /api/v1/apps/{app}/restore` is gone |
| `POST /api/v1/apps/{app}/backups/downloads` | write | starts a full copy of a database in the app's own bucket, `202` with its `id` |
| `GET /api/v1/apps/{app}/backups/downloads/{download}` | write | how the copy stands; `link`, valid 15 minutes, once `state` is `ready` |
| `GET /api/v1/buckets` · `POST /api/v1/buckets` | read · write | the workspace's buckets, the default first, with the apps that use each; adds one |
| `GET` · `PUT` · `DELETE /api/v1/buckets/{name}` | read · write | one bucket; changes it (an empty `secret_key` keeps the stored one, `default: true` makes it the default); removes it, `409` `bucket_in_use` while apps use it or left it |
| `PUT /api/v1/apps/{app}/bucket` | write | the app's bucket, `{"bucket": "client-a"}`, or `""` for the default; the history starts again there |

## Alerts

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/alert-destinations` | read | where [alerts](/docs/guides/alerts#slack-discord-telegram-and-webhooks) go besides email |
| `POST /api/v1/alert-destinations` | write | adds Slack, Discord, Telegram or a webhook: `{"kind", "name", "url"}`, or `bot_token` and `chat_id` for Telegram, and `events`; a webhook answers its `signing_secret` once (owner or admin) |
| `DELETE /api/v1/alert-destinations/{name}` | write | removes one (owner or admin) |
| `POST /api/v1/alert-destinations/{name}/test` | write | sends a test alert; `424` `alert_send_failed` with the reason when the destination refuses it (owner or admin) |

## Admin

For the admins of the hosted control plane, behind `/admin`.

| Endpoint | Token | What it does |
| --- | --- | --- |
| `GET /api/v1/admin/users` | session | the admin overview: metrics, a page of users (`query` filters by email or name), the latest activity |
| `POST /api/v1/admin/users/{id}/sessions/revoke` | session | signs a user out everywhere |
| `GET /api/v1/admin/old-domain` | session | what still arrives on the old domains (`OLD_URLS`) over the last 14 days, by day, host and kind; `quiet` after 14 quiet days |
