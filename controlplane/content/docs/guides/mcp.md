---
title: "AI agents (MCP)"
description: "Let Claude, Cursor and other agents run moin for you"
group: "Team"
order: 4
---

MCP lets an AI agent such as Claude or Cursor use moin for you: *"roll back my app and show me its logs"*. The control plane is an MCP server at `/mcp`, so Claude, Cursor, VS Code and any MCP client can connect. Almost every action is a tool; [Where things work](/docs/reference/surfaces) lists what stays in the web.

[Create an API token](/docs/guides/account#create-an-api-token), **Read and write** to change things or **Read only** to look, and put it where `pat_…` stands.

## Connect an agent

Claude Code:

```bash
claude mcp add --transport http moin https://moin.example.com/mcp \
  --header "Authorization: Bearer pat_…"
```

Cursor, in `.cursor/mcp.json`:

```json
{"mcpServers": {"moin": {"url": "https://moin.example.com/mcp", "headers": {"Authorization": "Bearer pat_…"}}}}
```

Any other client that speaks MCP over HTTP: the URL `https://moin.example.com/mcp` and the header `Authorization: Bearer pat_…`.

## Good to know

- No login tool and no browser login: the token is all an agent needs.
- A read-only token calls the read tools; changes need read and write.
- Tools that remove, replace or stop something are marked destructive, so clients ask first.
- Four cannot be undone: `remove_server`, `remove_app`, `remove_bucket` and `restore_app`. They also take `confirm`, the name of the server, app or bucket, and refuse without it.
- Tools take the app as `app` and the server as `server`; the older `name` still works for both, for now.

## Tools

| Tool | Does | Scope |
| --- | --- | --- |
| `whoami` | the account and token, with `email_alerts` | read |
| `set_email_alerts` | turns your [email alerts](/docs/guides/alerts) on or off | write |
| `list_alert_destinations` | where [alerts](/docs/guides/alerts#slack-discord-telegram-and-webhooks) go besides email, without URLs or tokens, and which fail | read |
| `add_alert_destination` | adds Slack, Discord, Telegram or a webhook; a webhook returns its `signing_secret` once | write |
| `test_alert_destination` | sends a test alert | write |
| `remove_alert_destination` | removes a destination | write, destructive |
| `get_status` | whether everything is OK: what needs you, catalog updates and counts | read |
| `list_servers` | servers with live status | read |
| `add_server` | adds a server, returns its install command | write |
| `remove_server` | removes a server; `confirm` is its name; returns the `uninstall_command` for the server, empty when no agent ever connected, and the `apps` that went with an offline server | write, destructive |
| `new_install_command` | a new one-time install command for a server | write, destructive |
| `rename_server` | renames a server | write |
| `update_agent` | puts the control plane's agent version on a server | write |
| `set_agent_updates` | turns automatic agent updates on or off | write |
| `get_version` | the control plane's version, the newest version of moin and whether agents update by themselves | read |
| `update_control_plane` | updates a self-hosted control plane to the newest version, for its admins | write |
| `list_apps` | apps with the live and the newest deploy | read |
| `get_app` | an app with its deploys and what was detected | read |
| `create_app` | creates an app on a server | write |
| `remove_app` | stops and removes an app, keeps its data; `confirm` is its name | write, destructive |
| `pin_app` | pins an app, or unpins it with `pinned` false | write |
| `get_deploy` | one deploy with its log | read |
| `redeploy_app` | runs the newest source or image again | write |
| `deploy_image` | deploys an image built in CI | write |
| `create_deploy_hook` | a new deploy hook URL for CI, shown once; the old one stops working | write, destructive |
| `remove_deploy_hook` | turns the deploy hook off; CI can no longer deploy | write, destructive |
| `list_registries` | registry logins, without passwords | read |
| `set_registry` | stores a registry login | write |
| `remove_registry` | removes a registry login | write, destructive |
| `rollback_app` | starts an earlier deploy again | write, destructive |
| `list_catalog` | the catalog with each app's newest version | read |
| `get_catalog_app` | a catalog app with every version | read |
| `install_catalog_app` | installs a catalog app on a server | write |
| `update_app` | sets a catalog app to a version and deploys it | write |
| `get_logs` | the last container lines | read |
| `exec_command` | [runs a command](/docs/guides/exec) in a container, no shell; the exit code and the last 64 KB of output (owner or admin) | write |
| `list_tables` | tables, row counts, schema and migration of the database | read |
| `query_database` | runs read-only SQL | write |
| `execute_sql` | runs SQL that changes data | write, destructive |
| `get_backups` | how each database is backed up, the daily check, and the moments to go back to | read |
| `restore_app` | goes back to a moment (`moment`, its id or label) or a time (`at`), to the minute; keeps a copy first, so it can be undone; `confirm` is the app's name. It runs on its own and answers its state; with `id` it tells how it stands | write, destructive |
| `download_backup` | a full copy of a database in the app's own bucket; call again with `id` until `state` is `ready`, then `link` works for 15 minutes | write |
| `list_buckets` | the buckets, without their secrets, with the apps that use each | read |
| `add_bucket` | adds a bucket; the first is the default | write |
| `edit_bucket` | changes a bucket; an empty `secret_key` keeps the stored one | write |
| `set_default_bucket` | makes a bucket the default | write |
| `remove_bucket` | removes a bucket nothing uses; `confirm` is the bucket's name | write, destructive |
| `set_app_bucket` | moves an app's uploads and backups to a bucket; the history starts again there | write |
| `check_domains` | whether each domain points at the server, and whether compose.yml sets it | read |
| `get_uptime` | uptime per domain over 24 hours, 7 and 30 days, the newest check and the last outages | read |
| `set_uptime_check` | the path of a domain's check, or pauses and resumes it | write |
| `get_metrics` | CPU, memory, disk and network of an app or a server, now and over 1h, 24h, 7d or 30d; for an app the deploys that went live, for a server its apps by memory | read |
| `set_domains` | domains and the canonical one, or with `service` the domains of another service | write |
| `get_instant` | the instant address, or why the app has none; `show` returns its password with a token that may write | read |
| `set_instant` | the provider of the instant address or off, and whether a password guards it | write |
| `set_protection` | a password in front of the app | write |
| `remove_protection` | makes the app public again | write, destructive |
| `restart_app` | starts the live deploy again with the current settings | write |
| `stop_app` | takes the app offline; its data stays | write, destructive |
| `start_app` | runs a stopped app again | write |
| `get_env` | env vars in plain text | write |
| `set_env` | sets and removes env vars | write |
| `replace_env` | replaces all env vars at once, like one `.env` file; keys not sent are removed; returns the keys added, changed and removed | write, destructive |
| `get_github` | GitHub installations and the install link | read |
| `list_branches` | the branches of a repo, the default first | read |
| `list_repos` | repos moin may read, and where on GitHub to give it more | read |
| `link_repo` | follows a repo and branch | write |
| `unlink_repo` | stops following it | write, destructive |
| `deploy_ref` | deploys a branch, tag or commit from GitHub | write |
| `get_integrations` | whether the Cloudflare token and Tailscale key are stored | read |
| `set_integrations` | stores them | write |
| `set_server_security` | Cloudflare Tunnel, SSH on Tailscale, firewall | write, destructive |
| `get_billing` | the subscription; on a self-hosted control plane it says billing is off | read |
| `list_workspaces` · `get_workspace` | the token's workspace with its team | read |
| `list_activity` | who did what, newest first, to the second; filter by `app`, `server` or `user` | read |
| `invite_member` | invites someone by email | write |
| `resend_invitation` | sends an invitation again | write |
| `revoke_invitation` | takes an invitation back | write, destructive |
| `rename_workspace` | renames the workspace | write |
