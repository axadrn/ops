---
title: "Self-hosting"
description: "Run all of moin yourself, free forever, the same product we host"
group: "Start"
order: 5
---

Self-hosting means you run the control plane yourself, on your own server, instead of using ours. All of moin is open source under AGPL-3.0, so you get the product we host: every feature, unlimited servers, apps and teammates, no license key, no account with us.

## One server

Point a domain at the server with an A record, then run this as root on a fresh Ubuntu or Debian server:

```bash
curl -fsSL https://raw.githubusercontent.com/axadrn/moin/main/controlplane/deploy/install.sh | sh -s -- --domain moin.example.com --email you@example.com
```

In a few minutes it installs Docker and security updates, writes the settings to `/etc/moin-controlplane/env` with a new `SECRETS_KEY`, starts the control plane with your account, a workspace and this server in it, installs the agent and serves the control plane on your domain with a Let's Encrypt certificate. At the end it prints a sign-in link.

Then point the CLI at it:

```bash
moin login --url https://moin.example.com
```

The rest of these docs apply as they are.

## Good to know

- Keep a copy of `SECRETS_KEY`: it seals the secrets of your apps. Without it they are lost.
- Email and a bucket can come later. Until email is set up, sign-in links go to the control plane's log; `docker compose -f /opt/moin/compose.yml exec controlplane ./main sign-in-link` prints a new one.
- Add `CLOUDFLARE_*` for email and `S3_*` with `BACKUP_DATABASE=true` for the control plane's own backup, then run `sh /opt/moin/update.sh`.
- Run the install command again any time: it keeps your settings and data.

| | Self-hosted | Hosted |
| --- | --- | --- |
| Price | free forever | $29 a month or $249 a year, per workspace |
| Features; servers, apps, teammates | all; unlimited | all; unlimited |
| Setup | one command on one server | ready to use |
| Updates | one click, when a new version is out | by us, the day they ship |
| GitHub App, email | you set them up | ready to use |
| Backup of the control plane | yours to run | every day, by us |
| Support | GitHub issues | email |
| Your apps and data | your servers | your servers |

## What is different

