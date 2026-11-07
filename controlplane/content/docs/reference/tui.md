---
title: "Terminal UI"
description: "Every view and key of moin without a command"
group: "Reference"
order: 4
---

The terminal UI is moin in your terminal: run `moin` in a repo for the daily work: what runs, deploys, logs, rollbacks, backups and restores, env vars, domains, uptime, metrics and commands, all live. Setup you do once opens the web app; [Where things work](/docs/reference/surfaces) says which.

## Start

| | |
| --- | --- |
| Sign in | without a token, `moin` shows a code and opens the browser to confirm it, then lands on the overview. It saves the token where `moin login` does, so you sign in once for both. `esc` quits the sign-in |
| Last seen | `moin` starts with what it showed last time, marked **last seen** in the header until the control plane answers; an app you come back to shows its tabs at once while they load again |
| Cache | in your cache folder, readable only by you, without env values, logs or the token; `moin logout` removes it |
| Screen | every view fills the screen, from 80 by 24 up. The header says where you are, the footer the keys you need most, and results (a few seconds) and errors (in red, a little longer) |
| Colour | only a status: green live, amber running, red failed, always with a dot and a word. It follows a dark or light terminal. With `NO_COLOR` or no colour, plain text, and `›` marks the selected row |

## Views

| View | Shows |
| --- | --- |
| Overview | apps live, servers online, backups checked and updates; your apps with their status, the servers with CPU and memory, the recent activity and what needs you |
| App | one app in seven tabs: deploys, logs, backups, env, domains, uptime, metrics |
| Deploy | the log of one deploy, live while it runs |
| Run | one command in a container of the app and what it writes |
| Servers | every server: status, system, agent, apps, access, last seen |
| Server | CPU, memory and disk over a range, what its agent set up, its rules and its apps |
| Activity | who did what in your workspace, newest first |
| Web app | the setup pages, to open in the browser |

## Everywhere

| Key | Does |
| --- | --- |
| `space` · `ctrl+k` · `/` | the palette, a box in the middle of the screen like `⌘K` on the web: finds every app, server, place and action and runs it. Type a few letters, like `shop logs` or `redeploy api` (words in any order, matched letters bold, the last pick first), move with `↑` `↓` or `ctrl+n` `ctrl+p`, `enter` runs it, `esc` closes it. With nothing typed, what you can do to the app or server you are on comes first. Every entry shows its key at the right, the one that does the same without the palette. `/` works everywhere but in the logs. [Palette and keys](/docs/reference/palette) |
| `esc` | back one level; closes what is open first |
| `?` | every key of this place and the ones that work everywhere; `↑` `↓` scroll them on a small screen |
| `q` | quits on the overview, goes back elsewhere |
| `ctrl+c` | quits from anywhere |
| `↑` `↓` · `j` `k` | move |
| `g` · `G` | the first row, the last row |
| `pgup` · `pgdown` | a page |
| mouse | the wheel scrolls what the arrows move; a click on an app's tab opens it; hold `shift` to select text |

## Questions and typing

| | |
| --- | --- |
| A question, like "Stop shop?" | `y` does it, `esc` cancels |
| A restore | cannot be undone: type the app's name, then `enter`; paste works |
| A line to type into | `enter` takes it, `esc` cancels, `ctrl+u` clears it, `ctrl+w` deletes the last word |
| A switch, a paused check, an env var | changes on the screen at once; if the control plane refuses, the footer says why in red and the tab loads what is true again |

## Overview

| Key | Does |
| --- | --- |
| `tab` · `shift+tab` | the next panel: apps, servers, what needs you; the one before |
| `enter` | opens the selected app, server or issue |
| `s` · `a` · `w` | the servers, the activity, the web app |
| `D` | deploys the directory `moin` started in to its app, after `y`; `moin deploy` creates a new app |

## App

