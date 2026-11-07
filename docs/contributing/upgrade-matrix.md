---
title: Upgrade matrix
parent: Contributing
nav_order: 4
---

# Upgrade matrix

This page has two jobs. It shows how to run the upgrade tests, and it shows how to review a template change.
If you change a template pack, go to [Review template changes](#review-template-changes) first.
For the user steps of an upgrade, read [Upgrade](../upgrading.md).

```sh
go test -race ./internal/upgrade/ -run 'Matrix|LockSchema'
```

Each pull request tests upgrades from the frozen apps in `testdata/corpus/`.
The matrix uses a temporary Git repository and the real upgrade runner.
It applies user edits, checks the exact conflicts, resolves them, and continues the upgrade.
It checks the final lock, preserved edits, tombstones, and clean Git state.
The build mode also runs `go build`, `go vet`, and `go test` in the upgraded API.

The current corpus is synthetic.
Real release corpora start at v0.3.
See `testdata/corpus/README.md` for the format and freeze procedure.

## Run the checks

Run these commands from the repository root:

```sh
go test -race ./internal/upgrade/ -run 'Matrix|LockSchema'
SPINUP_MATRIX_BUILD=1 go test -race ./internal/upgrade/ -run 'Matrix|LockSchema'
go test ./internal/templates/ -run PacksDigest
actionlint .github/workflows/upgrade-matrix.yml
make build lint
make doctest
```

The matrix sets `SPINUP_KIT_REPLACE` to the repository kit.
It uses the local Go toolchain and disables module downloads.
Before you run it, populate the module cache through the normal project installation procedure.
The CI workflow caches the kit dependencies before it starts the offline matrix.

The `coverage` job requires at least 70% statement coverage in each selected package.
It includes `merge`, `upgrade`, `manifest`, and `migrate`, with their subpackages.
It prints each package percentage and fails if any package is below the threshold.

## Review template changes

`internal/templates/packs.digest` records one SHA-256 digest and one migration decision for each pack.
The digest includes every embedded file, including metadata and dotfiles.
The test sorts paths and hashes each path, content length, and content.
File boundaries use NUL delimiters.

After a template change, run:

```sh
SPINUP_GOLDEN_UPDATE=1 go test ./internal/templates/ -run PacksDigest
```

The update writes current digests.
It preserves the migration field for unchanged packs.
It writes `REVIEW` for new or changed packs and fails until you make a decision.

Review the pack diff and the upgrade effects.
Replace `REVIEW` with a registered migration ID when a codemod is necessary.
Use `none` when no codemod is necessary.
Explain the `none` decision in the pull request.
The initial digest baseline uses `none` because it introduces no template change.

Run the digest test again without the update variable.
Commit the template changes, reviewed digest decisions, and applicable corpus edits together.
Do not replace a frozen release tree to hide a regression.
The digest guard verifies a recorded decision.
Human review establishes whether that decision is correct.

## Generate a synthetic starting app

This example tests the generation commands in a temporary directory:

```console
$ spinup new corpus --api-only --module example.com/corpus --no-git
...
$ test -f corpus/spinup.lock
$ test -f corpus/api/internal/routes/routes.go
$ rm corpus/.env
$ test ! -e corpus/.env
```

Use the generated files as the synthetic tree when you deliberately refresh the development corpus.
Preserve dotfiles and file permissions when you copy them.
Refresh the schema fixtures only when their intended inputs change.

## Upgrade an app to roles

This section is a test reference with the user steps inside it.
App owners find the same steps in [Roles](../build/roles.md).

The roles cases are in `testdata/corpus/roles/`.
They use a small synthetic tree and the real Git merger.
They check existing users, an app migration at version 5, a `main.go` conflict, and custom roles.
The upgrade creates `api/db/kit/00005_users_role.sql` and `api/internal/roles/roles.gen.go`.
It preserves app files and `spinup.toml`.

Run the roles checks. `AuthUpgradeWarnings` tests the plan warnings, and `RolesUpgradeCorpus` runs the roles corpus:

```sh
go test -race ./internal/upgrade/... -run 'AuthUpgradeWarnings|RolesUpgradeCorpus'
make script RUN='upgrade_roles_*'
```

The script compiles the API without a database.
With `DATABASE_URL`, it applies the kit migration 5 and then the app migration 4.
It then resets the app migrations and checks that the kit migrations stay applied.
It checks that existing sessions read the default role.
It uses a recorder and needs no HTTP listener.
Set `DATABASE_URL` to run the database checks.

The kit migrations and the app migrations have separate version numbers (ADR 0028).
An app migration `00005_*.sql` does not clash with `api/db/kit/00005_users_role.sql`, and the plan shows no warning.
The kit accepts a pending migration below the highest applied version.

The plan stores each warning in the reason for the new file.
The JSON report also includes this reason.

If `[auth]` exists without `roles.gen.go`, the plan creates the default file and prints a note.
Run `spinup g roles` after the upgrade.
A custom `[auth]` does not change the template merge base.
No codemod is necessary for the `auth`, `web`, or `api` pack.
New files and the merge process handle these changes.
The behavior version stays at 1.

### Complete the auth configuration

Resolve a conflict in `api/cmd/api/main.go` before you start the new binary.
Import the app package `api/internal/roles` through its module path.
Parse `AUTH_REGISTRATION` with `auth.ParseRegistration` and handle its error.
Pass these options to `auth.New`:

```go
auth.WithRoles(roles.Set),
auth.WithRegistration(registration),
```

Register the operator command in `app.New`:

```go
app.WithCommand("user", auth.UserCommand(authn)),
```

Run `spinup doctor` to check the role file and the auth configuration.
The `roles-drift` check reports missing configuration in `main.go`.
If `main.go` keeps its previous configuration, the kit uses `auth.DefaultRoles()`.
Custom roles require `auth.WithRoles(roles.Set)`.

Run `spinup db migrate up` before you start the new binary.
The new auth queries need the `role` column.
An old schema causes login to fail with HTTP 500.
Deploy already migrates before it starts the new binary.

Assign an administrator to the existing app.
Run the app binary with `user set-role --email <email> --role <admin_role>`.
Then run the app binary with `user backfill`.
The first command assigns the administrator role from `[auth]`.
The second command assigns the default role to rows with an empty role.
It refuses when the default role equals the administrator role.

If `main.go` does not register `user`, use this SQL fallback:

```sql
UPDATE users SET role = '<admin_role>' WHERE email = lower('<email>');
```

Replace both placeholders with the app values.
Then add the command registration and run `user backfill`.
Before assignment, existing users have an empty stored role.
They read as the default role unless it equals the administrator role.
In that case, an empty role grants no role.

## Upgrade an app to the audit module

App owners find the user guide in [Audit trail](../audit.md).

The audit cases are in `testdata/corpus/audit/`.
They use the same small synthetic tree and the real Git merger as the roles cases.
The new template adds `api/db/kit/00005_users_role.sql`, `api/db/kit/00006_audit_events.sql`, and the audit wiring in `main.go`.
Each case checks the plan, the written files, and the doctor check `audit-wired`.

| Case | App state | Expected result |
| --- | --- | --- |
| `main-conflict` | The app changed the `auth.New` line of `main.go` | The plan reports a conflict on `api/cmd/api/main.go`. After you keep the app version, `audit-wired` warns. After you add the wiring, it passes. |
| `migration-clash` | The app owns `00006_create_notes.sql` | The plan creates the kit migrations 00005, then 00006. The plan has no warning, because the kit has its own numbers. |
| `resource-chain` | The app has resource migrations 00003 to 00006 | The plan creates the kit migrations 00005, then 00006. The plan has no warning. |
| `todo-shape` | The app has migrations 00001 to 00003, with no mail and no OIDC | The plan creates the kit migrations 00005, then 00006. The plan has no warning. |

A test also runs `module add audit` on an app with an app migration of the same number.
It checks that the preview has no clash warning, and it checks the `Next:` lines for `main.go`.
The `Next:` lines come from `modules.AuditNextSteps`.

Run the audit checks. `AuditUpgradeCorpus` runs the cases above, and `AuditModuleAddNextSteps` tests the `Next:` lines:

```sh
go test -race ./internal/upgrade/... -run 'AuditUpgradeCorpus|AuditModuleAddNextSteps'
make script RUN=audit_module
make script RUN=gen_audit
SPINUP_APP_GATE=1 make script RUN=app_gate_audit
SPINUP_APP_GATE=1 make script RUN=app_gate_pdf
```

The planner never renames a migration, because the app may have applied it.
The audit pack and the `api` pack need no codemod for a new app.
They add new files, and the 3-way merge handles the `main.go` change.

## Upgrade an app to the kit migration directory

The codemod `0003_kit_migrations` moves the pack migrations from `api/db/migrations/` to `api/db/kit/` (ADR 0028).
It runs before the plan, so the plan sees the kit paths on disk.
The packs `api`, `auth`, and `audit` record this codemod in `internal/templates/packs.digest`.
The codemod `0004_kit_testkit` rewrites the `testkit.DBWithMigrations` call in the test files of `api/`.

Run the checks:

```sh
go test -race ./internal/migrate/... ./internal/upgrade/...
make script RUN=upgrade_kit_migrations
```

The script moves a legacy app to the kit layout.
The corpus app in `testdata/corpus/kit-migrations/` has an app migration `00003_create_todos.sql`, and the script checks that this file stays.

## See also

- [Upgrade](../upgrading.md), the user steps of an upgrade
- [Roles](../build/roles.md) and [Audit trail](../audit.md)
- [Release spinup](release.md)
- [Behavior versions](../policy/behavior-version.md)
