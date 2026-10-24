# R0.10 Generator approach and formatter

Issue: #18. Spike code: `spikes-rs/r0-10-generator/`.

## Question

Can minijinja templates generate a resource (the model, the handlers, the migration, and the generator-owned `routes_gen.rs` and `handlers/mod.rs`) into the Rust reference app? Which formatting plan keeps the lock hash stable and `cargo fmt --check` clean: (A) rustfmt-stable templates, (B) `prettyplease`, or (C) a `rustfmt` post-step? Which post-step order makes the first build after a new migration work?

## Method

The workspace has three members:

- `kit` and `todo-api`: copies of the R0.6 app. I removed the hand-written `todo-api/src/todos.rs`. The app template part (`lib.rs`, `main.rs`, `Cargo.toml`) now declares `handlers`, `models`, and `routes_gen`.
- `gen` (library `spinup_gen`, binaries `gen` and `compare`): the generator. `gen` is a reserved keyword in edition 2024, so the library has another name. The case helpers and the Go Unicode tables are copied from R0.2 (`gen/src/filters.rs`, `gen/fixtures/unicode.json`). The templates use the R0.2 delimiters (`[[ ]]`, `[% %]`, `[# #]`), strict undefined values, no escaping, and `trim_blocks` with `lstrip_blocks`.

`gen resource <name> <field:type>...` follows ADR 0007 §1. The plan step renders and formats, and it does not write. The apply step writes. It stops on a changed scaffold file (`Try: pass --force`), and it rewrites a generator-owned (`managed`) file. It records each file in `spinup.lock` with the class, `template = "gen/resource"`, and the SHA-256 of the written bytes. Per resource it writes:

| File | Class |
|---|---|
| `src/models/<singular>.rs` (types, enum, row, `TryFrom`) | scaffold |
| `src/handlers/<plural>.rs` (`#[utoipa::path]` handlers, `query_as!`, `routes()`) | scaffold |
| `migrations/NNNNN_create_<plural>.{up,down}.sql` (next number, first is `00002`) | scaffold |
| `src/handlers/mod.rs`, `src/models/mod.rs` (one line per resource, sorted) | managed |
| `src/routes_gen.rs` (one `merge` line per resource, sorted) | managed |

The rust types come from the field view in Rust code, so the templates hold no type logic. Supported types: `string`, `text`, `int`, `bigint`, `bool`, `float`, `time`, `date`, and `enum:a|b`, each with an optional `?`.

The committed `todo-api` is the generator output for `todo title:string done:bool due:date? priority:enum:low|normal|high`, then `note title:string body:text?`. The todo part of the emitted spec is equal to the R0.6 spec (every path and schema compares equal). The R0.6 tests pass without change, except for the note additions. I added one note CRUD test.

The `compare` binary runs each plan in a temporary Cargo workspace (kit plus a fresh base app). It generates todo then note, and measures:

1. the tree hash of the app over two fresh runs,
2. the files that a second run writes,
3. `cargo fmt --all --check` on the generated app,
4. the files that `cargo fmt --all` then changes, and the lock records that no longer match the files,
5. the plan step time (median of 5 runs),
6. a sweep of longer resource and field names through each plan (rustfmt check per file).

Plan C first used one `rustfmt` process per file through stdin. The final plan C writes all Rust files of one run to a staging directory and runs one `rustfmt --edition 2024` process. rustfmt follows `mod x;` and fails on a missing child, and `skip_children` is unstable. So the stage gets an empty stub for each missing child. The app `rustfmt.toml` applies through `--config-path`.

`gen/scripts/first-build.sh` tests the first build after a new migration. It copies the workspace, generates todo, migrates, prepares, and builds. Then it generates note and tries each order. It uses the database `spinup_r10` on 127.0.0.1:55433 and leaves it migrated with todos and notes. The app sets `SQLX_OFFLINE = "true"` in `.cargo/config.toml`, as in R0.6.

Machine: Apple M1 Max, 10 cores, 64 GB, macOS (Darwin 25.4.0), Rust 1.99.1, rustfmt 1.10.0, sqlx-cli 0.9.0. Other heavy sessions ran at the same time. The 1-minute load average was 12.6 to 23.5 during all timings. Commands ran with `nice -n 10` and `CARGO_BUILD_JOBS=4`.

## Numbers

