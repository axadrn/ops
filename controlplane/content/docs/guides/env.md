---
title: "Env vars"
description: "Encrypted settings and secrets per app"
group: "Apps"
order: 3
---

Env vars are the settings and secrets your app reads when it starts, such as `DATABASE_URL` or an API key. moin keeps them encrypted and hands them to your app on the next restart, start or deploy.

```text
moin env set KEY=value
   │
   ▼
control plane  saved encrypted;
   │           the change waits
   │
   │ moin restart, start or deploy
   ▼
agent          sets them on the
   │           app's services
   ▼
your app       reads KEY when it
               starts
```

They go to the services moin builds and the main one. An image you do not build, such as Postgres, takes them with `${NAME}`: [In compose.yml](#in-composeyml).

## Set an env var

```surfaces
Web: the app, **Environment**, **Set**
CLI: moin env set KEY=value
TUI: on the app, `e`, `a`, then type `KEY=value`
API: PATCH /api/v1/apps/{app}/env
MCP: set_env
```

```bash
moin env set KEY1=value KEY2=value
moin env set GREETING="hello world"
moin restart
```

Saving never restarts the app: change several, then [restart](/docs/guides/deploy#restart) once. The app page says how many changes wait. The API takes them as `set`.

## See the env vars

```surfaces
Web: the app's **Settings** tab, **Environment**
CLI: moin env
CLI: moin env --show
TUI: on the app, `e`; `v` shows the values
API: GET /api/v1/apps/{app}/env
MCP: get_env
```

## Remove an env var

```surfaces
Web: the app's **Settings** tab, **Environment**, **Show values**
CLI: moin env rm KEY
TUI: on the app, `e`, pick the key, `x`, then `y`
API: PATCH /api/v1/apps/{app}/env
MCP: set_env
```

The API and MCP take the names as `unset`.

## Edit them all as one text

Every env var as one `.env` file, `KEY=value` per line, so you can paste a whole file. Saving replaces all of them: a key missing from the text is removed. moin first shows the keys it adds, changes and removes, by name only, and asks.

```surfaces
Web: the app, **Environment**, switch **Fields** to **Text**, then **Review changes**
CLI: moin env push [file]
TUI: on the app, `e`, then `E`: the text opens in `$EDITOR`
API: PUT /api/v1/apps/{app}/env
MCP: replace_env
```

`moin env push` reads `.env` unless you name a file; `--yes` skips the question. The TUI opens `$VISUAL`, `$EDITOR` or `vi` on a file only you can read, deleted when the editor closes. The API and MCP take every env var as `env` and answer the keys `added`, `changed` and `removed`. The text takes `#` comments, `export KEY=value` and quoted values: in double quotes `\n` is a line break, in single quotes everything stays as it is.

## In compose.yml

`${NAME}` in `compose.yml` takes the env var from moin, like a `.env` next to the file. So an image you do not build, such as Postgres, gets its secrets too, and none lands in the repo:

```yaml
services:
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD}
```

A change to such a variable takes a deploy, not just a restart.

## Use them on your computer

```bash
moin env pull           # writes .env, readable only by you
moin run -- go run .    # runs a command with the env vars, without a file
```

## Good to know

- Reading values needs an API token with write access: reading a secret is as strong as writing it.
- An AI agent that reads or sets values over MCP sees them, and so does its model provider: give it only the secrets it needs.
- A rollback keeps the env vars of its deploy.
- Commands take the app from `--app`, from `x-moin.app` in [the repo](/docs/guides/config), or from the folder name.
