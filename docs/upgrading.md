---
title: Upgrade
nav_order: 6
---

# Upgrade

Three things have versions: the spinup CLI, the templates your app was generated from, and the kit library your API imports.
`spinup upgrade` moves the templates, and the kit with them, to a new version on a Git branch. Your main checkout never changes until you merge.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup upgrade --plan
Already up to date.
```

## Upgrade in four steps

1. Update the CLI: `spinup self-update`.
2. Preview: `spinup upgrade --plan`.
3. Apply on a branch: `spinup upgrade --yes`.
4. Merge the branch that the command prints.

<!-- doctest: skip -->
```console
$ spinup self-update
...
$ git status --short            # must be clean
$ spinup upgrade --plan
...
$ spinup upgrade --yes
...
$ git merge spinup/upgrade-0.12.0
```

## Check your versions

| What | Command | Where it is recorded |
| --- | --- | --- |
| The CLI | `spinup version` | Your machine. |
| The templates of your app | `grep template_version spinup.lock` | `spinup.lock` |
| The kit | `grep kit api/go.mod` | `api/go.mod` |
| The newest release | `spinup version --check` | GitHub releases |

```console
$ spinup version
...
```

`spinup releases` is not a version list. It shows the release history of a deployed environment. See [deploy](commands/deploy.md).

<!-- doctest: skip -->
```console
$ spinup version --check
...
$ spinup self-update --check
...
```

## Update the CLI

`spinup self-update` installs the newest stable release. It checks the SHA-256 sum, and the cosign signature when `cosign` is installed.

<!-- doctest: skip -->
```console
$ spinup self-update
$ spinup self-update --channel next
$ spinup self-update --version 0.10.0
$ spinup self-update --require-signature
```

If Homebrew installed spinup, run `brew upgrade --cask spinup`. The command prints this hint for you.
The CLI looks for a new release at most once a day and prints a notice on stderr. Set `SPINUP_NO_UPDATE_CHECK=1` to turn the notice off.
See [self-update](commands/self-update.md).

## Upgrade the app

### 1. Plan

```console
$ spinup upgrade --plan
Already up to date.
```

When a newer version exists, the plan lists each file as `merge`, `overwrite`, `create`, `keep`, or `unchanged`.
It also prints notes from the kit for steps you must do by hand, such as running a migration.
The plan writes `.spinup/plan.json` and changes no project file.

### 2. Apply

Commit your work first. The upgrade needs a clean checkout, Git 2.44 or later, and a valid `spinup.lock`.
A generated app already ignores `.spinup/` in `.gitignore`.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
...
```

spinup creates the branch `spinup/upgrade-<version>` in a separate worktree at `.spinup/upgrade/<version>`.
It applies the template changes, runs codemods, updates Go and pnpm dependencies, and then builds and tests the result.
Each step is its own commit, so you can read the history.
Use `--to <version>` for an exact target. Without it, spinup picks the newest version it can load.
If you skip several releases, the upgrade steps through each minor release in turn.

### 3. Resolve conflicts