Reference resources (todo, then note). Load 13.1 before, 12.6 after.

| Plan | Run 2 tree hash equal | Second run writes | `cargo fmt --check` | Files `cargo fmt` changes | Lock records stale after `cargo fmt` |
|---|---|---|---|---|---|
| A (stable templates) | yes (`67f245040a64…`) | 0 files | pass | 0 | 0 |
| B (prettyplease) | yes (`08125473167a…`) | 0 files | fail | 4 (`handlers/notes.rs`, `handlers/todos.rs`, `models/todo.rs`, `routes_gen.rs`) | 4 |
| C (rustfmt post-step) | yes (`67f245040a64…`) | 0 files | pass | 0 | 0 |

A and C give the same bytes for the reference resources. `cargo fmt --all` took 100 to 136 ms on the generated app.

Plan step time (render and format of todo plus note, no writes, median of 5 runs, load 13.1):

| Plan | Median | Min to max | Added over plan A |
|---|---|---|---|
| A | 3.3 ms | 3.1 to 3.7 ms | 0 ms |
| B | 10.5 ms | 10.4 to 13.1 ms | +7.2 ms |
| C, one rustfmt process per run | 90.3 ms | 89.7 to 101.1 ms | +87.0 ms (about 44 ms per resource) |
| C, one rustfmt process per file (first version) | 443.2 ms | 436.7 to 474.4 ms | +439.1 ms (load 22.8) |

Name sweep (fields of todo with a nullable enum). The cells give the number of files that rustfmt would change:

| Resource | Name length | A | B | C |
|---|---|---|---|---|
| note, event, ticket, invoice, customer, ship_note | 4 to 9 | 0 | 3 | 0 |
| order_line | 10 | 1 (`order_lines.rs`) | 3 | 0 |
| price_quote, product_note, shipping_label, customer_review | 11 to 15 | 1 | 3 | 0 |
| subscription_plan, warehouse_location, customer_support_ticket | 17 to 23 | 1 | 3 | 0 |
| todo, field `delivery_priority` | 4 | 1 (`todo.rs`) | 3 | 0 |
| todo, field `customer_delivery_priority_level` | 4 | 2 | 3 | 0 |

Why A breaks: rustfmt puts `async fn get_<x>(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<X>> {` on one line only when it fits in 100 columns. For `todo` the line is 91 columns. Each extra name character adds 2 columns. Long field names push the `TryFrom` and bind lines past the limit too. One template trick worked: `routes_gen.rs` uses one `router = router.merge(...)` statement per resource. A method chain would change layout with the number of resources (`chain_width` 60).

Why B fails: prettyplease removes the blank lines between items and between `use` groups. rustfmt then sorts the `crate::` import into the other group. prettyplease also removes `//` comments, puts call arguments on one line, indents chains after a macro call, and does not wrap long `match` arms. The output is valid Rust, but it is not rustfmt output. So every `cargo fmt` by the user makes the lock records stale.

First build after a new migration (`gen resource note`, migration `00003`). Load 12.8 before, 11.8 after. Times with a warm target directory:

| Step | Exit | Time | What the user sees |
|---|---|---|---|
| State 0: gen todo, `sqlx migrate run`, `cargo sqlx prepare --workspace`, `cargo build` | 0 | 0.5 s, 0.1 s, 4.2 s, 5.0 s | |
| 1. `cargo build` first (`SQLX_OFFLINE=true`, stale `.sqlx/`) | 101 | 0.9 s | ``error: `SQLX_OFFLINE=true` but there is no cached data for this query, run `cargo sqlx prepare` to update the query cache or unset `SQLX_OFFLINE` `` (one per new query) |
| 2. migrate through the app binary (`cargo run -- migrate up`) | 101 | 0.8 s | the same compile error: the binary cannot build, so it cannot migrate |
| 3. `cargo sqlx prepare` before migrate | 1 | 1.2 s | `error returned from database: relation "notes" does not exist` |
| 4. `SQLX_OFFLINE=false cargo build` before migrate | 101 | 1.2 s | `relation "notes" does not exist` |
| 5. migrate with sqlx-cli (`sqlx migrate run`, no app build) | 0 | 0.0 s | |
| 6. `cargo build` after migrate, before prepare | 101 | 0.8 s | the `SQLX_OFFLINE=true` error of step 1 |
| 7. `cargo sqlx prepare --workspace` | 0 | 1.4 s | |
| 8. `cargo build` | 0 | 3.4 s | |
| 9. `cargo sqlx prepare --check` | 0 | 1.0 s | |
| 10. edit a query, `cargo build` with the old `.sqlx/` | 101 | 1.0 s | the `SQLX_OFFLINE=true` error of step 1 |
| 11. `cargo sqlx prepare --check` after the edit | 1 | 1.1 s | `prepare check failed: .sqlx is missing one or more queries; you should re-run sqlx prepare` |
| 12. prepare, then build | 0 | 3.0 s | |

