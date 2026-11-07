---
title: "Activity"
description: "Who changed what, to the second, and what moin never writes down"
group: "Team"
order: 2
---

The activity is moin's notebook of who did what, and when: every change in a workspace, who made it, to the second, and how it came in. Your secrets and your data never go in.

```surfaces
Web: **Settings**, **Activity**, or `g` then `y`
CLI: moin activity
TUI: `a` on the overview, or `activity` in the palette (`space`)
API: GET /api/v1/activity
MCP: list_activity
```

| When (UTC) | Who | Via | What |
| --- | --- | --- | --- |
| 2026-11-02 14:03:07 | ada@example.com | CLI, token ci | deployed shop v42 from main at 3f2a9c1 |
| 2026-11-02 14:03:51 | moin | moin | made v42 of shop live after 44s |
| 2026-11-02 14:10:12 | ben@example.com | Web | set STRIPE_KEY in the env of shop |
| 2026-11-02 14:12:30 | ben@example.com | Web | edited status of row 1182 in orders of shop |
| 2026-11-02 14:25:44 | ada@example.com | MCP, token claude | ran UPDATE on orders in shop, 3 rows changed |
| 2026-11-03 03:00:18 | moin | moin | checked that app.db of shop can go back |

## Find one change

Narrow it to an app, a server or a person. A server shows its own changes and those of its apps.

```surfaces
Web: **Settings**, **Activity**, pick an app, a server or a person; the list follows
CLI: moin activity --app <app-name> --server <server-name> --user <email>
TUI: not here: it shows all of it; filter in the CLI or the web
API: GET /api/v1/activity?app=<app-name>&server=<server-name>&user=<email>
MCP: list_activity
```

## Go further back

Everything stays; page back as far as you like.

```surfaces
Web: **Settings**, **Activity**, **Show older** at the end
CLI: moin activity --cursor <cursor>
TUI: scroll down: older events load at the end
API: GET /api/v1/activity?cursor=<next_cursor>
MCP: list_activity
```

The CLI prints the next `--cursor` at the end. In the API and MCP, send `next_cursor` back as `cursor`, and filter with `app`, `server` and `user`. `moin activity --limit 200` shows up to 200 at once.

## What it records

- **When**: the exact time, stored in UTC. The web shows UTC with seconds, the CLI and the TUI your local time with its zone, the API and MCP RFC 3339.
- **Who**: the person by email, the API token by name, a deploy hook, a GitHub push with its branch and commit, or moin itself: a deploy that ended, a server that connected or went offline, a domain that went down or came back, a restore check.
- **How**: the web, the CLI, the TUI, the API, an agent over MCP, a deploy hook or GitHub.
- **Where from**: the IP address and browser or program. Only admins and owners see them.
- **What**: the action and what it touched, with the settings before and after: names, on and off, domains, branches, roles, counts, durations, versions, exit codes and error messages.

It covers deploys and how they ended, redeploys, restarts, starts, stops, rollbacks, env vars, domains, passwords in front of apps, deploy hooks, GitHub links, registry logins, buckets and the bucket of each app, backups, restores, restore checks, SQL writes, row edits, servers and their security, agent updates, uptime checks and outages, servers that went offline, alert destinations, their tests and failures, API tokens, plan changes and every change of the team.

## What it never records

- Env var values: only their names.
- Secrets, tokens and passwords.
- Your SQL: a write keeps the kind of statement, the tables and the row count, such as `UPDATE on orders, 3 rows changed`.
- Rows and cell values: a row edit keeps the table, the row and the column.
- File contents and the log lines of your apps.

## Good to know

- The whole team sees the activity; only admins and owners see IP addresses and browsers.
- Someone who deletes their account keeps their email on their old changes.
- Deleting a workspace deletes its activity.
- Who may do what: [Roles](/docs/guides/team#roles).
