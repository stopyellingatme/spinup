# R0.6 Reference app A: API and OpenAPI

Issue: #14. Spike code: `spikes-rs/r0-06-ref-api/`.

## Question

Can a hand-written Rust todo API (axum, utoipa, sqlx 0.9 `query_as!`, embedded migrations) keep the contract of the Go generated todo API? The contract is the routes, the operationIds, the schema names, the status codes, the problem responses, and the TypeScript client that the web pack generates from the spec.

## Method

I wrote two workspace members and one separate workspace:

- `kit` (package `spinup-kit`, library `kit`): the configuration from the environment with `Try:` errors, the `Problem` type, the extractors `Json`, `Path`, and `Query`, the request ID, request log, and security header middleware, `/healthz` and `/readyz`, graceful shutdown with `SHUTDOWN_TIMEOUT`, a lazy `PgPool` from `DATABASE_URL`, the `healthcheck` command, the `migrate` command, and the kit job-queue migration.
- `todo-api`: the todo resource in `src/todos.rs`, with `#[utoipa::path]` handlers, `utoipa-axum` routers, and `sqlx::query_as!`. The binary has the commands `serve` (the default), `migrate up|down|status|version`, `openapi`, and `healthcheck`.
- `genapp`: the pack lock test. It depends on `spinup-kit = "0.1.0"` and has `[patch.crates-io] spinup-kit = { path = "../kit" }`.

The Go oracle is the golden tree. I bundled `testdata/golden/api-openapi/api/openapi/openapi.yaml` with the `gen-resource` paths and schemas through the Go function `internal/gen/openapi.Bundle`. I ran it from a temporary file that I deleted after the run, so the Go code did not change. The result is `compare/go.bundle.gen.yaml`.

The comparison:

1. `todo-api openapi` writes `todo-api/openapi.gen.yaml`. A test fails when the emitted spec and the committed file differ.
2. `compare/specdiff.py` parses both specs and lists each semantic difference (`compare/specdiff.txt`).
3. openapi-typescript 7.13.0 and @hey-api/openapi-ts 0.99.0 generate the client from both specs. Both packages came from the offline pnpm store (`scripts/bin/pnpm install --offline`). I compared the outputs with `diff`.

The tests in `todo-api/tests/api.rs` copy the Go golden tests (`todos_test.go`): CRUD, bad input, and required fields for POST and PUT. They also cover pagination, `/readyz`, the spec contract, and the `migrate` command with the kit tables. The database tests use `#[sqlx::test]`. The kit unit tests cover the configuration errors, the problem defaults, the JSON error rules, a 401 problem, the problem 404 with the request ID, and graceful shutdown with `healthcheck`.

Machine: Apple M1 Max, 10 cores, 64 GB. Other heavy sessions ran at the same time. The 1-minute load average was 15 to 23 during all timings. Builds ran with `nice -n 10` and `CARGO_BUILD_JOBS=4`. Rust 1.99.1, macOS default `split-debuginfo` (unpacked).

## Numbers

Contract:

| Item | Result |
|---|---|
| operationIds | `listTodos`, `createTodo`, `getTodo`, `updateTodo`, `deleteTodo`: equal |
| Schema names | `Problem`, `Todo`, `TodoInput`, `TodoList`: equal, no other component |
| Status codes and descriptions | equal for all 5 operations |
| `application/problem+json` | on 400, 404, 409, 422, and `default`, as in Go. 401: see below |
| Several methods on one path | `routes!(get_todo, update_todo, delete_todo)` on `/todos/{id}`: works |
| `nest("/api", ...)` | the spec and the router both get the `/api` prefix: works |
| Semantic spec differences | 7 (list below) |
| `schema.d.ts` (openapi-typescript) | 0 differing lines (byte-equal) |
| heyapi output (all files) | 0 differing lines (byte-equal) |
| `cargo sqlx prepare --check --workspace` | pass (6 queries in `.sqlx/`) |
| Pack lock test (`genapp`, `cargo build --locked --offline`) | pass, 5.5 s with a warm target directory |
| Tests | kit 12, todo-api 8 (4 with `#[sqlx::test]`): all pass |

The 7 semantic spec differences (`compare/specdiff.txt`):

| Pointer | Go | Rust | Matters to the web client? |
|---|---|---|---|
| `/openapi` | `3.0.3` | `3.1.0` | No. Both generators read both versions. |
| `Todo.due.nullable`, `Todo.due.type` | `type: string`, `nullable: true` | `type: [string, "null"]` | No. Both give `string \| null`. |
| `TodoInput.due.nullable`, `TodoInput.due.type` | the same | the same | No. Both give `due?: string \| null`. |
| `listTodos` parameters `limit`, `offset`: `required` | absent | `false` | No. `false` is the default. |

Before two changes, the TypeScript output had two more differences. I removed both in the spike:

- Property order. utoipa with `preserve_order` keeps the field order. The Go bundle sorts the keys. Without `preserve_order`, utoipa sorts too.
- Path-level parameters. utoipa puts the `id` parameter on each operation. The Go spec puts it on the path item. `kit::openapi::hoist_path_params` moves a path parameter that all operations share to the path item.

utoipa also adds `info.description` and `info.license` from the Cargo package, and it turns `///` comments on schema types into `description` fields. `todo_api::openapi()` removes the two `info` fields. The schema types use `//` comments.

Behavior differences from the Go API (not in the spec):

