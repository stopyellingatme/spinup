---
title: generate resource — add a CRUD API
parent: Command guides
---

# generate resource — add a CRUD API

`spinup g resource` turns one line into a working REST API for a database table.
It writes the migration, the SQL queries, the OpenAPI files, the handlers, and a test.
In an app with a web app, it also writes the pages.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g resource todo title:string done:bool 'due:date?' 'priority:enum:low|normal|high' --dry-run
create api/db/migrations/00002_create_todos.sql (seed)
create api/db/queries/todos.sql (scaffold)
create api/internal/handlers/server.go (scaffold)
create api/internal/handlers/todos.go (scaffold)
create api/internal/handlers/todos_test.go (scaffold)
create api/internal/handlers/validation.go (scaffold)
update api/internal/routes/routes_gen.go (managed)
create api/openapi/paths/todos.yaml (scaffold)
create api/openapi/schemas/todo.yaml (scaffold)
$ spinup g resource todo title:string done:bool 'due:date?' 'priority:enum:low|normal|high'
Created 8, updated 1 files
Next: spinup db migrate up
Next: cd api && go test ./...
```

Run the two `Next:` commands. The API now has these routes:

| Route | What it does |
| --- | --- |
| `GET /api/todos` | List the todos. |
| `POST /api/todos` | Create a todo. |
| `GET /api/todos/{id}` | Read one todo. |
| `PUT /api/todos/{id}` | Replace one todo. |
| `DELETE /api/todos/{id}` | Delete one todo. |

## Name the resource and its fields

The name can be singular or plural, and in snake, kebab, or camel case.
`todo`, `todos`, and `Todo` all give the table `todos` and the URL `/api/todos`.

Write each field as `<name>:<type>`.
Put `?` after the type to allow `null`, such as `'due:date?'`.
Quote a field in the shell when it has `?` or `|`.

| Type | Database column | Go type | Example |
| --- | --- | --- | --- |
| `string`, `text` | `text` | `string` | `title:string` |
| `int` | `integer` | `int32` | `quantity:int` |
| `bigint` | `bigint` | `int64` | `views:bigint` |
| `bool` | `boolean` | `bool` | `done:bool` |
| `float` | `double precision` | `float64` | `ratio:float` |
| `decimal` | `numeric` (a string in JSON) | `string` | `price:decimal` |
| `time` | `timestamptz` | `time.Time` | `sent_at:time` |
| `date` | `date` | `openapi_types.Date` | `due:date` |
| `uuid` | `uuid` | `uuid.UUID` | `token:uuid` |
| `json` | `jsonb` | `map[string]interface{}` | `meta:json` |
| `ref:<model>` | `bigint` with a foreign key and an index | `int64` | `owner:ref:user` |
| `enum:a\|b` | `text` with a `CHECK` constraint | a named string type | `'priority:enum:low\|high'` |

A `ref:<model>` field adds a `<name>_id` column.
The referenced table must already exist in a migration.
Use [`spinup g model`](#add-only-a-table) to create it first.

## What it writes

| File | Class | Contents |
| --- | --- | --- |
| `api/db/migrations/NNNNN_create_<plural>.sql` | seed | The table with `id`, your fields, `created_at`, and `updated_at`. |
| `api/db/queries/<plural>.sql` | scaffold | The sqlc queries to list (with the total count), count, get, create, update, and delete. |
| `api/openapi/paths/<plural>.yaml` | scaffold | The five routes. |
| `api/openapi/schemas/<singular>.yaml` | scaffold | The `Todo`, `TodoInput`, and `TodoList` schemas. |
| `api/internal/handlers/<plural>.go` | scaffold | The handlers. They convert between database types and API types. |
| `api/internal/handlers/<plural>_test.go` | scaffold | A create, get, list, update, and delete round trip, and the error cases. |
| `api/internal/handlers/server.go` | scaffold | The `Server` type and the error mapping. Only the first resource writes it. |
| `api/internal/handlers/validation.go` | scaffold | Rejects a create or update body with a missing or null required field (422, one error for each field). Only the first resource writes it. |
| `api/internal/routes/routes_gen.go` | managed | The router. It is the same for every resource. |

After the write, the generator bundles the OpenAPI files and runs `oapi-codegen`, `sqlc`, and `gofmt`.
The [generate](generate.md) page explains these steps and the file classes.

## Use the API

The list route takes `limit` (1 to 200, default 50) and `offset` (0 to 10000, default 0).
It returns the items in `id` order and the total count.
One query gets the page and the total.

```sh
curl 'http://localhost:8080/api/todos?limit=10&offset=0'
```

An offset above 10000 gets a 422 answer with a field error on `offset`.
PostgreSQL reads and skips every row before the page, so a deep page is slow.
For a large collection, add a filter to the list query and to the route.
Do not page through all the rows.

Errors use the problem details format (RFC 9457, `application/problem+json`):

| Cause | Status |
| --- | --- |
| A bad parameter, or a body that is not JSON | 400 |
| A row that does not exist | 404 |
| A unique value that already exists, or a delete of a row that other rows refer to | 409 |
| A value of the wrong type, a bad enum value, a missing required field, or a broken database rule | 422 |
| The request deadline of the app expired | 503 |

If the client closes the connection, the handler writes no answer.
The log then has only a debug line, not an error.
The handlers log through the request logger of the kit, so each line has the `request_id`.

## Change the resource later

You own the scaffold files after the first write.
Edit the queries, the OpenAPI files, and the handlers as you need.
Then regenerate the code that depends on them:

<!-- doctest: skip -->
```console
$ spinup db sqlc
$ spinup g openapi
```

To add a column, write a new migration with [`spinup g migration`](#add-an-empty-migration).
Do not run `g resource` again for the same table.

If a migration already creates the table, the generator stops.
It does not touch your files.

```console
$ spinup g resource todo title:string
Error: generator "resource" failed: the table todos already exists in api/db/migrations/00002_create_todos.sql
  Try: pass --force to generate the files again, or choose another name
