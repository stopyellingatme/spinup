# Generate pages and components

These generators add SvelteKit code to the web app in `web/`.
The app must have the web pack.
`spinup new` adds the web pack unless you pass `--api-only`.
Run the generators from the application root.

| Command | What it does |
| --- | --- |
| `spinup g page <route>` | Writes a page at `web/src/routes/<route>/+page.svelte` |
| `spinup g component <name>...` | Runs the shadcn-svelte CLI to add components |
| `spinup g resource <name> <field:type>...` | Also writes the pages of the resource (see below) |

The long forms are `spinup generate page` and `spinup generate component`.
The page and component generators work the same for a Go API and a Rust API.

## Page

```console
$ spinup new demo --module example.com/demo --no-git --non-interactive
...
$ cd demo
$ spinup g page about
Created 1, updated 0 files
Next: cd web && pnpm check
$ spinup g page settings/profile --server --dry-run
create web/src/routes/settings/profile/+page.server.ts (scaffold)
create web/src/routes/settings/profile/+page.svelte (scaffold)
$ spinup g page Settings
Error: generator "page" failed: route "Settings" has the invalid segment "Settings": use lower-case letters, digits, and hyphens, a parameter such as [id], or a group such as (app)
  Try: spinup g page settings/profile
[exit 1]
```

The route is a path below `web/src/routes`, such as `about` or `settings/profile`.
Each segment is one of these:

- A lower-case name with letters, digits, and hyphens, such as `pricing-plans`.
- A parameter, such as `[id]` or `[slug]`.
- A group, such as `(marketing)`. A group does not change the URL.

The generator refuses a route below `api`, because `/api` goes to the API.
The page title comes from the last name segment.
With `--server`, the generator also writes `+page.server.ts` with a `load` function.
The page shows the title that `load` returns.
Both files are scaffold files: the application owns them after the first write.

## Component

```console
$ spinup g component dialog dropdown-menu --dry-run
run pnpm dlx shadcn-svelte@1.7.0 add dialog dropdown-menu --yes --no-deps-install (in web)
```

The generator runs the shadcn-svelte CLI in `web/`.
The CLI version is the `shadcn_registry` answer in `spinup.lock` (default `1.7.0`).
The CLI writes the components to `web/src/lib/components/ui/<name>/`.
It adds their npm dependencies to `web/package.json`, but it does not install them.
After the command, run `cd web && pnpm install`, then `pnpm check`.

The command needs the network: `pnpm dlx` downloads the CLI, and the CLI downloads the components.
The lock does not record the component files, because they are tool output.
`spinup upgrade --ui` compares them with a newer registry version.

## Resource pages

In an app with the web pack, `spinup g resource` also writes the pages of the resource.
Pass `--api-only` to skip them.

<!-- doctest: only go -->
```console
$ spinup g resource todo title:string done:bool 'due:date?' --dry-run
create api/db/migrations/00002_create_todos.sql (seed)
create api/db/queries/todos.sql (scaffold)
create api/internal/handlers/server.go (scaffold)
create api/internal/handlers/todos.go (scaffold)
create api/internal/handlers/todos_test.go (scaffold)
create api/internal/handlers/validation.go (scaffold)
update api/internal/routes/routes_gen.go (managed)
create api/openapi/paths/todos.yaml (scaffold)
create api/openapi/schemas/todo.yaml (scaffold)
update web/src/lib/nav.gen.ts (managed)
create web/src/routes/todos/+page.server.ts (scaffold)
create web/src/routes/todos/+page.svelte (scaffold)
create web/src/routes/todos/[id]/+page.server.ts (scaffold)
create web/src/routes/todos/[id]/+page.svelte (scaffold)
create web/src/routes/todos/[id]/edit/+page.server.ts (scaffold)
create web/src/routes/todos/[id]/edit/+page.svelte (scaffold)
create web/src/routes/todos/fields.svelte (scaffold)
create web/src/routes/todos/new/+page.server.ts (scaffold)
create web/src/routes/todos/new/+page.svelte (scaffold)
create web/src/routes/todos/schema.ts (scaffold)
```

With a Rust API, the API files are the Rust files of [g resource](generate-resource.md), and the pages are the same:

