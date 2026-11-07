---
title: "CLI"
description: "Every moin command, its flags and what it does"
group: "Reference"
order: 2
---

Every command of `moin`, by area. `moin <command> --help` shows the same on your terminal; [Where things work](/docs/reference/surfaces) compares the CLI with the web, the terminal UI, the API and MCP.

## Start and account

| Command | What it does |
| --- | --- |
| `moin` | opens the [terminal UI](/docs/reference/tui); without a login it signs you in through the browser first |
| `moin login [--url <url>] [--web] [--token <token>]` | logs this computer in through the browser: prints a code, opens `/device` and waits for **Approve**. Without a browser, open the printed address on any device. `--web` uses the browser even without a terminal. `--token`, or a token on stdin (`echo "$TOKEN" \| moin login`), skips the browser, for CI and scripts. See [Log in the CLI](/docs/guides/account#log-in-the-cli) |
| `moin logout` | forgets the token, and what the terminal UI kept for its next start |
| `moin status` | is everything OK: what needs you, catalog updates, what runs; exits `1` when something needs you ([Overview](/docs/guides/overview)) |
| `moin version` | the version of `moin` and of the control plane, and whether a newer one is out |
| `moin update [--yes]` | updates a self-hosted control plane to the newest version and waits until it is back; for its admins |
| `moin help` | the list of commands |

`--url` defaults to the saved control plane, else `https://moin.run`. It must be https; plain http works only for localhost and your tailnet (`100.x` addresses, `*.ts.net`).

## Team and activity

| Command | What it does |
| --- | --- |
| `moin team` | the team of your workspace and its open invitations |
| `moin team invite <email> [--admin]` | invites someone by email; `--admin` may invite others |
| `moin team resend <email>` | sends an invitation again; only the new link works |
| `moin team revoke <email> [--yes]` | takes an invitation back |
| `moin team rename <name>` | renames the workspace; its links stay the same |
| `moin activity [--app <app>] [--server <server>] [--user <email>] [--limit 30] [--cursor <cursor>]` | who deployed, restored or changed what, newest first, to the second in your time zone. `--server` includes its apps, `--limit` is at most 200, the hint at the end gives the `--cursor` for older events |

## Alerts

| Command | What it does |
| --- | --- |
| `moin alerts [on\|off]` | lists the destinations, and shows or sets your email alerts: a site or a server goes down or comes back, a deploy fails, in the workspaces you own or run as admin. On by default. See [Alerts](/docs/guides/alerts) |
| `moin alerts add slack\|discord\|telegram\|webhook <name> [--chat <id>] [--events <events>]` | sends alerts to Slack, Discord, Telegram or a webhook too. The URL, or the bot token for Telegram, comes on stdin. A webhook prints its signing secret once. Owners and admins. See [Alerts](/docs/guides/alerts#slack-discord-telegram-and-webhooks) |
| `moin alerts test <name>` | sends a test alert to a destination |
| `moin alerts rm <name> [--yes]` | removes a destination |

| Flag | Takes |
| --- | --- |
| `--chat` | the Telegram chat: an ID like `-1001234567890`, or `@channel` |
| `--events` | a comma list, default all: `uptime.down`, `uptime.up`, `server.down`, `server.up`, `deploy.failed`, `backup.failed`, `backup.recovered`, `drill.failed`, `drill.recovered`, `disk.full`, `disk.ok`, `update.available` |

## Servers

| Command | What it does |
| --- | --- |
| `moin servers` | lists servers: status, agent version, access, rules |
| `moin servers add [name]` | adds a server and prints its install command; without a name it gets one like `brave-otter` |
| `moin servers install-command <name>` | a new one-time install command, to install or move the agent; the old one stops working |
| `moin servers rename <name> <new name>` | renames a server; its apps stay on it |
| `moin servers rm <name> [--yes]` | asks you to type its name, removes the server and prints the command that removes its agent there |
| `moin servers update [name]` | updates the agent to the control plane's version now; without a name, every online server whose agent differs. See [Agent updates](#agent-updates) |
| `moin servers auto-update [on\|off]` | shows or sets whether agents update by themselves; on by default |
| `moin servers secure <server> [--tunnel on\|off] [--ssh tailscale\|open] [--firewall on\|off] [--yes]` | how the server is reached; prints what the agent set up. See [Security](/docs/guides/security) |
| `moin servers rules <name>` | the server's own rules for moin, from `/etc/moin/rules` on it ([Rules](/docs/guides/security#rules)) |
| `moin integrations` | whether a Cloudflare token and a Tailscale key are stored, never the keys |
| `moin integrations cloudflare` · `moin integrations tailscale` | stores the Cloudflare API token or the Tailscale auth key; the key comes on stdin |
| `moin integrations rm cloudflare\|tailscale [--yes]` | removes a key |

| Flag of `servers secure` | Does |
| --- | --- |
| `--tunnel on` · `off` | app traffic through a Cloudflare Tunnel, no open web port · ports 80 and 443 |
| `--ssh tailscale` · `open` | SSH only on the tailnet · SSH everywhere |
| `--firewall on` · `off` | drops every other inbound connection · no firewall |

## Apps

| Command | What it does |
| --- | --- |
| `moin apps` | lists apps |
| `moin apps <app>` | one app: its state, server, domains, password, repo and deploys, and "N changes waiting" with the restart that applies them; `moin app <app>` works too |
| `moin apps add --server <server> <name>` | adds an app |
| `moin apps rm <app> [--yes]` | asks you to type its name and removes the app; its data stays on the server |
| `moin apps pin [app]` · `moin apps unpin [app]` | keeps an app above the others that run fine, in every list |

## Deploys

| Command | What it does |
| --- | --- |
| `moin deploy [--dir .] [--server <server>] [--detach] [app]` | deploys a directory; the first deploy creates the app. `--server` picks the server of a new app, `--detach` starts the deploy and does not wait |
| `moin deploy --image <image> [app]` | deploys an image built in CI as the main service, with the compose file of an earlier deploy of the repo |
| `moin deploy --ref <ref> [app]` | deploys a branch, tag or commit of the linked GitHub repo |
| `moin redeploy [app]` | builds the newest source again, or pulls the image again, with the current env |
| `moin restart [app]` | starts the live deploy again with the current env and domains |
| `moin stop [app] [--yes]` | takes the app offline; its data stays |
| `moin start [app]` | runs a stopped app again |
| `moin deploys [app]` | the newest deploys: number, status, started, took, note |
| `moin deploys [app] v<number>` | one deploy with its whole log |
| `moin rollback [app] [v<number>] [--yes]` | starts an earlier deploy again; without a number, the one before the live one |
| `moin logs [--tail 200] [app]` | the last lines of the app's containers, `--tail` per container |
| `moin exec [app] [--service <service>] [--timeout 10m] -- <command>` | [runs a command](/docs/guides/exec) in a container, no shell. The main service by default, `--timeout` at most `1h`. Exits with its code, `124` after the timeout. Owners and admins |

## Domains

| Command | What it does |
| --- | --- |
| `moin domains [app]` | lists domains and their DNS |
| `moin domains add [--service <service>] <domain> ...` | adds domains; `--service` for a service of `compose.yml` other than the main one |
| `moin domains rm [--service <service>] <domain> ... [--yes]` | removes domains |
| `moin domains canonical <domain\|none>` | the domain the others redirect to, or none |
| `moin instant [--show] [app]` | the [instant address](/docs/guides/domains#instant-address), `https://<app>.<ip>.sslip.io`; `--show` adds the password moin made for it |
| `moin instant sslip.io\|nip.io\|off [app]` | picks the provider of the instant address, or turns it off |
| `moin instant open\|protect [app]` | takes the password away from the instant address, or puts it back |
| `moin protect [--user moin] [app]` | asks visitors for a password; the password comes on stdin, `--user` is the name the browser asks for |
| `moin protect off [app] [--yes]` | makes the app public again |

## Uptime and metrics

| Command | What it does |
| --- | --- |
| `moin uptime [app]` | uptime of every domain over 24 hours, 7 and 30 days, the newest check and the last outages |
| `moin uptime path <domain> <path\|default>` | where the check of a domain goes; `default` is the health path or `/` |
| `moin uptime pause <domain>` · `moin uptime resume <domain>` | pauses the check of a domain; checks it every minute again |
| `moin metrics [app\|server] [--range 1h\|24h\|7d\|30d]` | CPU, memory and network of an app now, a sparkline of the range and the deploys that went live; a name that is no app is a server. Default `1h` |
| `moin metrics --server <server>` | CPU, memory, disk, network and load of a server, and its apps by memory |

## GitHub and CI

| Command | What it does |
| --- | --- |
| `moin github` | where the moin GitHub App is installed, and the link to install it |
| `moin github repos` | the repos moin may read, and where to give it more |
| `moin github branches <owner/name>` | the branches of a repo, the default first |
| `moin link owner/name [--branch <branch>] [--auto=false] [app]` | follows a repo; every push to the branch deploys unless `--auto=false`. `--branch` defaults to the repo's default branch |
| `moin unlink [app] [--yes]` | stops following it |
| `moin hook [app] [--yes]` | asks, then prints a new deploy hook URL for CI; the old one stops working |
| `moin hook off [app] [--yes]` | turns the deploy hook off; CI can no longer deploy |
| `moin registry` | lists registry logins for private images |
| `moin registry set <host> <username>` | saves a login; the password or token comes on stdin |
| `moin registry rm <host> [--yes]` | removes a login |

## Catalog

| Command | What it does |
| --- | --- |
| `moin catalog` | the [catalog](/docs/guides/catalog), each app with its newest version |
| `moin catalog <app>` | one catalog app with every version |
| `moin catalog install <app> --server <server> [--version <tag>] [--name <name>] [--domain <domain>]` | installs a catalog app and follows its first deploy. `--version` defaults to the newest, `--name` to the catalog app's |
| `moin update <app> [--version <tag>]` | sets a catalog app to a version, the newest by default, and deploys it; `moin backups` offers the moment before |

## Env vars

| Command | What it does |
| --- | --- |
| `moin env [--show] [app]` | lists env vars, values hidden without `--show` |
| `moin env set [app] KEY=value …` | sets env vars |
| `moin env rm KEY … [--yes]` | removes env vars; `unset` works too |
| `moin env pull [--app <app>] [--yes] [file]` | writes them to a `.env` file, default `.env`; asks before it replaces a file |
| `moin env push [--app <app>] [--yes] [file]` | replaces all env vars with a `.env` file, default `.env`; shows the keys that change and asks first |
| `moin run [--app <app>] -- <command>` | runs a command on your computer with the env vars, like `moin run -- go run .` |

## Data

| Command | What it does |
| --- | --- |
| `moin db [--db <path>] [app]` | tables, row counts and migration of the app's database, SQLite or Postgres; `moin db tables` works too |
| `moin db rows <table> [--limit 20] [--offset 0]` | a page of a table, newest first |
| `moin db schema [table]` | the CREATE statements |
| `moin sql [--app <app>] [--db <path>] [--limit 100] "<sql>"` | runs SQL read-only; the SQL may come on stdin |
| `moin sql --write "<sql>" [--yes]` | runs SQL that changes data, in one transaction; DROP and DELETE ask first |

`--db` is the database path in the container, default the first.

## Backups

| Command | What it does |
| --- | --- |
| `moin backups [app]` | how each database is backed up, the daily check, and the moments to go back to |
| `moin restore [app] --to <moment> [--db <path>] [--yes]` | goes back to a moment from `moin backups`, by its ID or label (`1h`, `before-v12`, `undo-20261107T140200Z`), or to a time in UTC to the minute: `14:01` for today, `"2026-11-07 14:01"`, RFC 3339. Keeps a copy first, so it can be undone for 7 days |
| `moin backup download [app] [--db <path>] [--link]` | saves a full copy of a database in the current folder, from your bucket; `--link` prints its 15 minute link instead |

## Bucket

| Command | What it does |
| --- | --- |
| `moin buckets` | your buckets, the default first, with the apps that use each and the apps that left it |
| `moin buckets add <name> --endpoint … --bucket … --access-key … [--region auto] [--path <folder>] [--default]` | adds a bucket; the secret key comes on stdin; the first one is the default |
| `moin buckets edit <name> [--name <new>] [--endpoint …] [--bucket …] [--path …] [--region …] [--access-key … \| --secret]` | changes a bucket; a new access key, or `--secret`, reads the secret key on stdin, otherwise the stored one stays |
| `moin buckets default <name>` | makes a bucket the default for apps without one of their own |
| `moin buckets rm <name> [--yes]` | removes a bucket from moin; refused while apps use it or apps left it. What is in it stays |
| `moin buckets use <bucket> [app]` · `moin buckets use --default [app]` | moves an app's uploads and backups to a bucket, or back to the default; the history starts again there |

| Flag | Takes |
| --- | --- |
| `--endpoint` | the `https://` URL of an S3-compatible store; empty for AWS S3 |
| `--region` | the region; `auto` for Cloudflare R2 |
| `--bucket` | the bucket's name at your provider |
| `--path` | a folder inside the bucket, only if it holds other files too |
| `--access-key` | the access key ID |
| `--default` | `add`: make it the workspace's default. `use`: follow the default again |

## Agent

These run on a server, as root.

| Command | What it does |
| --- | --- |
| `moin agent register --url <url> --token <install token>` | connects this server; the install script does it |
| `moin agent` | runs the agent |
| `moin agent uninstall [--apps] [--yes]` | removes the agent from this server; the apps keep running. `--apps` removes the apps with their data, the proxy, the tunnel and the firewall too |

## How commands find the app

A command that works on an app takes it as an argument or as `--app`, then from `x-moin.app` in `compose.yml`, then from the name of the directory. When there is no such app, the error names it and where the name came from.

Flags may come before or after the arguments: `moin rollback v3 --app shop` is `moin rollback --app shop v3`. `ls` and `list`, `rm` and `remove` are the same subcommand.

## Asking first

| | Commands | Asks |
| --- | --- | --- |
| Big, cannot be undone | `servers rm`, `apps rm`, `buckets rm`, `restore` | type the name: `Type shop to confirm:` |
| Small or reversible | `update`, `stop`, `rollback`, `env rm`, `domains rm`, `protect off`, `registry rm`, `integrations rm`, `alerts rm`, `unlink`, `hook`, `hook off`, `team revoke`, `servers secure`, `agent uninstall`, `env pull` over a file, `env push`, `sql --write` with DROP or DELETE | `[y/N]` |
| Harmless | `deploy`, `redeploy`, `restart`, `start`, `backup download`, adding and setting things | nothing |

`--yes` skips the question, the typed name too. Without a terminal, as in CI, these commands refuse unless `--yes` is there, so a script never removes something by accident.

## Agent updates

Agents update themselves to the control plane's version, unless `moin servers auto-update off`. `moin servers` shows each agent's version and marks one that differs.

`moin servers update` has the agent download the `moin` binary the control plane serves, check its SHA-256, swap it in and restart through systemd; it waits until the new agent is back. The agent refuses while a deploy runs, and on a server with rules in `/etc/moin/rules`, since a new binary could allow what they stop. There, and for agents older than the update, update it on the server as root (the refused update prints this command for that server):

```bash
curl -fsSL https://moin.example.com/download/moin-linux-amd64 -o /usr/local/bin/moin.new && chmod 755 /usr/local/bin/moin.new && mv /usr/local/bin/moin.new /usr/local/bin/moin && systemctl restart moin-agent
```

`moin-linux-arm64` on an ARM server. `/download/SHA256SUMS` lists the checksums of every binary.

## Output

| Where | Looks like |
| --- | --- |
| A terminal | colors, status symbols (`● online`, `◐ deploying v4`, `✗ failed v4 · v3 live`), a spinner while a deploy runs |
| Piped or CI | plain text; tables keep their columns with the bare status words, for scripts |
| An error | starts with `✗`, names the command to run next, exits `1` |

## Environment

| Variable | Does |
| --- | --- |
| `MOIN_URL`, `MOIN_TOKEN` | override the saved login, for CI |
| `MOIN_STATE_DIR` | where the agent keeps its state, default `/var/lib/moin` |
| `MOIN_RULES` | where the agent reads its [rules](/docs/guides/security#rules), default `/etc/moin/rules` |
| `NO_COLOR` | turns colors off on a terminal too |
