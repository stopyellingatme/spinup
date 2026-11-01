---
title: db — operate the database
parent: Command guides
---

# db — operate the database

`spinup db` runs the migrations of your app and manages the development database.
Use it to apply, create, and undo migrations, and to dump, restore, seed, or reset local data.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup db new add_widgets
Created api/db/migrations/00002_add_widgets.sql
$ spinup db migrate --dry-run
go run ./cmd/api migrate up
```

| I want to ... | Run |
| --- | --- |
| Apply every pending migration | `spinup db migrate` |
| See which migrations ran | `spinup db status` |
| Create a migration file | `spinup db new <name>` |
| Check a migration for unsafe changes | [`spinup db lint`](db-lint.md) |
| Undo the last migration | `spinup db migrate down --i-know` |
| Undo and apply the last migration again | `spinup db redo --i-know` |
| Move to one version | `spinup db to <version>` |
| Open `psql` on the development database | `spinup db psql` |
| Save or load the development data | `spinup db dump`, `spinup db restore <file>` |
| Load sample rows | `spinup db seed` |
| Start again with an empty database | `spinup db reset` |
| Check the PostgreSQL version | `spinup db doctor` |
| Generate Go query code | `spinup db sqlc` |

The commands in the first group run the migrations through your app binary.
They read `DATABASE_URL` from the environment or from `.env`.
The commands in the second group (`psql`, `dump`, `restore`, `seed`, `reset`, `doctor`) work on the Docker Compose database of `spinup dev`.

## Apply migrations

Run `spinup db migrate` to apply every pending migration.
The command runs `go run ./cmd/api migrate up` in `api/` and shows its output.
Add `--dry-run` to print that command without running it.

<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup db migrate
...
```

Run `spinup db status` to list the applied and pending migrations.
Run `spinup db migrate status` to get the same answer.

```console
$ spinup db status --dry-run
go run ./cmd/api migrate status
```

## Create a migration

Run `spinup db new <name>` to write an empty migration with a goose `Up` and `Down` section.
Use a snake case name.
The number is the highest number in `api/db/migrations/` plus one.
If another migration has the name, the command fails.

```console
$ spinup db new add_widgets
Error: Cannot name the migration
  the migration 00002_add_widgets.sql already has the name add_widgets
  Try: choose another name, or edit the migration 00002_add_widgets.sql
[exit 1]
```

Before you deploy the migration, run [`spinup db lint`](db-lint.md).

## Undo a migration

A migration that goes down can delete data.
So `down`, `redo`, and a `to` that moves down all need `--i-know`.

{: .warning }
`spinup db migrate down --i-know` rolls back the last migration. Data in a dropped column or table is gone.

```console
$ spinup db migrate down
Error: This rolls back the last migration and can lose data
  down requires explicit acknowledgement
  Try: spinup db migrate down --i-know
[exit 1]
$ spinup db migrate down --i-know --dry-run
go run ./cmd/api migrate down
```

Run `spinup db redo --i-know` to roll back the last migration and apply it again.
Use it while you write a migration.

```console
$ spinup db redo --i-know --dry-run
go run ./cmd/api migrate redo
```

Run `spinup db to <version>` to move up or down to one version.
A move down needs `--i-know`.
A dry run cannot read the database, so it treats every version below the newest file as a move down.

```console
$ spinup db to 1 --i-know --dry-run
go run ./cmd/api migrate to 1
```

## Choose an environment

`--env <name>` picks an environment from `spinup.toml`.
It only selects the protection guard.
It does not change `DATABASE_URL`, so the migration still runs against the database in your environment.

| Your `spinup.toml` has | What `--env` does |
| --- | --- |
| One environment, or `default_env` | You can omit `--env`. The command uses that environment. |
| Several environments and no `default_env` | You must pass `--env <name>`. |
| No environments | Omit `--env`. An explicit `--env` is an error. |

Before a `down` on a protected environment, type the environment name in a terminal.
Without a terminal, pass `--confirm-env <name>`.
In CI, set `CI=true` and pass `--yes`.
A dry run does not skip these checks.

```sh
spinup db migrate down --env production --i-know --confirm-env production
```

## Use the development database

These commands use the `postgres` service in `deploy/compose.dev.yml`, as user `app` on database `app`.
Start it first with `docker compose -f deploy/compose.dev.yml up -d`, or run `spinup dev`.
If Docker is missing, the command fails and says what to install.

```console
$ spinup db psql --dry-run
docker compose -f deploy/compose.dev.yml exec postgres psql -U app -d app
```

