---
title: new — create an app
parent: Command guides
---

# new — create an app

`spinup new <name>` makes a new directory with a Go API, a SvelteKit web app, PostgreSQL, deploy files, and CI.
Use it once, at the start of a project.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls
README.md
api
deploy
renovate.json
spinup.lock
spinup.toml
```

`--api-only` leaves out the web app. Leave it off to get the full stack.
At the end, the command prints the next steps and the local URLs:

```text
Next:
  cd todo
  spinup doctor
  cd web && pnpm install
  spinup dev

App      http://localhost:5173
API      http://localhost:8080
Mailpit  http://localhost:8025
```

The `App` line appears only when the app has a web app.
The install line follows your package manager, and an API-only app or an embed-mode app has none.
The ports are the defaults. `spinup dev` picks other ports when these are busy.

## What you get

The command writes 43 files for an API-only app and about 160 with the web app. The most important are:

| Path | What it holds |
| --- | --- |
| `spinup.toml` | The settings of your app. You edit this file. |
| `spinup.lock` | A record of what spinup made. spinup edits this file. |
| `api/` | The Go API, the SQL migrations, and the OpenAPI contract. |
| `web/` | The SvelteKit app. |
| `deploy/` | Docker Compose, Caddy, and the Dockerfiles. |
| `.env` | Your local settings. The command makes it from `.env.example`, and Git ignores it. |
| `renovate.json` | Settings for Renovate, which proposes dependency updates. It leaves `.github/workflows/` to `spinup upgrade`. |

[Project layout](../get-started/project-layout.md) shows the whole tree and says who edits which file.

## Answer the questions in a prompt or with flags

In a terminal, the command asks for each answer that you did not give.
Every question has a flag, so you can skip the prompts. Without a terminal, the command never prompts.
It fails and names the missing flag.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
```

If you leave out `--module`, the command uses `example.com/<name>` and prints a warning.
The target directory must not exist, or it must be empty.

## Common recipes

Each recipe changes one choice. You can combine them.

Pick the PostgreSQL version with `--db-version`. The choices are `18` (default), `17`, and `16`:

```console
$ spinup new pg17 --api-only --module example.com/pg17 --db-version 17 --no-git --non-interactive
...
```

Serve the web app from the Go binary with `--frontend-mode embed`. The default is `node`, a SvelteKit server.
Read [Embed mode](../embed.md) first.

```console
$ spinup new site --module example.com/site --frontend-mode embed --no-git --non-interactive
...
```

Use bun instead of pnpm with `--package-manager bun`. Use the heyapi client with `--api-client heyapi`.
Read [Package manager and API client](../web-variants.md) for the trade-offs.

```console
$ spinup new fast --module example.com/fast --package-manager bun --api-client heyapi --no-git --non-interactive
...
```

Turn on optional modules with `--modules`. The default is `ci,deploy`. Use `--modules=''` for none.
Run `spinup module list` to see the names.

```console
$ spinup new blog --api-only --module example.com/blog --modules ci,deploy,secrets --no-git --non-interactive
...
```

Create the app in another directory with `--dir` (short form `-C`):

```console
$ mkdir projects
$ spinup new todo --dir projects --api-only --module example.com/todo --no-git --non-interactive
...
$ ls projects
todo
```

Add a [pack plugin](../plugins.md) with `--plugin <source>`. Repeat the flag for more sources.
Add `--trust` to trust the source without a prompt.

<!-- doctest: skip -->
```console
$ spinup new todo --plugin path:./plugins/example --trust --module example.com/todo
```

## Preview and script the result

