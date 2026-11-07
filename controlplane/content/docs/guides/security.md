---
title: "Security"
description: "No open port, SSH only on your tailnet, everything else dropped, and rules only root can change"
group: "Servers and data"
order: 2
---

Your server has doors to the internet, called ports; moin can close all of them, so nobody from outside gets in. Three switches per server each close a door, and a file on the server decides what moin may do there. The agent only calls out, so moin itself needs no open door.

```text
outside                    server
moin ◀──────────────────── agent
visitors ─▶ Cloudflare ◀── cloudflared
you, SSH ─▶ tailnet ◀───── Tailscale
                           firewall:
                           the rest
                           is shut
```

Each line starts on your server and calls out. With all three switches on, only 41641/udp stays open to everyone: Tailscale's port for direct connections.

| Switch | What it closes | Needs |
| --- | --- | --- |
| **Cloudflare Tunnel** | ports 80 and 443: visitors come through Cloudflare | your domains in a Cloudflare account |
| **SSH only on Tailscale** | SSH from everywhere but your tailnet | a [Tailscale](https://tailscale.com) account |
| **Firewall** | everything the other switches do not need | nothing: turn it on for every server |

```surfaces
Web: **Servers**, **Security** on the server, flip the switches, then **Apply**
CLI: moin servers secure web-1 --tunnel on --ssh tailscale --firewall on
TUI: on the server, `W` opens its security page in the web
API: PUT /api/v1/servers/{id}/security
MCP: set_server_security
```

Each switch works alone; `--tunnel off`, `--ssh open` and `--firewall off` turn them back. If you do one thing, turn on the firewall. The CLI asks first; `--yes` skips it in scripts. On the web, **Apply** changes only the switches you flipped, so a change made meanwhile elsewhere stays.

## Keys

Two switches need a key, which you give once for every server: the tunnel a Cloudflare API token, SSH on Tailscale a Tailscale auth key. They belong to the workspace, work for all its servers and are stored encrypted. A new key replaces the old one; an empty field keeps it.

```surfaces
Web: **Servers**, **Security** on the server, **Keys for every server**
CLI: moin integrations
CLI: moin integrations cloudflare
CLI: moin integrations tailscale
CLI: moin integrations rm cloudflare
CLI: moin integrations rm tailscale
TUI: on the server, `W` opens its security page in the web
API: GET /api/v1/integrations
API: PUT /api/v1/integrations
MCP: get_integrations
MCP: set_integrations
```

`moin integrations` shows which keys are stored, never the keys. The CLI reads a key from stdin, so it never lands in your shell history. The API and MCP remove a key when you send it empty; the web has **Remove Cloudflare API token** and **Remove Tailscale auth key** at the end of the security page.

- **Cloudflare token**: in Cloudflare, **My Profile**, **API Tokens**, **Create Token**, **Create Custom Token**. Account **Cloudflare Tunnel: Edit** and Zone **DNS: Edit** for the zones of your apps. Zone **Zone Settings: Edit** is optional: with it moin turns on **Always Use HTTPS**, without it you do that in Cloudflare. moin checks the token before it stores it.
- **Tailscale key**: in the Tailscale admin console, **Settings**, **Keys**. Make it reusable.
- Turn the tunnels off before you remove the Cloudflare token: without it moin cannot delete a tunnel. Removing a key changes no server at once; without the Tailscale key, the next security change of a server opens SSH again on every network.

## Cloudflare Tunnel

Visitors reach your app through Cloudflare, so the server keeps ports 80 and 443 closed. With your token moin creates a tunnel for the server, routes every domain of its apps through it (anything else gets a 404), points each domain's DNS at the tunnel in the zone that holds it, and turns on **Always Use HTTPS**: Cloudflare serves HTTPS, so the server needs no certificate. A domain you add later gets the same.

```bash
moin servers secure web-1 --tunnel on
```

- If the token cannot reach a domain's zone, **What the agent set up** says so: point the domain at `<tunnel>.cfargotunnel.com` yourself. Without Zone Settings Edit it says to turn on **Always Use HTTPS** yourself.
- Off deletes the tunnel and the DNS records moin made. Traefik opens 80 and 443 again and gets certificates from Let's Encrypt: point your domains back at the server with an A record ([Add a domain](/docs/guides/domains#add-a-domain)).
- A server behind the tunnel has no [instant address](/docs/guides/domains#instant-address): it needs ports 80 and 443.

## SSH only on Tailscale

SSH is the door you use to log in to your server yourself. With this switch it opens only on your tailnet, your own private network, so nobody else on the internet can even try.

```bash
moin servers secure web-1 --ssh tailscale
```

The agent installs Tailscale if it is missing and adds the server to your tailnet. Port 22 closes only once Tailscale is up and has an address; until then it stays open and moin says why. With the firewall off it closes too: the agent then drops only SSH from other networks. Before you close SSH, keep a second way in, such as your provider's web console.

## Firewall

The firewall shuts every door the other switches do not need.

```bash
moin servers secure web-1 --firewall on
```

It lets in answers to connections the server made itself, ping, and what the other switches leave open: SSH unless it is on Tailscale, ports 80 and 443 unless the tunnel carries the traffic, and 41641/udp for Tailscale. It drops everything else.

## See what is open

How traffic comes in, which ports are open to everyone, and the tailnet address.

```surfaces
Web: **Servers**, **Security** on the server, **What the agent set up**
CLI: moin servers
TUI: on the server, **Access**
API: GET /api/v1/servers
MCP: list_servers
```

The CLI shows it in the ACCESS column and after every `moin servers secure`; the API in the `security` field. Without the firewall, **Open to everyone** lists only the ports moin uses, but every other listening port is open too.

## No open port

With all three switches on, nothing is open to the internet: the agent dials out to moin, `cloudflared` to Cloudflare, Tailscale to its own servers. **What the agent set up** shows **Open to everyone: 41641/udp**, the Tailscale port, and nothing else.

## Rules

A file on your server says what moin may do there, and only you, as root on the server, can change it: `/etc/moin/rules`. The agent checks every request against it and refuses what it does not allow. Nobody can change it through moin: not us, not your team, not someone who breaks into moin.

```
# /etc/moin/rules
data: off
logs: off
exec: off
deploy-from: github.com/<owner>/*, ghcr.io/<owner>/*
backups-to: <endpoint-host>/<bucket-name>
network: locked
```

| Rule | What it stops |
| --- | --- |
| `data: off` | reading tables, running SQL and editing rows through moin |
| `logs: off` | reading container logs through moin; logs often hold what your users sent |
| `exec: off` | [running commands](/docs/guides/exec) in containers through moin |
| `deploy-from: <sources>` | deploys from anywhere else |
| `backups-to: <host>/<bucket>` | backups to and restores from any other bucket, also an app's own bucket or an older one |
| `network: locked` | changes to the tunnel, SSH on Tailscale and the firewall: set them up first, then lock them |

- A `deploy-from` source is a GitHub repo `github.com/<owner>/<repo>`, an image without its tag like `ghcr.io/<owner>/<image>`, or `upload` for `moin deploy` from your computer (with `backups-to` set, an upload must come from that bucket too). `*` stands for one part: `github.com/<owner>/*` allows every repo of that owner. A redeploy or a rollback brings no new code, so it is always allowed.
- The `backups-to` host is your [bucket's](/docs/guides/bucket#add-it) endpoint without `https://`, like `<account-id>.r2.cloudflarestorage.com`; for AWS S3 `s3.<region>.amazonaws.com`.
- No file: moin may do everything. A rule left out allows what it covers. A file with a mistake, such as an unknown rule, refuses everything until you fix it.
- Rules apply to every request at once. The agent reports them when it connects, so `systemctl restart moin-agent` after a change shows them everywhere.
- A server with rules never updates its agent from moin ([Updates](/docs/guides/servers#updates)).
- A refused request says why, on every surface: `refused by the rules of this server (/etc/moin/rules): data: off`.
- With all of them, a break-in at moin reads nothing and runs nothing of its own on your server: `data` and `logs` keep your users' data from passing through moin, `deploy-from` and `exec` stop code that could copy it.

### See the rules of a server

```surfaces
Web: **Servers**, **Security** on the server, **Rules**
CLI: moin servers rules web-1
TUI: on the server, **Access**, under the rules
API: GET /api/v1/servers
MCP: list_servers
```

`moin servers` shows them in its RULES column; the API in the `rules` field.

## Under the hood

- The tunnel is `cloudflared`, running next to Traefik, the server's proxy. It keeps a connection open to Cloudflare, and visitors come in through it.
- The agent hands the Cloudflare token to `cloudflared` through its environment and the Tailscale key through a file only root can read, deleted right after. Neither shows in `ps`.
- The firewall rules live in one nftables table, `moin`, changed in one step, so there is never a moment without rules. Docker publishes container ports on its own, past this table; with the tunnel on, no container port is published.
