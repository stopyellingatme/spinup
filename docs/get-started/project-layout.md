---
title: Project layout
parent: Get started
nav_order: 3
---

# Project layout

This page tells you what each folder of a generated app holds, and who edits it: you, or spinup.
Read it when you wonder "where does this go?" or "is it safe to edit this file?".

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls -A | LC_ALL=C sort
.env
.env.example
.gitattributes
.github
.gitignore
README.md
api
deploy
renovate.json
spinup.lock
spinup.toml
web
```

## The tree

```text
todo/
├── spinup.toml          your app settings (you edit)
├── spinup.lock          what spinup made, with hashes (spinup edits)
├── .env, .env.example   local settings and secrets for development
├── .spinup/             local state of the tools (made by `spinup dev`, not committed)
├── .github/workflows/   CI and deploy workflows
├── api/                 the Go API
│   ├── cmd/api/main.go      the entry point, where you wire the app
│   ├── internal/routes/     the route table
│   ├── internal/handlers/   HTTP handlers (made by `g resource`)
│   ├── internal/store/      Go code that sqlc writes from your queries (made by `g resource`)
│   ├── internal/gen/        Go code that oapi-codegen writes from the contract
│   ├── db/migrations/       SQL migrations, in order
│   ├── db/queries/          SQL queries, one file for each resource
│   ├── db/seeds/            SQL files for `spinup db seed`
│   └── openapi/             the OpenAPI contract: paths/ and schemas/
├── web/                 the SvelteKit app
│   ├── src/routes/          pages: one folder for each URL
│   ├── src/lib/             shared code, the API client, the UI components
│   └── e2e/                 browser tests
├── deploy/              Docker Compose, Caddy, Dockerfiles, one file for each environment
├── services/            sidecar services (made by `g service`)
└── secrets/             encrypted secrets (made by the `secrets` module)
```

Some folders appear only when you need them. `services/` appears with your first `spinup g service`.
`secrets/` appears with the `secrets` module. A project made with `--api-only` has no `web/`.
The files `renovate.json` and `.gitattributes` at the root keep dependencies current and line endings the same on every system.

### Folders that a module adds

| Module | What it adds | Example |
| --- | --- | --- |
| `auth` | `api/internal/roles/`, the pages `login`, `register`, and `account` in `web/src/routes/`, and `web/src/lib/server/auth.ts` | `spinup module add auth` |
| `jobs` | `api/cmd/worker/` and `api/internal/jobs/` | `spinup module add jobs` |
| `mail` | `api/internal/mail/` | `spinup module add mail` |
| `monitoring` | `monitoring/` with the Grafana, Prometheus, Loki, and Alloy files | `spinup module add monitoring` |
| `backups` | `deploy/backups/`, `deploy/pgbackrest/`, and the backup Compose files | `spinup module add backups` |
| `secrets` | `secrets/` and `.sops.yaml` | `spinup module add secrets` |
| `tofu` | `deploy/infra/tofu/` | `spinup module add tofu` |

Run `spinup module list` to see which modules are on.

## Who edits what

spinup puts every file it writes into one of three classes. The class tells spinup what to do when you upgrade.
The [concepts page](../concepts.md) explains the three classes in full.

| Class | Meaning | Example | What happens on upgrade |
| --- | --- | --- | --- |
| `managed` | spinup owns it. Do not edit it. | `api/internal/routes/routes_gen.go`, `deploy/compose.yml` | spinup replaces it with the new version |
| `scaffold` | A starting point that you own and change | `api/cmd/api/main.go`, `web/src/routes/+page.svelte` | spinup merges its changes with yours |
| `seed` | Written once, then yours | `api/db/migrations/00001_init.sql`, `README.md` | spinup does not touch it again |

A file that ends in `.gen.go` or `.gen.ts` has a header that says "DO NOT EDIT". Change its source instead, then run the generator.
You want to edit a managed file anyway? Run `spinup eject <path>` to take it over. You lose the automatic updates for that file.
`spinup diff` shows how your files differ from the templates. `spinup restore <path>` puts a file back.

## The files you edit most

| You want to ... | Edit | Then run |
| --- | --- | --- |
| Add a table and its API | nothing: use `spinup g resource` | `spinup db migrate up` |
| Write a query | `api/db/queries/<name>.sql` | `spinup db sqlc` |
| Change the API contract | `api/openapi/paths/*.yaml`, `api/openapi/schemas/*.yaml` | `spinup g openapi` |
| Change a page | `web/src/routes/<route>/+page.svelte` | nothing, the dev server reloads |
| Wire a module into the API | `api/cmd/api/main.go` | nothing, the dev server reloads |
| Change a setting of the app | `spinup.toml` | the command that the setting names |
| Change the replicas or limits of an environment | `deploy/environments/<env>.toml` | `spinup deploy` |
| Change the host or the public URL of an environment | `[env.<name>]` in `spinup.toml` | `spinup deploy` |

## Settings and secrets

| File | Holds | Commit it? |
| --- | --- | --- |
| `spinup.toml` | The app name, the module path, the module list, the monitoring profile, the environments (`[env.<name>]`), the sidecar services, and the roles | Yes |
| `spinup.lock` | The template version, your answers, and a hash of every file that spinup wrote | Yes |
| `.env.example` | The names and safe values of the development variables | Yes |
| `.env` | Your local values, for example `DATABASE_URL` | No |
| `secrets/<env>.enc.yaml` | The secrets of one environment, encrypted with `sops` and `age` | Yes, it is encrypted |
| `.spinup/` | Ports of `spinup dev` (`dev.json`), upgrade worktrees, database dumps, and plugin trust files | No, except `plugins.allow` and `plugins.history` |

{: .warning }
Never commit `.env`. The generated `.gitignore` already excludes it.
Do not edit `spinup.lock` by hand. spinup uses its hashes to tell your edits from the template.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spinup doctor` says a managed file is missing | You deleted or renamed it | Run `spinup restore <path>` |
| An upgrade reports a conflict in a file | You and the template changed the same lines | Read [Upgrade](../upgrading.md) |
| Your edit to a `.gen` file disappears | The generator wrote the file again | Edit the source file, or use `spinup eject` |

## See also

- [Concepts](../concepts.md)
- [Reference: spinup.toml](../reference/configuration.md)
- [The web app](../web.md)
- [Production deploy files](../deploy.md)
- [spinup eject](../commands/eject.md) and [spinup restore](../commands/restore.md)
