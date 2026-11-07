---
title: "Where things work"
description: "What the web app, the CLI, the terminal UI, the API and MCP can each do"
group: "Reference"
order: 1
---

Everything works in the web app, the [CLI](/docs/reference/cli), the [API](/docs/reference/api) and [MCP](/docs/guides/mcp); the [terminal UI](/docs/reference/tui) covers the daily work and opens the web for setup. ✓ means it works there end to end; otherwise the cell says why.

## Daily work

| Feature | Web | CLI | TUI | API | MCP |
| --- | --- | --- | --- | --- | --- |
| What needs you, what runs | ✓ | ✓ `status` | ✓ overview | ✓ | ✓ |
| Servers, live | ✓ | ✓ `servers` | ✓ | ✓ | ✓ |
| Server metrics, 1h to 30d | ✓ | ✓ `metrics --server` | ✓ | ✓ | ✓ |
| Update an agent | ✓ | ✓ `servers update` | ✓ `u` | ✓ | ✓ |
| Apps, live | ✓ | ✓ `apps` | ✓ | ✓ | ✓ |
| Pin an app | ✓ | ✓ `apps pin` | ✓ `*` | ✓ | ✓ `pin_app` |
| Deploy this directory | Your files are on your computer, not in the browser. | ✓ `deploy` | ✓ `D`, for an app that exists | ✓ | An agent has no directory to upload: use the CLI. |
| Deploy an image from CI | ✓ | ✓ `deploy --image` | ✓ `I` | ✓ | ✓ |
| Deploy a branch, tag or commit | ✓ | ✓ `deploy --ref` | ✓ `B` | ✓ | ✓ |
| Redeploy | ✓ | ✓ `redeploy` | ✓ `R` | ✓ | ✓ |
| Follow a deploy live | ✓ | ✓ | ✓ | ✓ event stream | Polls `get_deploy`: a tool answers once, it does not stream. |
| Deploys and their logs | ✓ | ✓ `deploys` | ✓ | ✓ | ✓ |
| Roll back | ✓ | ✓ `rollback` | ✓ `r`, `P` | ✓ | ✓ |
| Restart, stop, start | ✓ | ✓ | ✓ `T`, `S` | ✓ | ✓ |
| Container logs | ✓ | ✓ `logs` | ✓ live, with a filter | ✓ | ✓ |
| Run a command in a container | ✓ | ✓ `exec` | ✓ `X` | ✓ | ✓ |
| Env vars: see, set, remove | ✓ | ✓ `env` | ✓ | ✓ | ✓ |
| Env vars as one `.env` | ✓ | ✓ `env push`, `env pull` | ✓ in `$EDITOR` | ✓ | ✓ |
| Domains and their DNS | ✓ | ✓ `domains` | ✓ | ✓ | ✓ |
| Add, remove, canonical domain | ✓ | ✓ | ✓ | ✓ | ✓ |
| Password protection | ✓ | ✓ `protect` | ✓ `p` | ✓ | ✓ |
| Uptime and outages | ✓ | ✓ `uptime` | ✓ | ✓ | ✓ |
| Uptime check path, pause | ✓ | ✓ | ✓ | ✓ | ✓ |
| App metrics, 1h to 30d | ✓ | ✓ `metrics` | ✓ | ✓ | ✓ |
| Backups, the daily check and the moments to go back to | ✓ | ✓ `backups` | ✓ | ✓ | ✓ |
| Go back to a moment, to the minute | ✓ | ✓ `restore --to` | ✓ `enter`, `t` | ✓ | ✓ |
| Undo a restore | ✓ | ✓ `restore --to undo-…` | ✓ | ✓ | ✓ |
| Download a copy | ✓ | ✓ `backup download` | ✓ `c` | ✓ | ✓ |
| Update a catalog app | ✓ | ✓ `update` | ✓ `U` | ✓ | ✓ |
| Activity | ✓ | ✓ `activity` | ✓ | ✓ | ✓ |
| Activity by app, server or person | ✓ | ✓ `--app`, `--server`, `--user` | Shows all of it: filter in the CLI or the web. | ✓ | ✓ |
| Jump to anything | ✓ [palette](/docs/reference/palette), `⌘K` | Commands take the app by name. | ✓ palette: `space`, `ctrl+k` | Not a place to jump in. | Not a place to jump in. |

## Setup

The terminal UI opens these in your browser: `w` on the overview, or `web app` in the palette.

