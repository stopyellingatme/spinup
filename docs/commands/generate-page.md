---
title: generate page — add a web page
parent: Command guides
---

# generate page — add a web page

`spinup g page` adds a SvelteKit page to the web app in `web/src/routes/`.
Use it for pages that are not tied to a table, such as an "About" or a "Settings" page.
For a table, `spinup g resource` writes the list, detail, and form pages for you.

The app needs the web app, which `spinup new` adds unless you pass `--api-only`.

```console
$ spinup new todo --module example.com/todo --modules ci,auth --no-git --non-interactive
...
$ cd todo
$ spinup g page about
Created 1, updated 0 files
Next: cd web && pnpm check
```

The page is now at `/about`.
The file is `web/src/routes/about/+page.svelte`, and it is yours to edit.

## Add a page

The argument is a route: a path below `web/src/routes`.
Nested routes work.
Add `--server` to also write `+page.server.ts`, where a `load` function can read data on the server.

```console
$ spinup g page settings/profile --server --dry-run
create web/src/routes/settings/profile/+page.server.ts (scaffold)
create web/src/routes/settings/profile/+page.svelte (scaffold)
```

The page title comes from the last part of the route.
With `--server`, the page shows the title that `load` returns.
Edit `load` to read from the Go API with the typed client.
If the app has the `auth` module, `load` also calls `requireUser`, so a visitor who is not signed in goes to `/login`.
A server page needs a SvelteKit server, so `--server` fails in an app made with `--frontend-mode embed`.

## Route rules

Each part of the route is one of these:

| Part | Example | Note |
| --- | --- | --- |
| A lower-case name | `pricing-plans` | Letters, digits, and hyphens only. |
| A parameter | `items/[id]` | SvelteKit passes `id` to `load`. |
| A group | `(marketing)/pricing` | The group does not change the URL. |

A route below `api` fails, because `/api` goes to the Go API.

```console
$ spinup g page Settings
Error: generator "page" failed: route "Settings" has the invalid segment "Settings": use lower-case letters, digits, and hyphens, a parameter such as [id], or a group such as (app)
  Try: spinup g page settings/profile
[exit 1]
$ spinup g page api/status
Error: generator "page" failed: route "api/status" is under /api, which goes to the Go API
  Try: choose another route, or add an API operation in api/openapi/
[exit 1]
```

If a page file exists with different content, the generator stops.
See the overwrite rules in [generate](generate.md#overwrite-rules).

## Limit a page to roles

Use `--role` to let only some roles open the page.
The flag takes a comma list of role names from the `[auth]` table of `spinup.toml`.
The default roles are `manager` and `staff`.

Only a server `load` can guard a page, so `--role` needs `--server`.
The `load` function and the form actions call `requireRole`.
A visitor who is not signed in goes to `/login`.
A user with another role sees a 403 page.

```console
$ spinup g page reports --server --role manager --dry-run
create web/src/routes/reports/+page.server.ts (scaffold)
create web/src/routes/reports/+page.svelte (scaffold)
$ spinup g page reports --role manager
Error: generator "page" failed: cannot generate a role-guarded page without --server, because only a server load can guard a page
  Try: add --server
[exit 1]
```

If the role is not declared, the generator lists the roles that exist.
See [Roles](../build/roles.md) for the `[auth]` table.

## Pages for a resource

`spinup g resource` writes a full set of pages when the app has a web app.
Pass `--api-only` to the resource to skip them.

```console
$ spinup g resource todo title:string done:bool --dry-run | grep web/
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

The route folder uses the plural name in kebab case, such as `web/src/routes/line-items`.

| Page | What the user sees |
| --- | --- |
| `todos` | A table with 20 items per page and pagination. The title links to the item. Each row has a menu (View, Edit, Delete) and a checkbox for the first `bool` field. |
| `todos/[id]` | One item with Edit and Delete buttons. Delete asks for confirmation. An unknown id gives a 404 page. |
| `todos/[id]/edit` | The edit form. A save goes back to the item. |
| `todos/new` | The create form. A create goes to the new item. |

`fields.svelte` holds the inputs that the create and edit forms share.
`schema.ts` holds the zod schema of the form and the conversion to the JSON body.

The forms use superforms.
The server checks the values, and each error shows next to its input.
A create, a save, and a delete show a toast after the redirect.
If the API returns 422, the form shows each field error next to its input.

Each field type gets one input:

| Field type | Input |
| --- | --- |
| `string`, `uuid` | Text input |
| `decimal` | Text input with a decimal keyboard |
| `text` | Textarea |
| `json` | Textarea that accepts only a JSON object |
| `bool` | Checkbox |
| `int`, `bigint`, `float` | Number input |
| `ref:<model>` | Number input for the id |
| `date` | Date input |
| `time` | `datetime-local` input, in UTC |
| `enum:a\|b` | Select |

An empty input of a nullable field sends `null`.

The generator also rebuilds `web/src/lib/nav.gen.ts`.
This file lists one sidebar link for each resource and sets the first page that a signed-in user sees.
With the `auth` module, each user sees only the items that the user owns.

After the write, the `client` step regenerates `web/src/lib/api/schema.d.ts` so the pages type-check.
It needs pnpm, and it runs `pnpm install` first when `web/node_modules` is missing.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `the app has no web pack` | The app was made with `--api-only`. Pages need the web app. |
| `has the invalid segment` | Use lower-case letters, digits, and hyphens in each part. |
| `cannot generate resource pages: the app is missing ...` | The app is older than the page templates. Run `spinup upgrade`, then run the generator again. |
| `cannot generate a server page in embed mode` | Embed mode has no SvelteKit server. Drop `--server`. |
| `cannot generate a role-guarded page without --server` | Add `--server` to the command. |

## See also

- [generate component](generate-component.md): add shadcn-svelte components to a page
- [generate resource](generate-resource.md): the API behind the pages
- [The web app](../web.md) and [Roles](../build/roles.md)
- Flag reference: [`generate page`](../site/commands/generate-page.md)
