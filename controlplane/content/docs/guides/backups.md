---
title: "Backups"
description: "Go back to any minute of the last 7 days, undo it, and download a copy"
group: "Servers and data"
order: 3
---

moin writes down every change of your database for 7 days in your bucket. Every day it checks that going back works. Go back to any minute; a restore can be undone for 7 days.

It backs up every SQLite and Postgres database of your apps once the workspace has a [bucket](/docs/guides/bucket#add-it), with nothing to set up. Your data goes to your own bucket only, never through moin. [How backups work](/docs/concepts/backups) says how.

## See your backups

One line says how each database stands: "Backed up · checked 2h ago", or in red why it does not work, like "The bucket can't be reached.", with the details under it. Then the moments you can go back to.

```surfaces
Web: the app, **Data**, a database
CLI: moin backups shop
TUI: on the app, `b`
API: GET /api/v1/apps/{app}/backups
MCP: get_backups
```

The moments, only those inside the last 7 days:

- **1 minute ago**, **15 minutes ago**, **1 hour ago**, **Yesterday this time**
- **Before v12**: the moment before a deploy started its new version
- **Before the update to 3.4.1**: before a [catalog](/docs/guides/catalog) update
- **Before your SQL change**, **Before your edit in orders**: before a [data](/docs/guides/data) edit in moin
- **Before the restore at 14:02**: the copy moin took before a restore; going back to it undoes the restore. One from another day says it: **Before the restore on Nov 6 at 14:02**

## Go back

Pick a moment, then **Restore**, and type the app's name. The app stops writing, moin keeps a copy of the database as it is, puts it back to that moment and starts the app again, also when something fails on the way. A big database takes a while: the restore runs on its own, the page says "Restoring" and then how it ended, and the CLI and the TUI wait for it.

```surfaces
Web: the app, **Data**, a database: pick a moment under **Go back to**, **Restore**, then type the app name
CLI: moin restore shop --to before-v12
TUI: on the app, `b`, pick a moment, `enter`, then type the app name
API: POST /api/v1/apps/{app}/restores
MCP: restore_app
```

`--to` takes a moment from `moin backups`, by its ID or its label (`1h`, `before-v12`, `"Before your SQL change"`), or a time in UTC to the minute: `14:01` for today, `"2026-11-07 14:01"` or an RFC 3339 time. In the TUI `t` asks for a time. The API and MCP take `moment` or `at`; the API answers `202` with the restore to follow at `GET /api/v1/apps/{app}/restores/{restore}`, and `restore_app` answers its state, then takes its `id` to tell how it stands. Two moments with the same label, like two SQL changes, need the moment's ID. An app with more than one database: name it with `--db /data/app.db` or `--db postgres-db`.

## Undo a restore

Every restore keeps the database as it was right before, for 7 days. Go back to **Before the restore at 14:02** to undo it, the same way:

```surfaces
Web: the app, **Data**, a database: **Before the restore at 14:02**, **Restore**
CLI: moin restore shop --to undo-20261107T140200Z
TUI: on the app, `b`, pick **Before the restore at …**, `enter`
API: POST /api/v1/apps/{app}/restores
MCP: restore_app
```

An undo is a restore too: it keeps a copy first, so you can undo the undo.

## Download a copy

The full database as one file, to keep or to open anywhere. Your server puts it in your bucket's `downloads/` folder, and you get a link that works for 15 minutes; the copy waits there for a day. A big database takes a while: the page shows "Making a copy" until it is ready.

```surfaces
Web: the app, **Data**, a database, **Download a copy**
CLI: moin backup download shop
TUI: on the app, `b`, then `c`
API: POST /api/v1/apps/{app}/backups/downloads
MCP: download_backup
```

The CLI saves it in the current folder; `--link` prints the link instead. A SQLite copy is gzipped: `gunzip` it and open it with `sqlite3`. A Postgres copy is a `pg_dump`: `pg_restore --list` shows what is in it, `pg_restore` puts it into any Postgres. You need no moin for either.

## Postgres

Any service that runs Postgres is backed up: no password, no change to your `compose.yml`. The data must be on a volume:

```yaml
services:
  web:
    build: .
    environment:
      DATABASE_URL: postgres://app:${POSTGRES_PASSWORD}@db:5432/shop
  db:
    image: postgres:17
    environment:
      POSTGRES_USER: app
      POSTGRES_DB: shop
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql/data
volumes:
  pgdata:
```

In moin this database is `postgres-db` (`postgres-<service>`): in the bucket, in the app's backups and in `--db postgres-db`.

## Requirements

- **SQLite**: the file is on a volume and uses WAL mode (`PRAGMA journal_mode=WAL`), as most apps do. Do not run a second Litestream for the same file.
- **Postgres**: a service that runs `postgres`, `postgis/postgis`, `pgvector/pgvector` or `timescale/timescaledb`, with its data on a volume and a Debian-based image, like `postgres:17`. An Alpine image such as `postgres:17-alpine` has no backups, and the app says so; **Download a copy** still works.
- No database shows up? See [Data](/docs/guides/data#requirements).

## Good to know

- A restore replaces the live data, so moin asks you to type the app's name first, on the web, in the CLI and in the TUI. `--yes` skips it in scripts; an agent passes the name as `confirm`.
- After a restore of a SQLite file its history starts again from the restored file; the copy before the restore is the way back.
- A restore never changes data in place: it builds the restored database beside the live one and switches in one step. So it needs room on the server's disk: once more the database's size for Postgres (the restored volume), twice the file's size for SQLite (the restored file and the copy before it). Without it the restore says so before it changes anything.
- After a Postgres restore the database runs on a new volume, `moin-restore-<app>-<service>-<time>`. Your compose file still names the old one: moin puts the restored one in each time it starts the app, also for a deploy or a rollback. A `docker compose up` by hand on the server starts the volume before the restore. When a deploy renames the database service or moves its volume, moin does not start it, so it never runs on the data from before a restore: the deploy says what to put back. The volume before stays on the server for 7 days as the undo, then moin removes it; a folder of the server (a bind mount) stays, moin never deletes it. moin removes only volumes it wrote down: one it cannot account for stays.
- Right after a Postgres restore, until its first base backup is done (a few minutes), moin offers no moment after it: the log of those minutes cannot be replayed yet. A restore to a moment past a missing piece of the history says so and changes nothing, instead of going back to an earlier moment.
- When you remove an app, the volumes of its restores stay 7 days, then moin removes them. An app made again under the same name starts on its own volume.
- A restore goes on when the server goes offline or the control plane restarts in the middle: it shows as "the server is offline", no other restore of the app starts meanwhile, and moin asks the agent every minute how it ended once the server is back. The CLI and the TUI wait 10 minutes for that, then say what to check. An agent that stops in the middle (a reboot) ends the restore when it starts again: the app runs again, on the database before or the restored one, never on half of one, and the restore says which.
- A restore that fails says why. Mostly nothing changed then, and the app runs again; when only the start after the switch failed, it says so: the restored data is in place, and starting the app again is what is left. Postgres keeps its whole history across restores.
- When an app moves to another bucket, its history starts again there.
- Removing the bucket from moin stops its backups; what is in it stays. See [Remove a bucket](/docs/guides/bucket#remove-a-bucket).
- To keep a server's backups in one bucket only, use the `backups-to` [rule](/docs/guides/security#rules).
- An agent older than this version answers "update the agent": update it under **Servers** or with `moin servers update`.
