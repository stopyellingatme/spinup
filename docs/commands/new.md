# Create an application

`spinup new <name>` composes template packs and creates an application directory.
The default packs are `base`, `api`, `web`, and `ci`.
Pass `--api-only` to create the Go API without the web frontend.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls
README.md
api
deploy
spinup.lock
spinup.toml
```

The command creates `.env` from `.env.example` with private permissions.
The lock excludes `.env`.
The lock records all pinned stack images and the normalized hash of each rendered file.
The file count includes rendered files.
It excludes `.env` and `spinup.lock`.

| Flag | Purpose |
| --- | --- |
| `--api-only` | Create the Go API without a web frontend. |
| `--module <path>` | Set the Go module path. The API module adds `/api`. |
| `--modules <names>` | Select optional packs with comma-separated names. The default is `ci`. Use `--modules=''` for none. |
| `--db-version <major>` | Select PostgreSQL `18`, `17`, or `16`. The default is `18`. |
| `--shadcn-registry <version>` | Record the shadcn-svelte registry version of the web components. The default is `1.7.0`. |
| `--no-git` | Do not initialize a git repository. |
| `--dir`, `-C <directory>` | Create the app below this directory. The default is the current directory. |
| `--non-interactive` | Use defaults without prompts. |
| `--dry-run` | Print planned paths and file classes without writes. |
| `--json` | Print an object with `dir`, `packs`, and `files`. |
| `--env <name>` | Global flag. This command does not select an environment. |
| `--yes`, `-y` | Global flag. This command does not require confirmation. |
| `--verbose` | Enable extra diagnostics. |
| `--no-color` | Disable color. |
| `--help`, `-h` | Print command help. |

Every question from a public embedded pack becomes a flag.
Question keys use hyphens in flags.
The positional argument supplies `app_name`.
Explicit flags take precedence over prompts and defaults.
An interactive terminal receives prompts for missing answers.
Without a terminal, missing required answers produce an error that names the flag.

If `--module` is absent, the command proposes `example.com/<name>` and prints a warning when it uses that value.
The target directory must be absent or empty.
Unknown modules produce an error with the available modules.
If git is absent or a commit fails, generation continues with a warning.
Configure your git identity and commit the app manually.

For local kit development, set `SPINUP_KIT_REPLACE` to the kit directory.
The command adds an absolute `replace` directive to `api/go.mod` and prints a note.
The lock hashes the modified file.

Image builds require a published kit version without this local replacement.
The CLI sends no telemetry.
Set `SPINUP_NO_UPDATE_CHECK=1` to disable update checks.

```console
$ spinup new preview --api-only --module example.com/preview --no-git --non-interactive --dry-run
...
api/cmd/api/main.go (scaffold)
...
```

## Web app

Without `--api-only`, the app also gets the `web` pack: a SvelteKit 3 app in `web/`.
The app uses adapter-node, Tailwind CSS 4, and the shadcn-svelte components `button`, `card`, `input`, and `label`.
`deploy/Dockerfile.web` builds the production image.

```console
$ spinup new shop --module example.com/shop --no-git --non-interactive
...
$ ls shop/web
components.json
package.json
pnpm-lock.yaml
pnpm-workspace.yaml
src
tsconfig.json
vite.config.js
$ ls shop/web/src
app.css
app.d.ts
app.html
env.ts
hooks.server.ts
lib
routes
```

The lock records the file class of each web file:

- `managed`: `vite.config.js`, `tsconfig.json`, `pnpm-workspace.yaml`, `routes/healthz/+server.ts`, and `deploy/Dockerfile.web`. Upgrades keep them current.
- `scaffold`: the app shell, the hooks, `package.json`, and the API client. You own them, and upgrades merge changes into them.
- `seed`: `pnpm-lock.yaml`, `src/lib/api/schema.d.ts`, and the shadcn-svelte components. Upgrades do not change them.

The lock also records the `shadcn_registry` answer.
`spinup upgrade --ui` uses it to offer newer component versions.

```console
$ grep -o '"shadcn_registry": "[0-9.]*"' shop/spinup.lock
"shadcn_registry": "1.7.0"
```

Install the web dependencies before you use the app:

<!-- doctest: skip -->
```console
$ cd shop/web
$ pnpm install
$ pnpm check
$ pnpm build
```

See [the web app](../web.md) for the layout and for how the web app talks to the Go API.
