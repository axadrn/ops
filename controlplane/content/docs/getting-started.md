---
title: "Get started"
description: "From a fresh server to a live app, in four steps"
group: "Start"
order: 2
---

moin puts your app on your own server and keeps its data safe. You type `moin deploy`, and a minute later your app is on the internet. New to the words? [moin in five words](/docs/moin-in-five-words).

Install the CLI on your Mac or Linux computer:

```bash
curl -fsSL https://moin.example.com/cli.sh | sh
```

Then four steps, and your app is live.

## 1. Log in

```bash
moin login --url https://moin.example.com
```

Your browser opens with a code like `WDJB-MJHT`. Check it is the same as in the terminal and press **Approve**. No account yet? The same page signs you up, with GitHub or your email.

## 2. Add a server

A server is your computer on the internet: any Linux server you can log in to as root, amd64 or arm64, at any provider.

```surfaces
Web: **Servers**, **Add server**
CLI: moin servers add web-1
TUI: opens the web: `w`, then `a`
API: POST /api/v1/servers
MCP: add_server
```

Run the command it prints on the server, as root, within the hour:

```bash
curl -fsSL https://moin.example.com/install.sh | sh -s -- ins_…
```

It installs Docker if it is missing and starts the agent, the little helper that only calls out to moin. `moin servers` shows the server `online`.

## 3. Add your bucket

Your bucket is your own box on the internet: your backups and uploads go there, never to us. Any S3-compatible one works: Cloudflare R2, AWS S3, Backblaze B2, Hetzner or Exoscale. Make a bucket and a key that may read, write and delete.

```surfaces
Web: **Settings**, **Bucket**, **Add bucket**
CLI: moin buckets add default \
  --endpoint https://<account-id>.r2.cloudflarestorage.com \
  --bucket <bucket-name> \
  --access-key <access-key>
TUI: opens the web: `w`, then `k`
API: POST /api/v1/buckets
MCP: add_bucket
```

The CLI asks for the secret key. The endpoint starts with `https://`; for AWS S3 leave it out and set the region. [Bucket](/docs/guides/bucket) has the details.

## 4. Deploy

A deploy puts a new version of your app live. In the folder of your app:

```bash
moin deploy
```

```text
Created app shop on web-1
Uploading 42 files to your bucket (18.3 KB)
Deploy v1 of shop started
  detected go, service web on port 8080
  v1 is live
✓ moin. shop is live at https://shop.203-0-113-7.sslip.io
```

No Dockerfile needed: moin builds Node, Bun, Python, Ruby, PHP, Go and more, or uses your `Dockerfile` or `compose.yml`. The app is online at once at its [instant address](/docs/guides/domains#instant-address), with HTTPS and a password in front; `moin instant --show` prints both.

## Good to know

- `moin` lands in `/usr/local/bin`, or in `~/.local/bin` when that is not writable. Then add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` or `~/.profile`.
- No browser on this computer, as over SSH? Open the address `moin login` prints on any other device and approve there.
- The login belongs to this computer and one workspace. It shows as **CLI on** and the computer's name under **Settings**, **Account**, **Tokens** tab, and ends after 90 days without use. See [Account and tokens](/docs/guides/account#log-in-the-cli).
- With GitHub sign-in your name and workspace come from your GitHub profile; GitHub finds your account by any verified email on it. With email, moin sends a sign-in link and asks for both. An invited teammate only gives a name.
- On a self-hosted control plane only the admins and invited teammates sign up: [Who signs in](/docs/self-hosting#who-signs-in).
- Not logged in to the server as root? Put `sudo` before `sh`.
- More than one server? `moin deploy --server web-1`. A failed deploy ends its log with the reason; `moin deploys v1` shows it again.
- The instant address needs ports 80 and 443, so a server behind a Cloudflare Tunnel has none. For your visitors, [add a domain](/docs/guides/domains): `moin domains add shop.example.com`, then an A record at your DNS provider.

## Next

- [Why moin](/docs/why-moin): one binary, an agent that only dials out, restores that are checked.
- [Palette and keys](/docs/reference/palette): `⌘K` finds everything.
- [Catalog](/docs/guides/catalog): n8n, Umami, Open WebUI and more in one click.
- [Backups](/docs/guides/backups) and [Security](/docs/guides/security).
