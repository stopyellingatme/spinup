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
Use `--api-only` to write only the API files.
Use `--no-tests` to leave out the handler test.

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