The first `cargo sqlx prepare --workspace` in the spike workspace (a cold `cargo check` of all members) took 47.4 s at load 9 to 15. The committed `.sqlx/` holds 12 queries.

Tests: `gen` has 2 unit tests and 7 acceptance tests (committed app is current, second run changes nothing, determinism and idempotence of each plan, rustfmt cleanliness of each plan, long-name break of A, owned files and migration numbers, file classes). `kit` has 12 tests, and `todo-api` has 9 (5 with `#[sqlx::test]`). All pass.

## Verdict

Verdict: go

minijinja generates a resource that compiles, keeps the R0.6 contract, and passes the R0.6 tests. Plan C passes every check for every name: the hash is stable over two runs, a second run writes nothing, and `cargo fmt --check` passes. Plan A passes for the reference names, but it breaks at a resource name of 10 characters. Plan B fails `cargo fmt --check` for every resource. The cost of C is about 44 ms per resource with one rustfmt process per run. The first build after a new migration works with the order migrate, prepare, build. Migrate must not build the app.

## Recommendation

- **Plan C.** The generator formats all planned Rust files with one `rustfmt --edition 2024` process in the plan step, before apply. The lock hash covers the formatted bytes. Use a staging directory with stubs for declared child modules, and pass the app `rustfmt.toml` with `--config-path`. Do not use stdin per file: it costs about 5 times more.
- Keep the templates rustfmt-stable anyway. Then plan C is a no-op for usual names, and the templates stay readable. Keep one golden test that asserts A equals C for the reference resources (`rustfmt_cleanliness_of_each_plan`). It finds template drift early. Use statement-per-line shapes for lists that grow (`routes_gen.rs`), not method chains.
- Drop `prettyplease` and the `syn` printing path from the plan.
- The generated app pins its toolchain in `rust-toolchain.toml` with `components = ["rustfmt", "clippy"]`. Then the rustfmt version, and so the hashes, stay the same on every machine of the project. When rustfmt is missing, the generator fails with `Try: rustup component add rustfmt`.
- **Post-step order** for `spinup g resource` and `spinup g migration`: (1) plan with rustfmt, apply, and lock; (2) `migrate up` on the development database; (3) `cargo sqlx prepare --workspace`; (4) `cargo build` (or `cargo check`); (5) `openapi-emit` (it runs the app binary, so it needs step 4); (6) `client`. Keep `SQLX_OFFLINE = "true"` in `.cargo/config.toml`.
- **Change to ADR 0005 §1 for Rust.** `spinup db migrate` cannot run through the app binary after a new resource: the binary does not compile until `.sqlx/` is current (step 2 above). The spinup CLI must run the migrations itself, with the sqlx migrator on the app `migrations/` directory at run time. It must also run the kit migrations (table `_spinup_kit_migrations`, R0.6) from the kit package source, which `cargo metadata` locates. The app binary keeps `migrate` for production, where the binary is already built.
- When `cargo build` fails with the `SQLX_OFFLINE=true` message, `spinup` should add `Try: spinup db prepare` (it runs migrate and prepare). The sqlx message alone names `cargo sqlx prepare`, which fails with `relation ... does not exist` when the migration is not applied.
- Changes to `rust-plan.md`: decision 8 and risk 9 choose plan C with a hash after formatting. R2.11 lists the post-steps `migrate`, `sqlx-prepare`, `build`, `openapi-emit`, and `client`. rustfmt is part of the plan step, not a post-step. Add a `spinup db prepare` command.
- Not done: the `json`, `decimal`, `uuid`, and `ref` field types, auth ownership, handler tests from the generator, web pages, and a Go oracle comparison of the generated Rust (Go generates Go, so there is no byte oracle). R2 adds them. The sweep checked formatting only. It did not compile the sweep resources.
