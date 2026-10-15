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

```console
$ spinup db lint --help
...
```
