# ADR 0028: a separate version space for kit pack migrations

- Status: accepted (owner, 2026-11-06)
- Date: 2026-11-06
- Issue: #44. It blocks #45 (flags), #46, and #49.

## Context

### Current behavior

Pack migrations and app migrations share one directory, one goose version space, and one version table.

1. The pack migrations live in the app directory `api/db/migrations`:
   - `api`: `00001_init.sql` (`internal/templates/packs/api/pack.toml:31-32`). It creates no database objects (`internal/templates/packs/api/files/api/db/migrations/00001_init.sql:1-5`).
   - `auth`: `00002_create_auth.sql` and `00005_users_role.sql` always (`internal/templates/packs/auth/pack.toml:10-11` and `:62-63`), `00007_users_auth_version.sql` always (`:68-69`), `00003_password_resets.sql` with the mail module (`internal/templates/packs/auth/pack.d/reset.toml:2-4`), and `00004_oidc_identities.sql` with `auth_oidc` (`internal/templates/packs/auth/pack.d/oidc.toml:8-11`).
   - `audit`: `00006_audit_events.sql` (`internal/templates/packs/audit/pack.toml:9-11`).
   - Every one of these files has the class `seed`. A seed file is written once and never touched again (`internal/upgrade/plan.go:183-186`).
2. The generated `api/db/migrations/embed.go` embeds `*.sql` at the root of the directory (`internal/templates/packs/api/files/api/db/migrations/embed.go.tmpl:8-9`). The generated `main.go` passes that one file system to `db.MigrateCommand` (`internal/templates/packs/api/files/api/cmd/api/main.go.tmpl:122`).
3. The kit migrator builds one goose provider with `goose.WithAllowOutofOrder(true)` (`kit/db/migrate.go:134-138`). The provider uses the default table `goose_db_version` (`goose v3.29.0, provider_options.go:48-60`). The table has the columns `id`, `version_id`, `is_applied`, and `tstamp` (`goose internal/dialects/postgres.go:20-24`). It has no file name.
4. `spinup db new`, `spinup g migration`, and `spinup g resource` name a new app migration with the highest 5-digit number in `api/db/migrations` plus one (`internal/db/ops/migrations.go:37-71`, ADR 0007 §4). So the todo dogfood app has `00003_create_todos.sql` next to `00001_init.sql` and `00002_create_auth.sql`. Its lock lists the three files and `embed.go` (`/Users/king/Documents/code/todo/spinup.lock:92-104`).
5. A pack migration that gets the number of an app migration gets only a warning. `modules.MigrationClashes` writes the warning (`internal/modules/audit.go:16-41`). `migrationClashWarnings` attaches it to the plan (`internal/upgrade/authwarn.go:61-82`). A special case warns about `00005_users_role.sql` (`internal/upgrade/authwarn.go:25-39`). The warning tells the owner to rename a file. Goose rejects two files with one version (ADR 0025 §2.6), so the owner must act before `migrate up` works.
6. Goose applies pending migrations in version order, also below the highest applied version (`goose provider_options.go:177-186`). `migrate down` rolls back the newest row of the version table, by row id (`goose provider.go:438-465`, `internal/dialects/postgres.go:45`). A row without a file stops `down` with `ErrVersionNotFound` (`goose provider_run.go:415-422`, `provider.go:459-461`).
7. Several tools read the one directory or the one table:
   - the upgrade planner adds every file of `api/db/migrations` to its path set (`internal/upgrade/run_plan.go:103-105`, `:185-200`)
   - `g resource` finds the table of a `ref` field and the owner key in the migrations (`internal/gen/resource/sql.go:174-200`, `internal/gen/resource/owner.go:109-139`, `internal/gen/resource/resource.go:186-192`)
   - `g audit` finds the create statement and the existing triggers of a table (`internal/gen/audit/audit.go:80-152`)
   - the doctor check `audit-wired` reads every file (`internal/doctor/auditwired/check.go:45-62`)
   - deploy lists the files (`internal/deploy/flow_release.go:316-345`) and reads `goose_db_version` on the VM (`internal/deploy/flow_release.go:373-399`), then computes the pending set (`:358-370`)
   - the pre-deploy backup reads the same directory for the `-- spinup:safe` marker (`internal/backup/hook.go:28`, `:31`, `:205-230`)
   - `db lint` lints the directory before a deploy (`internal/deploy/flow.go:105-109`)
   - the major upgrade of Postgres compares `goose_db_version` (`internal/maintain/pg/major.go:16`)
   - the generated tests build a template database from `migrations.FS` (`internal/gen/resource/handlers.go:696`, `internal/gen/job/job.go:408`, `kit/testkit/template.go:40`)
   - `spinup db to` reads the newest file version from the directory and the current version from `migrate version` (`internal/db/ops/migrations.go:150-203`)
