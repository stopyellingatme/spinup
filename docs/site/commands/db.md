# spinup db

Operate the application database

## Use

```text
spinup db
```

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-C, --dir` | `` | run in this project directory |
| `--dry-run` | `false` | show changes without writing |
| `--env` | `` | target environment |
| `--json` | `false` | write JSON output |
| `--no-color` | `false` | turn off color |
| `--non-interactive` | `false` | never prompt |
| `--offline` | `false` | never download template versions |
| `--verbose` | `false` | write extra detail |
| `-y, --yes` | `false` | answer yes to prompts |

## Subcommands

- [`spinup db doctor`](db-doctor.md) — Check the PostgreSQL version and pending migrations
- [`spinup db dump`](db-dump.md) — Dump the development database (default .spinup/dumps/app.dump)
- [`spinup db lint`](db-lint.md) — Check migrations for unsafe deploy changes
- [`spinup db migrate`](db-migrate.md) — Run migrations through the application API
- [`spinup db new`](db-new.md) — Create an empty SQL migration
- [`spinup db psql`](db-psql.md) — Open psql on the development database
- [`spinup db redo`](db-redo.md) — Roll back the last migration and apply it again
- [`spinup db reset`](db-reset.md) — Recreate the development database and migrate up
- [`spinup db restore`](db-restore.md) — Restore a dump into the development database
- [`spinup db seed`](db-seed.md) — Run the seed files in the development database
- [`spinup db sqlc`](db-sqlc.md) — Generate the Go query code with sqlc
- [`spinup db status`](db-status.md) — List applied and pending migrations
- [`spinup db to`](db-to.md) — Migrate up or down to a version
