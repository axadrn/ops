---
title: "Overview"
description: "Whether everything is OK, at a glance, and the search to jump anywhere"
group: "Every day"
order: 1
---

The Overview answers one question first: is everything OK? It says **All good.**, or lists the few things that need you, each with a link to where you fix it.

```surfaces
Web: **Overview**, the first page of a workspace, or `g` then `o`
CLI: moin status
TUI: the overview, where `moin` starts
API: GET /api/v1/status
MCP: get_status
```

`moin status` exits with `1` when something needs you and `0` when all is good, so a script or a cron job can check it:

```bash
moin status || echo "moin needs you"
```

## What needs you

The most urgent first:

- **A server offline**: its agent stopped answering. A server whose agent never connected is not on the list.
- **A failed deploy**: the newest deploy of an app failed, with the version that still runs.
- **Backups that stopped**, and **a daily check that failed** ([Backups](/docs/guides/backups)).
- **A domain down**, from the [uptime](/docs/guides/uptime) checks.
- **A disk over 90%** on a server ([Metrics](/docs/guides/metrics)).
- **An agent behind** the control plane; it updates by itself or with `moin servers update` ([Servers](/docs/guides/servers)).

Below that: apps live, servers online, backups checked in the last 24 hours, the [catalog](/docs/guides/catalog) apps with a newer version, and the newest [activity](/docs/guides/activity).

## Jump anywhere

`⌘K`, `ctrl+k`, `/` or the search in the bar opens the palette: every page, app and server, and actions such as **Deploy** and **Restart**. Type a few letters, then `enter`; `?` shows every key. [Palette and keys](/docs/reference/palette).

## The bar and Settings

The bar on every page holds the wordmark (back to the Overview), the search and your picture. Your picture opens **Settings**, a page: the workspace's **Team**, **GitHub**, **Alerts**, **Bucket**, **Activity** and **Billing**, your workspaces, and under **You** your **Account** with the tabs **Profile**, **Security** and **Tokens**.

## Good to know

- The API answers `ok`, the `issues` with their `kind`, `target`, `detail` and the `path` in the web where you fix them, the `updates` and the `counts`.
- The Overview stores nothing new; it reads what moin already knows.
