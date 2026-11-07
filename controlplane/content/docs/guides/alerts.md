---
title: "Alerts"
description: "A mail, Slack, Discord, Telegram or a webhook when a site goes down, a server goes offline, a deploy fails, backups stop or a disk fills up"
group: "Every day"
order: 7
---

When something breaks, moin tells you: the owners and admins of the workspace get a mail. Nothing to set up. Add Slack, Discord, Telegram or a webhook to tell the team where it already is.

```text
🔴 shop.example.com is down
App: shop
Since: 2026-11-03 14:05 UTC
Reason: HTTP 502
Open in moin
```

## What sends an alert

One alert per event, never one per minute, and one when it is fixed. Each links to its page in moin.

| Event | When |
| --- | --- |
| `uptime.down`, `uptime.up` | a site goes down, with the reason such as `HTTP 502`; back up, with how long it was down ([Uptime](/docs/guides/uptime#down-and-back-up)) |
| `server.down`, `server.up` | an agent has not answered for two minutes, with the apps on the server; back online |
| `deploy.failed` | a deploy fails, with its number, the error and the version that runs now ([Deploy](/docs/guides/deploy#what-a-deploy-does)) |
| `backup.failed`, `backup.recovered` | the backup of a database has not reached the bucket for three minutes, or its history lost its place and starts again; and when it works again. What is in the bucket stays ([Backups](/docs/guides/backups)) |
| `check.failed`, `check.recovered` | the daily check could not go back to the newest state; passes again |
| `disk.full`, `disk.ok` | the data disk of a server is over 90%; under 85% again |
| `update.available` | a newer version of a [catalog](/docs/guides/catalog) app is out, once per version |

## Who gets it

Every **owner** and **admin** whose **Email me alerts** is on, at the verified address of their account. On by default. Members get no mail.

```surfaces
Web: **Settings**, **Account**, **Email me alerts**
CLI: moin alerts off, then moin alerts on; moin alerts shows it
TUI: opens the web: `w`, then `s`
API: PUT /api/v1/account/email-alerts {"on": false}
MCP: set_email_alerts
```

It counts for you in every workspace. `GET /api/v1/account` and `whoami` return it as `email_alerts`.

## Slack, Discord, Telegram and webhooks

A workspace sends its alerts to up to 20 destinations, each with all events or the ones you pick.

### Add a destination

```surfaces
Web: **Settings**, **Alerts** (`g` then `l`), **Add a destination**
CLI: moin alerts add slack team-chat, then the URL on stdin; moin alerts add telegram phone --chat -1001234567890, then the bot token; --events deploy.failed,uptime.down picks events
TUI: opens the web: `w`, then `n`
API: POST /api/v1/alert-destinations {"kind": "slack", "name": "team-chat", "url": "https://hooks.slack.com/services/…"}
MCP: add_alert_destination
```

| Kind | What it needs |
| --- | --- |
| **Slack** | an [incoming webhook](https://api.slack.com/messaging/webhooks) of the channel, `https://hooks.slack.com/services/…` |
| **Discord** | the channel's **Edit Channel**, **Integrations**, **Webhooks**, **New Webhook**, **Copy Webhook URL**, `https://discord.com/api/webhooks/…`; messages never ping anyone |
| **Telegram** | a bot from [@BotFather](https://t.me/BotFather) in the chat: its token, like `123456:ABC-DEF…`, and the chat ID, like `-1001234567890` for a group or channel, or `@channelname` for a public channel |
| **Webhook** | any `http` or `https` URL of your own, see [Webhooks](#webhooks) |

### Send a test

```surfaces
Web: **Settings**, **Alerts**, **Send test** next to the destination
CLI: moin alerts test team-chat
TUI: opens the web: `w`, then `n`
API: POST /api/v1/alert-destinations/team-chat/test
MCP: test_alert_destination
```

moin waits up to 10 seconds. A refused test says why, such as `HTTP 404: no_service`; the API answers `424` with `alert_send_failed`.

### Remove a destination

```surfaces
Web: **Settings**, **Alerts**, **Remove** next to the destination
CLI: moin alerts rm team-chat
TUI: opens the web: `w`, then `n`
API: DELETE /api/v1/alert-destinations/team-chat
MCP: remove_alert_destination
```

To change one, remove it and add it again.

## Webhooks

Each alert comes as a JSON `POST`; fields that do not belong to the event are empty:

```json
{
  "event": "uptime.down",
  "workspace": "acme",
  "app": "shop",
  "server": "web-1",
  "domain": "shop.example.com",
  "reason": "HTTP 502",
  "since": "2026-11-03T14:05:00Z",
  "duration": "",
  "url": "https://moin.example.com/app/acme/apps/shop/uptime"
}
```

`event` is one of the events above, or `test`. `reason` is why a site is down or the error of a failed deploy; `since` when a site or server went down; `duration` how long, like `4m30s`.

A new webhook comes with a **signing secret**, shown once. Every post carries `X-Moin-Signature: sha256=` and the hex HMAC-SHA256 of the body. Check it:

```go
mac := hmac.New(sha256.New, []byte(secret))
mac.Write(body)
ok := hmac.Equal([]byte(r.Header.Get("X-Moin-Signature")), []byte("sha256="+hex.EncodeToString(mac.Sum(nil))))
```

Answer with any `2xx`; moin follows no redirects. A lost secret cannot be shown again: remove the webhook and add it again.

## Good to know

- The team sees the destinations; only owners and admins add, test and remove them.
- A destination that got every event keeps getting new kinds of events too.
- Each destination gets 10 seconds per alert. One that fails is marked **failing** with the reason on every surface, once in the activity; the next alert or test that works clears it. Mails go out either way.
- URLs and bot tokens are stored encrypted and never shown again in full, only the host and the last characters, like `hooks.slack.com/…WXYZ`. They never go in the activity.
- The hosted control plane posts only to public addresses.
- A disk at 88% does not alert back and forth: it alerts over 90% and clears under 85%.
