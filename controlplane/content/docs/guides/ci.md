---
title: "Build in CI"
description: "Build images in GitHub Actions or any CI and deploy them with a hook"
group: "Apps"
order: 5
---

CI is a computer elsewhere, such as GitHub Actions, that builds your app for you. It packs the app into an image, a box ready to run, and puts it on a registry, a shelf for images; moin deploys it. The server only downloads and starts it. Good for heavy builds, build caches and many servers. Deploy the repo once with `moin deploy` first: an image deploy keeps that compose file and swaps only the image of the main service.

```text
git push
   │
   ▼
CI         builds the image and
   │       puts it on a registry
   │
   │ calls the deploy hook
   ▼
moin       tells the agent
   │
   ▼
agent      pulls the image from
   │       the registry
   ▼
live ✓     started and checked
```

The deploy hook is a secret URL: when CI calls it, moin deploys the image.

## 1. Save a registry login

Only for private images. For GitHub Container Registry, use a token with `read:packages`.

```surfaces
Web: **Settings**, **Account**, **Tokens** tab, **Registries**, **Save login**
CLI: moin registry set ghcr.io <username>
TUI: opens the web: `w`, then `s`
API: PUT /api/v1/registries/{host}
MCP: set_registry
```

The CLI reads the token from stdin. It is stored encrypted, and the agent gets it only for the pull, in a throwaway Docker config; nothing stays on the server.

## 2. Create a deploy hook

A secret URL that deploys an image when CI calls it. moin shows it once: store it as the CI secret `MOIN_DEPLOY_HOOK`.

```surfaces
Web: the app's **Deploys** tab, **Deploy from CI**, **New deploy hook**
CLI: moin hook <app-name>
TUI: on the app, `W` opens **Deploy from CI** in the web
API: POST /api/v1/apps/{app}/hook
MCP: create_deploy_hook
```

A new hook stops the old one, so moin asks first when there is one; `--yes` skips it.

## 3. The workflow

```yaml
name: deploy
on:
  push:
    branches: [main]
permissions:
  contents: read
  packages: write
jobs:
  deploy:
    runs-on: ubuntu-24.04-arm
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/build-push-action@v6
        with:
          push: true
          platforms: linux/arm64
          tags: ghcr.io/${{ github.repository }}:${{ github.sha }}
      - run: |
          curl -fsS -X POST \
            "$MOIN_DEPLOY_HOOK?image=ghcr.io/${{ github.repository }}:${{ github.sha }}"
        env:
          MOIN_DEPLOY_HOOK: ${{ secrets.MOIN_DEPLOY_HOOK }}
```

Build for the arch of your server (`moin servers` shows it). The hook answers `202` with the deploy; follow it on the web, in the TUI or with `moin logs`.

## Or use the moin action

Installs `moin` from your control plane and runs `moin deploy` with a personal API token. With `image` it deploys that image; without, it puts the repo into [your bucket](/docs/guides/bucket) and your server builds it. The step waits until the deploy is live and fails the job when it fails.

```yaml
      - uses: axadrn/moin@main
        with:
          url: https://moin.example.com
          token: ${{ secrets.MOIN_TOKEN }}
          image: ghcr.io/${{ github.repository }}:${{ github.sha }}
```

## Deploy an image by hand

```surfaces
Web: the app's **Deploys** tab, **Deploy from CI**, **Deploy image**
CLI: moin deploy --image ghcr.io/<owner>/<image>:<tag> <app-name>
TUI: on the app, `I`, type the image, then `enter`
API: POST /api/v1/apps/{app}/deploys
MCP: deploy_image
```

The API takes `{"image": "ghcr.io/<owner>/<image>:<tag>"}`. A redeploy of an image deploy pulls the same image again.

## Turn the deploy hook off

The hook URL stops working, so CI can no longer deploy. Do it when you stop using CI or the URL leaked; a new hook can be made any time.

```surfaces
Web: the app, **Deploy from CI**, **Deploy hook**, **Turn off**
CLI: moin hook off <app-name>
TUI: on the app, `W` opens **Deploy from CI** in the web
API: DELETE /api/v1/apps/{app}/hook
MCP: remove_deploy_hook
```

It works also when the plan allows no changes. `get_app` and `GET /api/v1/apps/{app}` say in `hook` whether the app has one.

## Registry logins

```surfaces
Web: **Settings**, **Account**, **Tokens** tab, **Registries**; **Remove** on a login
CLI: moin registry
CLI: moin registry rm ghcr.io
TUI: opens the web: `w`, then `s`
API: GET /api/v1/registries
API: DELETE /api/v1/registries/{host}
MCP: list_registries
MCP: remove_registry
```

The list never shows passwords. Without a login, servers can no longer pull private images from that registry.

## Good to know

- `moin deploy --image` also works after one `moin deploy` of the repo, whose compose file it reuses.
- Health check, the fallback to the last good deploy, `moin rollback` and env vars work the same as for a server build.
- Public images need no registry login.
