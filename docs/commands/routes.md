# List HTTP routes

`spinup routes` lists the HTTP methods, paths, and sources.
The table has `METHOD`, `PATH`, and `SOURCE` columns.
The command sorts entries by path, then method.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup routes --json
[{"method":"GET","path":"/healthz","source":"kit"},{"method":"GET","path":"/readyz","source":"kit"},{"method":"GET","path":"/version","source":"kit"}]
```

The kit supplies `/healthz`, `/readyz`, and `/version`.
OpenAPI operations come from `api/openapi/bundle.gen.yaml`.
Their source is `openapi:<operationId>`.

The command also reads `mux.Handle` and `mux.HandleFunc` registrations in `api/internal/routes/*.go`.
The pattern must be a string constant.
Their source is the file name and line number.
The method is `*` when the pattern has no method.
The command ignores test files and dynamic patterns.

Use `--json` for a JSON array with `method`, `path`, and `source` fields.
Use `--dir <directory>` to select a project.

## Rust API

In a Rust API, the command reads the hand-written routes in `api/src/routes.rs` only.
It finds each `.route("<path>", <method>(...))` call with a string literal path.
The source is `routes.rs:<line>`, the line of the `.route` call.
A method router with more than one method gives one entry for each method.
The method is `*` for `any(...)`.
The command ignores paths that are not string literals, and it never builds the app.

The generated resources do not need this scan.
The `openapi-emit` step writes their operations to `api/openapi/bundle.gen.yaml`.
That step needs a database, so the bundle changes only after `spinup g resource` runs with `DATABASE_URL`, or after `spinup g openapi`.

<!-- doctest: only rust -->
```console
$ printf 'use axum::{Router, routing::get};\n\npub fn routes() -> Router<kit::db::PgPool> {\n    Router::new().route("/ping", get(|| async { "pong" }))\n}\n' > api/src/routes.rs
$ spinup routes
METHOD  PATH      SOURCE
GET     /healthz  kit
GET     /ping     routes.rs:4
GET     /readyz   kit
GET     /version  kit
```
