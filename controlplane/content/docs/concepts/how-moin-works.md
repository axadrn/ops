---
title: "How moin works"
description: "The pieces, the words, where your code goes and what a deploy does"
group: "Start"
order: 4
---

moin has two halves: the control plane is the place you sign in to, and the agent is the little helper on each of your servers that does the work. Your code and your backups never live with the control plane.

## The pieces

| Piece | What it is |
| --- | --- |
| **control plane** | The place you sign in to: the moin web app and API. It remembers your servers, apps and settings, and tells the agents what to do. We host it, or you [host it yourself](/docs/self-hosting). |
| **agent** | The little helper on each server. It calls the control plane and keeps the line open; nothing on your server waits for calls. |
| **`moin`** | The one program on your computer: `moin` opens the terminal UI, `moin <command>` is the CLI, `moin agent` is the agent. |

The web, the CLI, the terminal UI, the [API](/docs/reference/api) and AI agents through [MCP](/docs/guides/mcp) do the same work. Your account and the roles of your team change only on the web. [Where things work](/docs/reference/surfaces) lists what each one does.

```
workspace            your team, your bucket and your servers
├── server web-1     app shop, app blog
└── server web-2     app api

moin deploy   laptop ──▶ your bucket ──▶ web-1     (your code, gone after the deploy)
GitHub       GitHub ──────────────────▶ web-2     (no bucket needed)
backups      web-1, web-2 ──▶ your bucket          (every change for 7 days, only there)
```

## The words

The five that matter most are in [moin in five words](/docs/moin-in-five-words).