### Dump and restore

`spinup db dump [file]` writes a `pg_dump -Fc` archive.
The default file is `.spinup/dumps/app.dump`.
A failed dump keeps the old file.

`spinup db restore <file>` loads an archive with `pg_restore --clean --if-exists`.
It asks for confirmation in a terminal.
Without a terminal, pass `--yes`.

```console
$ spinup db dump backup.dump --dry-run
docker compose -f deploy/compose.dev.yml exec -T postgres pg_dump -U app -d app -Fc > backup.dump
$ touch backup.dump
$ spinup db restore backup.dump --dry-run --yes
docker compose -f deploy/compose.dev.yml exec -T postgres pg_restore -U app -d app --clean --if-exists < backup.dump
```

### Seed sample rows

`spinup db seed` runs the files `api/db/seeds/*.sql` in name order.
If the directory has no files, the command tells you to add one.

```console
$ mkdir -p api/db/seeds
$ echo "INSERT INTO widgets (id) VALUES (1);" > api/db/seeds/01_widgets.sql
$ spinup db seed --dry-run
docker compose -f deploy/compose.dev.yml exec -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -f - < api/db/seeds/01_widgets.sql
```

### Reset the database

`spinup db reset` drops and recreates the `public` schema, then runs `migrate up`.
It asks for confirmation in a terminal.
Without a terminal, pass `--yes`.

{: .warning }
`spinup db reset` deletes every row in the development database.

```console
$ spinup db reset --dry-run --yes
docker compose -f deploy/compose.dev.yml exec -T postgres psql -U app -d app -v ON_ERROR_STOP=1 -c 'DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;'
go run ./cmd/api migrate up
```

The development commands work only on a local database.
If `--env` names an environment other than `local`, `dev`, or `development`, they fail.

```console
$ spinup db reset --env production --yes
Error: Cannot reset the database
  spinup db reset works only on the local development database, and --env names "production"
  Try: omit --env to use the postgres service in deploy/compose.dev.yml
[exit 1]
```

### Check the PostgreSQL version

`spinup db doctor` reads the server version and lists the pending migrations as a warning.

| Server version | Result |
| --- | --- |
| 15 to 18 | Pass |
| 14 | Pass with a warning. This version is near its end of life. |
| 13 and older | Fail |
| 19 and newer | Fail. Generated apps do not support it yet. |

```console
$ spinup db doctor --dry-run
docker compose -f deploy/compose.dev.yml exec -T postgres psql -U app -d app -At -c 'SELECT current_setting('"'"'server_version_num'"'"')'
go run ./cmd/api migrate status
```

## Generate query code with sqlc

`spinup db sqlc` runs sqlc in `api/`.
It reads the schema from `api/db/migrations` and your queries from `api/db/queries`.
It writes the Go package `store` to `api/internal/store`.
A new app has no queries, so the command stops early and shows the next step.

```console
$ spinup db sqlc
No queries in api/db/queries, so sqlc has nothing to generate.
Try: spinup g resource <name> <field:type>...
```

## Flags

| Flag | Meaning | Example |
| --- | --- | --- |
| `--i-know` | Allow `db migrate down`, `db redo`, or a `db to` that moves down. | `spinup db redo --i-know` |
| `--env <name>` | Pick the environment and its protection guard. | `spinup db migrate down --env staging --i-know` |
| `--confirm-env <name>` | Confirm a protected environment without a terminal. | `--confirm-env staging` |
| `--yes`, `-y` | Confirm `reset`, `restore`, or a protected environment in CI. | `spinup db reset --yes` |
| `--dry-run` | Print the command and skip it. Guards still apply. | `spinup db migrate --dry-run` |
| `--dir`, `-C <dir>` | Find the project from this directory. | `spinup db status -C ~/code/todo` |
| `--json` | Print errors as JSON. `db lint` also prints its findings as JSON. App output keeps its format. | `spinup db lint --json` |

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `down requires explicit acknowledgement` | Add `--i-know`. |
| `could not resolve environment` | `spinup.toml` has several environments. Pass `--env <name>`. |
| The command cannot find Docker | The development commands need Docker. Install it and start `deploy/compose.dev.yml`. |
| `DATABASE_URL` is empty | Set it in the environment or in `.env`. |

## See also

- [db lint — check migrations](db-lint.md)
- [dev — run the local stack](dev.md)
- [Flag reference: db](../site/commands/db.md), [db migrate](../site/commands/db-migrate.md), [db to](../site/commands/db-to.md), [db restore](../site/commands/db-restore.md)
