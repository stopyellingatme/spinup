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

The long forms are `spinup generate resource`, `spinup generate model`, `spinup generate handler`, and `spinup generate migration`.
The Go API and the Rust API get different files.
Each section shows the Go output first, and then the Rust output.
The Rust API section near the end of this page lists the Rust files.

## Resource

<!-- doctest: only go -->
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

<!-- doctest: only rust -->
```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g resource todo title:string done:bool 'due:date?' 'priority:enum:low|normal|high' --dry-run
create api/migrations/00002_create_todos.down.sql (seed)
create api/migrations/00002_create_todos.up.sql (seed)
update api/src/handlers/mod.rs (managed)
create api/src/handlers/todos.rs (scaffold)
create api/src/handlers/todos_tests.rs (scaffold)
update api/src/models/mod.rs (managed)
create api/src/models/todo.rs (scaffold)
update api/src/routes_gen.rs (managed)
```

A Rust resource needs a database for its post-steps (see the Rust API section).

<!-- doctest: only rust -->
<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup g resource todo title:string done:bool 'due:date?' 'priority:enum:low|normal|high'
...
Created 5, updated 3 files
Next: spinup db migrate up
Next: cd api && cargo nextest run
$ test -s api/openapi/bundle.gen.yaml
$ ls api/.sqlx | grep -c '^query-.*[.]json$'
...
```

The resource name can be singular or plural, in snake, kebab, or camel case.
The table and the URL use the plural snake case name, such as `todos` and `/api/todos`.
Each field is `<name>:<type>`. These are the types:

| Type | SQL | OpenAPI | Go API type | Rust API type |
| --- | --- | --- | --- | --- |
| `string`, `text` | `text` | `string` | `string` | `String` |
| `int` | `integer` | `integer` (int32) | `int32` | `i32` |
| `bigint` | `bigint` | `integer` (int64) | `int64` | `i64` |
| `bool` | `boolean` | `boolean` | `bool` | `bool` |
| `float` | `double precision` | `number` (double) | `float64` | `f64` |
| `decimal` | `numeric` | `string` (decimal) | `string` | `String` |
| `time` | `timestamptz` | `string` (date-time) | `time.Time` | `time::OffsetDateTime` |
| `date` | `date` | `string` (date) | `openapi_types.Date` | `time::Date` |
| `uuid` | `uuid` | `string` (uuid) | `uuid.UUID` | `uuid::Uuid` |
| `json` | `jsonb` | `object` | `map[string]interface{}` | `serde_json::Value` |
| `ref:<model>` | `bigint REFERENCES <models> (id)` | `integer` (int64) | `int64` | `i64` |
| `enum:a\|b` | `text CHECK (... IN ('a', 'b'))` | `string` with `enum` | a named string type | a named Rust `enum` |

Put `?` after a type to make the field nullable.
A nullable field has the Rust type `Option<T>`.
The Rust API reads a `decimal` value as text and writes it with a `::text::numeric` cast, so it needs no decimal crate.
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

<!-- doctest: only go -->
```console
$ spinup g resource todo title:string
Error: generator "resource" failed: the table todos already exists in api/db/migrations/00002_create_todos.sql
  Try: pass --force to generate the files again, or choose another name
[exit 1]
```

<!-- doctest: only rust -->
<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup g resource todo title:string
Error: generator "resource" failed: the table todos already exists in api/migrations/00002_create_todos.up.sql
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

<!-- doctest: only go -->
```console
$ spinup g model tag name:string
Created 3, updated 0 files
Next: spinup db migrate up
Next: cd api && go build ./...
```

In a Rust API, `g model` writes the migration pair and `api/src/models/<name>.rs`, and adds the module to `api/src/models/mod.rs`.

<!-- doctest: only rust -->
<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup g model tag name:string
...
Created 3, updated 1 files
Next: spinup db migrate up
Next: cd api && cargo build
```

## Handler

