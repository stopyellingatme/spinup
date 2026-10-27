# Check database migrations

`spinup db lint` checks the `Up` sections in `api/db/migrations`.
It rejects schema changes that can break a rolling deploy or a rollback.

Use an expand and contract sequence for destructive changes.
First, add a compatible column or table in one release.
Deploy code that reads and writes both shapes.
Backfill data after that deploy.
Remove the old shape in a later release, after old application versions stop running.

The command rejects dropped tables and columns, renamed objects, unsafe type changes, and unsafe required columns.
It also rejects indexes that can block writes.
Use `CREATE INDEX CONCURRENTLY` with `-- +goose NO TRANSACTION` for an existing table.

Use `-- spinup:allow-destructive <reason>` on the preceding line to document an approved exception.
The comment needs a reason.
Use `--json` to write the findings as JSON.
Pass file paths to check only those files.
A relative path starts at the project root.

```console
$ spinup db lint --help
...
```

## Rust API

In a Rust API, `spinup db lint` checks the `.up.sql` files in `api/migrations`.
It ignores the `.down.sql` files.
For `CREATE INDEX CONCURRENTLY`, start the migration with the line `-- no-transaction`, the sqlx form of the goose comment.

<!-- doctest: only rust -->
```console
$ spinup new lint --api-only --module example.com/lint --no-git --non-interactive
...
$ cd lint
$ spinup db lint
$ printf 'ALTER TABLE widgets DROP COLUMN name;\n' > api/migrations/00002_drop_name.up.sql
$ spinup db lint
00002_drop_name.up.sql:1: drop-column: dropping a column breaks old application versions
  Try: remove the column in a later release after all old versions stop using it
Error: Database lint failed
  found 1 unsafe migration changes
  Try: split the contract step into a later release
[exit 1]
$ spinup db lint api/migrations/00001_init.up.sql
```
