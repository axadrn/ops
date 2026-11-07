---
title: "How backups work"
description: "Every change for 7 days in your own bucket, a daily check, and what lives where"
group: "Servers and data"
order: 4
---

moin writes down every change of your database for 7 days in your bucket. Every day it checks that going back works. Go back to any minute; a restore can be undone for 7 days.

That is the whole idea: one backup, the history of your database. Nothing to set up, nothing to schedule.

## Your data goes to your bucket only

Your database goes from your server straight to your own bucket, the one you added in moin. It never goes to moin's servers, not even in passing, and never to a bucket of ours. When you download a copy, your server puts it in your bucket and moin gives you a link that works for 15 minutes. moin keeps the bucket's secret key encrypted and uses it for one thing: to sign such short-lived links.

```
every change   your server ──▶ your bucket
a restore      your bucket ──▶ your server
a copy         your server ──▶ your bucket ──▶ a 15 minute link ──▶ you
```

## Going back

- **Any minute of the last 7 days.** Deleted the wrong rows at 14:02? Go back to 14:01.
- **Moments moin remembers.** Before every deploy, every data edit you make in moin (SQL, a cell, a row) and every catalog update, moin notes the moment, so the list offers "Before v12" or "Before your SQL change".
- **Undo a restore.** Right before a restore moin takes one full copy of the database as it is, and keeps it 7 days. The list offers it as "Before the restore at 14:02": going back to it undoes the restore. It is the only full copy moin keeps.

Going back is to the minute: the history keeps older changes in pieces of about half a minute.

## The daily check

Every day moin takes the newest state from the history, restores it into a throwaway copy on your server and checks it. The app shows when the check last passed, and an alert goes out when it fails. "Backed up" means "went back and checked".

## A copy for yourself

**Download a copy** gives you the full database as one file, any time: SQLite as a gzipped file (`gunzip`, then `sqlite3`), Postgres as a `pg_dump` (`pg_restore`). You need no moin to open it. The copy waits in your bucket for a day.

## What lives where

Everything of one workspace goes into one folder named after the workspace's ID, so two workspaces can share a bucket, even with apps of the same name. With a **Folder** set, the whole tree moves one level down, into it.

```
<your-bucket>/
└── <folder>/                        ← only when you set one
    └── <workspace>/                 ← your workspace's ID, it never changes
        └── <app-name>/
            └── <app-id>/            ← a new app with the same name gets a new one
                ├── uploads/         ← your code on its way to the server
                ├── downloads/       ← copies you asked for
                └── <database>/      ← one per database: data-app.db for /data/app.db, postgres-db for service db
                    ├── litestream/      ← SQLite: every change of the last 7 days
                    ├── walg/            ← Postgres: every change of the last 7 days
                    └── before-restore/  ← the copy before each restore
```

A `snapshots/` folder is from moin before the history: moin no longer reads it. You may delete it.

## How long things stay

| What | How long | Who deletes it |
| --- | --- | --- |
| `litestream/`, `walg/` | every change of the last 7 days | the agent, with Litestream's and WAL-G's own cleanup |
| `before-restore/` | 7 days | the agent, once an hour |
| `downloads/` | a day | the agent, once an hour |
| `uploads/` | until the server has it, a day at most | the agent: right after the download, and once an hour |
| `snapshots/` from before | until you delete it | you: moin no longer reads it |
| an app you removed | until you delete it | you: its `<app-id>/` folder, in your provider's dashboard or with any S3 tool |
| a bucket or folder you left | until you delete it | you: moin starts the history again in the new place |

A new app with the name of a removed one starts with an empty folder: old history never mixes with new.

## Several buckets

A workspace can keep several buckets: a client's data in the client's bucket, a bucket near the server, separate costs, production apart from tests. One is the **default**: every app without a bucket of its own, and every new app, uses it.

When an app moves to another bucket its history starts again there, and you can go back to moments from the move on. What is in the old bucket stays; moin deletes nothing.

## Under the hood

The agent on your server does it all, with two tools: [Litestream](https://litestream.io) for SQLite and [WAL-G](https://github.com/wal-g/wal-g) for Postgres. moin writes no backup logic of its own. Both work behind the same four verbs, one file each in the agent (`backup_sqlite.go`, `backup_postgres.go`):

```
                Record                         Check (daily)
  database ─────────────▶ bucket: every change ─────────────▶ throwaway copy ✓
     ▲                     for 7 days
     └─────────────────── GoBack to a minute  (a full copy of now first)

  database ── Download ──▶ bucket downloads/ ──▶ a 15 minute link
```

- **Record** writes down every change. SQLite: Litestream runs inside the agent and sends each change within a second. Postgres: WAL-G runs in a small companion container next to Postgres, streams its log, and moin closes the current piece of the log once a minute when there were writes; a base backup runs every day and after each restore.
- **GoBack** restores the history to a moment into a new file (SQLite) or a new volume (Postgres) while the app keeps running and checks it. Only then it stops the app from writing, takes the one full copy of now, swaps the data in and starts the app again, also after a failure. The swap copies next to the live data first and then renames: if anything fails before that, nothing changed. Postgres starts a new timeline with each restore; moin keeps track of it, so you can still go back to any moment before the restore. SQLite's history starts again from the restored file; the copy before the restore is the way back.
- **Check** restores the newest state into a throwaway copy. SQLite: Litestream restores it into a temp file, then `PRAGMA integrity_check`. Postgres: WAL-G fetches the newest base backup and the log after it, then a throwaway Postgres of the same image, with no network, replays it all, and moin checks that every table of the live database came back and reads.
- **Download** makes a full copy: SQLite with `VACUUM INTO`, gzipped; Postgres with `pg_dump -Fc`. It goes to `downloads/` in your bucket.

Postgres needs a Debian-based image, like `postgres:17`: WAL-G does not run on Alpine. moin sets `max_slot_wal_keep_size` to 4 GB when it is still the default, so a WAL-G that stays down never fills your disk: past that Postgres drops WAL-G's place, moin starts the history again at once, and the app says so for a day.