8. The jobs pack already has a second version space. River keeps its own table and its own `jobs-migrate` command. A kit `db.Hook` runs River after the app migrations (`kit/jobs/migrate.go:64-73`, `kit/db/migrate.go:69-78`). Its status lines carry the word `river` (`kit/jobs/migrate.go:192`).
9. The sqlc schema is the one directory `db/migrations` (`internal/templates/packs/api/files/api/sqlc.yaml.tmpl:5`). sqlc parses the audit functions in it (ADR 0025 §3.12).
10. The lock entry of a seed file holds only its class. It has no template path and no hash (`internal/upgrade/plan_lock.go:91-95`). A codemod cannot tell a pack seed file from an app seed file through the lock.

### The problem

Every new pack migration takes the next free number of the pack set. Every app that ran `g resource` or `db new` after its last pack migration can hold that number already. The flags epic (#45) needs a new pack migration with two tables and a foreign key to `users`. Without this ADR, it would be `00008_feature_flags.sql`, and every app with eight or more migrations would get the warning of §5. The owner then renames a file by hand, and an applied file cannot be renamed without a row change in `goose_db_version`.

## Designs

Three designs were compared. The columns are the questions of issue #44.

### (a) A second goose provider, its own table, its own directory

The pack migrations move to `api/db/kit/`. The kit migrator gets a second goose provider on the table `spinup_kit_version`. Kit numbers and app numbers are two spaces.

| Question | Answer |
|---|---|
| Apps that applied 00002-00007 | The rows sit in `goose_db_version`. A one-time adoption moves each row to `spinup_kit_version` when the kit directory holds that version and the app directory does not (§3 below). Nothing applies twice. |
| Order of kit and app | `migrate up` runs the kit space first, then the app space. An app migration can reference `users`. A kit migration never references an app table. |
| Rollback | `migrate down` rolls back the app space. `migrate kit down` rolls back the kit space. Postgres refuses `DROP TABLE users` while an app table has a foreign key to it, so a wrong order fails safely. |
| `db status` | Kit lines first, with the word `kit`: `applied  kit 00002_create_auth.sql`. Then the app lines as today. |
| sqlc schema | `schema: [db/kit, db/migrations]`. sqlc reads the paths in order, so `users` exists before an app foreign key. |
| Upgrade step | One codemod moves the known pack files, one planner rule drops their dead lock entries, and the pack renders the new paths. The kit adopts the rows on its first run. |

Cost: the kit grows a second provider and an adoption step. Ten readers of the directory learn the second directory (§7 of the context). The status output changes for pack files.

### (b) A reserved high version range for pack migrations

New pack migrations take numbers from 90001 up. App migrations keep 00001 to 89999. One directory and one table stay.

| Question | Answer |
|---|---|
| Apps that applied 00002-00007 | The old pack files keep their low numbers. The low range stays mixed forever. A rename of the applied files needs `UPDATE goose_db_version SET version_id = ...`, a data change in the version table, in every deployed database. |
| Order of kit and app | Goose applies pending files in version order. A new kit migration (90008) always runs after every pending app migration. An app migration that needs a new kit table cannot come after it in version order. |
| Rollback | By row id, as today. `migrate to <v>` loses its meaning, because the high range sits above every app version. `spinup db to 3` moves down through 90008 (`internal/db/ops/migrations.go:160-166` treats every version below the newest file as a move down). |
| `db status` | One list in version order. The kit files sit at the bottom, also when they were applied first. |
| sqlc schema | One directory. sqlc reads the files in name order. A new kit table (90008) comes after every app file. An app foreign key to a new kit table fails in sqlc. The foreign key to `users` still works, because `00002` stays low. |
| Upgrade step | `NextMigrationName` must skip the high range (`internal/db/ops/migrations.go:28`, `:66-71`, and the 99999 cap at `:24`). The clash warnings stay for the low range. No codemod, no adoption. |

Cost: the existing clash stays for every app that already has eight or more files, until 00007 is the last low pack number. The high range breaks `db to`, the status order, and sqlc for a later kit table. Five digits leave 9999 kit numbers in the high range.

### (c) Timestamps for app migrations

`g resource` and `db new` name app migrations with a 14-digit timestamp, like Rails. Pack migrations keep 00001 to 00007 and continue with 00008.

| Question | Answer |
|---|---|
| Apps that applied 00002-00007 | The existing app files keep their low numbers. The todo app keeps `00003_create_todos.sql`. The next pack number 00008 clashes with every app that already has a `00008`. The clash moves forward one number for each release and never ends, unless a codemod renames the applied app files and updates `goose_db_version` in every database. |
| Order of kit and app | Pack numbers sort before every timestamp. A new kit migration applies before every pending app migration. An old app migration with a low number sorts between pack files. |
| Rollback | By row id, as today. |
| `db status` | Pack files first, then the timestamped app files. Readable. |
| sqlc schema | One directory. Pack files come first in name order. Works. |
| Upgrade step | `NextMigrationName` writes a timestamp. The clock enters the generated output. The rule in `CLAUDE.md` forbids the clock in a render, and the golden and script tests compare file names (`testdata/script/gen_resource.txtar`). |

Cost: the clash for existing apps is not solved. The determinism rule breaks. Two naming schemes live in one directory.

### Variant of (a): the kit module ships the SQL

The kit module could embed the pack SQL itself, like River does. Then the app directory would hold no kit file. This variant was rejected. sqlc needs the kit schema on disk in the app (`sqlc.yaml.tmpl:5`, ADR 0025 §3.12). The owner could not read the kit SQL in the repository. The optional modules (mail, OIDC, audit, flags) would need a module list at run time. The pack mechanism already gives each module its files.

## Decision

Design (a) is the decision. The sections below are the contract.

### 1. Layout

1. The directory `api/db/kit/` holds the kit migrations of an app. The directory `api/db/migrations/` holds the app migrations only.
2. The pack migrations move to the kit directory and keep their names and numbers:
   - `auth`: `api/db/kit/00002_create_auth.sql`, `00003_password_resets.sql` (mail), `00004_oidc_identities.sql` (`auth_oidc`), `00005_users_role.sql`, `00007_users_auth_version.sql`
   - `audit`: `api/db/kit/00006_audit_events.sql`
   - The class stays `seed`. The file content does not change. An existing migration never changes (ADR 0025 §2).
3. `api/db/migrations/00001_init.sql` stays in the app space. It is the first app migration. It creates no objects. The embed pattern `*.sql` needs at least one file, and `g resource` counts from it (`internal/db/ops/migrations.go:37-71`).
4. The `auth` pack ships `api/db/kit/embed.go`, class `managed`. It is the same text as the app embed with the package name `kit` and the doc line "Package kit embeds the migrations that spinup ships.". Every module that ships a kit migration requires `auth` today (`internal/templates/packs/audit/pack.toml:4`). If a later module without `auth` needs a kit migration, the file moves to the `api` pack with a no-op `00001_kit_init.sql`. That move is a separate ADR.
5. The kit numbers are owned by spinup. The next kit number is the highest number across all packs plus one. The flags migration is `00008_feature_flags.sql` (§6). A repository test, `TestKitMigrationNumbers` in `internal/templates`, fails when two packs ship one kit number or when a kit file sits under `api/db/migrations`.
6. The generated `main.go` adds, under `has "auth" .modules`, the import `kitmigrations "<module>/api/db/kit"` and the option `db.WithKitMigrations(kitmigrations.FS)` to the `migrate` command (`main.go.tmpl:122`).
7. `api/sqlc.yaml` (managed) sets `schema: [db/kit, db/migrations]` under `has "auth" .modules`, and `schema: db/migrations` without it. sqlc v2 accepts a list of paths and reads them in order.

### 2. The kit migrator

1. New option: `func WithKitMigrations(fsys fs.FS) MigrateOption`. `NewMigrator` then builds a second goose provider on `fsys` with `goose.WithTableName(KitVersionTable)` and the same locker options as the app provider (`kit/db/migrate.go:128-138`). The exported constant is `const KitVersionTable = "spinup_kit_version"`. Without the option, nothing changes for the app.
2. `goose.ErrNoMigrations` from the kit file system leaves the kit provider nil, as for the app (`kit/db/migrate.go:139-141`).
3. The order of the spaces in `MigrateCommand` (`kit/db/command.go:33-123`):
   - `up`: kit up, then app up, then the `AfterUp` hooks. The plan line counts both spaces: `migrate: N pending`.
   - `down`, `redo`, `to <v>`, `version`: the app space only, as today. The `BeforeDown` hooks run as today.
   - `status`: the kit lines first, then the app lines. A kit line is `applied  kit <file>` or `pending  kit <file>`. The app lines do not change.
   - New: `migrate kit up | down | status | to <version> | version`. Each acts on the kit space only. `migrate kit down` prints `rolled back  kit <file>`. Without the option, `migrate kit ...` fails: `migrate kit: this app has no kit migrations`, `Try: add a module with spinup module add auth`.
4. The `Migrator` type gains `KitStatus`, `KitUp`, `KitDown`, `KitVersion`, `KitUpTo`, `KitDownTo`, and `KitHasVersion`, with the same output lines as their app twins plus the word `kit`. `Pending` and `Status` keep their meaning for the app space. The generated `main.go` needs only `MigrateCommand`.
5. The CLI does not change its commands. `spinup db migrate up` applies both spaces through the app binary (ADR 0005 §1). `spinup db status` prints both spaces. `spinup db to <v>`, `db redo`, and `db migrate down` act on the app space. A kit rollback runs through the app binary: `go run ./cmd/api migrate kit down`. The docs name it (§7).

### 3. Adoption: the transition for an existing database

An existing database holds the pack versions in `goose_db_version`. The kit must recognize them and never apply them twice.

1. Every method of `Migrator` that opens a connection runs `adopt` first, when the kit provider is not nil. `adopt` runs in one transaction under the goose advisory lock `lock.DefaultLockID` (4097083626) with `pg_advisory_xact_lock`, so a concurrent runner waits.
2. `adopt` does nothing when `goose_db_version` does not exist (a new database). The kit provider then creates `spinup_kit_version` on its first use, as goose does today for the app table.
3. Otherwise `adopt` creates `spinup_kit_version` when it does not exist, with the goose DDL (`goose internal/dialects/postgres.go:20-24`) and the zero row. Then, for each version `V` of the kit file system, in version order:
   - skip `V` when `spinup_kit_version` has a row with `version_id = V`
   - skip `V` when the app file system has a file with version `V` (that row belongs to the app)
   - skip `V` when `goose_db_version` has no applied row with `version_id = V`
   - otherwise `INSERT INTO spinup_kit_version (version_id, is_applied) VALUES (V, true)` and `DELETE FROM goose_db_version WHERE version_id = V`
4. The delete is necessary. A row without a file in the app space stops `migrate down` with `ErrVersionNotFound` (`goose provider_run.go:415-422`). After the move, `goose_db_version` holds app rows only, and `deploy.AppliedMigrations` keeps its meaning for the app space (`internal/deploy/flow_release.go:379-399`).
5. `adopt` is idempotent. A second run finds every kit row present or every app file present, and changes nothing. A new database never enters step 3.
6. `adopt` prints one line for each moved row: `adopted  kit <file>`. So an operator sees the move in the deploy log.
7. The todo dogfood app, as the worked case. Files after the codemod (§4): app `00001_init.sql`, `00003_create_todos.sql`. Kit `00002_create_auth.sql`, `00005_users_role.sql`, `00007_users_auth_version.sql`. Rows in `goose_db_version`: 1, 2, 3. `adopt` moves row 2 (kit file 2 exists, no app file 2, row 2 applied). It skips 5 and 7 (not applied). `migrate up` then prints:

   ```
   adopted  kit 00002_create_auth.sql
   migrate: 2 pending
   applied  kit 00005_users_role.sql
   applied  kit 00007_users_auth_version.sql
   ```

   `migrate status` then prints:

   ```
   applied  kit 00002_create_auth.sql
   applied  kit 00005_users_role.sql
   applied  kit 00007_users_auth_version.sql
   applied  00001_init.sql
   applied  00003_create_todos.sql
   ```

8. The rule in step 3 holds because the old space never held two files with one version (goose refuses that, ADR 0025 §2.6). So one applied row has one owner, and the owner is the app when an app file with that version exists on disk.
9. `adopt` checks the schema before it moves a row. The clash warning of ADR 0025 §2.6 told the owner to rename the *app* file. Consider an app that applied `00005_create_x.sql` and then renamed it to `00008_create_x.sql`. Its row 5 has no app file, but `00005_users_role.sql` never ran, so step 3 alone would record a kit file as applied that never ran. Each legacy kit version therefore has a fixed probe in `kit/db/adopt.go`:
    - 2: `to_regclass('public.users')`
    - 3: `to_regclass('public.password_resets')`
    - 4: `to_regclass('public.user_identities')`
    - 5: column `users.role`
    - 6: `to_regclass('public.audit_events')`
    - 7: column `users.auth_version` and column `sessions.user_id`

    `adopt` moves row `V` only when the probe of `V` finds the object. A missing object stops `adopt` and rolls back the transaction: `migrate: row <V> in goose_db_version is not kit migration <file>. Try: renumber the app migration row by hand (UPDATE goose_db_version SET version_id = <app number> WHERE version_id = <V>), then run migrate up again.` Kit versions after 7 never need adoption, so they have no probe. A test covers the renamed case.
10. An app whose owner renamed a pack file after the clash warning (for example `00008_users_role.sql`) has an applied row at a number that the kit does not know. The codemod refuses that app (§4.4). The ADR accepts this. The warning path exists since ADR 0024 and 0025, a few weeks.

### 4. The upgrade of an existing app: codemod `0003_kit_migrations`

1. The codemod runs in the `Before` stage (`internal/migrate/contract.go:14-16`). It moves files before the plan, so the plan sees the kit paths on disk. `Since` is the first release that carries this change. It ships in the same release as codemod 0002, so both use `0.1.0-alpha.419` (amended after review R1a). It moves a file only when the lock lists the pack that ships it (`auth` for 00002-00005 and 00007, `audit` for 00006), so an app-owned file with a pack name stays.
2. It knows the pack base names: `00002_create_auth.sql`, `00003_password_resets.sql`, `00004_oidc_identities.sql`, `00005_users_role.sql`, `00006_audit_events.sql`, `00007_users_auth_version.sql`. The lock cannot tell a pack seed file from an app seed file (context §10), so the names are the key.
3. For each name present in `api/db/migrations`, it moves the file to `api/db/kit/<name>` with the same bytes. It creates the directory. `Plan` lists one change for each file: `Move <name> to api/db/kit`.
4. If `api/db/migrations` holds a file `NNNNN_<pack suffix>.sql` with a number other than the pack number, `Plan` returns the change `Manual step: <file> is a renamed kit migration` and an error with `Try: move the file to api/db/kit under its own number, run UPDATE goose_db_version SET version_id = <pack number> WHERE version_id = <NNNNN> once in every database, then rename the file to the pack name and run spinup upgrade again`. The upgrade stops with this message.
5. If `api/db/kit/<name>` exists already, `Apply` compares the bytes. Equal bytes remove the old file. Different bytes stop with `Try: remove one of the two copies`.
6. The planner gets one rule. A tracked file that is missing on disk and that the new template does not render gets `ActionDelete`, so the dead lock entry goes (`internal/upgrade/plan.go:181-182` today gives `ActionKeep`, "missing on disk"). The apply step then tolerates a missing file on `ActionDelete` (`internal/upgrade/run.go:509`). The moved kit path is not tracked, exists on disk, and is rendered as seed, so it gets `ActionKeep` with the reason `seed` and a new lock entry (`plan.go:183-186`, `plan_lock.go:84-98`).
7. `api/db/kit/embed.go` is a new managed file: `ActionCreate`. `api/sqlc.yaml` is managed: `ActionOverwrite` or `ActionMerge`. `api/cmd/api/main.go` is scaffold: a 3-way merge adds the import and the option. An app that edited the `migrate` line gets `ActionConflict`, like ADR 0024 §11.4.
8. The planner adds the entries of `api/db/kit` to its path set, next to `api/db/migrations` (`internal/upgrade/run_plan.go:185-200`).
9. The warnings for a clash go. `authUpgradeWarnings` drops its `00005_users_role.sql` case (`internal/upgrade/authwarn.go:25-39`). `migrationClashWarnings` and `modules.MigrationClashes` are deleted (`internal/upgrade/authwarn.go:61-82`, `internal/modules/audit.go:12-47`). The troubleshooting entry "Two migrations have the same number" (`docs/troubleshooting.md:202-208`) becomes "A kit migration is in api/db/migrations" with the codemod as the fix.
10. A second codemod, `0004_kit_testkit`, runs in the `After` stage. It rewrites `testkit.DBWithMigrations(t, migrations.FS)` in `api/**/*_test.go` to the form of §5.4, adds the import, and runs `gofmt` through `Ctx.Runner`. A file without the exact call is skipped. A file that fails to format is listed as a manual step.
11. `packs.digest` records `0003_kit_migrations` for `api`, `auth`, and `audit` (`internal/templates/packs.digest`, `docs/contributing/upgrade-matrix.md`).
12. `docs/upgrading.md` tells the owner to run `spinup db migrate up` after this upgrade, and shows the `adopted  kit` line.

### 5. Readers of the migrations directory

Each reader learns the kit directory. The app directory keeps its role for new files.

1. `NextMigrationName` takes the highest number across `api/db/migrations` and `api/db/kit`, plus one (amended after review R1a). A new app migration never reuses a kit number. Without this rule, a new app file `00002_*.sql` would make §3.3 skip the adoption of an applied auth row 2 on a database that has not adopted yet, and kit 00002 would run twice. `newestVersion` reads `api/db/migrations` only (`internal/db/ops/migrations.go:203-217`).
2. `g resource` reads both directories in `findTable` and `hasOwnerKey` (`internal/gen/resource/sql.go:174`, `owner.go:109`). A `ref` field to `users` resolves through the kit directory. The kit directory comes first in the scan order. New files still go to the app directory (`resource.go:218`, `:233`).
3. `g audit` scans both directories for the create statement and for existing triggers (`internal/gen/audit/audit.go:80-128`). It writes its attach migration in the app space (`:152`). The kit space runs first, so the audit functions exist when the attach runs.
4. `kit/testkit.DBWithMigrations` gains options: `func DBWithMigrations(t testing.TB, fsys fs.FS, opts ...DBOption) *pgxpool.Pool` and `func WithKitMigrations(fsys fs.FS) DBOption`. The template database runs the kit space first. The template hash covers both file systems (`kit/testkit/template.go:40-46`). The generated tests pass the option under `auth` (`internal/gen/resource/handlers.go:59`, `:696`, `internal/gen/job/job.go:399`, `:408`).
5. The doctor check `audit-wired` reads both directories (`internal/doctor/auditwired/check.go:45-62`).
6. Deploy lists both directories with a space tag (`internal/deploy/flow_release.go:316-345`), reads both tables on the VM (`:373-399`, the kit table through `to_regclass('public.spinup_kit_version')`), and computes the pending set for each space (`:358-370`). The release journal keeps one list with the tag.
7. The pre-deploy backup checks the `-- spinup:safe` marker in both spaces (`internal/backup/hook.go:205-230`). `backup.AppliedVersion` returns the newest app version, as today (`:180-191`). New kit migrations carry the marker when they keep all data. Existing kit files never change.
8. `db lint` lints both directories (`internal/deploy/flow.go:105-109`, `internal/cli/db_lint.go`).
9. The Postgres major upgrade compares both version tables (`internal/maintain/pg/major.go:16`, `:119-125`).
10. `spinup destroy` keeps both directories (`internal/gen/destroy/destroy.go:82`).

### 6. Worked example: the flags migration (#45, F3)

The `flags` pack requires `auth`. It ships `api/db/kit/00008_feature_flags.sql`, class `seed`, with the marker. The number 00008 is the next kit number after 00007 (§1.5). Sketch. ADR 0029 §6.1 owns the final text (it adds `updated_at` to `feature_flag_users` and an audit attach block):

```sql
-- +goose Up
-- spinup:safe
-- Feature flags (ADR 0029). The kit reads a snapshot of both tables.
CREATE TABLE feature_flags (
    key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,47}$'),
    enabled boolean NOT NULL DEFAULT false,
    roles text[] NOT NULL DEFAULT '{}',
    percentage smallint NOT NULL DEFAULT 0 CHECK (percentage BETWEEN 0 AND 100),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE feature_flag_users (
    flag_key text NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    enabled boolean NOT NULL,
    PRIMARY KEY (flag_key, user_id)
);

-- +goose Down
DROP TABLE feature_flag_users;
DROP TABLE feature_flags;
```

What happens in the todo app: `spinup module add flags` renders the file as `ActionCreate` under `api/db/kit`. No app file can clash, because `00008` is a kit number. `spinup db migrate up` prints `applied  kit 00008_feature_flags.sql`. The foreign key to `users` holds, because `00002` is applied in the same space. sqlc reads `db/kit` first, so the generated store sees `users` and `feature_flags`. `g audit feature_flags` writes an app-space attach migration, and the kit space runs first.

### 7. Docs

- `docs/reference/app-commands.md:96-109`: the `status` output with the `kit` lines, and the `migrate kit` subcommands.
- `docs/commands/db.md:53-58`: both spaces in `spinup db status`. A kit rollback runs through the app binary.
- `docs/concepts.md:107`, `:352`: the two directories, and the glossary entry for "kit migration".
- `docs/troubleshooting.md:202-208`: the new entry of §4.9.
- `docs/upgrading.md`: the `adopted  kit` line and the manual step of §4.4.
- `docs/auth.md` and `docs/audit.md`: the new paths of the migrations.
- `docs/contributing/upgrade-matrix.md`: the codemod IDs.

## Consequences

- A new pack migration never clashes with an app migration. The number rule for kit files is a repository test, not a run-time warning.
- The output of `migrate status` changes for pack files. The scripts that match `applied  00002_create_auth\.sql` or `00005_users_role\.sql` change: `testdata/script/upgrade_roles_order.txtar`, `upgrade_roles_plan.txtar`, `auth_app.txtar`, `auth_ratelimit.txtar`, `audit_module.txtar`, `db_cmds.txtar`, `gen_audit.txtar`. `upgrade_roles_order.txtar` tested the out-of-order case between a pack file and an app file. That case no longer exists. The script becomes a two-space test: an app `00004` with a kit `00005`, and `spinup db to 0 --i-know` resets the app space only.
- `goose.WithAllowOutofOrder(true)` stays (ADR 0024 §5.11). An app that enables mail or OIDC later still gets a lower kit number after a higher one.
- The adoption changes `goose_db_version` once in every database. A backup from before the upgrade restores the old rows, and the next `migrate up` adopts them again. `backup verify` counts rows of `goose_db_version` only (`docs/backups-pgdump.md:146`). The count stays above zero, so the check holds.
- A kit migration that references an app table is forbidden. A review rule, not a test.
- The `api` pack keeps `00001_init.sql`. A new app has app version 1 and kit versions 2, 5, and 7 (plus 3, 4, 6 by module). The same number can exist in both spaces. The docs say so.
- `spinup db to <v>` keeps its meaning for the app space. A kit `to` runs through the app binary.
- A pack that needs `auth` for its kit migration is the rule today. §1.4 names the move if that changes.

## Units for the implementation (KM1)

Every unit also runs `make build lint` and `gofmt -l` on its files. Checks marked † need `DATABASE_URL`.

| Unit | Scope | File set | Check |
|---|---|---|---|
| KM1a kit migrator | §2, §3, §5.4 | `kit/db/migrate.go`, `kit/db/command.go`, `kit/db/adopt.go` (new), `kit/db/doc.go`, `kit/db/*_test.go`, `kit/testkit/template.go`, `kit/testkit/*_test.go` | `go test -race ./kit/db/... ./kit/testkit/...` † with a test that seeds `goose_db_version` with 1, 2, 3 and proves the todo case of §3.7, a concurrent adoption test, and a `migrate kit` lifecycle test |
| KM1b packs and templates | §1, §6 test, §4.11 | `internal/templates/packs/auth/files/api/db/kit/**` (moved files and `embed.go.tmpl`), `internal/templates/packs/auth/pack.toml`, `pack.d/reset.toml`, `pack.d/oidc.toml`, `internal/templates/packs/audit/files/api/db/kit/00006_audit_events.sql` (moved, shared file: the main loop moves it), `internal/templates/packs/audit/pack.toml`, `internal/templates/packs/api/files/api/cmd/api/main.go.tmpl`, `internal/templates/packs/api/files/api/sqlc.yaml.tmpl`, `internal/templates/packs.digest`, `internal/templates/kit_numbers_test.go` (new), `testdata/golden/**` | `make golden`, `go test ./internal/templates/...`, `make script RUN='app_gate_*'` at the gate † |
| KM1c codemods and planner | §4 | `internal/migrate/0003_kit_migrations.go`, `internal/migrate/0004_kit_testkit.go`, their tests and `internal/migrate/testdata/**`, `internal/upgrade/plan.go`, `internal/upgrade/run.go`, `internal/upgrade/run_plan.go`, `internal/upgrade/authwarn.go`, `internal/upgrade/*_test.go`, `internal/modules/audit.go`, `internal/modules/*_test.go`, `testdata/corpus/**` (a corpus app with `00003_create_todos.sql`) | `go test -race ./internal/migrate/... ./internal/upgrade/... ./internal/modules/...`, `make script RUN='upgrade*'`, `make script RUN=module_add` |
| KM1d readers | §5.1-5.3, §5.5-5.10 | `internal/gen/resource/sql.go`, `owner.go`, `handlers.go`, `internal/gen/job/job.go`, `internal/gen/audit/audit.go`, `internal/doctor/auditwired/check.go`, `internal/deploy/flow_release.go`, `internal/deploy/flow.go`, `internal/deploy/*_test.go`, `internal/backup/hook.go`, `internal/backup/*_test.go`, `internal/maintain/pg/major.go`, `internal/maintain/pg/*_test.go`, `internal/gen/**/*_test.go` | `go test -race ./internal/gen/... ./internal/doctor/... ./internal/deploy/... ./internal/backup/... ./internal/maintain/...`, `make script RUN='gen_resource'`, `RUN='gen_audit'`, `RUN='audit_module'`, `RUN='db_cmds'` † |
| KM1e docs and scripts | §7, consequences | `docs/reference/app-commands.md`, `docs/commands/db.md`, `docs/concepts.md`, `docs/troubleshooting.md`, `docs/upgrading.md`, `docs/auth.md`, `docs/audit.md`, `docs/contributing/upgrade-matrix.md`, `testdata/script/upgrade_roles_order.txtar`, `upgrade_roles_plan.txtar`, `auth_app.txtar`, `auth_ratelimit.txtar`, `db_cmds.txtar` | `make doctest`, `make script RUN='upgrade_roles*'` †, `make script RUN='auth_*'` † |

Dependencies: KM1b, KM1c, and KM1d depend on the option names of KM1a (`db.WithKitMigrations`, `db.KitVersionTable`, `testkit.WithKitMigrations`, `testkit.DBOption`). This ADR fixes the names, so the units run in parallel. KM1e runs after the others.

Gate (issue #44): `make build lint test golden script doctest`. Then an upgrade of the todo dogfood app at `/Users/king/Documents/code/todo` moves `00002_create_auth.sql`, applies no migration twice, and `spinup db status` prints the two spaces of §3.7.

Shared files for the main loop: the audit migration moves from `internal/templates/packs/audit/files/api/db/migrations/00006_audit_events.sql` to `.../files/api/db/kit/00006_audit_events.sql` (CLAUDE.md lists it as a shared file). No `go.mod`, lockfile, or `stackversions.yaml` change is needed. `kit/db` uses `goose.WithTableName`, `lock.DefaultLockID`, and `pgx`, all present in `kit/go.mod`.

## Owner answers (2026-11-06)

1. §1.4: the `auth` pack owns `api/db/kit/embed.go`.
2. §2.3: each kit status line carries the word `kit`.
3. §3.6: `adopt` prints one `adopted  kit <file>` line for each moved row.
4. §4.4: an app with a renamed pack file gets a refusal and a manual step. There is no `adopt-as` command.

## Amendments after review R1a (2026-11-06)

1. §5.1: new app migration numbers continue after the highest number in both directories.
2. §3.9: the probe of version 7 also checks `sessions.user_id`.
3. §4.1: `Since` is `0.1.0-alpha.419` for codemods 0003 and 0004, and the codemod checks the lock before it moves a file.
4. §5.6 and §5.7: on a VM where `spinup_kit_version` does not exist yet, deploy and the backup hook project the adoption of §3.3. A legacy kit version counts as applied when `goose_db_version` has it applied and the app directory has no file with that version.
5. §3.6: the `adopted  kit` lines go to standard error, so `migrate version` prints only the number. The CLI parser of `migrate version` reads the last line.
6. §5.8: `db lint` lints both directories with one table set, kit first, so an app index on a kit table still needs `CONCURRENTLY`.
