---
title: "Deploy"
description: "Deploy, redeploy, restart, roll back and remove apps"
group: "Apps"
order: 1
---

A deploy puts a new version of your app live: v1, then v2, then v3. If it fails, the last good deploy keeps running.

## Deploy from a repo

Sends the code in your folder to the server, which builds and starts it. The first deploy creates the app, named after the folder.

```surfaces
CLI: moin deploy
TUI: `D` on the overview or the app
API: POST /api/v1/apps/{app}/uploads
API: POST /api/v1/apps/{app}/deploys
```

```bash
moin deploy                                                  # the app of this folder
moin deploy <app-name>                                       # a named app
moin deploy --server <server-name> --dir ./site <app-name>   # a new app on one of several servers, from another folder
```

No Dockerfile needed: moin uses your `compose.yml` or `Dockerfile`, or builds Node, Bun, Python, Ruby, PHP, Go and more by itself. Your app listens on the port in `PORT` (8080 unless your compose file or Dockerfile says otherwise). [How moin works](/docs/concepts/how-moin-works#how-moin-builds-a-repo) has the details; [Config in the repo](/docs/guides/config) changes the port, the health check and more.

### No Dockerfile

Without a `Dockerfile` or `compose.yml`, moin builds the repo with [Railpack](https://railpack.com). The deploy says what it found:

```text
→ Railpack: Node 24.22.1, start: npm run start
```

Set your own start command with the env var `RAILPACK_START_CMD`. `DATA_DIR=/data` is set. If Railpack cannot tell how to build or start the repo, the deploy fails with its reason: add a `Dockerfile` or a `compose.yml`.

## What a deploy does

It downloads the code, builds it on your server, starts it, checks it stays up and calls the health path; a failed start brings the last good deploy back.

```text
your code   from a folder, GitHub
   │        or CI
   ▼
build       on your server;
   │        v2 keeps running
   ▼
start       v3 takes v2's place
   │
   ▼
check       v3 runs 10 seconds
   │        without a restart; the
   │        health path, if set,
   │        answers
   ▼
live ✓      v3 is live

build fails: v2 never stopped.
start or check fails: v2 starts
again, v3 is marked failed.
```

Step by step: [What a deploy does](/docs/concepts/how-moin-works#what-a-deploy-does).

## See your apps

```surfaces
Web: **Apps**
CLI: moin apps
TUI: the overview, where `moin` starts
API: GET /api/v1/apps
MCP: list_apps
```

## Create an app

An empty app on a [server](/docs/guides/servers), ready for its first deploy. `moin deploy` does this by itself.

```surfaces
Web: **Apps**, **Add app**
CLI: moin apps add --server <server-name> <app-name>
TUI: opens the web: `w`, then `p`
API: POST /api/v1/apps
MCP: create_app
```

## Follow a deploy

```surfaces
Web: the app, **Deploys**, the deploy log
CLI: moin deploy
TUI: opens by itself when a deploy starts
API: GET /api/v1/apps/{app}/deploys/{number}/events
MCP: get_deploy
```

The web follows every new deploy by itself, also one started from the CLI. An agent calls `get_deploy` again until the deploy is done.

## See past deploys

Every deploy keeps its log, a failed one too. A failed log ends with the last lines of the service that stopped: start there.

```surfaces
Web: the app, **Deploys**, click a deploy
CLI: moin deploys <app-name>
CLI: moin deploys v3
TUI: on the app, `d`, pick the deploy, then `enter`
API: GET /api/v1/apps/{app}
API: GET /api/v1/apps/{app}/deploys/{number}
MCP: get_app
MCP: get_deploy
```

## Redeploy

Builds the newest code again from the copy the server kept, with the env vars as they are now. Nothing is uploaded. An image deploy pulls and runs the newest image again.

```surfaces
Web: the app, **Redeploy**
CLI: moin redeploy <app-name>
TUI: on the app, `R`, then `y`
API: POST /api/v1/apps/{app}/redeploy
MCP: redeploy_app
```

## Restart

Starts the live deploy again with the [env vars](/docs/guides/env), [domains and password](/docs/guides/domains) as they are now, without a build. Saving a setting never restarts an app: the changes wait until a restart applies them all. The app page and `moin apps <app>` say "2 changes waiting"; the API has them as `changes_waiting`.

```surfaces
Web: the app, **Restart**
CLI: moin restart <app-name>
TUI: on the app, `T`, then `y`
API: POST /api/v1/apps/{app}/restart
MCP: restart_app
```

If the images are gone from the server, after `docker image prune` for example, the restart builds them again from the source the deploy kept. If that is gone too, it says so: deploy again.

## Stop and start

Stop takes the app offline. It stays off, also after a reboot; its data and settings stay. Start runs it again, like a restart; a deploy starts it too.

```surfaces
Web: the app, **Stop** or **Start**
CLI: moin stop <app-name>
CLI: moin start <app-name>
TUI: on the app, `S` (stop asks `y`)
API: POST /api/v1/apps/{app}/stop
API: POST /api/v1/apps/{app}/start
MCP: stop_app
MCP: start_app
```

`moin stop` asks first; `--yes` skips it. Changes to the domains or the password of a stopped app wait until it starts. Starting an app that runs says it is already running.

## Roll back

Starts an earlier successful deploy again, with the env vars it had.

```surfaces
Web: the app, **Deploys**, **Roll back** on a deploy
CLI: moin rollback v3
TUI: on the app, `d`, pick the deploy, `r`, then `y`; `P` rolls back to the one before the live one
API: POST /api/v1/apps/{app}/rollback
MCP: rollback_app
```

A rollback is a new deploy, such as v4 with the note *rollback to v3*. `moin rollback` without a number takes the successful deploy before the live one. The API takes `{"number": 3}`. The web, the CLI and the TUI ask first; `--yes` skips it. The server keeps the last 5 builds to roll back to.

## Pin your apps

Pinned apps show on the overview right under what needs you. A pin is for the whole workspace.

```surfaces
Web: the star next to the app's name
CLI: moin apps pin <app-name>, moin apps unpin <app-name>
TUI: on the app, `*`
API: PUT /api/v1/apps/{app}/pin {"pinned": true}
MCP: pin_app
```

Everywhere, apps sort the same way: what needs you, then what deploys, then pinned apps, then the rest by name. The overview shows pinned apps and the ones deployed last; **All apps** lists every one.

## Remove an app

Stops the app and removes its containers and images. Its volumes and data stay on the server, its backups in your bucket until you delete them: [How long things stay](/docs/concepts/backups#how-long-things-stay).

```surfaces
Web: the app, **Remove app**
CLI: moin apps rm <app-name>
TUI: opens the web: `w`, then `p`
API: DELETE /api/v1/apps/{app}
MCP: remove_app
```

You type the app's name to confirm; `--yes` skips it in scripts, and an agent passes the name as `confirm`.

## Push to deploy with GitHub Actions

Deploys every push to `main`. It needs a [bucket](/docs/guides/bucket) and an API token with **Read and write** access, saved in the repo under **Settings**, **Secrets and variables**, **Actions** as `MOIN_TOKEN` ([create the token](/docs/guides/account#create-an-api-token)). Without CI, [GitHub](/docs/guides/github) deploys every push by itself; [Build in CI](/docs/guides/ci) builds the image in CI.

```yaml
name: deploy
on:
  push:
    branches: [main]
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: go install github.com/axadrn/moin/cmd/moin@latest
      - run: moin deploy
        env:
          MOIN_URL: https://moin.example.com
          MOIN_TOKEN: ${{ secrets.MOIN_TOKEN }}
```

## Good to know

- Commands on an app take its name as the last word. Without it, moin uses the app of your folder: `app` in [x-moin](/docs/guides/config), or the folder's name.
- The web and MCP cannot reach files on your computer: they deploy from [GitHub](/docs/guides/github) or an [image](/docs/guides/ci). In the API, upload first, then start the deploy.
- moin never stores your code: the archive passes through your bucket and is deleted once the server has it.
- Your data survives every deploy: mounted folders like `./data` move to `/var/lib/moin/apps/<app>/volumes/` and stay. [Your data survives every deploy](/docs/concepts/how-moin-works#your-data-survives-every-deploy).
- The Railpack result shows on the app page, in `moin apps <app-name>`, the TUI, the API and `get_app`.
