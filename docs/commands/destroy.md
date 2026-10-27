# Remove generated files

`spinup destroy <kind> <name>` removes unchanged files for a resource, model, or handler.
The command compares each file with its hash in `spinup.lock`.
The command preserves changed files and files without a lock entry.
Each preserved file has a `Try:` line.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup destroy resource todo --dry-run
Next: spinup db new drop_todos
```

Use `--dry-run` to inspect the deletions without changing files or running tools.
Use `--dir <directory>` to select a project.

Resource destruction uses `resource.Paths` and preserves shared handler and route files.
Model destruction removes the queries and schemas.
Handler destruction removes the handler and its test.
The command removes lock entries only for deleted files.

For an app with the web pack, resource destruction also removes the pages of the resource.
These are the list, the read view, the edit page, the create page, `fields.svelte`, and `schema.ts` in `web/src/routes/<plural>/`.
The command then rebuilds `web/src/lib/nav.gen.ts`, so the sidebar link of the resource goes away.
If you changed a page, the command keeps it and prints a `Try:` line.
The command also removes the folders that the deletions leave empty, such as `web/src/routes/<plural>/`.
A folder that still holds a kept file or a file of your own stays.
The command never follows a symbolic link, and it never removes the project root.
The command removes the legacy `form.ts` of pages from versions before ADR 0015 when that file is unchanged.

Migrations remain on disk and in the lock.
The `Next:` line suggests `spinup db new drop_<plural>` for resources and models.
Review that migration before applying it.

After deletion, the command uses the registered OpenAPI, sqlc, and gofmt steps.
The sqlc step skips execution when no queries remain.
The gofmt step formats unchanged, managed Go files from generators.
Shared files and preserved handlers can need manual changes after resource destruction.

## Rust API

In a Rust API, resource destruction removes `api/src/handlers/<plural>.rs`, `api/src/handlers/<plural>_tests.rs`, and `api/src/models/<name>.rs`.
Model destruction removes only `api/src/models/<name>.rs`.
Handler destruction removes `api/src/handlers/<name>.rs` and `api/src/handlers/<name>_tests.rs`.
The command then rewrites `api/src/handlers/mod.rs`, `api/src/models/mod.rs`, and `api/src/routes_gen.rs` without the removed modules.
It formats the rewritten files with `rustfmt`.
The migrations in `api/migrations` remain.

After deletion, the command runs the post-steps `sqlx-prepare`, `build`, and `openapi-emit`.
An app with the web pack also gets the `client` step.
If no database URL is set, the command skips the post-steps and prints `Next: spinup db prepare` and `Next: spinup g openapi`.
The other kinds, such as `job`, fail with a `Try:` line.

<!-- doctest: only rust -->
<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup new shelf --api-only --module example.com/shelf --no-git --non-interactive
...
$ cd shelf
$ spinup g resource book title:string
...
$ spinup destroy resource book --dry-run
would remove api/src/handlers/books.rs
would remove api/src/handlers/books_tests.rs
would update api/src/handlers/mod.rs
would remove api/src/models/book.rs
would update api/src/models/mod.rs
would update api/src/routes_gen.rs
Next: spinup db new drop_books
$ spinup destroy resource book
remove api/src/handlers/books.rs
remove api/src/handlers/books_tests.rs
update api/src/handlers/mod.rs
remove api/src/models/book.rs
update api/src/models/mod.rs
update api/src/routes_gen.rs
...
Next: spinup db new drop_books
$ ls api/migrations
00001_init.down.sql
00001_init.up.sql
00002_create_books.down.sql
00002_create_books.up.sql
$ spinup destroy job book
Error: cannot destroy "job": unsupported kind
  Try: spinup destroy resource|model|handler <name>
[exit 1]
```