<!-- doctest: only rust -->
```console
$ spinup g resource todo title:string done:bool 'due:date?' --dry-run
create api/migrations/00002_create_todos.down.sql (seed)
create api/migrations/00002_create_todos.up.sql (seed)
update api/src/handlers/mod.rs (managed)
create api/src/handlers/todos.rs (scaffold)
create api/src/handlers/todos_tests.rs (scaffold)
update api/src/models/mod.rs (managed)
create api/src/models/todo.rs (scaffold)
update api/src/routes_gen.rs (managed)
update web/src/lib/nav.gen.ts (managed)
create web/src/routes/todos/+page.server.ts (scaffold)
create web/src/routes/todos/+page.svelte (scaffold)
create web/src/routes/todos/[id]/+page.server.ts (scaffold)
create web/src/routes/todos/[id]/+page.svelte (scaffold)
create web/src/routes/todos/[id]/edit/+page.server.ts (scaffold)
create web/src/routes/todos/[id]/edit/+page.svelte (scaffold)
create web/src/routes/todos/fields.svelte (scaffold)
create web/src/routes/todos/new/+page.server.ts (scaffold)
create web/src/routes/todos/new/+page.svelte (scaffold)
create web/src/routes/todos/schema.ts (scaffold)
```

The route directory uses the plural name in kebab case, such as `web/src/routes/line-items`.

| File | Contents |
| --- | --- |
| `+page.server.ts`, `+page.svelte` | The list: 20 items per page in a table, with pagination. The title is the link to the read view. Each row has a menu (View, Edit, Delete) and an inline checkbox for the first `bool` field that is not null. |
| `[id]/+page.server.ts`, `[id]/+page.svelte` | The read view of one item, with Edit and Delete buttons. Delete asks for a confirmation in a dialog. An unknown id gives a 404 page. |
| `[id]/edit/+page.server.ts`, `[id]/edit/+page.svelte` | The edit form. A save goes back to the read view. |
| `new/+page.server.ts`, `new/+page.svelte` | The create form. A create goes to the read view of the new item. |
| `fields.svelte` | The inputs of the fields. The create and edit forms share them. |
| `schema.ts` | The zod schema of the form, the conversion between the form values and the JSON body, and the helpers that format values |

The forms use superforms with the zod schema in `schema.ts`.
The server checks the values, and each error shows next to its input with `aria-invalid` and `aria-describedby`.
A create, a save, and a delete show a toast after the redirect.
The redirect carries a flag (`?created=1`, `?saved=1`, or `?deleted=1`), and the page removes the flag from the address bar.

The generator also rebuilds `web/src/lib/nav.gen.ts`.
This managed file lists one link for each resource, and the sidebar of the app shell shows the links.
It also sets `home`, the first page that a signed-in user sees.
When the app has the `auth` module, each user sees only the items that the user owns.

The pages call the API with the typed client in `web/src/lib/api/client.ts`.
After the write, the `client` post-step regenerates `web/src/lib/api/schema.d.ts`, so the pages type-check.
With a Rust API, the `client` post-step reads the bundle that `openapi-emit` writes, so it also needs a database URL.
That post-step needs pnpm. If `web/node_modules` is missing, it runs `pnpm install` first.

Each field type gets one input:

| Field type | Input |
| --- | --- |
| `string`, `uuid` | Text input |
| `decimal` | Text input with a decimal keyboard |
| `text` | Textarea |
| `json` | Textarea. The form refuses text that is not a JSON object. |
| `bool` | Checkbox |
| `int`, `bigint`, `float` | Number input |
| `ref:<model>` | Number input for the id |
| `date` | Date input |
| `time` | `datetime-local` input, in UTC |
| `enum:a\|b` | Select |

An empty input of a nullable field sends `null`.
If the API returns 422, the form shows each field error next to its input.
The API reference of the app is at `/api/docs` in dev mode, and in production when `SPINUP_API_DOCS` is true.

## External commands

A generator can ask the runner to run external commands, such as the shadcn-svelte CLI.
The runner runs them after it writes the files and before the post-steps.
`--dry-run` and `--diff` print each command as `run <command> (in <directory>)` and run nothing.
`--json` lists them in the `commands` field.
If a command fails, the written files stay, and the error tells you how to run the command by hand.