| Key | Does |
| --- | --- |
| `d` `l` `b` `e` `o` `u` `m` | the tabs: deploys, logs, backups, env, domains, uptime, metrics |
| `tab` `→` · `shift+tab` `←` | the next tab, the one before |
| `R` | redeploys the newest source or image, after `y` |
| `T` | restarts the live deploy with today's env and domains, after `y` |
| `S` | stops the app, after `y`; starts a stopped one |
| `P` | rolls back to the deploy before the live one, after `y` |
| `I` | deploys an image built in CI: type it, then `enter` |
| `B` | deploys a branch, tag or commit of the linked GitHub repo; empty deploys the linked branch |
| `D` | deploys the directory `moin` started in, when it is this app, after `y` |
| `U` | updates a catalog app to its newest version, after `y` |
| `N` | backs up the app's databases now |
| `*` | pins the app, or unpins it: pinned apps stay above the others that run fine |
| `X` | [runs a command](/docs/guides/exec) in the main service, no shell; start the line with `@service` for another one |
| `W` | opens the app in the web app |

## App tabs

| Tab | Key | Does |
| --- | --- | --- |
| Deploys | `enter` | the log of the picked deploy, live while it runs |
| Deploys | `r` | rolls back to the picked deploy, after `y` |
| Logs | `/` · `c` | filters the lines while you type; clears the filter |
| Logs | `p` | pauses and resumes; the logs load every three seconds |
| Logs | `↑` `↓` · `G` | scrolls back; follows the end again |
| Backups | `enter` | goes back to the picked moment once you typed the app's name; **Before the restore at …** undoes a restore |
| Backups | `t` | goes back to a time you type, in UTC to the minute, like `14:01`, then the app's name |
| Backups | `c` | downloads a full copy: it opens the 15 minute link to your bucket in the browser |
| Env | `a` · `enter` | sets `KEY=value`; edits the picked one |
| Env | `x` | removes the picked one, after `y` |
| Env | `v` | shows or hides the values |
| Env | `E` | opens all env vars as one `.env` in `$EDITOR`, and asks before it saves what changed |
| Domains | `a` | adds a domain, or `service:domain` for another service of `compose.yml` |
| Domains | `x` | removes the picked one, after `y` |
| Domains | `c` | makes the picked domain the canonical one |
| Domains | `p` | password protection: `user:password`, or removes it after `y` |
| Uptime | `p` | pauses or resumes the check of the picked domain |
| Uptime | `c` | sets the path it checks, empty for the default |
| Uptime | `r` | checks again |
| Metrics | `[` `]` | the range: 1h, 24h, 7d, 30d |

## Deploy, run, servers, activity

| View | Key | Does |
| --- | --- | --- |
| Deploy | `↑` `↓` · `G` | scrolls back; follows the end again |
| Deploy | `r` | rolls back to this deploy, after `y` |
| Deploy | `o` | the app |
| Run | `enter` | runs the line; a first word `@service` picks the service |
| Run | `↑` `↓` · `G` | scrolls back; follows the end again |
| Servers | `enter` | opens the server |
| Servers, server | `u` | updates the agent to the control plane's version, after `y` |
| Server | `[` `]` | the range: 1h, 24h, 7d, 30d |
| Server | `enter` | opens the picked app |
| Server | `W` | its security page in the web app |
| Activity | `enter` | opens the app or server of the event; older events load as you move down |

## Web app

`w` on the overview, or `web app` in the palette, lists the pages that open in your browser, signed in as you. `enter` or the page's letter opens it.

| Key | Page |
| --- | --- |
| `a` | Servers: add, rename, order and remove servers |
| `p` | Apps: create, order and remove apps |
| `c` | Catalog |
| `t` | Team: invite people and change roles |
| `g` | Connect GitHub |
| `k` | Bucket: where backups and uploads go |
| `n` | Alerts: Slack, Discord, Telegram and webhooks |
| `d` | Data: browse and edit the rows of the open app's databases |
| `s` | Settings: your account, API tokens, registry logins |
| `b` | Billing, on moin.run |
