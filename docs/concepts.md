---
title: Concepts
parent: Get started
nav_order: 4
---

# Concepts

This page explains the ideas behind spinup, one at a time, with a short example for each.
Read it once, then come back to the [glossary](#glossary) when a word is new.

The most important idea is this: spinup writes files into your app, and then it remembers what it wrote.
That memory lets `spinup upgrade` bring new template changes into your app without it overwriting your work.

```console
$ spinup new todo --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls spinup.toml spinup.lock
spinup.lock
spinup.toml
```

The sections below use this `todo` app.

## The app and spinup.toml

An app is one folder that holds everything: the Go API, the web app, the deploy files, and two files that spinup reads.
`spinup.toml` holds your settings. You edit it. `spinup.lock` is spinup's memory. spinup edits it.

```console
$ cat spinup.toml
# Select the kit behavior defaults.
behavior_version = 1
# Enable these optional modules.
modules = [ "ci", "deploy" ]

[app]
# Name the application.
name = "todo"
# Set the root Go module path.
module = "example.com/todo"

[monitoring]
# Pick the monitoring stack: lite, full, or none.
profile = "lite"
```

Later sections add tables to this file: `[env.<name>]` for environments, `[[services]]` for sidecar services, and `[auth]` for roles.
Commands find the app by looking for `spinup.toml` in the current folder and its parents. Use `-C <dir>` to run a command on another folder.
See [spinup.toml](reference/configuration.md) for every key, and [Project layout](get-started/project-layout.md) for the folders.

## Template packs and modules

spinup builds your app from template packs. A pack is a set of template files with a short description of what it adds.
Three packs are always on: `base` (shared files), `api` (the Go API), and `web` (the SvelteKit app). They are not modules.
If you pass `--api-only` to `spinup new`, the `web` pack is off.

A module is an optional pack that you turn on. Run `spinup module list` to see them:

```console
$ spinup module list
NAME        STATE  REQUIRES  DESCRIPTION
audit       off    auth      An audit trail: database triggers record who changed a record, and when, with a History page and an operator command.
auth        off    web       Accounts with kit auth: register, log in, log out, and PostgreSQL sessions, with login pages.
backups     off    deploy    Continuous PostgreSQL backups with pgBackRest: full, differential, and WAL to encrypted S3-compatible storage.
ci          on     -         Build, test, deploy, verify backups, and propose upgrades with GitHub Actions.
deploy      on     api       Production Compose for blue/green deploys, Caddy, a Postgres image with pgBackRest, cloud-init, and environments.
flags       off    auth      Feature flags: declared in spinup.toml, targeted by role, user, or percentage, and changed at runtime with an app command or an admin page.
jobs        off    api       Background jobs on River: a worker process and River migrations in the migrate step.
mail        off    api       Typed email templates, SMTP delivery, Mailpit, and optional River retries.
monitoring  off    deploy    Production monitoring next to the app: Alloy, Prometheus, Loki, and Grafana (lite), plus Tempo and exporters (full).
secrets     off    api       Encrypted secrets for each environment with sops and age.
tofu        off    deploy    OpenTofu infrastructure for a Hetzner Cloud VM.
```

| Module | What it gives you | Guide |
| --- | --- | --- |
| `auth` | Accounts, sign-in, and sessions | [Accounts and sign-in](auth.md) |
| `audit` | A history of who changed each record | [Audit trail](audit.md) |
| `jobs` | Background jobs and a worker process | [Background jobs](build/jobs.md) |
| `mail` | Typed emails and a development inbox | [Mail](mail.md) |
| `ci` | GitHub Actions workflows | [Generated CI](ci.md) |
| `deploy` | Production Compose files and environments | [Deploy to your own server](deploy.md) |
| `secrets` | Encrypted secrets for each environment | [Secrets](deploy/secrets.md) |
| `backups` | Database backups. The default backend is pgBackRest. See [Which backup backend?](operate/index.md#which-backup-backend) | [Backups with pg_dump](backups-pgdump.md) or [Backups with WAL-G](backups-walg.md) |
| `monitoring` | Grafana, Prometheus, Loki, and Alloy on the VM | [Monitoring](monitoring.md) |
| `tofu` | OpenTofu files for a Hetzner Cloud VM | [Hetzner Cloud with OpenTofu](infra.md) |

A module can need another one. The `REQUIRES` column shows it, and spinup turns on the required modules for you.
A new app has `ci` and `deploy`. Choose others at creation with `--modules`, or add one later:

```console
$ spinup module add jobs --dry-run
...
```

`spinup module add` does not change your branch. It makes a new git branch in a separate worktree, so you can review the files and merge them.

## File classes: managed, scaffold, and seed

Every file that spinup writes belongs to one of three classes. The class decides what `spinup upgrade` does with the file.

| Class | Who owns it | On upgrade | Example |
| --- | --- | --- | --- |
| `managed` | spinup | Replaced with the new template version. If you edited it, the upgrade tries a merge, and `--force-managed` overwrites your edit. | `api/internal/routes/routes_gen.go`, `deploy/compose.yml` |
| `scaffold` | You | Merged: spinup applies its changes to yours, and asks when they clash | `api/cmd/api/main.go`, `spinup.toml` |
| `seed` | You | Never touched again | `api/db/migrations/00001_init.sql`, `README.md` |

Think of it this way. A `managed` file is plumbing that you should not need to edit.
A `scaffold` file is a starting point that you change, while still getting template fixes.
A `seed` file is written once and then belongs to you.

Every generator prints the class of each file it writes:

```console
$ spinup g resource task title:string done:bool --dry-run
...
create api/db/migrations/00002_create_tasks.sql (seed)
...
create api/internal/handlers/tasks.go (scaffold)
...
update api/internal/routes/routes_gen.go (managed)
...
```

Three commands help you when you edit a managed file:

| Command | What it does | Example |
| --- | --- | --- |
| `spinup diff` | Shows how your files differ from the templates | `spinup diff deploy/Dockerfile.api` |
| `spinup eject <path>` | Makes a managed file yours (it becomes `scaffold`) | `spinup eject deploy/Dockerfile.api` |
| `spinup restore <path>` | Writes the template version back | `spinup restore api/cmd/api/main.go --force` |

To see drift, edit a managed file in the `todo` app. Then eject it:

```console
$ echo "# mine" >> deploy/Dockerfile.api
$ spinup check
deploy/Dockerfile.api: managed file differs from its lock hash (drift)
...
[exit 4]
$ spinup eject deploy/Dockerfile.api
$ spinup check
```

`spinup check` exits with code 4 when a managed file differs from the lock. This is "drift".
After `eject`, the file is yours, so the check passes. You lose automatic updates for that file.

{: .tip }
Files with `.gen.go` or `.gen.ts` in the name are `managed`. Change their source, and run the generator again.

## spinup.lock

`spinup.lock` is a JSON file that you commit and never edit. It records what spinup wrote.
That record is how an upgrade can tell your edits from the template.

```console
$ grep -E '"(template_version|behavior_version|schema)"' spinup.lock
...
```

| Key | What it holds |
| --- | --- |
| `template_version` | The template version that made your files |
| `cli_version` | The spinup version that last wrote the lock |
| `kit` | The kit module and version your API needs |
| `packs` | The packs that are on |
| `answers` | Your answers to the questions of `spinup new` |
| `files` | For each file: its class, its template, and a hash of the content that spinup wrote |
| `stack_versions` | The pinned Docker images and tool versions |
| `applied_migrations` | The IDs of the codemods that already ran |
| `plugins` | The trusted plugins and their hashes |

The hashes make three things possible. `spinup check` finds drift. `spinup diff` shows your edits.
`spinup upgrade` merges the old template, the new template, and your file.

Commit `spinup.lock` with your code. If it is damaged, `spinup doctor` fails the `lock-valid` check. Restore the file from git.

## Three versions

Three different things have a version. They move on their own schedule.

| Version | What it is | Where to see it |
| --- | --- | --- |
| spinup version | The CLI program you installed | `spinup version` |
| Template version | The set of template files that made your app | `template_version` in `spinup.lock` |
| Kit version | The runtime library your API imports | The `kit` line of `api/go.mod` |

```console
$ spinup version
...
```

<!-- doctest: skip -->
```console
$ spinup version --check
...
```

You update the CLI with `spinup self-update`. You update the templates and the kit together with `spinup upgrade`.
So your app can stay on an older template version while you use a newer CLI. `spinup upgrade --plan` shows the gap.
The CLI and the kit share the major and minor number. The `kit-version` check of `spinup doctor` warns when they differ.

## Upgrades and codemods

`spinup upgrade` moves your app to a newer template version. It works in a git branch and a separate worktree, so your checkout does not change until you merge.

```console
$ spinup upgrade --plan
Already up to date.
```

The steps are: preview with `--plan`, apply with `--yes`, fix any conflicts and run `--continue`, then merge the branch.
Read [Upgrade](upgrading.md) for the full walk-through.

A codemod is a small program that edits your code for a breaking change, such as a renamed import.
The upgrade runs the codemods that your app has not seen. It records each one in `applied_migrations` in the lock, so none runs twice.
The plan lists the codemods by ID before it changes anything.

## The kit

The kit is the Go library that your API imports. It holds the code you should not write again for each app: the HTTP server, sessions, roles, the database pool, jobs, mail, and logging.
You see it in `api/cmd/api/main.go`:

```console
$ grep -n 'spinup/kit' api/cmd/api/main.go
...
```

The kit is a separate Go module, `github.com/stopyellingatme/spinup/kit`. Your `api/go.mod` requires one version of it.
The kit follows a compatibility promise: a deprecated function keeps working for one minor version. See [The kit library](reference/kit.md) and [Kit compatibility](policy/kit-compat.md).

## Kit migrations

An app has two sets of migrations, and each set has its own version numbers.
The app migrations are in `api/db/migrations/`. You own them, and `spinup g resource` and `spinup db new` add to them.
The kit migrations are in `api/db/kit/`. A module ships them, such as the tables of `auth` and `audit`. They are `seed` files, so an upgrade does not touch them after the first write.

The same number can exist in both sets. For example, `api/db/kit/00005_users_role.sql` and `api/db/migrations/00005_create_orders.sql` do not clash.
The command `migrate up` applies the kit migrations first. The command `migrate status` lists them first, with the prefix `kit`.
The command `spinup db to <version>` moves the app migrations only. To roll back a kit migration, run `go run ./cmd/api migrate kit to <version>` in `api/`.
Never write a kit migration that uses a table of your app.

## The .spinup folder

`.spinup/` holds local state for the tools. A new app ignores it in git, so each machine has its own copy.

| Path | What it holds |
| --- | --- |
| `.spinup/dev.json` | The ports that `spinup dev` chose |
| `.spinup/upgrade/<version>/` | The worktree of a running upgrade |
| `.spinup/upgrade.json` | The state of a running upgrade, for `--continue` and `--abort` |
| `.spinup/dumps/app.dump` | The default file of `spinup db dump` |
| `.spinup/plugins.allow`, `.spinup/plugins.history` | Your plugin approvals. Commit these two files. |

The `.gitignore` of a new app already has this rule. See [Project layout](get-started/project-layout.md).

## Behavior versions

A behavior version lets the kit change a default without surprising you. It is the number `behavior_version` in `spinup.toml`.

```console
$ grep behavior_version spinup.toml
behavior_version = 1
```

When you upgrade, your app keeps its old behavior. `spinup upgrade --plan` prints a `Kit behavior` line when a newer one exists.
You opt in by raising the number yourself. See [Behavior versions](policy/behavior-version.md).

## Environments

An environment is a place you deploy to, such as `staging` or `production`.
Each one has its own server, secrets, release history, and deploy lock. You pick one with `--env`.

```toml
default_env = "staging"

[env.production]
host = "203.0.113.20"
user = "deploy"
public_url = "https://todo.example.com"
protected = true
```

The server address is in `spinup.toml`. The replicas, resource limits, and smoke URLs are in `deploy/environments/<env>.toml`.
The secrets are in `secrets/<env>.enc.yaml`.

A protected environment asks you to type its name before a risky command. In a script, pass `--confirm-env production`.
Without a terminal and without that flag, the command stops. In CI, `--yes` also works. See [Environments](deploy/environments.md).

## Releases, colors, and the deploy lock

Each `spinup deploy` makes a release. A release runs as one of two Compose projects, called colors: `blue` and `green`.
The old color keeps serving while the new color starts. Caddy switches traffic only after the new color is healthy. Then the old color stops.

```console
$ spinup deploy --help
...
```

While a deploy runs, it holds a lock on the VM. A second deploy to the same environment stops with exit code 5.
`spinup rollback` switches back to a stored release. It never undoes a database migration. See [deploy](commands/deploy.md) and [rollback](commands/rollback.md).

## Sidecar services

A sidecar service is a small program in its own container, next to your API. Write it in Rust, Python, or Node when Go is not the right tool.
It lives in `services/<name>/` and has a `[[services]]` table in `spinup.toml`.

```console
$ spinup g service ocr --lang python --dry-run
...
update spinup.toml (scaffold)
```

Your API calls the service over HTTP on a private network. See [Sidecar services](services.md) and [PDF reports](pdf-reports.md).

## Plugins

A plugin adds your own files and generators to an app. A pack plugin is data only. spinup never runs its code.
An executable plugin runs a program, so it can add doctor checks and more.

spinup uses a plugin only after you approve its exact contents. The approval stores a hash, and a changed plugin stops until you approve it again.

```console
$ spinup plugin --help
...
```

See [Plugins](plugins.md), [Executable plugins](plugins-exec.md), and [Plugin trust](policy/plugins.md).

## Glossary

Each term below is the name the docs use for that thing.

| Term | Meaning |
| --- | --- |
| age | The tool that makes the keys for secrets. sops uses these keys to encrypt. |
| API client | The TypeScript code in `web/` that calls your API. `spinup g client` rebuilds it. |
| app | One folder made by `spinup new`, with the API, the web app, and the deploy files |
| app command | A command of the app program `api/cmd/api`, such as `migrate` or `user`. See [App binary commands](reference/app-commands.md). |
| audit trail | A record of who changed each row of an audited table, written by database triggers |
| behavior version | The number `behavior_version` in `spinup.toml`. It selects the defaults of the kit. |
| blue, green | The two colors of a release. The old color serves until the new color is healthy. |
| Caddy | The web server in front of your app on the VM. It switches colors. |
| check | `spinup check`. It exits with code 4 when a managed file has drift. |
| class | The type of a template file: `managed`, `scaffold`, or `seed` |
| codemod | A small program that edits your code for a breaking change during an upgrade |
| Compose | Docker Compose. It runs your app, and the services of the dev stack, in containers. |
| deploy lock | A lock on the VM that stops two deploys of one environment at the same time |
| doctor | `spinup doctor`. It checks your tools and your app, and prints a fix for each failure. |
| drift | A managed file that differs from the hash in `spinup.lock` |
| eject | To take ownership of a managed file, so it becomes `scaffold` |
| environment | A place you deploy to, such as `staging`. It has its own server, secrets, and releases. |
| executable plugin | A plugin that runs a program. It can add doctor checks. |
| generator | A command of `spinup generate` (`g`) that writes files into your app |
| journal | A file on the VM that records each step of a deploy, so a stopped deploy can continue |
| kit | The Go library that your API imports |
| kit migration | A numbered SQL file in `api/db/kit/` that a module ships, such as the tables of `auth`. It has its own version numbers. |
| kit version | The version of the kit in `api/go.mod` |
| lock, spinup.lock | The file that records what spinup wrote, with a hash of each file |
| managed | The class of a file that spinup owns and replaces on upgrade |
| Mailpit | A development inbox. It shows the email that your app sends on your machine. |
| maintenance window | The hours in which risky work on the VM may run. The key is `[maintenance] window`. |
| migration | A numbered SQL file in `api/db/migrations/` that changes the database. A kit migration is a different kind. |
| module | An optional template pack that you turn on, such as `auth` or `jobs` |
| OpenAPI file | The description of your API in `api/openapi/`. The client and the API reference page read it. |
| pack | A set of template files. The core packs are `base`, `api`, and `web`. |
| pack plugin | A plugin that has only templates and generators. spinup never runs its code. |
| plan | The preview of an upgrade (`spinup upgrade --plan`) or of a deploy (`spinup deploy --plan`) |
| protected environment | An environment with `protected = true`. A risky command asks you to type its name. |
| recipient | A person or machine that can decrypt a secrets file |
| release | One deployed version of your app, with an ID such as `20261015T080000Z-0123456789ab` |
| resource | A table with a CRUD API and web pages, made by `spinup g resource` |
| role | A word, such as `manager`, that limits who can use a route or a page |
| scaffold | The class of a file that starts from a template and then belongs to you. Upgrade merges it. |
| River | The job queue library behind background jobs. It keeps its jobs in PostgreSQL. |
| secret | A password or key of your app, kept encrypted with sops and age |
| seed | The class of a file that spinup writes once and never touches again |
| sidecar service | A small program in its own container next to your API |
| sops | The tool that encrypts the secrets files. It uses age keys. |
| smoke URL | A URL that a deploy requests after the switch, to prove the new release works |
| spinup version | The version of the CLI program |
| sqlc | The tool that turns SQL queries in `api/db/queries/` into Go code. `spinup db sqlc` runs it. |
| stack versions | The pinned versions of Docker images and tools that spinup uses |
| template version | The version of the template files that made your app |
| trust | Your approval of one exact copy of a plugin |
| upgrade worktree | The separate folder `.spinup/upgrade/<version>` where an upgrade works |
| worker | The program `api/cmd/worker` that runs background jobs |

## See also

- [Project layout](get-started/project-layout.md): the folders of an app
- [Your first app](get-started/first-app.md): a hands-on tutorial
- [Upgrade](upgrading.md): the full upgrade steps
- [spinup.toml](reference/configuration.md): every setting
- [Recipes](recipes.md): short answers for common tasks