- **Billing is off** (`BILLING=false`): no prices, no checkout, no billing page, and every workspace is unlimited.
- **It is private.** No landing page: `APP_URL` opens the sign-in page, or your workspace when you are signed in. Sign-up is closed (see [Who signs in](#who-signs-in)); anyone else sees "This moin is private. Ask its admin for an invitation."
- **The docs stay at `/docs`.** The terms and privacy pages of the hosted service are not there, and `ROBOTS_NOINDEX` asks search engines to stay away.
- Everything else is the code we run: web, API, MCP, deploys, backups, data, GitHub, teams and the activity.

## Two servers

Once it matters, run the control plane on its own small server, not next to your apps. The agent on an app server takes ports 80 and 443, and a control plane apart from them keeps working when an app server fails or is broken into: you can still restore, roll back and see what happened. It needs no SSH key to your app servers: their agents dial out to it.

You need a small Ubuntu or Debian server and a domain for it, an S3-compatible bucket, Cloudflare Email Sending for sign-in links and invitations, and, if you like, a Cloudflare Tunnel and [your own GitHub App](#your-own-github-app).

### Run it

The files are in the repo, in `controlplane/deploy/`:

| File | What it does |
| --- | --- |
| `setup.sh` | prepares the server once: Docker, security updates, and with `--tailscale` SSH on your tailnet |
| `compose.yml` | runs the control plane, with a tunnel token also `cloudflared` |
| `update.sh` | starts the control plane, and later updates it to the newest version |
| `updater.sh` | runs next to it and does **Update now** |

1. Prepare the server, as root:

   ```bash
   git clone https://github.com/axadrn/moin
   cd moin/controlplane/deploy
   sh setup.sh
   ```

2. Write the settings into `/etc/moin-controlplane/env` and `chmod 600` it:

   ```bash
   APP_ENV=production
   APP_URL=https://moin.example.com
   BILLING=false
   # You: the first admin. Sign up with this address.
   ADMIN_EMAILS=you@example.com
   DB_CONNECTION=./data/app.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)
   # openssl rand -base64 32. Keep a copy: it seals the secrets of your apps.
   SECRETS_KEY=
   EMAIL_FROM=hello@example.com
   CLOUDFLARE_ACCOUNT_ID=
   CLOUDFLARE_API_TOKEN=
   S3_ENDPOINT=
   S3_REGION=auto
   S3_BUCKET=
   S3_ACCESS_KEY=
   S3_SECRET_KEY=
   ```

3. Start it with `sh update.sh`. It listens on `127.0.0.1:8090`. Put it on your domain with a **Cloudflare Tunnel** (no open port: a tunnel in Zero Trust, Networks, Tunnels, with a public hostname for your domain and the service `http://controlplane:8090`, then `TUNNEL_TOKEN=` and `TRUSTED_PROXY_CIDRS=172.16.0.0/12` in the settings and `update.sh` again) or **a proxy of your own** with HTTPS, such as Caddy, pointed at `127.0.0.1:8090`.

### Settings

`controlplane/.env.example` lists every setting. The ones these docs use:

| Setting | What it does |
| --- | --- |
| `APP_URL` | the address of the control plane |
| `ADMIN_EMAILS` | the admins, separated by commas; the only ones who may sign up without an invitation |
| `SECRETS_KEY` | seals the secrets of your apps; keep a copy |
| `BILLING` | `false` turns off prices, checkout and the billing page |
| `SIGNUPS_OPEN` | `true` lets anyone sign up; off by default without billing, keep it off on a control plane the internet can reach |
| `EMAIL_FROM`, `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_TOKEN` | email through Cloudflare Email Sending |
| `S3_ENDPOINT`, `S3_REGION`, `S3_BUCKET`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | the bucket of the control plane |
| `BACKUP_DATABASE` | backup of the control plane's database; on by default in production once `S3_BUCKET` is set, `false` turns it off |
| `UPTIME_CHECKS` | the [uptime checks](/docs/guides/uptime); on in production, off in development |
| `TUNNEL_TOKEN`, `TRUSTED_PROXY_CIDRS` | a Cloudflare Tunnel in front of the control plane |
| `SAME_SERVER` | `true` when the control plane runs on the same server as its apps, as the one-server install sets it |
| `OLD_URLS` | old domains the control plane still answers on, see [A new domain](#a-new-domain) |
| `TELEMETRY` | `false` stops the count it sends us, see [What it sends us](#what-it-sends-us) |
| `UPDATE_CHECK` | `false` stops the daily check for a new version |
| `ROBOTS_NOINDEX` | asks search engines to stay away; on by default without billing |
| `UMAMI_WEBSITE_ID`, `UMAMI_HOST` | count visitors with your own Umami; off by default |
| `GITHUB_APP_*` | [your own GitHub App](#your-own-github-app) |

### Keep the settings in 1Password

Create a 1Password service account that may read one vault, put its token in `/etc/moin-controlplane/op-token` (root only), and write the settings as `/etc/moin-controlplane/env.tpl` with references like `SECRETS_KEY={{ op://vault/item/field }}`. `update.sh` fills them in on every start; a changed password counts after the next `update.sh`.

### Keep it on your tailnet

The safest control plane is one the internet cannot reach:

1. Prepare the server with `sh setup.sh --tailscale`.
2. `tailscale serve --bg 8090` serves it at `https://<server>.<tailnet>.ts.net`. Use that as `APP_URL`, with no tunnel and no open port.
3. Put each server on the tailnet with `tailscale up` before you install the agent, and your computer too, for the web, the CLI and the TUI.

Your apps stay public through their own tunnel or ports 80 and 443. GitHub and CI outside your tailnet cannot reach the control plane, so **Deploy every push** and deploy hooks do not work: deploy from the web, the CLI or a CI runner on your tailnet.

### Move from one server to two

1. Turn on the control plane's backup: `S3_*`, `BACKUP_DATABASE=true`, `sh /opt/moin/update.sh`. Use a bucket no other control plane backs up to (folder `controlplane/litestream`).
2. Wait a few seconds, then on the old server stop the control plane and its updater: `docker compose -f /opt/moin/compose.yml --profile '*' down`. Your apps keep running.
3. On the new server, [run it](#run-it) with the same settings, without `SAME_SERVER`, and above all the same `SECRETS_KEY`. It restores its database from the bucket on its first start. Point the domain at it.
4. On the old server, run a new install command from `moin servers install-command <server>`: its agent now dials your domain, as the same server.
5. Once all works, on the old server: `rm -r /opt/moin /etc/moin-controlplane /var/lib/moin-controlplane`.

## Your own GitHub App

For deploys from GitHub the control plane needs its own GitHub App. Create it on GitHub under **Settings**, **Developer settings**, **GitHub Apps**:

1. **Permissions:** Contents read, Commit statuses read and write. **Events:** Push and Installation.
2. **Callback URL:** `{APP_URL}/app/github/setup`. Tick **Request user authorization (OAuth) during installation** and **Redirect on update**.
3. **Webhook URL:** `{APP_URL}/webhooks/github`, with a secret of your own.
4. Generate a private key and a client secret.

Put the key at `/etc/moin-controlplane/github-app.pem`, add the rest and run `update.sh` again:

```bash
GITHUB_APP_ID=
GITHUB_APP_SLUG=
GITHUB_APP_PRIVATE_KEY_FILE=/run/moin-controlplane/github-app.pem
GITHUB_APP_WEBHOOK_SECRET=
GITHUB_APP_CLIENT_ID=
GITHUB_APP_CLIENT_SECRET=
```

With the client ID and secret, moin connects an installation only for someone whose GitHub account may manage it, and **Connect GitHub** works when the app is already installed. Without them, moin trusts the installation in the link GitHub sends back, and says so in its log when it starts.

## Who signs in

- **You, first.** Sign up with your address from `ADMIN_EMAILS`, with a sign-in link. Add a password later under **Settings**, **Account**.
- **Your team.** Invite teammates; they sign up with the invited address.
- **Nobody else.** If `ADMIN_EMAILS` is empty nobody can create the first account, and the log says so when the control plane starts.

`/admin` is only for the addresses in `ADMIN_EMAILS`: users, sessions, operator activity and **Update now**.

## Updates

Once a day the control plane asks GitHub for the newest version. When one is out, the admins see a note at the top of the web: the version, what is new, a link to the [changelog](/changelog). Then click **Update now**, run `moin update`, or `sh update.sh` on the server.

- moin is away for about a minute; your apps keep running.
- Before the database changes it keeps a copy next to it, like `/var/lib/moin-controlplane/backup-before-v0.3.0.db`; the last three stay.
- If the new version is not up within two minutes, the old one starts again and the note says why.
- `moin version` shows the version and whether a newer one is out. You only get published versions, never work in between.
- **Update now** needs the updater next to the control plane, the only part with access to Docker: someone who breaks into moin can at most ask for an official version, never run their own code. A control plane set up before it gets it once with `git pull`, then `sh update.sh`.
- `UPDATE_CHECK=false` stops the check; then update with `sh update.sh`.
- The agents update themselves to the version of the control plane, one server at a time, never during a deploy: [Servers](/docs/guides/servers#updates).

## Database backup

Like your apps' databases, the control plane's SQLite database goes to its bucket with Litestream, every change within a second (`S3_BUCKET`, folder `controlplane/litestream`). On by default in production once `S3_BUCKET` is set; `BACKUP_DATABASE=false` turns it off.

| File | What is in it | Backup |
| --- | --- | --- |
| `app.db` (the name in `DB_CONNECTION`) | users, apps, deploys, settings, the state of each uptime check and every outage | every change, within a second |
| `history.db` | [metrics](/docs/guides/metrics) and [uptime](/docs/guides/uptime) per hour | none: it grows back, losing it costs old charts |

A control plane that starts without a database restores the newest one from the bucket by itself; an empty bucket means a fresh start. So to move or rebuild: prepare a new server with `setup.sh`, put your settings in `/etc/moin-controlplane/` again and run `update.sh`. With a tunnel nothing else changes.

## A new domain

Set `APP_URL` to the new one and put the old one in `OLD_URLS`, like `OLD_URLS=https://old.example.com`. A browser on the old domain goes on to the same page on the new one, agents and the CLI move by themselves, and webhooks and API calls there keep working. **Admin**, **Old domain** (or `GET /api/v1/admin/old-domain`) shows what still arrives, by day and kind; after 14 quiet days it can go.

## What it sends us

Every five minutes a self-hosted control plane tells us it runs, with a few counts:

```json
{"instance": "8f14e45f-ceea-467a-9575-6c6f2b2b8f1e", "version": "v0.1.0", "counts": {"servers": 3, "apps": 7, "deploys": 412}}
```

The instance ID is random, made on the first start. No name, domain, email, address or app data, and we do not keep the address it comes from. One that stops sending drops off within fifteen minutes. The code is in `controlplane/internal/service/telemetry.go`, and the log says so at start. `TELEMETRY=false` turns it off. The daily check for a new version carries nothing about you.
