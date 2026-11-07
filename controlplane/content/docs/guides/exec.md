---
title: "Run a command"
description: "A one-off command in an app's container, without SSH"
group: "Every day"
order: 3
---

Run a command inside your app, as if you typed it on the server, and see what it prints: one command in a container of your app, its output as it comes and the exit code at the end. For migrations, the first admin user or a look at a file. No SSH, no key on your laptop.

```text
you: moin exec shop -- ls /data
  │                       ▲
  ▼                       │
control plane             │
  │                       │
  ▼                       │
agent, on the line        │
it already has open       │
  │                       │
  ▼                       │
container of shop ────────┘
runs ls /data      what it prints,
                   then the exit code
```

## Run a command

```surfaces
Web: the app's **Logs** tab, **Run a command**
CLI: moin exec [app] [--service web] -- <command>
TUI: on the app, `X`, then type the command
API: POST /api/v1/apps/{app}/exec
MCP: exec_command
```

```bash
moin exec shop -- rails db:migrate
moin exec shop -- php artisan migrate
moin exec shop -- rails runner 'User.create!(email: "me@example.com", admin: true)'
moin exec shop -- ls /data
moin exec shop --service db -- psql -U shop -d shop -c "select count(*) from orders"
```

Everything after `--` is the command, word by word. For pipes and the like, ask for a shell: `moin exec shop -- sh -c 'ls /data | wc -l'`. `moin exec` exits with the command's exit code, so it fits in a script. It runs in the main service unless `--service` names another; the web offers the main service and the database.

## Good to know

| | |
| --- | --- |
| No shell | pipes, `>`, `&&` and `$VAR` mean nothing; ask for a shell, as above. The web, the TUI and MCP split one line into words like a shell quotes them, `'…'` and `"…"`, nothing more |
| Not interactive | no terminal, no input: a command that asks waits until it times out. Pass `--force`, `--no-interaction` or the like |
| Timeout | 10 minutes, at most 1 hour with `--timeout 1h`; then `moin exec` exits with `124`. The command may still run in the container: `moin restart` stops it |
| Output | up to 1 MB, the rest is cut and moin says so. MCP keeps the last 64 KB |
| Needs | the server online and the app live |
| Who | owners and admins only: a command can do anything the app can |
| Activity | every command lands in the [activity](/docs/guides/activity) as `app.exec`, with the service, the command and its exit code. Env var values in the command show as `•••`; the output is never stored |

## Turn it off on a server

`exec: off` in the [rules](/docs/guides/security#rules) of a server refuses every command through moin there, for everyone:

```
# /etc/moin/rules
exec: off
```

A refused command says `refused by the rules of this server (/etc/moin/rules): exec: off`. An agent older than this feature answers `update it`: `moin servers update <server>`.
