---
title: destroy — undo a generator
parent: Command guides
---

# destroy — undo a generator

`spinup destroy <kind> <name>` removes the files that a generator made, and keeps every file you changed.
Use it when you generated the wrong resource, or when you no longer need one.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup generate resource todo title:string --api-only
...
$ spinup destroy resource todo --dry-run
would remove api/db/queries/todos.sql
would remove api/internal/handlers/todos.go
would remove api/internal/handlers/todos_test.go
would remove api/openapi/paths/todos.yaml
would remove api/openapi/schemas/todo.yaml
Next: spinup db new drop_todos
```

`--dry-run` lists the changes and writes nothing. Run it first, then run the command again without it.

## What you can destroy

| Kind | What it removes | Example |
| --- | --- | --- |
| `resource` | The queries, the handler and its test, the OpenAPI files, and the web pages of a CRUD resource. | `spinup destroy resource todo` |
| `model` | The queries and the OpenAPI schemas of a table. | `spinup destroy model note` |
| `handler` | The handler and its test. | `spinup destroy handler ping` |

The name is the one you gave the generator. [`generate`](generate.md) explains the kinds.
Any other kind stops with `cannot destroy "page": unsupported kind`.

## Remove a resource

Run the command without `--dry-run`. It removes each file, then runs the OpenAPI and `sqlc` steps again and formats the Go code:

```console
$ spinup destroy resource todo
remove api/db/queries/todos.sql
remove api/internal/handlers/todos.go
remove api/internal/handlers/todos_test.go
remove api/openapi/paths/todos.yaml
remove api/openapi/schemas/todo.yaml
...
Next: spinup db new drop_todos
```

If your app has a web app, the command also removes the pages of the resource in `web/src/routes/<plural>/`.
It rebuilds `web/src/lib/nav.gen.ts`, so the sidebar link goes away.
It then removes the folders that the deletions leave empty. A dry run of such an app adds these lines:

```text
would update web/src/lib/nav.gen.ts
would remove web/src/routes/todos/+page.svelte
...
would remove web/src/routes/todos/new/
would remove web/src/routes/todos/
```

## Files you changed stay

The command compares each file with its hash in `spinup.lock`. It keeps a file that you changed and a file that the lock does not list.
For each kept file, it prints a `keep` line and a `Try:` line:

```console
$ spinup generate resource task title:string --api-only
...
$ echo "// mine" >> api/internal/handlers/tasks.go
$ spinup destroy resource task
keep api/internal/handlers/tasks.go (changed or untracked)
Try: delete it by hand, or keep it
remove api/db/queries/tasks.sql
...
```

The command never follows a symbolic link and never removes the project root.
Shared files, such as the route table, can still mention the resource. Check them after a destroy.

## Remove a model or a handler

```console
$ spinup generate model note title:string
...
$ spinup destroy model note --dry-run
would remove api/db/queries/notes.sql
would remove api/openapi/schemas/note.yaml
Next: spinup db new drop_notes
$ spinup generate handler ping
...
$ spinup destroy handler ping --dry-run
would remove api/internal/handlers/ping.go
...
```

The generator for a handler does not edit `api/internal/routes/routes.go`. If you added a route line there, delete it by hand.

## The database stays

The command never touches migrations. The migration file and the table of the resource stay.
To generate the same name again, pass `--force` to the generator, because the migration exists.
The `Next:` line suggests a new migration that drops it. Review the migration before you apply it.

<!-- doctest: skip -->
```console
$ spinup db new drop_todos
$ spinup db migrate up
```

{: .warning }
A migration that drops a table deletes its rows. Take a backup before you apply it to production.

## Flags

| Flag | What it does | Example |
| --- | --- | --- |
| `<kind> <name>` | What to remove: `resource`, `model`, or `handler`, and its name. | `spinup destroy resource todo` |
| `--dry-run` | List the changes and write nothing. | `spinup destroy resource todo --dry-run` |
| `--dir <directory>`, `-C` | Use the app in this folder. | `spinup destroy resource todo -C ~/code/todo` |

## See also

- [`generate resource`](generate-resource.md) makes what `destroy` removes.
- [`db`](db.md) creates and applies the migration that drops the table.
- Flag reference: [`destroy`](../site/commands/destroy.md)
