---
title: "Why moin"
description: "Small stack, an agent that only dials out, backups whose restores are checked"
group: "Start"
order: 3
---

moin is small, it never lets anyone into your server, and it keeps your data safe. Four things make it different: a small stack you can read, an agent that only calls out, backups you can trust, and a keyboard that reaches everything.

## A small stack

moin is two programs you can read, not a pile of tools.

| Part | What it is |
| --- | --- |
| `moin` | One Go binary: `moin` is the terminal UI, `moin <command>` the CLI, `moin agent` the agent on your servers. |
| Control plane | One more Go binary from the same repo: the web app, the API and the MCP server. |
| Web | Server-rendered HTML with templ and htmx. Tailwind is built by its standalone binary: no Node, no npm tree. |
| Database | SQLite, in a file next to the control plane. |

All of it is open source under AGPL-3.0, in one repo: [github.com/axadrn/moin](https://github.com/axadrn/moin).

## The agent only dials out

The agent on your server calls moin; moin never calls your server. Nothing on the server waits for moin, so it needs no open port for it, and moin holds no SSH key to it.

```text
SSH tools          moin

the tool           control plane
 │ holds a key      ▲
 │ to a root shell  │ the agent
 ▼                  │ calls out
┌──────────┐       ┌┴─────────┐
│ server   │       │ server   │
│ port 22  │       │ agent    │
│ is open  │       │ no port  │
│          │       │ for moin │
└──────────┘       └──────────┘
```

moin's requests come back down the line the agent opened, and the agent checks each one against your rules.

With the three switches of [Security](/docs/guides/security) on, the server can close almost everything:

| Switch | What closes |
| --- | --- |
| Cloudflare Tunnel | Ports 80 and 443: visitors come through a connection the server opens to Cloudflare. |
| SSH only on Tailscale | Port 22 for everyone outside your tailnet. |
| Firewall | Everything else. |

What stays open to everyone is `41641/udp`, Tailscale's port for direct connections. **What the agent set up** on the server's security page shows it.

A file on the server, `/etc/moin/rules`, decides what moin may do there: no data, no logs, no commands, deploys only from your repos, backups only to your bucket. Only root on the server changes it, so a break-in at moin cannot. [Rules](/docs/guides/security#rules).

## How others reach your servers

From each project's own docs, read November 7, 2026:

| Tool | How it reaches a server |
| --- | --- |
| [Coolify](https://coolify.io/docs/core/how-coolify-works) | "The Coolify instance connects to the selected server over SSH." Its [firewall page](https://coolify.io/docs/core/infrastructure/servers/firewall) lists port 22 (or your SSH port) open to Coolify. SSH can also run [through a Cloudflare Tunnel](https://coolify.io/docs/integrations/networking/cloudflare/tunnels/server-ssh), so the public port can close. |
| [Dokploy](https://docs.dokploy.com/docs/core/ssh-keys) | Remote servers over SSH, with a key you add in Dokploy, as root. |
| [Kamal](https://kamal-deploy.org/docs/configuration/ssh/) | "Kamal uses SSH to connect and run commands on your hosts", as root on port 22 by default, from the machine that runs `kamal`. A proxy or jump host works. |
| [Portainer Edge Agent](https://docs.portainer.io/advanced/edge-agent) | Dials out too: the agent polls the Portainer server and opens a tunnel to it on port 8000. |

In short: with SSH-based tools, whoever runs the tool holds a key that opens a shell on your server, and the server keeps SSH reachable for it. With moin, the control plane sends the agent requests, and the agent checks each one against your rules.

## Backups whose restores are checked

moin writes down every change of your database for 7 days, in your own bucket, so you can go back to any minute; a restore can be undone for 7 days. Every day moin restores the newest state into a throwaway copy and checks that the data came back, and an [alert](/docs/guides/alerts) tells you when the check fails. [How backups work](/docs/concepts/backups).

## Every way in

Use moin the way you like: click in the web, type in the terminal, script it with the CLI or the API, or let an AI agent do it through MCP. Each one does the same work, so you never get stuck in one of them. [Where things work](/docs/reference/surfaces) lists what each one does.

## Keyboard first

One box finds everything: `⌘K` in the web, `space` in the terminal UI. Type a few letters, even with a typo, and press `enter`. `j` and `k` move through lists, `?` shows every key. [Palette and keys](/docs/reference/palette).

## Under the hood

- The `moin` binary is Go's standard library plus a few modules: Bubble Tea and Lip Gloss for the terminal UI, `coder/websocket` for the agent's connection, `x/term`, the AWS S3 SDK for your bucket, Litestream and a pure-Go SQLite for backups.
- Built for Linux the way a release builds it (`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" ./cmd/moin`, Go 1.26.8), the binary is 25.8 MB for arm64 and 28.3 MB for amd64. Measured on November 7, 2026.
- The agent's call is one WebSocket connection from your server to the control plane, kept open.
- SQLite changes reach your bucket within about a second, Postgres changes within a minute.

## Next

- [Get started](/docs/getting-started)
- [Security](/docs/guides/security)
- [Self-hosting](/docs/self-hosting)
