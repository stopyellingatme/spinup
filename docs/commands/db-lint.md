---
title: db lint — check migrations
parent: Command guides
---

# db lint — check migrations

`spinup db lint` reads the `Up` section of each migration in `api/db/kit` and `api/db/migrations`.
It checks kit migrations first and uses one table history for both directories.
It rejects changes that break the old version of your app while a new version rolls out.
`spinup deploy` runs the same check before it deploys.

```console
$ spinup new shop --api-only --module example.com/shop --no-git --non-interactive
...
$ cd shop
$ printf -- '-- +goose Up\nCREATE TABLE widgets (id bigint PRIMARY KEY, color text);\n\n-- +goose Down\nDROP TABLE widgets;\n' > api/db/migrations/00001_init.sql
$ spinup db new tidy
Created api/db/migrations/00002_tidy.sql
$ printf -- '-- +goose Up\nALTER TABLE widgets DROP COLUMN color;\n\n-- +goose Down\nALTER TABLE widgets ADD COLUMN color text;\n' > api/db/migrations/00002_tidy.sql
$ spinup db lint
00002_tidy.sql:2: drop-column: dropping a column breaks old application versions
  Try: remove the column in a later release after all old versions stop using it
Error: Database lint failed
  found 1 unsafe migration changes
  Try: split the contract step into a later release
[exit 1]
```

Each finding shows the file, the line, the rule name, and a safer way.
If the migrations are safe, the command prints nothing and exits with 0.
To check only some files, name them: `spinup db lint api/db/migrations/00002_tidy.sql`.

## What the command rejects

A rolling deploy runs the old and the new app at the same time.
A rollback runs the old app on the new schema.
So a migration must work with both.

| Rule | Rejected change | Safe way |
| --- | --- | --- |
| `drop-table` | `DROP TABLE` | Stop using the table first. Drop it in a later release. |
| `drop-column` | `ALTER TABLE ... DROP COLUMN` | Stop using the column first. Drop it in a later release. |
| `rename` | Rename a table, column, or other object | Add the new name, copy the data, and drop the old name later. |
| `alter-type` | Change the type of a column | Add a new column, copy the data, and switch over later. |
| `set-not-null` | Add a required column without a default, or `SET NOT NULL` on a column | Add it as nullable or with a default. Backfill, then add the constraint later. |
| `index-not-concurrent` | `CREATE INDEX` on an existing table | Use `CREATE INDEX CONCURRENTLY` in a `NO TRANSACTION` migration. |
| `concurrent-in-tx` | `CREATE INDEX CONCURRENTLY` inside a transaction | Add `-- +goose NO TRANSACTION` to the migration. |
| `unchecked-dynamic-sql` | A `DO` or `EXECUTE` statement, which can hide changes | Write the statement out, or allow it with a reason. |

A table that the same migration creates is new, so you can index or change it freely.

## Change a column safely

Use an expand and contract sequence.
Each step is its own release:

1. Expand. Add the new column or table. Keep the old one.
2. Deploy code that writes both shapes and reads the new one.
3. Backfill the old rows.
4. Contract. After no old version runs, drop the old column in a later migration.

## Create an index on a live table

A plain `CREATE INDEX` blocks writes on a big table.
Create the index in a migration of its own.
Put `-- +goose NO TRANSACTION` first, because `CONCURRENTLY` cannot run in a transaction.

```sql
-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY widgets_color_idx ON widgets (color);

-- +goose Down
DROP INDEX CONCURRENTLY widgets_color_idx;
```

## Allow one exception

If you know a change is safe, put `-- spinup:allow-destructive <reason>` on the line before it.
The reason is required.
The comment allows the next line only.

```console
$ printf -- '-- +goose NO TRANSACTION\n-- +goose Up\n-- spinup:allow-destructive color is unused since release 1.4\nALTER TABLE widgets DROP COLUMN color;\n\n-- +goose Down\nALTER TABLE widgets ADD COLUMN color text;\n' > api/db/migrations/00002_tidy.sql
$ spinup db lint
```

To skip the check once in a deploy, use `spinup deploy --allow-destructive`.
Prefer the comment, because it stays in the file and shows why.

## Machine-readable output

Use `--json` to get the findings as a JSON list.
Each item has `file`, `line`, `rule`, `message`, and `try`.

```console
$ printf -- '-- +goose Up\nALTER TABLE widgets DROP COLUMN color;\n\n-- +goose Down\nALTER TABLE widgets ADD COLUMN color text;\n' > api/db/migrations/00002_tidy.sql
$ spinup db lint --json
[
  {
    "file": "00002_tidy.sql",
    "line": 2,
    "rule": "drop-column",
    "message": "dropping a column breaks old application versions",
    "try": "remove the column in a later release after all old versions stop using it"
  }
]
{"error":{"what":"Database lint failed","why":"found 1 unsafe migration changes","try":"split the contract step into a later release","code":1}}
[exit 1]
```

## See also

- [db — operate the database](db.md)
- [deploy](deploy.md)
- [Flag reference: db lint](../site/commands/db-lint.md)
