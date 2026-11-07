---
title: "FAQ"
description: "Common questions, short answers"
group: "Reference"
order: 6
---

The questions people ask most, each with a short answer and a link to the page that says more.

## Does my server need open ports?

Not for moin: the agent dials out. Visitors reach your apps on ports 80 and 443, and with a [Cloudflare Tunnel](/docs/guides/security#cloudflare-tunnel) not even those are open.

## Does moin build on its own servers?

No. Builds run on your server: `moin deploy` sends your code through your own bucket, a GitHub deploy goes from GitHub straight to your server. [How moin works](/docs/concepts/how-moin-works).

## How is moin different from Coolify, Kamal or Heroku?

moin holds no SSH key to your servers, your code and backups stay in your own bucket, and every restore is checked every day. [Why moin](/docs/why-moin) compares them fact by fact.

## Do I need a domain?

Not to start. Every app answers at its [instant address](/docs/guides/domains#instant-address), like `https://shop.203-0-113-7.sslip.io`, with HTTPS and a password in front. It needs ports 80 and 443, so a server behind a Cloudflare Tunnel has none.

## Can I install apps like n8n or Umami?

Yes: 47 open source apps from the [catalog](/docs/guides/catalog), in one click, with their secrets made, their database backed up and an alert when a new version is out.

## What happens when a deploy fails?

The previous deploy starts again and the failed one says why. `moin deploys v4` shows its log. [Deploy](/docs/guides/deploy).

## Where is my data?

On your server: bind mounts under `/var/lib/moin/apps/<app>/volumes`, named volumes in Docker. Removing an app keeps both.

## Can moin see my data?

Only what you open, passed through to you and never stored. `data: off` and `logs: off` in `/etc/moin/rules` on your server close even that, and nobody can change the file through moin. [Rules](/docs/guides/security#rules).

## Where are my env vars?

In the control plane, sealed with AES-256-GCM, and handed to your server with each deploy. Next they move to your servers only, then to end-to-end encryption: [Roadmap](/docs/roadmap).

## What is a workspace?

Your team in moin: its servers, apps, bucket, settings and people. The plan, the trial and the [activity](/docs/guides/activity) belong to it, and you can be in several. [Team](/docs/guides/team).

## Do I pay per workspace?

Yes, one price per workspace with unlimited servers, apps and teammates. A second workspace has its own plan; the free trial comes once, with your first.

## How does the hosted trial work?

Your first workspace gets 30 days with everything, no card. Then it is read-only until you subscribe: your apps and their backups keep running, and you can still see everything, stop and start apps and manage your account and team.

## Can an AI agent do everything?

Almost: every action on servers, apps, data, backups, domains and invitations is an [MCP tool](/docs/guides/mcp). Your account, roles and deleting a workspace stay on the web, so a leaked token can never take them over.

## Can my team work on it too?

Yes, at no extra cost. [Invite them](/docs/guides/team#invite-someone) with `moin team invite` or from **Team**; the [activity](/docs/guides/activity) shows who changed what.

## Is moin open source?

Yes, all of it, under AGPL-3.0 at [github.com/axadrn/moin](https://github.com/axadrn/moin): the `moin` binary and the control plane. [Host it yourself](/docs/self-hosting), free.

## Why AGPL, and can I contribute?

AGPL keeps moin open: whoever runs a changed moin as a service shares the changes. Open an issue first, then a pull request; on your first one you agree once to the [Contributor License Agreement](https://github.com/axadrn/moin/blob/main/CLA.md) (your contribution comes in under Apache 2.0, you keep your copyright). [CONTRIBUTING.md](https://github.com/axadrn/moin/blob/main/CONTRIBUTING.md).

## Why the name?

**moin** /mɔɪn/ is Hamburg for hello, at any hour. Hamburg is a container port: millions of boxes, packed once, shipped anywhere, nothing goes missing. Your apps are containers too, and moin ships them to your servers and keeps every change to their data.
