---
title: generate — add code to your app
parent: Command guides
---

# generate — add code to your app

`spinup generate` (alias `spinup g`) writes new code into your app: a CRUD API, a page, a background job, a mailer, and more.
Run it from the app root.
Start with a preview, then write the files.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g handler ping --dry-run
create api/internal/handlers/ping.go (scaffold)
create api/internal/handlers/ping_test.go (scaffold)
$ spinup g handler ping
Created 2, updated 0 files
Next: add mux.HandleFunc("GET /api/ping", handlers.Ping) to Register in api/internal/routes/routes.go, and import "example.com/todo/api/internal/handlers"
Next: cd api && go test ./internal/handlers/...
```

The last lines start with `Next:`. They are the commands that you most likely run after the generator.

## Available generators

Run `spinup g <kind> --help` for the arguments and flags of one generator.

| I want to ... | Run | Guide |
| --- | --- | --- |
| Add a CRUD API for a table | `spinup g resource task title:string` | [Resources](generate-resource.md) |
| Add a table without an API | `spinup g model tag name:string` | [Resources](generate-resource.md#add-only-a-table) |
| Add one plain HTTP handler | `spinup g handler ping` | [Resources](generate-resource.md#add-a-plain-handler) |
| Add an empty SQL migration | `spinup g migration add_index` | [Resources](generate-resource.md#add-an-empty-migration) |
| Add a page to the web app | `spinup g page about` | [Pages](generate-page.md) |
| Add shadcn-svelte components | `spinup g component dialog` | [Components](generate-component.md) |
| Rebuild the TypeScript API client | `spinup g client` | [API client](generate-client.md) |
| Bundle OpenAPI files and rebuild the Go server code | `spinup g openapi` | [OpenAPI](generate-openapi.md) |
| Add a background job | `spinup g job send_digest` | [Jobs](generate-job.md) |
| Add an email template and sender | `spinup g mailer welcome name:string` | [Mailers](generate-mailer.md) |
| Regenerate the role constants | `spinup g roles` | [Roles](generate-roles.md) |
| Add an audit trail to a table | `spinup g audit task` | [Audit](generate-audit.md) |
| Add a typed tool for AI clients | `spinup g mcp tool lookup_order` | [MCP tools](generate-mcp.md) |
| Add a Rust, Python, or Node service | `spinup g service ocr --lang rust` | [Services](generate-service.md) |
| Add a Grafana dashboard | `spinup g dashboard service-health` | [Dashboards](generate-dashboard.md) |
| Add a Prometheus alert | `spinup g alert HighErrors --expr 'up == 0'` | [Alerts](generate-alert.md) |

A plugin can add more kinds. They appear in `spinup g --help`.

## Preview before you write

Use `--dry-run` to list each file, what the generator does with it, and its file class.
Use `--diff` to see the content as a unified diff.
Neither flag writes a file.

```console
$ spinup g handler pong --diff
--- a/api/internal/handlers/pong.go
+++ b/api/internal/handlers/pong.go
@@ -0,0 +1,18 @@
...
```

The action is `create`, `update`, or `unchanged`.
The class tells you who owns the file after the write:

| Class | Meaning |
| --- | --- |
| `scaffold` | The file is yours. Edit it freely. |
| `managed` | The generator owns the file. Do not edit it. The generator rewrites it on the next run. |
| `seed` | The file is a starting point, such as a migration. Spinup writes it once, and you own it from then on. |

## Overwrite rules

A generator never silently replaces a file that you edited.
If the content is the same, the generator writes nothing.
If the content is different, the generator stops and lists the files.
Pass `--force` to replace them.
The exception is a `managed` file that a generator wrote, such as `api/internal/routes/routes_gen.go`. The generator updates it without `--force`.

```console
$ echo "// mine" >> api/internal/handlers/ping.go
$ spinup g handler ping
Error: generator "handler" would overwrite existing files with different content:
  api/internal/handlers/ping.go
  Try: pass --force, or remove the files
[exit 1]
$ spinup g handler ping --force
Created 0, updated 1 files
...
```

{: .warning }
`--force` throws away your edits to the listed files. Run `spinup g <kind> --diff` first to see what changes.

`--dry-run` and `--diff` follow the same rules, so a preview shows the error that a real run would show.

## What happens after the write

Some generators run tools right after they write files.
These steps keep generated code in sync with its source:

| Step | What it does |
| --- | --- |
| `openapi` | Bundles `api/openapi/` and generates the Go server code. |
| `sqlc` | Generates the Go query code from `api/db/queries/`. |
| `client` | Generates the TypeScript types for the web app. |
| `service-clients` | Generates a Go client for each sidecar service that has an OpenAPI file. |
| `gofmt` | Formats the Go files that the generator wrote. |

The steps run in this order, once each. A generator runs only the steps that its files need.
`--dry-run` and `--diff` skip them.

If a tool fails, your new files stay on disk.
The error shows the tool output and the command to run by hand.
Fix the cause and run that command. You do not need to run the generator again.

## JSON output

Add `--json` to get one JSON object for scripts and editors.
It works with `--dry-run`.

```console
$ spinup g migration add_index --dry-run --json
{"files":[{"path":"api/db/migrations/00002_add_index.sql","action":"create","class":"seed"}],"post_steps":[],"next":["edit api/db/migrations/00002_add_index.sql","spinup db migrate up"]}
```

The fields are `files`, `post_steps`, `next`, and `commands` (the external commands, when there are any).

## The picker

Run `spinup g` in a terminal to choose a generator from a list.
Then type its arguments on one line, such as `resource task title:string`.
The picker splits the line at spaces. If an argument has a space, run the full command with quotes.

Without a terminal, `spinup g` lists the generators and fails.
A name that does not exist fails too:

```console
$ spinup g nope
Error: Cannot generate files
  generator "nope" is not registered
  Try: spinup g <kind> --help
[exit 2]
```

## Flags that every generator accepts

| Flag | What it does | Example |
| --- | --- | --- |
| `--dry-run` | List the files without writing. | `spinup g job send_digest --dry-run` |
| `--diff` | Show the changes as a diff without writing. | `spinup g job send_digest --diff` |
| `--force` | Replace files that have different content. | `spinup g job send_digest --force` |
| `--json` | Print the result as JSON. | `spinup g job send_digest --json` |
| `--dir`, `-C <directory>` | Use the app in this directory. | `spinup g roles -C ~/code/todo` |
| `--non-interactive` | Never show the picker. | `spinup g --non-interactive` |

## See also

- [Resources, models, handlers, and migrations](generate-resource.md)
- [Command guides](index.md) and the [Concepts](../concepts.md) page (file classes, the lock file)
- Flag reference: [`generate`](../site/commands/generate.md)