| Feature | Web | CLI | TUI | API | MCP |
| --- | --- | --- | --- | --- | --- |
| Add a server, install command | ✓ | ✓ `servers add` | Opens the web. | ✓ | ✓ |
| Rename, remove a server | ✓ | ✓ | Opens the web. | ✓ | ✓ |
| Tunnel, SSH on Tailscale, firewall | ✓ | ✓ `servers secure` | Shows what the agent set up; `W` changes it in the web. | ✓ | ✓ |
| Server rules | ✓ | ✓ `servers rules` | ✓ on the server | ✓ | ✓ |
| Cloudflare token, Tailscale key | ✓ | ✓ `integrations` | Opens the web. | ✓ | ✓ |
| Agent auto-update | ✓ | ✓ `servers auto-update` | Opens the web. | ✓ | ✓ |
| Uninstall the agent | Runs on the server. | ✓ `agent uninstall`, on the server | Runs on the server. | Runs on the server. | Runs on the server. |
| Create, remove an app | ✓ | ✓ `apps add`, `deploy` | Opens the web; `moin deploy` creates one too. | ✓ | ✓ |
| Catalog: browse and install | ✓ | ✓ `catalog` | A form wants a page: opens the web. | ✓ | ✓ |
| Connect GitHub | ✓ | Shows the link: GitHub needs a browser. | GitHub needs a browser. | Gives the link: GitHub needs a browser. | Gives the link: GitHub needs a browser. |
| Link or unlink a repo | ✓ | ✓ `link`, `unlink` | Opens the web. | ✓ | ✓ |
| Deploy hook for CI | ✓ | ✓ `hook` | Opens the web. | ✓ | ✓ |
| Registry logins | ✓ | ✓ `registry` | Opens the web. | ✓ | ✓ |
| Instant address: provider, open | ✓ | ✓ `instant` | Shows it; change it in the web. | ✓ | ✓ |
| Buckets | ✓ | ✓ `buckets` | Opens the web. | ✓ | ✓ |
| Move an app to another bucket | ✓ | ✓ `buckets use` | Opens the web. | ✓ | ✓ |
| Data: tables, rows, schema | ✓ | ✓ `db` | A grid of rows wants a page: opens the web. | ✓ | ✓ `list_tables`, rows through SQL |
| SQL, read and write | ✓ | ✓ `sql` | Opens the web. | ✓ | ✓ |
| Alert destinations | ✓ | ✓ `alerts add` | Opens the web. | ✓ | ✓ |
| Email alerts on or off | ✓ | ✓ `alerts on`, `alerts off` | Opens the web. | ✓ | ✓ |
| Team and invitations | ✓ | ✓ `team` | Opens the web. | ✓ | ✓ |
| Rename the workspace | ✓ | ✓ `team rename` | Opens the web. | ✓ | ✓ |
| Update a self-hosted control plane | ✓ | ✓ `update` | Opens the web. | ✓ | ✓ |

## Account, roles and money

These stay in the web on purpose: they move money or decide who is in charge, so they want your password, not a token. "With a session" means the endpoint answers a signed-in browser and refuses API tokens; the [API reference](/docs/reference/api) marks each one.

| Feature | Web | CLI | TUI | API | MCP |
| --- | --- | --- | --- | --- | --- |
| Sign up, sign in, passwords, email | ✓ | `login` signs this computer in; the rest is the web. | Signs in with the browser; the rest is the web. | ✓ with a session | Agents use a token, never your password. |
| Sessions | ✓ | A token can't see your sessions. | Web only. | ✓ with a session | Web only. |
| API tokens | ✓ | A token can't make tokens. | A token can't make tokens. | ✓ with a session | A token can't make tokens. |
| Name, picture | ✓ | Set it in the web. | Set it in the web. | ✓ | Set it in the web. |
| Change roles, remove someone, leave, delete the workspace | ✓ | Who is in charge is decided with a password. | Web only. | ✓ with a session | Web only. |
| Create a workspace, accept an invitation | ✓ | It starts with a signed-in browser. | Web only. | ✓ with a session | Web only. |
| Plan and trial | ✓ | Web only. | Web only. | ✓ | ✓ `get_billing` |
| Checkout, card, invoices | ✓ | Your card belongs to Stripe, not your terminal. | Your card belongs to Stripe, not your terminal. | ✓ with a session | Agents never move money. |
| Delete your account | ✓ | Web only, with your password. | Web only. | ✓ with a session | Agents never delete accounts. |
