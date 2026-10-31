# Generate resources, models, handlers, and migrations

These four generators add database and API code to the application.
Run them from the application root.
Each generator shows its plan with `--dry-run` or `--diff`, and does not write.
Without `--force`, a generator does not change a file that you edited.

| Command | What it writes |
| --- | --- |
| `spinup g resource <name> <field:type>...` | A full CRUD API: migration, queries, OpenAPI files, handlers, and a test |
| `spinup g model <name> <field:type>...` | The migration, the queries, and the OpenAPI schemas only |
| `spinup g handler <name>` | A plain `net/http` handler and its test |
| `spinup g migration <snake_name>` | An empty goose migration |

## Resource

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
$ test -s api/internal/store/todos.sql.go
$ test -s api/internal/gen/api.gen.go
```

The resource name can be singular or plural, in snake, kebab, or camel case.
The table and the URL use the plural snake case name, such as `todos` and `/api/todos`.
Each field is `<name>:<type>`. These are the types:

| Type | SQL | OpenAPI | Go API type |
| --- | --- | --- | --- |
| `string`, `text` | `text` | `string` | `string` |
| `int` | `integer` | `integer` (int32) | `int32` |
| `bigint` | `bigint` | `integer` (int64) | `int64` |
| `bool` | `boolean` | `boolean` | `bool` |
| `float` | `double precision` | `number` (double) | `float64` |
| `decimal` | `numeric` | `string` (decimal) | `string` |
| `time` | `timestamptz` | `string` (date-time) | `time.Time` |
| `date` | `date` | `string` (date) | `openapi_types.Date` |
| `uuid` | `uuid` | `string` (uuid) | `uuid.UUID` |
| `json` | `jsonb` | `object` | `map[string]interface{}` |
| `ref:<model>` | `bigint REFERENCES <models> (id)` | `integer` (int64) | `int64` |
| `enum:a\|b` | `text CHECK (... IN ('a', 'b'))` | `string` with `enum` | a named string type |

Put `?` after a type to make the field nullable.
A `ref:<model>` field adds a `<name>_id` column with a foreign key and an index.
The referenced table must exist in a migration.

The generator writes these files:

| File | Class | Contents |
| --- | --- | --- |
| `api/db/migrations/NNNNN_create_<plural>.sql` | seed | The table with `id`, the fields, `created_at`, and `updated_at`. Enum fields get a `CHECK` constraint. |
| `api/db/queries/<plural>.sql` | scaffold | The sqlc queries `List`, `Count`, `Get`, `Create`, `Update` (full replace), and `Delete`. |
| `api/openapi/paths/<plural>.yaml` | scaffold | `GET` and `POST` on `/api/<plural>`, and `GET`, `PUT`, and `DELETE` on `/api/<plural>/{id}`. |
| `api/openapi/schemas/<singular>.yaml` | scaffold | The `<Singular>`, `<Singular>Input`, and `<Singular>List` schemas. |
| `api/internal/handlers/<plural>.go` | scaffold | The strict-server methods and the conversions between the store and API types. |
| `api/internal/handlers/<plural>_test.go` | scaffold | A create, get, list, update, and delete round trip, and the error cases. |
| `api/internal/handlers/server.go` | scaffold | The `Server` type and the error mapping. Only the first resource writes it. |
| `api/internal/handlers/validation.go` | scaffold | The check that rejects a create or update body with a missing or null required field (422 with one field error per key). Only the first resource writes it. |
| `api/internal/routes/routes_gen.go` | managed | The OpenAPI router. Its content is the same for every resource. |

After the write, the generator bundles the OpenAPI files and runs oapi-codegen, then sqlc, then gofmt.

The list operation takes `limit` (1 to 200, default 50) and `offset` (default 0).
It returns the items in id order and the total count.

Errors are problem details (RFC 9457) with the type `application/problem+json`:

| Cause | Status |
| --- | --- |
| A bad parameter, or a body that is not JSON | 400 |
| A row that does not exist | 404 |
| A unique value that exists, or a delete of a row that other rows refer to | 409 |
| A value of the wrong type or format, a bad enum value, or a broken database rule | 422 |

The application owns the scaffold files after the first write.
Edit the queries, the OpenAPI files, and the handlers as you need.
Then run `spinup db sqlc` and `spinup g openapi` again.

If a migration already creates the table, the generator stops.
Pass `--force` to write the files again.
With `--force`, the generator writes the `NNNNN_create_<plural>.sql` migration again in place.
If you applied that migration, run `spinup db redo --i-know` after the change.

```console
$ spinup g resource todo title:string
Error: generator "resource" failed: the table todos already exists in api/db/migrations/00002_create_todos.sql
  Try: pass --force to generate the files again, or choose another name