| Word | Means |
| --- | --- |
| **control plane** | the place you sign in to; every server reports to it |
| **workspace** | your own space: your apps, servers, bucket and team live in it. You get one when you sign up; on hosted moin you pay per workspace |
| **team** | the people in a workspace. Members deploy and fix things, admins also invite people, the owner also pays. [Team](/docs/guides/team#roles) |
| **server** | your computer on the internet: a Linux machine you rent, for example at Hetzner, with the agent on it |
| **agent** | the little helper on a server. It only calls out to the control plane; it never lets anyone in |
| **app** | the thing you made, such as your shop. It lives on one server |
| **service** | one part of an app, such as its website or its database: one container of its `compose.yml` |
| **deploy** | a new version of your app, put live: v1, v2, v3. Also the verb |
| **restart** | starts the live deploy again, so the changes that wait take effect |
| **domain** | the name people type to reach your app, such as `shop.example.com`, always with HTTPS. Every app also gets an [instant address](/docs/guides/domains#instant-address) |
| **env vars** | the settings and secrets of an app, such as a password it needs, kept encrypted |
| **bucket** | your own box on the internet, for example at Cloudflare R2: your backups and uploads go there, not to us |
| **backup** | moin writes down every change of your database in your bucket for 7 days, so you can go back to any minute; every day it checks that going back works |
| **restore** | going back: the database as it was at the minute you pick. A restore can be undone for 7 days |
| **health check** | after a deploy, moin checks that the app starts and stays up |
| **catalog** | the place with apps you install in one click, such as n8n or Umami |
| **API token** | a key for the CLI, scripts and AI agents, for one workspace |
| **MCP** | the way AI agents such as Claude or Cursor use moin; almost every action is a tool |
| **activity** | who did what in your workspace, and when |

Your app runs in containers on the server, with Docker. You do not need to know Docker: for a `Dockerfile`, a Go module or a repo without either, moin writes the compose file for you.

## Saving and restarting

Saving a setting never restarts your app: the change waits for you. Env vars, domains and the password wait; the app page says how many ("2 changes waiting"). A restart starts the live deploy again with all of them, without a build. A deploy or a start applies them too. A change to a variable used as `${NAME}` in `compose.yml` takes a deploy.

## Your data survives every deploy

What your app saves stays when you deploy a new version, as long as it lives in a mounted folder.

A volume is a folder on the server that a container sees, such as `./data` on the server shown as `/data` in the container.

- A folder your app mounts but the repo does not bring, like `./data`, moves to `/var/lib/moin/apps/<app>/volumes/` and stays there across deploys.
- Named volumes are kept as they are.
- Files the repo brings, like a config file, belong to the deploy.

Keep your database in a mounted folder. If your app keeps it somewhere other than `/data`, mount that folder in `compose.yml`, such as `./data:/app/data`; moin keeps it and backs up the SQLite files in it.

## Under the hood

### Where builds run

Builds run on **your** server.

- `moin deploy` packs the files git knows, including uncommitted changes and new files that are not ignored; outside a git repo, everything but `.git`. The archive goes straight into your bucket. The agent downloads it, deletes it from the bucket, and builds it.
- A deploy from [GitHub](/docs/guides/github) goes from GitHub straight to your server, with a short-lived token the control plane gets for it. No bucket needed.
- A deploy from [CI](/docs/guides/ci) builds the image elsewhere; the server only pulls it.
- Backups go from your server straight to your bucket.

The control plane only relays messages. It never stores or runs your code.

How `moin deploy` carries your code from your computer:

```text
1 computer  moin deploy packs your
     │      folder and puts it into
     ▼      your bucket
2 bucket    keeps it for a moment
     │
     ▼
3 server    the agent downloads it,
            checks it, deletes it
            from the bucket, builds it

moin only signs the links: your
code never passes through moin.
```

### How moin builds a repo

| The repo has | moin does |
| --- | --- |
| `compose.yml` | uses it (`compose.yaml` and `docker-compose.yml` work too) |
| only a `Dockerfile` | runs it as service `web`, mounts `./data` at `/data` |
| a Go module | also writes the Dockerfile: it builds `.`, `./cmd/server` or the only package under `cmd/`, and sets `PORT` and `DATA_DIR=/data` |
| none of these | builds it with [Railpack](https://railpack.com), the open source builder Railway made |

Railpack finds the language and its version from `package.json`, `requirements.txt`, `pyproject.toml`, `Gemfile`, `composer.json` and the like, and the start command from the `start` script, a `Procfile` or `main.py`. It covers Node, Bun, Python, Ruby, PHP, Deno, Rust, Elixir, Java, .NET, static sites and more. Your env vars reach the build as secrets that never land in the image or a log. The agent downloads Railpack once, checked against its published checksum, and builds with Docker's own BuildKit.

### What a deploy does

1. Downloads the code from your bucket, or the commit from GitHub, checks it and keeps it with the deploy.
2. Reads the compose file with `docker compose config`, together with the [x-moin block](/docs/guides/config).
3. Tags the built images per deploy, sets the env vars and `restart: unless-stopped`.
4. Builds, starts with `docker compose up --wait` and checks that the main service runs for 10 seconds without a restart, within 30 seconds.
5. Calls the health path, if x-moin sets one, until it answers below 500.
6. Reports the SQLite databases on the app's volumes and the Postgres services in its compose file, for the [data browser](/docs/guides/data) and the [backups](/docs/guides/backups).
7. Reports what it detected: kind, service, port, databases and the x-moin block. The app page, `moin deploy`, `moin apps <app>`, the TUI, the API and `get_app` show it.

If the start or the health check fails, the last good deploy starts again and the new one is marked **failed**. Its log ends with the last lines of the service that stopped.

The server keeps the deploys of the last 5 builds (of code or of an image) with their images, and every restart, start, domain change and rollback since the oldest of them. A rollback can go to any of those.

### How moin reads your database

The [data browser](/docs/guides/data) reads the live database on the server; nothing is copied to the control plane.

- moin finds every SQLite file on a volume of the live deploy, up to three folders deep, and every service that runs Postgres, named `postgres-<service>`.
- For Postgres it runs `psql` inside your Postgres container over its local socket, so no password leaves the container. Every value you type travels as a psql variable, never as SQL.
- A read opens the database read-only; on Postgres it runs in a `READ ONLY` transaction and as one statement. A statement that would change data fails, so a typo cannot delete rows. Every change runs in one transaction.
- moin changes data, never the schema. Tables, columns and indexes change through migrations in your repo, which your app runs when it starts, so the database and the code stay the same. moin names the migration tool and its newest version when it finds a `goose_db_version`, `schema_migrations`, `_prisma_migrations` or `__drizzle_migrations` table.
- WAL mode (`PRAGMA journal_mode=WAL`) lets your app and moin read and write SQLite at the same time.
- The agent runs as root; after a change it gives the database files back to their owner.

### How a command runs

[Run a command](/docs/guides/exec) uses the connection the agent already has: no SSH and no key on your laptop. The agent runs `docker compose exec` in the live deploy, without a shell and without a terminal, and streams what it prints back.

### The catalog

Each [catalog](/docs/guides/catalog) app is a folder in the moin repo, `controlplane/content/catalog/<app>`: an `app.md` with what it is, and a `compose.yml` that runs the image with `${VERSION}` as its tag and every secret as `${NAME}`. moin reads the image registry of each app once an hour. A self-hosted control plane ships the same catalog and asks the registries itself; nothing goes through us.