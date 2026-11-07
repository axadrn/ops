---
title: "Servers"
description: "Add, watch, update and remove servers"
group: "Servers and data"
order: 1
---

A server is your computer on the internet, and the agent is the little helper on it that does the work. The agent only calls out, so the server needs no open door for moin. Any Linux machine at any provider works, amd64 or arm64, with systemd: Ubuntu and Debian are the easy choice.

## Add a server

Gives you one install command. Run it on the server as root within one hour.

```surfaces
Web: **Servers**, **Add server**
CLI: moin servers add web-1
TUI: opens the web: `w`, then `a`
API: POST /api/v1/servers
MCP: add_server
```

```bash
curl -fsSL https://moin.example.com/install.sh | sh -s -- ins_…
```

It installs Docker if it is missing, installs the agent as the `moin-agent` service and connects it.

- The name: 1 to 63 lowercase letters, digits or dashes, starting with a letter or digit, unique in the workspace. Leave it empty and moin picks one like `brave-otter`.
- Not root? Put `sudo` before `sh`. The command works once; if the install stops halfway, get [a new install command](#a-new-install-command).
- `MOIN_URL=http://127.0.0.1:8090` before `sh` makes the agent dial that address instead of the control plane's `APP_URL`, as on a server that runs the control plane too ([one server](/docs/self-hosting#one-server)).

## See your servers

Every server with its live status; lists update by themselves.

```surfaces
Web: **Servers**
CLI: moin servers
TUI: `s` on the overview
API: GET /api/v1/servers
MCP: list_servers
```

| Status | Means |
| --- | --- |
| **waiting** | the agent never connected: the install command did not finish |
| **online** | the agent is connected |
| **offline** | the connection dropped: the agent cannot reach moin |

A server offline for two minutes, and back online, mails the owners and admins ([Alerts](/docs/guides/alerts)). The API streams changes from `GET /api/v1/servers/events`.

## A new install command

The install command shows once; moin keeps only a fingerprint of its token. Lost it, or moving the agent to another machine? Get a new one. It works once within one hour; the old command and the old agent's token stop working.

```surfaces
Web: **Servers**, open the server, **New install command**
CLI: moin servers install-command web-1
TUI: opens the web: `w`, then `a`
API: POST /api/v1/servers/{id}/install
MCP: new_install_command
```

## Rename a server

Same rules as a new name. Its apps stay on it. A server added before the rule keeps its old name until you rename it.

```surfaces
Web: **Servers**, open the server, **Name**, **Save**
CLI: moin servers rename web-1 web-2
TUI: opens the web: `w`, then `a`
API: PATCH /api/v1/servers/{id}
MCP: rename_server
```

## Updates

The agents update themselves to the version of the control plane: one server at a time, never during a deploy, and your apps keep running while the agent restarts. If an agent does not come back, moin stops there and says so in the activity. Until then the server shows **update pending** next to its version. The control plane itself: we update the hosted one; for your own see [Self-hosting](/docs/self-hosting#updates).

```surfaces
Web: **Servers**, **Update agents automatically**
CLI: moin servers auto-update on|off
TUI: opens the web: `w`, then `a`
API: PUT /api/v1/agent-updates
MCP: set_agent_updates
```

Update by hand; without a name the CLI updates every online server with an older agent:

```surfaces
Web: **Servers**, open the server, **Update agent**
CLI: moin servers update web-1
TUI: on the server, `u`, then `y`
API: POST /api/v1/servers/{id}/update
MCP: update_agent
```

A server with rules in `/etc/moin/rules` never updates from moin: update it on the server ([CLI reference](/docs/reference/cli#agent-updates)).

## Remove a server

Disconnects the agent and makes its token stop working. It cannot be undone, so moin asks you to type the server's name: in the web dialog (with a copy button) and in the CLI; `--yes` skips it in scripts, an agent passes the name as `confirm`.

```surfaces
Web: **Servers**, open the server, **Remove server**
CLI: moin servers rm web-1
TUI: opens the web: `w`, then `a`
API: DELETE /api/v1/servers/{id}
MCP: remove_server
```

- **Online with apps**: refused. [Remove the apps](/docs/guides/deploy#remove-an-app) first, so nothing keeps running unseen.
- **Offline**: it goes with its apps, and every surface and the activity name them. The apps stay on the machine with their data if it comes back.

The agent stays on the machine. Every surface answers the command that removes it (none if the agent never connected); run it on the server as root:

```bash
moin agent uninstall          # the agent goes, the apps keep running
moin agent uninstall --apps   # the apps and their data go too, for good
```

| | `uninstall` | `uninstall --apps` |
| --- | --- | --- |
| `moin-agent` service, `moin` binary, agent state in `/var/lib/moin` | removed | removed |
| apps and their data in `/var/lib/moin/apps` | keep running | removed with containers, images and volumes |
| proxy, tunnel, firewall | stay | removed |
| `/etc/moin/rules`, Docker | stay | stay |

## Good to know

- **Disk space**: after every deploy and once a day the agent removes images no kept deploy or container uses, images without a name, and build cache older than a week. What a rollback needs, your volumes and anything moin did not put there stay. Its log says what it freed. Over 90% full anyway, moin [alerts](/docs/guides/alerts).
- **A new domain**: agents and the CLI that still use an old domain of the control plane move to the new one by themselves, once it answers.
- Next: close every port with the [Cloudflare Tunnel and the firewall](/docs/guides/security).

## Under the hood

The agent is the `moin-agent` service. On the server:

```bash
systemctl status moin-agent     # does the agent run?
journalctl -u moin-agent -f     # its log
```

It keeps its state in `/var/lib/moin`: its token in `agent.json` and every app under `apps/<app>`.