[exit 1]
```

Other packs can add files to each resource, such as web pages.
The web list page shows each row with a submit button for the first non-null `bool` field (it works without JavaScript) and one shared actions menu.
The page needs `web/src/lib/components/app/row-menu.svelte`.
Run `spinup upgrade` to add this file to an older app.
Use `--api-only` to write only the API files.
Use `--no-tests` to leave out the handler test.

## Ownership and authentication

When `spinup.lock` lists `auth`, each new resource belongs to its signed-in user.
The table has a `user_id` foreign key and an index on `(user_id, id)`.
Every query uses the signed-in user ID.
The API schemas do not expose `user_id`.

Every resource route requires a signed-in user, including resources with `--shared`.
A request without a user receives a 401 problem response.
A request for another user's row receives 404, including update and delete requests.
The list includes only the signed-in user's rows and their total count.
The generated tests verify these rules with two users.

Use `--shared` for a global table that all signed-in users can access.
The shared table has no owner column.
Without `auth`, resource output stays the same, and `--shared` fails.

```console
$ spinup g resource announcement title:string --shared --api-only
Error: generator "resource" failed: cannot generate a shared resource without the auth module
  Try: drop --shared
[exit 1]
```

The names `doc`, `docs`, `openapi`, and `openapis` are reserved for the API reference.
Both `g resource` and `g model` refuse these names.
Use `document` for application documents.

```console
$ spinup g resource doc title:string
Error: generator "resource" failed: resource name "doc" is reserved for the API reference
  Try: choose another name, for example document
[exit 1]
$ spinup g model openapi title:string
Error: generator "model" failed: resource name "openapi" is reserved for the API reference
  Try: choose another name, for example document
[exit 1]
```

## Model

`spinup g model` writes the migration, the queries, and the OpenAPI schemas, but no API paths or handlers.
Use it for a table that other resources refer to.

```console
$ spinup g model tag name:string
Created 3, updated 0 files
Next: spinup db migrate up
Next: cd api && go build ./...
```

## Handler

`spinup g handler` writes a plain `http.HandlerFunc` and its test.
Generators do not edit `routes.go`, so the generator shows the line to add.

```console
$ spinup g handler ping
Created 2, updated 0 files
Next: add mux.HandleFunc("GET /api/ping", handlers.Ping) to Register in api/internal/routes/routes.go, and import "example.com/todo/api/internal/handlers"
Next: cd api && go test ./internal/handlers/...
```

## Migration

`spinup g migration` writes an empty goose migration.
The number is the highest number in `api/db/migrations` plus one.

```console
$ spinup g migration add_index
Created 1, updated 0 files
Next: edit api/db/migrations/00004_add_index.sql
Next: spinup db migrate up
```

## Run the tests

The handler tests need PostgreSQL.
They use the server in `DATABASE_URL` when it is set.
Run `spinup db migrate up`, then `cd api && go test ./...`.

## Roles

Use `--role` to limit a resource to some roles of the app.
The flag takes a comma list of names from the `[auth]` table of `spinup.toml`.
It applies to all five operations.
Without `--role`, the resource only needs a signed-in user.
See [Accounts and sessions](../auth.md#roles).

```console
$ spinup new roleapp --module example.com/roleapp --modules ci,auth --no-git --non-interactive
...
$ cd roleapp
$ spinup g resource todo title:string --role manager,staff --dry-run
create api/db/migrations/00006_create_todos.sql (seed)
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
$ spinup g resource todo title:string --role manager --api-only
Created 8, updated 1 files
Next: spinup db migrate up
Next: cd api && go test ./...
Next: a role guard does not share owned rows. Try: add --shared to share rows between users.
```

Each handler checks `user.HasRole(roles.Manager)` after it checks the user.
A caller with another role gets a 403 problem response.
The OpenAPI file lists a `403` response for each operation.
The generated tests run as an allowed role, and a new test sends a request from a role outside the list to each operation.

The generator stops in these cases:

```console
$ spinup g resource note body:text --role admin
Error: generator "resource" failed: cannot generate a role-guarded resource because role "admin" is not declared
  Try: use one of: manager, staff, or add the role to [auth] and run spinup g roles
[exit 1]
```

- The app has no `auth` module. Run `spinup add auth`.
- A role is not in `[auth]`.
- `api/internal/roles/roles.gen.go` is missing or differs from `[auth]`. Run `spinup g roles`.

Two rules matter:

- A role does not change the owner filter. A resource with `--role` and without `--shared` still shows each user only the rows of that user. Add `--shared` to share the rows between the allowed roles.
- A caller with the wrong role, who sends bad input, gets 400 or 422 before 403. The caller learns only the shape of the schema.

If the resource has web pages, they call `requireRole` in the `load` function and in each form action.