| Case | Go | Rust spike |
|---|---|---|
| Bad date in the body | 422, no field error | 422, field error `due: has an invalid format` |
| Unknown path | 404 `text/plain` from `ServeMux` | 404 problem |
| JSON response body | ends with `\n` | no final newline |
| Unknown JSON properties | not checked in this spike | ignored |
| Handler panic | 500 problem (`Recover`) | not done: needs the `catch-panic` feature of tower-http |
| `migrate redo`, `migrate to <v>` | yes | not done |
| `*_FILE` variables, `.env`, `/version`, API docs, dev tools | yes | not done |

401: the `gen-resource` app has no auth, so its spec has no 401. The `gen-resource-auth` spec adds a 401 problem response to each operation. In Rust that is one more line in each `responses(...)`. The kit test `unauthorized_is_a_problem` shows a 401 `application/problem+json` answer through the full middleware stack. R0.7 adds the session check.

Build cost (workspace `kit` plus `todo-api`, 234 packages in `Cargo.lock`, 1747 lines of Rust without tests):

| Measurement | Value | Load (1 min) |
|---|---|---|
| Clean debug build (`cargo build --workspace`) | 44.9 s (user 103.8 s) | 17.7 |
| Warm rebuild after a handler-body edit, 5 runs | median 1.68 s (1.49 to 1.81 s) | 17.0 to 17.8 |
| Release build after the debug build (default release profile) | 101.6 s (user 242.3 s) | 15.9 to 22.7 |
| Debug binary `todo-api` | 34.6 MB | |
| Release binary `todo-api` (no LTO, not stripped) | 11.3 MB | |
| `target/` after debug and release builds | 1.9 GB (debug 1.2 GB, release 0.7 GB) | |
| `target/` after build, test, clippy, and prepare | 3.0 GB (from `cargo clean`) | |

The high load makes the build times pessimistic. R0.11 and R0.12 measure on an idle machine.

## Verdict

Verdict: go

The stack keeps the Go contract. The operationIds, the schema names, the status codes, and the problem media type are equal. The generated TypeScript is byte-equal for both openapi-typescript and heyapi. The only spec differences come from OpenAPI 3.1. `utoipa-axum` handles several methods on one path and `nest()` in the router and in the spec. sqlx 0.9 `query_as!` with `.sqlx/` and `cargo sqlx prepare --check` works. The sqlx migrator can own the kit tables. The pack lock test passes. I did not need aide.

## Recommendation

- Use axum 0.8, utoipa 6 with `utoipa-axum` 0.3, and sqlx 0.9. Drop aide and schemars from the plan. Do not enable the utoipa feature `preserve_order`, so the spec sorts its keys as the Go bundle does.
- Put `kit::openapi::hoist_path_params` in the kit. Emit the spec from the binary (`app openapi`). Commit it as the bundle, and keep a drift test.
- Keep the kit extractors `Json`, `Path`, and `Query`. `Json` reads the required properties from the `ToSchema` schema, so the check and the spec cannot drift. It uses `serde_path_to_error` for the field path (already in the lock through axum).
- Box the problem in `ApiError` (`ApiError(Box<Problem>)`). Clippy `result_large_err` rejects a 152-byte error.
- Generated apps must set `SQLX_OFFLINE = "true"` in `.cargo/config.toml` (`[env]`). Without it, a set `DATABASE_URL` makes the macros check the live database, and a database without the app tables breaks the build. `cargo sqlx prepare` overrides the setting. The spinup command for prepare must run `migrate up` on a scratch database first.
- Use reversible migrations (`.up.sql`, `.down.sql`) with sequential versions (`00002_create_todos`). The `migrate!` macro, `sqlx migrate run`, and `#[sqlx::test(migrator = ...)]` share `_sqlx_migrations`, so the CLI and the app binary agree.
- The kit migrator owns the kit tables (the job queue) through its own table, `_spinup_kit_migrations` (`Migrator::dangerous_set_table_name`). The kit runs its migrations first, so app migrations can refer to kit tables. Kit and app version numbers never collide. The spike reads the hidden field `Migrator::table_name`. R1 must keep the table names in its own constants. When the kit is published, its `migrations/` directory must be in the package. R0.8 replaces the stand-in schema `kit/migrations/0001_job_queue.*.sql`. The test shows that a River-style schema (enum, partial and GIN indexes, an unlogged table, and a PL/pgSQL trigger with `$$`) runs through the sqlx migrator.
- Pack lock: a `[patch.crates-io]` path entry gives a lock entry for `spinup-kit` with no `source` and no `checksum`. `cargo build --locked --offline` passes with it. When the kit is published, the entry must change, so `--locked` then fails. Plan: during development, the main loop makes the pack `Cargo.lock` with the patch, offline, from the shared `CARGO_HOME`. The release job publishes the kit, removes the patch, runs `cargo update -p spinup-kit` on the pack, and commits the new lock in the same release. A generated app gets the patch only with `SPINUP_KIT_REPLACE`, as the Go `replace` does now (ADR 0018).
- Not done in this spike: panic recovery, the `*_FILE` variables and `.env`, `/version`, the API docs route, dev tools, `migrate redo` and `migrate to`, and the Go check for unknown JSON properties. R1 adds them in the kit. The database `spinup_r06` on 127.0.0.1:55433 holds the app tables for `cargo sqlx prepare`. Make it again with `sqlx database create` and `sqlx migrate run --source todo-api/migrations`.
