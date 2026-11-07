---
title: "GitHub"
description: "Deploy any branch, tag or commit without a checkout, and every push"
group: "Apps"
order: 4
---

Connect GitHub, and moin takes your code straight from it: link a repo to an app once, then deploy any branch, tag or commit with no checkout on your computer, and every push if you like. The pull request shows a green check, the commit status **moin/deploy**, when the deploy is live.

## Connect GitHub

Installs the moin GitHub App on your account or an organization. Once per workspace.

```surfaces
Web: **Settings**, **GitHub**, **Connect GitHub**; or the app's **Deploys** tab, **GitHub**, **Connect GitHub**
CLI: moin github
TUI: opens the web: `w`, then `g`
API: GET /api/v1/github
MCP: get_github
```

Connecting always happens on github.com: the CLI, the API and MCP give you the link, and GitHub sends you back. The same command shows where the app is installed and gives a link to install it on another account or organization. The app may only read the contents of the repos you choose and write commit statuses. It cannot push code.

## Link a repo

```surfaces
Web: the app's **Deploys** tab, **GitHub**, choose the repo and the branch, **Deploy**
CLI: moin link <owner>/<repo> --branch main
TUI: on the app, `W` opens its GitHub card in the web
API: PUT /api/v1/apps/{app}/repo
MCP: link_repo
```

Deploy every push is on by default. On the web, **Deploy** links and deploys in one step; elsewhere, link first and then deploy. The app needs no deploy before.

## Deploy from GitHub

```surfaces
Web: the app's **Deploys** tab, **GitHub**, type a branch, tag or commit or leave it empty, **Deploy**
CLI: moin deploy --ref <ref>
TUI: on the app, `B`, type a branch, tag or commit or nothing, then `enter`
API: POST /api/v1/apps/{app}/deploys
MCP: deploy_ref
```

```bash
moin deploy --ref main      # a branch
moin deploy --ref v1.4.0    # a tag
moin deploy --ref 3f2a9c1   # a commit
```

Empty means the linked branch. The API takes `{"ref": "v1.4.0"}`, or `{"ref": ""}`. Your server downloads the commit straight from GitHub with a short-lived token; the code never passes through moin. The deploy list shows the commit and its message, and the commit gets **pending**, then **success** or **failure** with a link to the app.

## Deploy every push

```surfaces
Web: the app's **Deploys** tab, **GitHub**, **Deploy every push**
CLI: moin link <owner>/<repo> --auto=false
TUI: on the app, `W` opens its GitHub card in the web
API: PUT /api/v1/apps/{app}/repo
MCP: link_repo
```

`--auto=false` turns it off, `moin link <owner>/<repo>` on again; the API and MCP call it `auto_deploy`. A push to another branch does nothing. A push while a deploy of the app runs is skipped; the next push, or **Deploy**, takes the newest commit.

## See the repos and branches

```surfaces
Web: the app, **GitHub**; the **Branch** select after you choose the repo
CLI: moin github repos
CLI: moin github branches <owner>/<repo>
TUI: on the app, `W` opens its GitHub card in the web
API: GET /api/v1/github/repos
API: GET /api/v1/github/repos/{owner}/{repo}/branches
MCP: list_repos
MCP: list_branches
```

Branches list the default one first. Your repo is missing? Under the list, every surface links to the page on github.com where you choose the repos moin may read. Add it there and come back: **Refresh** on the web, or list again.

## Unlink

The app stops following the repo and keeps running.

```surfaces
Web: the app's **Deploys** tab, **GitHub**, **Unlink**
CLI: moin unlink
TUI: on the app, `W` opens its GitHub card in the web
API: DELETE /api/v1/apps/{app}/repo
MCP: unlink_repo
```

## Good to know

- A ref is a branch like `main`, a tag like `v1.4.0` or a commit like `3f2a9c1`. A branch or ref name follows git's rules and has at most 255 characters; moin refuses others, such as one that starts with `-` or has `..`, and a branch the repo does not have.
- Already installed the app? Press **Connect GitHub** anyway: moin asks GitHub which accounts have it and connects them; if none has it, GitHub asks you to install it. An account connected to another workspace stays there until you disconnect it there.
- Linking the repo and branch an app follows already changes only Deploy every push, without asking GitHub.
- moin remembers the moment before a deploy from GitHub, like before every deploy, so a restore can go back to it.
- In the TUI, the app's header shows the repo and branch it follows, and `B` deploys from it.
- Without the GitHub App: [`moin deploy`](/docs/guides/deploy) uploads your folder through [your bucket](/docs/guides/bucket), and [CI](/docs/guides/ci) can build an image and call a deploy hook.
- A self-hosted control plane needs [your own GitHub App](/docs/self-hosting#your-own-github-app).
