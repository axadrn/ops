---
title: "Domains"
description: "The instant address, your own domains with HTTPS, the www redirect and a password"
group: "Apps"
order: 2
---

A domain is the name people type to reach your app, like `shop.example.com`. Every app gets one right after its first deploy, its instant address; add your own when you are ready. HTTPS is always on.

```text
browser   opens shop.example.com
   │
   ▼
DNS       shop.example.com is
   │      203.0.113.7: your A record
   ▼
server    203.0.113.7, port 443
   │
   ▼
Traefik   HTTPS with a Let's Encrypt
   │      certificate; finds the app
   │      by its domain
   ▼
app shop  on its port, 8080 unless
          you say so
```

DNS is the phone book of the internet: it turns a name into the server's address. Behind a [Cloudflare Tunnel](/docs/guides/security#cloudflare-tunnel), the browser reaches Cloudflare instead, and Cloudflare passes the visit to Traefik through the tunnel.

## Add a domain

Add one or several at once, such as the domain with and without `www.`:

```surfaces
Web: the app page, **Domains**, **Add a domain**, then **Add**
CLI: moin domains add <domain> www.<domain>
TUI: on the app, `o`, `a`, then type the domain
API: PUT /api/v1/apps/{app}/domains
MCP: set_domains
```

Then, where you bought the domain, add an **A record**: name the domain, value the server's IPv4 address. If the server has IPv6, add an **AAAA record** too. When you add a domain, moin says whether it already points at the server, or the record to set; **Check** next to the domain asks again later. Once DNS points at the server, usually after a few minutes, the domain gets HTTPS by itself. Behind a [Cloudflare Tunnel](/docs/guides/security#cloudflare-tunnel), moin sets the DNS for you when the domain is in your Cloudflare account.

Saving never restarts the app: a change of the domains, the password or the instant address waits for the next restart, start or deploy (before the first deploy, for that one), so you can make several at once. The app page and `moin apps <app>` say how many changes wait.

## Instant address

```text
https://<app>.<server IPv4 with dashes>.sslip.io
https://shop.203-0-113-7.sslip.io
```

The name carries the server's IP, so it needs no DNS, and Let's Encrypt gives it a certificate like any domain. A password guards it by default, user `moin`: the app's own [password](#protect-with-a-password) when it has one, else one moin made.

```surfaces
Web: the app, **Instant address**, **Show password**
CLI: moin instant --show
TUI: on the app, `o`: the instant address
API: GET /api/v1/apps/{app}/instant?show=true
MCP: get_instant
```

Switch the provider, drop the password, or turn the address off:

```surfaces
Web: the app, **Instant address**, **Provider** or the **Password** switch
CLI: moin instant nip.io, moin instant open, moin instant protect, moin instant off
TUI: on the app, `W`
API: PUT /api/v1/apps/{app}/instant
MCP: set_instant
```

The providers are `sslip.io` (the default) and `nip.io`: if one is down, switch to the other. It needs a public IPv4 and ports 80 and 443 open. A server behind a [Cloudflare Tunnel](/docs/guides/security#cloudflare-tunnel) or without a public IPv4 gets none, and moin says so: give that app a domain.

## Check the DNS

```surfaces
Web: the app page, **Domains**, **Check** next to the domain
CLI: moin domains
TUI: on the app, `o`
API: GET /api/v1/apps/{app}/domains
MCP: check_domains
```

```text
DOMAIN          DNS            RESOLVES TO
<domain>        ● points here  203.0.113.5
www.<domain>    ○ set DNS
```

A domain that resolves to either address of the server points here.

## Remove a domain

```surfaces
Web: the app, **Domains**, the × next to the domain
CLI: moin domains rm <domain>
TUI: on the app, `o`, pick the domain, `x`, then `y`
API: PUT /api/v1/apps/{app}/domains
MCP: set_domains
```

The web, the CLI and the TUI ask first; `--yes` skips it. The API and MCP take the domains the app keeps. Remove the DNS record too.

## www or not

Pick the main domain; the others redirect to it for good.

```surfaces
Web: the app page, **Domains**, **Canonical**
CLI: moin domains canonical <domain>
TUI: on the app, `o`, pick the domain, then `c`
API: PUT /api/v1/apps/{app}/domains
MCP: set_domains
```

`moin domains canonical none` turns the redirect off. The API and MCP take it as `canonical`.

## Protect with a password

A username and password in front of the app, for staging or a preview. Visitors see their browser's sign-in box. moin stores only a hash.

```surfaces
Web: the app page, **Domains**, **Ask for a password**, then **Set password**
CLI: moin protect --user <username>
TUI: on the app, `o`, `p`, then type `<username>:<password>`
API: PUT /api/v1/apps/{app}/protection
MCP: set_protection
```

The username is `moin` unless you pick one with `--user`. The CLI reads the password from stdin. To make the app public again:

```surfaces
Web: the app page, **Domains**, **Remove password**
CLI: moin protect off
TUI: on the app, `o`, `p`, then `y`
API: DELETE /api/v1/apps/{app}/protection
MCP: remove_protection
```

## Domains per service

An app with several services, such as `web` and `api`, can give each its own domains. The main service keeps the plain ones:

```surfaces
Web: the app page, **Domains**, the domain and its service, **Add**
CLI: moin domains add --service api api.<domain>
TUI: on the app, `o`, `a`, then type `api:api.<domain>`
API: PUT /api/v1/apps/{app}/domains with "service": "api"
MCP: set_domains with service
```

Each service gets its own port: `x-moin.ports`, else its `ports` or `expose`, then 8080. A domain reaches one service only. The password, the redirect and [uptime checks](/docs/guides/uptime) work per domain the same way.

## In the repo

Keep domains next to your code, in `compose.yml`:

```yaml
x-moin:
  domains: [<domain>, www.<domain>]
  canonical: <domain>
  protect: true   # the password itself is set in moin, never in the repo
  instant: nip.io # or sslip.io, off
```

For several services, `domains` takes a map and `ports` their ports:

```yaml
x-moin:
  service: web
  domains:
    web: [<domain>]
    api: [api.<domain>]
  ports:
    api: 5678
```

Fields in `x-moin` win over the settings in moin. Once a deploy with `x-moin.domains` is live, those domains show **from compose.yml** everywhere and change only in the file; trying it in moin says so (API code `domains_from_compose`). [Config in the repo](/docs/guides/config) lists every field.

## Good to know

- Each server runs one proxy, Traefik, started with the first app. It answers on ports 80 and 443, gets certificates from Let's Encrypt and sends HTTP to HTTPS.
- A change that changes nothing, like the same domains again, says "Nothing changed"; the API answers `"unchanged": true`.
- [Restart](/docs/guides/deploy#restart) applies every change that waits, without a build.