If you changed a file that the new template also changed, spinup merges both. When the merge cannot decide, it exits with code 3 and lists the files.

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
...
[exit 3]
$ git -C .spinup/upgrade/0.12.0 diff --name-only --diff-filter=U
api/cmd/api/main.go
```

1. Open each listed file in the worktree. Edit out the `<<<<<<<`, `=======`, and `>>>>>>>` lines. Keep the code you want.
2. From the main checkout, not the worktree, run `spinup upgrade --continue --yes`.

To give up, run `spinup upgrade --abort --yes`. It removes the worktree and the branch.

### 4. Merge

<!-- doctest: skip -->
```console
$ git merge spinup/upgrade-0.12.0
$ git worktree remove .spinup/upgrade/0.12.0
$ spinup db migrate up
```

Run `spinup db migrate up` if the plan mentioned a migration. Then run `spinup dev` or your tests.

### Upgrade flags

| Flag | Use it to ... | Example |
| --- | --- | --- |
| `--plan` | Preview and change nothing. | `spinup upgrade --plan` |
| `--to` | Pick a version. | `spinup upgrade --to 0.12.0 --yes` |
| `--continue` | Resume after you fix conflicts. | `spinup upgrade --continue --yes` |
| `--abort` | Remove the worktree and branch. | `spinup upgrade --abort --yes` |
| `--force-managed` | Overwrite managed files that you edited. | `spinup upgrade --force-managed --yes` |
| `--no-verify` | Skip the build and test pass. | `spinup upgrade --no-verify --yes` |
| `--commit-conflicts` | Commit conflict markers and fix them later. | `spinup upgrade --commit-conflicts --yes` |
| `--allow-dirty` | Upgrade while the main checkout has changes. The worktree still starts from `HEAD`. | `spinup upgrade --allow-dirty --yes` |
| `--ui` | Merge new shadcn-svelte components. | `spinup upgrade --ui --yes` |
| `--offline` | Never download templates. | `spinup upgrade --offline --plan` |

### Codemods and the kit

A codemod is a small script that edits your code for a breaking change, such as a renamed import.
The upgrade runs codemods before and after the template merge. Each one prints its ID and what it does.
The lock records the codemods that ran, so none runs twice.

The kit moves with the app. For a release version, the upgrade sets the kit requirement in `api/go.mod` to the same version.
If you point the kit at a local directory for development, set `SPINUP_KIT_REPLACE`. The upgrade keeps that replacement.
The kit changes a default only when you raise `behavior_version` in `spinup.toml`, so an upgrade does not change how your app behaves.
See [Kit compatibility](policy/kit-compat.md) and [Behavior versions](policy/behavior-version.md).

### Update UI components

The shadcn-svelte components are seed files, so a normal upgrade keeps them. Add `--ui` to merge new component versions:

<!-- doctest: skip -->
```console
$ spinup upgrade --ui --yes
```

## What happens to your edits

Each template file has a class. The class decides what an upgrade does with your changes.

| Class | Meaning | Upgrade | Your edits |
| --- | --- | --- | --- |
| `managed` | spinup owns it. | Replaced if you did not edit it. Merged if you did. | `--force-managed` overwrites them. `spinup check` reports drift (exit 4). |
| `scaffold` | Starts from a template, then you own it. | Merged with a three-way merge. | Kept. Conflicts may need you. |
| `seed` | Created once. | Never touched. | Yours. |

If you edited a managed file, such as `deploy/compose.yml` for a CDN or a trusted proxy, the merge can conflict.
Resolve it like any other conflict. Use `eject` below to stop the merges for that file.

Three commands let you change the class or undo an edit:

```sh
spinup diff
spinup eject deploy/compose.yml
spinup restore deploy/compose.yml --force
```

| Command | Effect | Example |
| --- | --- | --- |
| `spinup diff [path]` | Show how your files differ from the template. | `spinup diff deploy/compose.yml` |
| `spinup eject <path>` | Take ownership of a managed file. It becomes scaffold. | `spinup eject deploy/compose.yml` |
| `spinup restore <path>` | Write the template version back. `--force` replaces your edits. | `spinup restore deploy/compose.yml --force` |
| `spinup check` | Fail with exit code 4 if a managed file drifted, or if a newer template exists. For CI. | `spinup check` |

If you edit a managed file, `spinup check` fails and suggests `eject` or `restore`.
Eject when you want to keep the edit. Restore when you do not.
See [diff](commands/diff.md), [eject](commands/eject.md), [restore](commands/restore.md), and [check](commands/check.md).

## Add modules

A module is an optional feature, such as `jobs`, `mail`, or `monitoring`.

```console
$ spinup module list
...
$ spinup module add jobs --dry-run --yes --non-interactive
...
```

`spinup module add <name>` works like an upgrade. It creates a branch `spinup/module-<names>` in a worktree, and you merge it.
It also adds required modules. Conflicts exit with code 3, and `--continue` and `--abort` work the same way.
Some modules ask questions, such as `--monitoring-profile`. In a script, pass the flag and `--non-interactive`.

spinup has no `module remove`, because module files can hold your data. To drop a module, delete its files and remove its name from `modules` in `spinup.toml`.
See [module](commands/module.md).

## Upgrade PostgreSQL

A PostgreSQL upgrade is a separate operation on the VM. It is not part of `spinup upgrade`.

The major upgrade examples assume that the VM runs PostgreSQL 17. A new app already runs 18 and needs no major upgrade.
The target must be higher than the running major. To read the running major, print the plan of a minor upgrade.
Its second step reads `SHOW server_version` on the VM.

| Upgrade | Command | Downtime |
| --- | --- | --- |
| Minor (patch level) | `spinup maintenance pg-minor-upgrade --env production` | One restart. |
| Major, by dump and restore | `spinup maintenance pg-major-upgrade --env production --to 18 --i-know` | The app is down while it copies. |
| Major, in place | `spinup maintenance pg-upgrade-in-place --env production --to 18 --i-know` | The app is down during the upgrade. |

{: .warning }
A major upgrade stops the app. Run it in your maintenance window with `--plan` first, and make sure a recent backup exists.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-minor-upgrade --env production --plan
...
$ spinup maintenance pg-major-upgrade --env production --to 18 --plan
...
```

See [PostgreSQL upgrades](commands/maintenance-pg.md) and [In-place PostgreSQL upgrades](commands/maintenance-pgupgrade.md).

## Checklist

1. Read the release notes. Run `spinup version --check`.
2. If the app has the `backups` module, take a backup: `spinup backup now --env production`.
3. Update the CLI: `spinup self-update`.
4. Commit your work, so `git status` is clean.
5. Run `spinup upgrade --plan`.
6. Run `spinup upgrade --yes`. Resolve conflicts, then `spinup upgrade --continue --yes`.
7. Merge the branch. Run `spinup db migrate up`.
8. Run `spinup check` and your tests.
9. Deploy to staging first: `spinup deploy --env staging`.
10. Deploy to production: `spinup deploy --env production`.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The upgrade says the checkout is dirty. | You have uncommitted changes. | Commit or stash them. |
| Exit code 3. | A conflict. | Resolve the files in the worktree, then `--continue`. |
| Verification fails. | The merged code does not build or test. | Fix it in the worktree, then `--continue`. The commits stay for you to read. |
| The upgrade cannot download a template. | You are offline. | Run `spinup cache warm` when online, then use `--offline`. |
| `spinup check` exits 4 after the merge. | A managed file differs from the lock. | Run `spinup diff`, then `eject` or `restore`. |
| `spinup check` exits 4 after `self-update`, with no edits. | A newer template version exists. | Run `spinup upgrade`, or pass `--allow-behind` to `check`. |
| A kit replace points to a missing directory. | `SPINUP_KIT_REPLACE` or `api/go.mod` has a bad path. | Fix the path or remove the replace. |

## See also

- [upgrade](commands/upgrade.md) and [self-update](commands/self-update.md) for every detail.
- [Support policy](policy/support.md) and [Kit compatibility](policy/kit-compat.md).
- Flag reference: [upgrade](site/commands/upgrade.md), [self-update](site/commands/self-update.md), [module add](site/commands/module-add.md), [version](site/commands/version.md).
- [Backups with WAL-G](backups-walg.md) and [Backups with pg_dump](backups-pgdump.md) before a risky upgrade.
