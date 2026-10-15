# Operate the database

`spinup db` prints help for database operations.
The `db` group has these commands:

| Command | What it does |
| --- | --- |
| `db migrate [up\|down\|status]` | Run the migrations through the app binary. |
| `db status` | List the applied and pending migrations. |
| `db redo` | Roll back the last migration and apply it again. Needs `--i-know`. |
| `db to <version>` | Migrate up or down to a version. A move down needs `--i-know`. |
| `db new <name>` | Create an empty migration file. |
| `db psql` | Open psql on the development database. |
| `db dump [file]` | Write a `pg_dump -Fc` archive (default `.spinup/dumps/app.dump`). |
| `db restore <file>` | Load an archive with `pg_restore --clean --if-exists`. |
| `db seed` | Run the `api/db/seeds/*.sql` files in name order. |
| `db reset` | Recreate the `public` schema of the development database, then migrate up. |
| `db doctor` | Check the PostgreSQL version and the pending migrations. |
| `db sqlc` | Generate the Go query code in `api/internal/store`. |

## Migrations

`spinup db migrate [up|down|status]` runs migrations through the generated app.
The default operation is `up`.
The CLI runs `go run ./cmd/api migrate <operation>` in `api/`.
The command streams both output streams and returns the process exit status.

The target database always comes from `DATABASE_URL` in the environment or `../.env`.
The app reads these values through the kit.
The CLI passes no secrets.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup db migrate --dry-run
go run ./cmd/api migrate up
$ spinup db migrate status --dry-run
go run ./cmd/api migrate status
$ spinup db migrate down
Error: This rolls back the last migration and can lose data
  down requires explicit acknowledgement
  Try: spinup db migrate down --i-know
[exit 1]
```

| Flag | Purpose |
| --- | --- |
| `--i-know` | Acknowledge that `down` can lose data. Available on `db migrate`. |
| `--env <name>` | Resolve an environment from `spinup.toml`. Check its protection before `down`. |
| `--dir`, `-C <directory>` | Find the project root from this directory. |
| `--dry-run` | Print the migration command without execution. Guards still apply. |
| `--yes`, `-y` | Confirm a protected environment only when `CI` is set. |
| `--non-interactive` | Disable prompts. Protected operations fail without confirmation. |
| `--json` | Format CLI errors as JSON. App output retains its format. |
| `--verbose` | Enable extra diagnostics. |
| `--no-color` | Disable color. |
| `--help`, `-h` | Print command help. |

In M1, `--env` selects only the protection guard.
It does not replace `DATABASE_URL`.
Remote targets arrive with deploy.

If `spinup.toml` declares environments, the CLI always resolves the environment.
Without `--env`, it uses `default_env` or the only environment.
If the environment is ambiguous, pass `--env <name>`.
If no environments exist, omit `--env` to use the local database without an environment guard.
If no environments exist, an explicit `--env` produces an error.

For a protected environment, type the environment name in a terminal.
For automation, set `CI=true` and pass `--yes`.
The `down` operation also requires `--i-know`.
A dry run does not bypass either guard.

A missing project, unresolved environment, or missing Go executable produces an error with a `Try:` line.
A failed app process returns its exit status.
Inspect its output before another migration attempt.

<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup db migrate up
...
```

`spinup db status`, `db redo`, and `db to` also run the app binary.
They use the same guards as `db migrate down`.
`db redo` always needs `--i-know`.
`db to <version>` needs `--i-know` when it moves down.
To find the direction, the CLI first runs `go run ./cmd/api migrate version`.
A dry run cannot read the database.
Thus a dry run treats every version below the newest file as a move down.

`spinup db new <name>` writes `api/db/migrations/NNNNN_<name>.sql` with a goose `Up` and `Down` skeleton.
The number is the highest 5-digit number in the directory, plus one.
The name must be snake case, and no other migration can have it.

```console
$ spinup new shop --api-only --module example.com/shop --no-git --non-interactive
...
$ cd shop
$ spinup db new add_widgets
Created api/db/migrations/00002_add_widgets.sql
$ spinup db new add_widgets
Error: Cannot name the migration
  the migration 00002_add_widgets.sql already has the name add_widgets
  Try: choose another name, or edit the migration 00002_add_widgets.sql
[exit 1]
$ spinup db status --dry-run
go run ./cmd/api migrate status
$ spinup db redo
Error: This rolls back the last migration and can lose data
  redo requires explicit acknowledgement
  Try: spinup db redo --i-know
[exit 1]
$ spinup db to 1 --i-know --dry-run
go run ./cmd/api migrate to 1
```

## The development database

The commands are `spinup db psql`, `spinup db dump`, `spinup db restore`, `spinup db seed`, `spinup db reset`, and `spinup db doctor`.

`db psql`, `db dump`, `db restore`, `db seed`, `db reset`, and `db doctor` use the `postgres` service in `deploy/compose.dev.yml`.
They run `docker compose -f deploy/compose.dev.yml exec postgres ...` as user `app` on database `app`.
Start the service first with `docker compose -f deploy/compose.dev.yml up -d`.
If Docker is not installed, the command fails with a `Try:` line.

These commands work only on the local development database.
If `--env` names an environment other than `local`, `dev`, or `development`, the command fails.
`db reset`, `db restore`, and `db seed` call the protection guard when `--env` names a protected environment.
`db reset` and `db restore` also ask for confirmation in a terminal.
Without a terminal, pass `--yes`.

```console
$ spinup db psql --dry-run
docker compose -f deploy/compose.dev.yml exec postgres psql -U app -d app
$ spinup db dump --dry-run
docker compose -f deploy/compose.dev.yml exec -T postgres pg_dump -U app -d app -Fc > .spinup/dumps/app.dump
$ spinup db seed --dry-run
No seed files in api/db/seeds.
Try: add a file such as api/db/seeds/01_users.sql with INSERT statements
$ spinup db reset --dry-run
Error: Cannot reset the database
  this needs confirmation, and spinup cannot prompt without a terminal
  Try: spinup db reset --yes
[exit 1]
$ spinup db reset --dry-run --yes
docker compose -f deploy/compose.dev.yml exec -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -c 'DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;'
go run ./cmd/api migrate up
$ spinup db reset --env production --yes
Error: Cannot reset the database
  spinup db reset works only on the local development database, and --env names "production"
  Try: omit --env to use the postgres service in deploy/compose.dev.yml
[exit 1]
```

`db dump` streams the archive to a temporary file.
It renames the file only when `pg_dump` succeeds, so a failed dump keeps the old file.
`db reset` drops and recreates the `public` schema.
Then it runs `go run ./cmd/api migrate up`, which uses `DATABASE_URL`.

`db doctor` reads `server_version_num` from the server:

- PostgreSQL 15 to 18 pass.
- PostgreSQL 14 passes with a warning. Its end of life is 2026-11-12.
- PostgreSQL 13 and older fail.
- PostgreSQL 19 and newer fail, because version 19 is a beta.

It also lists the pending migrations as a warning.

## sqlc

`spinup db sqlc` runs `go tool -modfile=tools.mod sqlc generate` in `api/`.
The managed file `api/sqlc.yaml` reads `db/migrations` as the schema and `db/queries` as the queries.
It writes the package `store` to `api/internal/store`.
sqlc fails when it has no query, so the command stops early on a new app:

```console
$ spinup new books --api-only --module example.com/books --no-git --non-interactive
...
$ cd books
$ spinup db sqlc
No queries in api/db/queries, so sqlc has nothing to generate.
Try: spinup g resource <name> <field:type>...
```
