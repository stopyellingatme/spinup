---
title: module — add optional features
parent: Command guides
---

# module — add optional features

A module is an optional feature of your app, such as `auth`, `jobs`, `mail`, or `monitoring`.
`spinup module add` turns one on in an existing app.
It does the work on a new Git branch, so your current files stay as they are.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup module list
NAME        STATE  REQUIRES  DESCRIPTION
...
$ spinup module add jobs --dry-run --yes --non-interactive
...
```

To add a module for real, run it without `--dry-run`:

<!-- doctest: skip -->
```console
$ spinup module add monitoring --monitoring-profile lite --yes --non-interactive
...
Next: git merge spinup/module-monitoring
```

## See what is available

`spinup module list` shows every module with its state (`on` or `off`), the modules it requires, and a description.
The state comes from the `modules` list in `spinup.toml`.
Add `--json` for a structured list.

| Module | What it gives you | Read more |
| --- | --- | --- |
| `auth` | Accounts, sessions, and sign-in pages | [Accounts and sign-in](../auth.md) |
| `audit` | A history of who changed which record | [Audit trail](../audit.md) |
| `flags` | Feature flags for a feature, a role, a user, or a percentage of users | [Feature flags](../build/flags.md) |
| `jobs` | Background jobs with a worker process | [Background jobs](../build/jobs.md) |
| `mail` | Email templates and SMTP delivery | [Mail](../mail.md) |
| `secrets` | Encrypted secrets for each environment | [Secrets](../deploy/secrets.md) |
| `monitoring` | Grafana, Prometheus, and Loki next to the app | [Monitoring](../monitoring.md) |
| `backups` | Scheduled PostgreSQL backups (pg_dump, WAL-G, or pgBackRest) | [Backups with pg_dump](../backups-pgdump.md) |
| `tofu` | A Hetzner Cloud VM with OpenTofu | [Hetzner Cloud with OpenTofu](../infra.md) |
| `ci`, `deploy` | GitHub Actions and production Compose (on by default) | [Generated CI](../ci.md) |

`base`, `api`, and `web` are core parts of the app, not modules.
To add several modules, list the names: `spinup module add jobs mail`.
If a module needs another module, the command adds that one too and prints its name.
An API-only app cannot add a module that needs the web app.

## Answer the questions with flags

Each module can ask questions.
In a terminal, `module add` asks the ones you did not answer.
Without a terminal, it uses the default or tells you which flag is missing.
Pass `--non-interactive` to never prompt, and `--yes` to confirm the change.

```console
$ spinup module add mail --dry-run --yes --non-interactive
...
```

| Flag | What it sets | Example |
| --- | --- | --- |
| `--monitoring-profile lite\|full\|none` | The monitoring stack when you add `monitoring` (default `lite`) | `spinup module add monitoring --monitoring-profile full` |
| `--ci-deploy-on-push` | Deploy the default environment on each push to `main`, when you add `ci` | `spinup module add ci --ci-deploy-on-push` |
| `--auth-oidc` | Sign-in with an identity provider, when you add `auth` | `spinup module add auth --auth-oidc` |
| `--db-version <major>` | The PostgreSQL major version, when you add `ci` or `deploy` (default `18`) | `spinup module add deploy --db-version 17` |

An explicit flag wins over the lock answers and the defaults.
A flag works only for a module that you are adding now.
If you pass a flag for a module that is already on, the command fails with `it does not belong to a new pack`.

The web app and the Go module are core parts of every app, so they are never "added".
These flags are listed in `module add --help`, but they only work in `spinup new`:
`--frontend-mode`, `--package-manager`, `--api-client`, `--shadcn-registry`, and `--module`.
The `auth` module needs `--frontend-mode node`, which is the default.
If the app uses `--frontend-mode embed`, `module add auth` fails with `cannot use auth in embed mode`.
See [Embed mode](../embed.md).

## Preview the change

`--dry-run` prints the plan and creates no branch.
The plan lists the files to create, merge, and overwrite.

```console
$ spinup module add monitoring --dry-run --yes --non-interactive
Upgrade 0.0.0-dev -> 0.0.0-dev
merge: 1
  spinup.toml (module configuration)
...
create: 17
...
```

## Review and merge the branch

`module add` needs a clean Git repository and Git 2.44 or later.
It renders the template version in `spinup.lock`, even when a newer one exists.
It works in a separate worktree at `.spinup/upgrade/<locked-version>` on the branch `spinup/module-<names>`.
Then it adds the module to `spinup.toml`, keeps your comments, and checks that the app builds.

When it ends, it prints the next step:

<!-- doctest: skip -->
```console
$ git merge spinup/module-monitoring
$ git worktree remove .spinup/upgrade/0.1.0
```

Read the branch first if you want to review the files.
The branch stays after you remove the worktree.

| Flag | What it does | Example |
| --- | --- | --- |
| `--no-verify` | Skip the build and tests in the worktree | `spinup module add jobs --no-verify` |
| `--commit-conflicts` | Commit conflict markers and resolve them later | `spinup module add jobs --commit-conflicts` |
| `--offline` | Never download template versions | `spinup module add jobs --offline` |

## If files conflict

If a file you edited changed in the module, the command exits with code 3 and lists the files.
Fix the markers in the worktree, then continue from the main checkout.
To give up, remove the worktree and branch.

<!-- doctest: skip -->
```console
$ spinup module add --continue --yes
$ spinup module add --abort --yes
```

The steps are the same as for an app upgrade.
Read [upgrade](upgrade.md) for the full flow.

## Remove a module

There is no `module remove` command.
Module files can hold your own code and data, so remove them by hand.
Delete the files and take the name out of `modules` in `spinup.toml`.

## Add the audit module to an existing app

`audit` needs `auth` and the web app.
Upgrade the app first, then add the module.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
$ spinup module add audit --yes --non-interactive
```

The command prints `Next:` lines with the exact code to paste into `api/cmd/api/main.go`.
That file belongs to you, so spinup does not edit it.
If the plan warns about duplicate migration versions, fix them before you run the migrations.
Then run `spinup db migrate` and `spinup g audit <resource>` for each table you want to track.
[Audit trail](../audit.md) explains the wiring, the access rules, and the limits.

## Add the flags module to an existing app

`flags` needs `auth` and the web app.
Upgrade the app first, then add the module.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
$ spinup module add flags --yes --non-interactive
```

The migration goes in `api/db/kit/`, so it does not clash with your own migrations.
The command prints `Next:` lines with the code to paste into `api/cmd/api/main.go` and the web files.
Then run `spinup db migrate up`, declare a flag in `spinup.toml`, and run `spinup g flags`.
[Feature flags](../build/flags.md) shows each edit.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `unknown module` | Check the name in `spinup module list`. |
| `module add` says the Git tree has uncommitted changes | Commit your changes first. |
| `a requested module requires web, but this app is API-only` | An API-only app has no web app. Pick a module without that need. |
| Exit code 3 | A file conflicts. Resolve it, then run `spinup module add --continue --yes`. |
| The locked template is missing offline | Run `spinup cache warm` online, or remove `--offline`. |

## See also

- [upgrade — move an app to a new version](upgrade.md)
- [cache — manage template versions](cache.md)
- [Flag reference: module add](../site/commands/module-add.md), [module list](../site/commands/module-list.md)