[exit 1]
```

{: .warning }
`--force` writes the create migration again in place. If you already applied it, run `spinup db redo --i-know` to apply the new version. This deletes the rows of the table.

## Skip the test or the web pages

Use `--no-tests` to leave out the handler test file.
Use `--api-only` to write only the API files when the app has a web app.

```console
$ spinup g resource note body:text --no-tests --dry-run
create api/db/migrations/00003_create_notes.sql (seed)
create api/db/queries/notes.sql (scaffold)
create api/internal/handlers/notes.go (scaffold)
unchanged api/internal/routes/routes_gen.go (managed)
create api/openapi/paths/notes.yaml (scaffold)
create api/openapi/schemas/note.yaml (scaffold)
```

The web pages are described in [generate page](generate-page.md#pages-for-a-resource).
If the generator says the app lacks a web file or package, run `spinup upgrade` and run the generator again.

## Add only a table

`spinup g model` writes the migration, the queries, and the OpenAPI schemas, but no routes or handlers.
Use it for a table that other resources refer to.

```console
$ spinup g model tag name:string
Created 3, updated 0 files
Next: spinup db migrate up
Next: cd api && go build ./...
```

## Add a plain handler

`spinup g handler` writes one `http.HandlerFunc` and its test.
It does not edit `routes.go`, so add the line from the `Next:` output yourself.

```console
$ spinup g handler ping
Created 2, updated 0 files
Next: add mux.HandleFunc("GET /api/ping", handlers.Ping) to Register in api/internal/routes/routes.go, and import "example.com/todo/api/internal/handlers"
Next: cd api && go test ./internal/handlers/...
```

## Add an empty migration

`spinup g migration` writes an empty goose migration.
Its number follows the highest number in `api/db/migrations` and `api/db/kit`.
The name must be in snake case.

```console
$ spinup g migration add_index
Created 1, updated 0 files
Next: edit api/db/migrations/00004_add_index.sql
Next: spinup db migrate up
```

## Own your data with auth

If the app has the `auth` module, each new resource belongs to the signed-in user.
The `auth` module needs the web app, so this example app has no `--api-only`.
The flag `--api-only` on the resource skips the web pages.

```console
$ spinup new crm --module example.com/crm --modules ci,auth,audit --no-git --non-interactive
...
$ cd crm
$ spinup g resource contact name:string --api-only --dry-run | head -n 3
create api/db/migrations/00008_create_contacts.sql (seed)
create api/db/queries/contacts.sql (scaffold)
create api/internal/handlers/contacts.go (scaffold)
```

What changes with `auth`:

- The table gets a `user_id` column, and the API does not show it.
- Every route needs a signed-in user. A request without one gets 401.
- A user sees only their own rows. A request for another user's row gets 404, also for update and delete.
- A `ref:<model>` field to another owned resource can refer only to rows of the same user. This rule also applies to a field that refers to the resource itself.
- The generated tests check these rules with two users.

The migration enforces the reference rule with a foreign key on the reference and `user_id` together.
PostgreSQL checks it in the same statement as the insert or update, so the check needs no extra query.
A reference to a row of another user gets the same answer as a reference to a row that does not exist.
The answer is 422, with a field error on the reference field.
Thus the API does not show that the row of the other user exists.

```console
$ spinup g resource project name:string --api-only
...
$ spinup g resource task title:string project:ref:project --api-only --dry-run --diff | grep 'FOREIGN KEY'
+    CONSTRAINT tasks_project_id_fkey FOREIGN KEY (project_id, user_id) REFERENCES projects (id, user_id)
```

The foreign key needs a unique index on `(user_id, id)` in the referenced table.
Each new owned table has this index.
If an older owned table does not have it, the new migration adds it.
That statement can block writes to the older table while PostgreSQL builds the index.
The statement has a `-- spinup:allow-destructive` line, so `spinup db lint` accepts it.

Use `--shared` for a table that every signed-in user can read and write.
A shared table has no owner column, but its routes still need a signed-in user.

```console
$ spinup g resource announcement title:string --shared --api-only --dry-run --diff | grep -A5 'CREATE TABLE'
+CREATE TABLE announcements (
+    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
+    title text NOT NULL,
+    created_at timestamptz NOT NULL DEFAULT now(),
+    updated_at timestamptz NOT NULL DEFAULT now()
+);
```

Without the `auth` module, `--shared` fails with `Try: drop --shared`.
Without `auth`, the output of the resource does not change.

## Limit a resource to roles

Use `--role` to let only some roles use the resource.
The flag takes a comma list of role names from the `[auth]` table of `spinup.toml`.
The default roles are `manager` and `staff`.
The guard covers all five routes.

```console
$ spinup g resource report title:string --role manager --api-only --dry-run --diff | grep -m 1 HasRole
+	if !user.HasRole(roles.Manager) {
```

Each handler checks the role after it checks the user.
A caller with another role gets a 403 problem response.
The OpenAPI files list the 403 answer, and the generated tests send a request from a role outside the list.

Two rules matter:

- A role does not change the owner filter. With `--role` and without `--shared`, each user still sees only their own rows. The generator prints a `Next:` line to remind you. Add `--shared` to share rows between the allowed roles.
- A caller with the wrong role who sends bad input gets 400 or 422 before 403. The caller learns only the shape of the schema.

If a role is not declared, the generator stops and lists the roles that exist.
Add the role to `[auth]` and run [`spinup g roles`](generate-roles.md).
If the app has no `auth` module, add it with `spinup module add auth`.
If `api/internal/roles/roles.gen.go` is missing or differs from `[auth]`, run `spinup g roles`.
If the resource has web pages, they call `requireRole` in each `load` function and form action.

## Add an audit trail

If the app has the `audit` module, each new resource gets an audit trail by default.
The migration attaches triggers that record every insert, update, delete, and truncate.
The resource also gets a history route and a history page.

| Flag | What it does | Example |
| --- | --- | --- |
| `--audit` | Require the audit trail. Fails when the module is missing. | `spinup g resource note body:text --audit` |
| `--no-audit` | Leave the audit trail out of this resource. | `spinup g resource note body:text --no-audit` |
| `--audit-redact a,b` | Hide the values of more columns. | `spinup g resource note body:text --audit-redact body` |
| `--history-role a,b` | Let only these roles read the history. | `spinup g resource note body:text --role manager --history-role manager` |

The trail already hides columns whose names contain `password`, `passwd`, `secret`, `token`, `api_key`, `apikey`, `hash`, or `credential`.

```console
$ spinup g resource note body:text --audit-redact body --role manager --api-only --dry-run --diff | grep 'CREATE TRIGGER notes_audit '
+CREATE TRIGGER notes_audit AFTER INSERT OR UPDATE OR DELETE ON notes FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'body');
```

The history roles default to the resource roles.
The create, update, and delete handlers use `audit.InTx`, so the trail records who made each change.
The history route has the limits and the paging that [generate audit](generate-audit.md#read-the-history) describes.
For a table that already exists, use [`spinup g audit`](generate-audit.md).

## Reserved names

The names `doc`, `docs`, `openapi`, and `openapis` belong to the API reference page.
`g resource` and `g model` refuse them.
Use `document` for documents of your app.

```console
$ spinup g resource doc title:string
Error: generator "resource" failed: resource name "doc" is reserved for the API reference
  Try: choose another name, for example document
[exit 1]
```

## Run the tests

The handler tests need PostgreSQL.
They use the server in `DATABASE_URL` when it is set.

<!-- doctest: skip -->
```console
$ spinup db migrate up
$ cd api && go test ./...
```

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `the table todos already exists` | A migration creates the table. Choose another name, or see [Change the resource later](#change-the-resource-later). |
| `resource name "doc" is reserved` | The name belongs to the API reference. Use another name. |
| `cannot generate a shared resource without the auth module` | Drop `--shared`, or add the `auth` module. |
| `cannot audit the resource because the audit module is missing` | Run `spinup module add audit`, or drop `--audit`. |
| `role "x" is not declared` | Add the role to `[auth]` and run `spinup g roles`. |

## See also

- [generate](generate.md): preview, overwrite rules, and post-steps
- [generate page](generate-page.md): the pages of a resource
- [generate audit](generate-audit.md) and [generate roles](generate-roles.md)
- [Accounts and sign-in](../auth.md) and [Audit trail](../audit.md)
- Flag reference: [`generate resource`](../site/commands/generate-resource.md), [`generate model`](../site/commands/generate-model.md), [`generate handler`](../site/commands/generate-handler.md), [`generate migration`](../site/commands/generate-migration.md)
