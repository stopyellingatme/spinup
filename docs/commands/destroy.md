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

Migrations remain on disk and in the lock.
The `Next:` line suggests `spinup db new drop_<plural>` for resources and models.
Review that migration before applying it.

After deletion, the command uses the registered OpenAPI, sqlc, and gofmt steps.
The sqlc step skips execution when no queries remain.
The gofmt step formats unchanged, managed Go files from generators.
Shared files and preserved handlers can need manual changes after resource destruction.