Use `--dry-run` to list the files and their [classes](#file-classes) without writing anything:

```console
$ spinup new preview --api-only --module example.com/preview --no-git --non-interactive --dry-run
...
api/cmd/api/main.go (scaffold)
...
```

Use `--json` to get a result that a script can read.
The `files` value is the number of files that the command wrote. It does not count `.env` and `spinup.lock`.

```console
$ spinup new scripted --api-only --module example.com/scripted --no-git --non-interactive --json
...
```

The output looks like this:

```json
{
  "dir": "/home/you/scripted",
  "packs": ["base", "api", "ci", "deploy"],
  "files": 43
}
```

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `--api-only` | Create the Go API without a web app. | `--api-only` |
| `--module <path>` | Set the Go module path. The API module adds `/api`. | `--module github.com/acme/todo` |
| `--modules <names>` | Choose optional modules, separated by commas. Default: `ci,deploy`. | `--modules ci,deploy,secrets` |
| `--frontend-mode node\|embed` | Serve the web app from SvelteKit (`node`) or from the Go binary (`embed`). | `--frontend-mode embed` |
| `--package-manager pnpm\|bun` | Choose the web package manager. | `--package-manager bun` |
| `--api-client openapi-fetch\|heyapi` | Choose the generated web API client. | `--api-client heyapi` |
| `--auth-oidc` | Turn on sign-in with an identity provider. | `--auth-oidc` |
| `--db-version <major>` | Choose PostgreSQL `18`, `17`, or `16`. Default: `18`. | `--db-version 17` |
| `--monitoring-profile <name>` | Choose `lite`, `full`, or `none` for the monitoring module. Default: `lite`. | `--monitoring-profile full` |
| `--ci-deploy-on-push` | Make the CI deploy the default environment on each push to `main`. | `--ci-deploy-on-push` |
| `--shadcn-registry <version>` | Record the version of the shadcn-svelte components. Default: `1.7.0`. | `--shadcn-registry 1.7.0` |
| `--plugin <source>` | Add a pack plugin source. Repeat the flag for more. | `--plugin path:./plugins/example` |
| `--trust` | Trust the plugin sources and their hashes. | `--trust` |
| `--no-git` | Do not start a Git repository. | `--no-git` |
| `--dir <directory>`, `-C` | Create the app below this directory. | `-C projects` |
| `--non-interactive` | Use the defaults. Never prompt. | `--non-interactive` |
| `--dry-run` | Print the files without writing. | `--dry-run` |
| `--json` | Print the result as JSON. | `--json` |

[Global flags](../reference/global-flags.md) such as `--env` and `--yes` do not change what `new` does.
`--auth-oidc` needs the `auth` module. Read [Sign in with an identity provider](../build/oidc.md).

If Git is missing, or the first commit fails, the command still creates the app and prints a warning.
Set your Git identity and make the first commit yourself.

The CLI sends no telemetry. To turn off the notice about a new release, set `SPINUP_NO_UPDATE_CHECK=1`. See [`self-update`](self-update.md).

## File classes

Every file that `spinup new` writes has a class. The class decides what `spinup upgrade` does with the file.
`--dry-run` and `spinup.lock` show the class of each file.

| Class | Who owns it | What `spinup upgrade` does | Example |
| --- | --- | --- | --- |
| `managed` | spinup | Replaces the file with the new template. | `deploy/Dockerfile.api`, `web/vite.config.js` |
| `scaffold` | You | Merges template changes into your edits. | `api/cmd/api/main.go`, `web/package.json` |
| `seed` | You | Never touches the file again. | `web/pnpm-lock.yaml`, the first migration |

If you must edit a managed file, run [`spinup eject`](eject.md) first.
[Concepts](../concepts.md) explains the idea in full.

## The web app

Without `--api-only`, the app gets a SvelteKit 3 app in `web/`.
It uses Tailwind CSS 4 and the shadcn-svelte components `button`, `card`, `input`, and `label`.
`deploy/Dockerfile.web` builds the production image.

```console
$ spinup new shop --module example.com/shop --no-git --non-interactive
...
$ ls shop/web/src
app.css
app.d.ts
app.html
env.ts
hooks.server.ts
instrumentation.server.ts
lib
routes
```

The lock keeps the registry version in the `shadcn_registry` answer.
`spinup upgrade --ui` uses it to offer newer component versions.

```console
$ grep -o '"shadcn_registry": "[0-9.]*"' shop/spinup.lock
"shadcn_registry": "1.7.0"
```

Install the web packages before you use the app:

<!-- doctest: skip -->
```console
$ cd shop/web
$ pnpm install
$ pnpm check
$ pnpm build
```

[The web app](../web.md) explains the layout and how the web app talks to the API.

## Use a local kit checkout

This section is for people who work on spinup itself.
Set `SPINUP_KIT_REPLACE` to a checkout of the kit, and `new` adds a `replace` line to `api/go.mod`.
The command prints a note when it does this.

<!-- doctest: skip -->
```console
$ SPINUP_KIT_REPLACE=$HOME/code/spinup/kit spinup new todo --api-only --module example.com/todo
```

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `cannot create app ... target directory is not empty` | The directory already holds files. | Pick another name, or empty the directory. |
| The command asks for a flag and stops. | There is no terminal, and an answer is missing. | Add the flag that the error names, or add `--non-interactive`. |
| `unknown module "nope"` | A name in `--modules` does not exist. | Run `spinup module list`, or use the names in the error. |
| A Git warning at the end. | Git is missing or has no identity. | Set `user.name` and `user.email`, then commit by hand. |

## See also

- [Your first app](../get-started/first-app.md) walks through `new`, `dev`, and a first resource.
- [Project layout](../get-started/project-layout.md) explains each folder.
- [`dev`](dev.md) starts the app you just made.
- Flag reference: [`new`](../site/commands/new.md)