`spinup g handler` writes a plain `http.HandlerFunc` and its test.
Generators do not edit `routes.go`, so the generator shows the line to add.

<!-- doctest: only go -->
```console
$ spinup g handler ping
Created 2, updated 0 files
Next: add mux.HandleFunc("GET /api/ping", handlers.Ping) to Register in api/internal/routes/routes.go, and import "example.com/todo/api/internal/handlers"
Next: cd api && go test ./internal/handlers/...
```

In a Rust API, `g handler` writes `api/src/handlers/<name>.rs` and `api/src/handlers/<name>_tests.rs`.
It adds the module to `api/src/handlers/mod.rs` and the route to the managed file `api/src/routes_gen.rs`.

<!-- doctest: only rust -->
```console
$ spinup g handler pong --dry-run
update api/src/handlers/mod.rs (managed)
create api/src/handlers/pong.rs (scaffold)
create api/src/handlers/pong_tests.rs (scaffold)
unchanged api/src/models/mod.rs (managed)
update api/src/routes_gen.rs (managed)
```

<!-- doctest: only rust -->
<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup g handler ping
...
Created 2, updated 2 files
Next: cd api && cargo test --locked ping
```

## Migration

`spinup g migration` writes an empty goose migration.
The number is the highest number in `api/db/migrations` plus one.

<!-- doctest: only go -->
```console
$ spinup g migration add_index
Created 1, updated 0 files
Next: edit api/db/migrations/00004_add_index.sql
Next: spinup db migrate up
```

In a Rust API, `g migration` writes an empty sqlx pair in `api/migrations`, as `spinup db new` does.

<!-- doctest: only rust -->
<!-- doctest: requires-env DATABASE_URL -->
```console
$ spinup g migration add_index
...
Created 2, updated 0 files
Next: edit api/migrations/00004_add_index.up.sql
Next: spinup db migrate up
```

## Run the tests

The handler tests need PostgreSQL.
They use the server in `DATABASE_URL` when it is set.
Run `spinup db migrate up`, then `cd api && go test ./...`.
In a Rust API, run `spinup db migrate up`, then `cd api && cargo nextest run`.

## Rust API

A Rust resource writes these files:

| File | Class | Contents |
| --- | --- | --- |
| `api/migrations/NNNNN_create_<plural>.up.sql` | seed | The table with `id`, the fields, `created_at`, and `updated_at`. |
| `api/migrations/NNNNN_create_<plural>.down.sql` | seed | The `DROP TABLE` statement. |
| `api/src/models/<singular>.rs` | scaffold | The row type, the input type, and the `query_as!` queries. |
| `api/src/handlers/<plural>.rs` | scaffold | The axum handlers with their utoipa annotations. |
| `api/src/handlers/<plural>_tests.rs` | scaffold | A create, get, list, update, and delete round trip, and the error cases. |
| `api/src/handlers/mod.rs`, `api/src/models/mod.rs` | managed | One sorted `mod` line for each resource. |
| `api/src/routes_gen.rs` | managed | The routes of every resource. |

The generator formats every planned `.rs` file with `rustfmt` before it writes.
After the write, it runs these post-steps in order: `migrate`, `sqlx-prepare`, `build`, and `openapi-emit`.
An app with the web pack also gets the `client` step.
`sqlx-prepare` is `spinup db prepare`, and it refreshes the query cache in `api/.sqlx`.
`openapi-emit` writes `api/openapi/bundle.gen.yaml` from the built app.

The post-steps need a database.
The URL comes from `DATABASE_URL`, then the file that `DATABASE_URL_FILE` names, then `api/.env`, then `.env`.
If no URL is set, the generator writes the files and skips the post-steps.
Then it prints `Skipped post-steps: DATABASE_URL is not set` with `Next: spinup db prepare` and `Next: spinup g openapi`.
After you edit a query, run `spinup db prepare`, and then `spinup g openapi`.
