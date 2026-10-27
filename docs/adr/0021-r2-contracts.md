# ADR 0021: R2 contracts (the kit, the Rust API pack, and the generators)

Status: accepted (2026-10-25)

## Context

R2 builds the Rust kit, the Rust `api` pack, the Rust generators, and the `db`, `build`, `test`, `routes`, and `destroy` commands. The plan is `docs/design/rust-plan.md` §5 R2. The R0 decisions are in ADR 0019. The R1 contracts are in ADR 0020.
The R2 units run in parallel offline lanes. This ADR fixes the contracts between them. The main loop writes the contract files, the module lists, and the stubs in the R2 pre-seed commit. Units implement them and never edit them.
Go stays the parity oracle until R5. Go product behavior does not change. Two Go test-harness changes are accepted as test-only changes (see "Decisions taken by the main loop").

## Supersedes

This ADR replaces these earlier texts:
- ADR 0019 §4 "Migrations": the CLI does not run the sqlx migrator in process. An in-process migrator pulls tokio and sqlx into the CLI. The CLI runs `sqlx-cli` 0.9.0 through `spinup-exec` (§6). The app binary keeps `api migrate` with the in-process kit migrator.
- ADR 0020 §5: the `tokio` and `aws-lc-*` bans leave `deny.toml`. `scripts/rust/cli-graph-check.sh` checks the CLI graph (§9).
- ADR 0020 §8 "The `--module` flag" and rust-plan.md decision 9: `--module` no longer feeds a crate slug. It feeds `registry_owner` (§5). The package name is `api`.
- rust-plan.md §1 kit table: there is no `spinup-kit-testkit` crate. The testkit is the feature `testkit` of `spinup-kit` (§2).
- rust-plan.md §5 R2 rule that units move `rust-parity.txt` lines: `rust-parity.txt`, `rust-filter.toml`, and `parity.txt` are main-loop files. Units report stale lines (§10, §11).
- rust-plan.md R2 pre-seed "new Action pins": no new action. Workflows use `rustup` and the pinned `actions/cache` (§12).
- rust-plan.md R2.30 "kit-compat workflow source": R4.9 rewrites `kit-compat.yml` (`rust-plan.md:700`).

Go parity changes that R2 accepts (also in `rust-plan.md` §5 R2):
- `spinup test` has no `--race` flag. Rust has no race detector.
- `spinup db migrate` and `db status` print the `sqlx-cli` text, not the Go kit lines.
- `api/openapi/bundle.gen.yaml` is a tracked `seed` file. Go wrote it untracked (`internal/gen/openapi/generator.go:40`).
- `spinup routes` reads literal routes only in `api/src/routes.rs`. Go read every `api/internal/routes/*.go` (`internal/routes/routes.go:41`).

## Decisions

### 1. Contract files and seams

| File | Holds | Implemented by |
|---|---|---|
| `crates/spinup-gen/src/contract.rs` (changed) | `Input` gets `runner: &dyn Runner` and `cancel: &Cancellation`. `Output` does not change. | R2.12 (`alert` runs `promtool`), every generator |
| `crates/spinup-gen/src/part.rs` (new) | `Spec`, `PartPlan`, `Part`, the `RESOURCE_PARTS` slice | R2.12 (`pages`), R2.13 (runs the parts) |
| `crates/spinup-project/src/env.rs` (new, main loop) | `database_url(root, lookup)`, `database_url_from_process(root)`, `SQLX_NO_CACHE`, `SQLX_PREPARE_TRY` | used by R2.9, R2.11, R2.15, R2.16 |
| `crates/spinup-vfs/src/apply.rs` (changed, main loop) | `emptied_dirs(root, deleted)`, `remove_dirs(root, dirs)` (ports of Go `internal/gen/destroy/dirs.go`) | used by R2.15 (`destroy`) |

`Part` lives in `spinup-gen`, not in `spinup-gen-app`. `spinup-gen-app` depends on `spinup-gen-web`, so the pages part in `spinup-gen-web` cannot see a slice in `spinup-gen-app`.

Seam placeholders. The pre-seed writes them. The owner replaces the body and keeps the path, the signature, and the derives:

| Seam | Owner | Keep |
|---|---|---|
| `spinup_gen::fields::{Name, Field}` | R2.11 | `Debug`, `Clone`, `PartialEq`, `Eq` |
| `spinup_gen_app::resource_rs::plan_api(spec: &Spec<'_>, tests: bool) -> anyhow::Result<Plan>` | R2.14 | the signature (R2.13 calls it) |
| `spinup_upgrade::cargo::kept_manifest(path: &str) -> Option<&'static str>` | R2.10 | the signature (`plan/mod.rs` calls it for the keep rule) |
| `spinup_kit::auth::required::Placeholder` | R2.5 | nothing: R2.5 deletes it. The item exists because `pub use required::*;` of an empty module gives `unused import`, and lint runs with `-D warnings`. |

Fixed signatures without a stub, because the owner merges before the consumer starts:
- `spinup_gen_web::nav::plan_nav(root: &Path, plan: Plan, removed: &[String]) -> anyhow::Result<Plan>` (R2.12, for R2.13 and R2.15).
- `spinup_gen_app::resource_core::{parse_spec, paths, sql, modules::plan_module_lists}` (R2.13, for R2.14 and R2.15). `plan_module_lists(root, plan, removed)` rewrites `api/src/handlers/mod.rs`, `api/src/models/mod.rs`, and `api/src/routes_gen.rs`, one sorted line for each resource.
- `spinup_gen::poststeps::run_steps(steps: &[PostStep], input: &StepInput<'_>) -> anyhow::Result<()>` (R2.11, for `destroy`).
- `spinup_db::{names::MIGRATIONS_DIR, names::next_migration_name, migrate, prepare, ops, lint}` (R2.9, for R2.13, R2.15, R2.23).

New slice: `spinup_gen::part::RESOURCE_PARTS`. `crates/spinup-cli/src/registry.rs` lists it as `resource-part <name>`. `wire_gen.rs` gets `spinup_gen_web` and `spinup_gen_app`.

Kit seams. `crates/spinup-kit/src/lib.rs`, `src/auth/mod.rs`, and `src/testkit/mod.rs` are main-loop module lists. `auth/mod.rs` lists `error` (R2.6) and has `pub use required::*;`, so `kit::auth::{Auth, AuthUser, User, Options}` come from R2.5. R2.1 merges before every other kit unit. These kit names are fixed now, because two parallel units use them:
- `kit::auth::error::AuthError` (R2.6): the Go texts and `IntoResponse`. R2.5 and R2.7 use it. `auth/mod.rs` has no re-export of it.
- `kit::app::Command` (R2.1): the type that `App::command(name, Command)` takes. `kit::db::migrate_command` (R2.2) and `kit::jobs::worker_command` (R2.8) return it.
- `kit::app::{Migration, MigrationStatusFn}` (R2.1): `Migration { name: String, applied: bool }` (Go `devtools.Migration`), and `MigrationStatusFn = Arc<dyn Fn() -> Pin<Box<dyn Future<Output = anyhow::Result<Vec<Migration>>> + Send>> + Send + Sync>` (std types only, because `futures` is optional). R2.2 provides `kit::db::migration_status(pool: PgPool) -> MigrationStatusFn`. R2.4 `with_devtools` takes `Option<MigrationStatusFn>`, so `devtools` does not need `db`.
- `App::routes(axum::Router)` (R2.1) takes a router without state. Each feature applies its own state before the call.

### 2. The kit crate

- **One crate.** `spinup-kit` holds the old testkit as the feature `testkit` (`src/testkit/`). There is no `spinup-kit-testkit` crate. Testcontainers is dropped. Reason: one published crate, one `[patch.crates-io]` line, and one Docker kit context.
- **Testkit without a database.** The Go rule stays (`kit/testkit/server.go:42-66`, without the container). `testkit::db::db()` returns `Option<TestDb>`. Without `DATABASE_URL` it prints `skip: DATABASE_URL is not set` to stderr and returns `None`. With `CI` set it panics with `Try: export DATABASE_URL=...`. Generated tests write `let Some(db) = kit::testkit::db::db().await else { return };`. This is decision 1 of the main loop.
- **Self-contained manifest.** `crates/spinup-kit/Cargo.toml` does not inherit from the workspace. It has its own `version = "0.1.0"`, lints tables, and caret requirements with the versions of the root lock. A Docker build with `SPINUP_KIT_REPLACE` copies only the kit directory. A workspace key fails there. The test `crates/spinup-newapp/tests/it/kit_manifest.rs` checks the kit version, each kit requirement, the root lock versions, the stack `crates` entries, and the pack lock (§4).
- **Name in apps.** Apps depend on it as `kit = { package = "spinup-kit", ... }`. The library name stays `spinup_kit`. Code in apps writes `kit::...`.
- **Features.** `default = ["db"]`. `app`, `config`, `secrets`, `httpx`, `logx`, and `deployx` always compile. Optional: `db`, `auth` (implies `db`, has `url` for `auth::reset`), `oidc` (implies `auth`), `mail` (has `async-trait`), `jobs` (implies `db`), `telemetry`, `devtools`, `embedweb`, `apidocs`, `testkit` (implies `db`). `auth::reset` compiles with `auth` and `mail`.
- **Telemetry rule.** `auth` and `jobs` do not imply `telemetry`. Every metric call and every span-link call outside `src/telemetry/` is under `#[cfg(feature = "telemetry")]` (R2.6 `auth.rate_limited`, R2.8 job metrics and the insert-to-job link).
- **Feature checks.** The kit has a self dev-dependency with every feature, so lane checks compile all feature code. That hides a missing `dep:` entry. So each kit unit also runs `make rs-kit-features FEATURES='<its sets>'`. The gate runs `make rs-kit-features`: `--no-default-features`, each feature alone, and the sets that `Cargo.toml.tmpl` renders (`auth,mail`, `auth,jobs`, `jobs,mail`, `auth,oidc,mail`, and the `default` lists of the `small`, `embed`, `auth-mail`, and `full` apps with `db`).
- **`kit/api`.** It holds no Go code. It is the public API baseline of the Go kit for `internal/apicompat`. R2 does not port it. R4.9 makes the Rust baseline. R2.1 uses `kit/api/current.txt` as a checklist.
- **Dropped:** `kit/backup`, `kit/deprecation`, and `kit/internal/deps`. `kit/MIGRATION.json` is copied to `crates/spinup-kit/MIGRATION.json`. The Scalar bundle is copied to `crates/spinup-kit/assets/scalar.js.gz` (main loop).
- **Commands.** `kit::app` dispatches the app commands itself, with the Go texts. There is no clap in the kit and no clap in apps. Built-in: `serve` (default), `healthcheck`, `openapi`, `version`. `openapi` runs before the config load and needs no environment (`spikes-rs/r0-09-ref-web/todo-api/src/main.rs:50-53`): `openapi-emit` and the CI drift check run without app secrets. `kit::db::migrate_command` (R2.2) and `kit::jobs::worker_command` (R2.8) register through `App::command`. The `api` pack wires them.
- **Version.** `/version` answers `{"name","version","rust"}`. The app passes `option_env!("SPINUP_APP_VERSION")`, `option_env!("SPINUP_APP_GIT_SHA")`, and `option_env!("SPINUP_APP_RUST")` from its `main.rs`. `spinup build` and `Dockerfile.api` set these three variables.
- **sqlx rule.** The kit uses runtime queries only (`sqlx::query`, `query_as::<_, T>` with `FromRow`, `query_scalar`). `sqlx::migrate!` and `#[sqlx::test]` are allowed. The kit has no `.sqlx/` directory. `crates/spinup-kit/clippy.toml` bans the query macros and repeats the root subprocess bans.
- **Kit migrations.** Directory `crates/spinup-kit/migrations/`, at the crate root. Files are reversible pairs `<NNNNN>_<name>.{up,down}.sql` with five digits. The table is `_spinup_kit_migrations`. Kit migrations run before app migrations, for every feature set. The path `migrations/` inside the kit package is a contract with `spinup-db`.
- **Tests.** A test that needs Postgres has a function name that starts with `pg_`. `make rs-test NOPG=1` and the Windows CI job skip them with `not test(/(^|::)pg_/)`. The ubuntu and macOS CI jobs start Postgres for nextest. Tests never call `std::env::set_var`: they inject a lookup function. Span tests set a global subscriber and run only under nextest (one process per test). A doc example that needs a database or a network is `no_run`.
- **TLS.** sqlx and lettre keep `ring`. reqwest 0.13 uses its default aws-lc provider. `jsonwebtoken` uses `aws_lc_rs` (no `rsa`, no advisory ignores). `aws-lc-rs` turns on `prebuilt-nasm` for Windows.
- **Not used:** `garde` (the `ValidJson` extractor checks required fields from the schema, as in R0.6), `syn`, testcontainers, `rust-embed`, `dotenvy` (the kit ports the Go `.env` parser).
- **The `mail.send` job** lives in the `mail` pack (R2.19), as in Go. The kit gives `mail::Mailer`, `jobs::Args`, and the re-export `kit::mail::async_trait`.

### 3. The generated API (the `api` pack)

- The package name is fixed: `name = "api"`. The binary is `api`. `api/Cargo.toml` starts with an empty `[workspace]` table.
- The `module` answer no longer feeds a crate slug. It feeds `registry_owner` (§5).
- Layout: `src/main.rs` (scaffold), `src/lib.rs` (scaffold), `src/routes.rs` (scaffold, hand routes), `src/openapi.rs` (managed), `src/routes_gen.rs`, `src/handlers/mod.rs`, `src/models/mod.rs` (managed, generator-owned), `migrations/` (sqlx pairs), `seeds/` (`db seed`), `webdist/` (embed build output), `openapi/bundle.gen.yaml` (seed), `rust-toolchain.toml`, `.cargo/config.toml` (`SQLX_OFFLINE = "true"`), `.dockerignore` (`target/`).
- A resource writes `src/handlers/<plural>.rs`, `src/handlers/<plural>_tests.rs` (absent with `--no-tests`, included with `#[cfg(test)] #[path]`), `src/models/<singular>.rs` (types and queries), and one migration pair.
- The router state is `kit::db::PgPool`. Each handler module exports `pub fn routes() -> utoipa_axum::router::OpenApiRouter<PgPool>`. `routes_gen.rs` merges one line for each resource, sorted. R2.18 ships the zero-resource bytes of the three module lists. They must equal the R2.13 planner output.
- **Spec names.** The utoipa names equal Go (`internal/gen/resource/openapi.go:83,97,172-232`) and the reference spec (`spikes-rs/r0-09-ref-web/todo-api/openapi.gen.yaml`). The web pages of R2.12 read them (`internal/gen/resource/pages_schema.go:17`). R2.14 sets them with `#[schema(as = ...)]` and `operation_id`, and its golden asserts them.

| Item | Name (resource `todo`) |
|---|---|
| schemas | `<Singular>` (`Todo`), `<Singular>Input` (`TodoInput`), `<Singular>List` (`TodoList`), `Problem` |
| list | `list<Plural>` (`listTodos`) |
| create, get, update, delete | `create<Singular>`, `get<Singular>`, `update<Singular>`, `delete<Singular>` (`createTodo` ...) |

- **App-side names that `main.rs` calls.** R2.18 writes the calls. R2.19 defines the names:
  - `crate::mail::Mailers::new(pool: &kit::db::PgPool) -> anyhow::Result<crate::mail::Mailers>` (Go `mailapp.New(pool)`).
  - `crate::reset::routes(auth: &kit::auth::Auth, mailers: &crate::mail::Mailers, public_origin: &str) -> anyhow::Result<axum::Router>` (Go `passwordResetRoutes`). The router has no state (§1).
  - `crate::jobs::register(worker: &mut kit::jobs::Worker, pool: &kit::db::PgPool) -> anyhow::Result<()>`, one call for each job file.
  - With `mail` and `jobs`: `crate::mail::Mailers::register_jobs(&self, worker: &mut kit::jobs::Worker) -> anyhow::Result<()>` (the `mail.send` job).
  - `kit::jobs::worker_command(pool: PgPool, config: kit::jobs::Config, register: impl FnOnce(&mut kit::jobs::Worker) -> anyhow::Result<()> + Send + 'static) -> kit::app::Command` (R2.8). `main.rs` passes a closure that calls `crate::jobs::register` and `register_jobs`.
- **App crates.** App code and generated code use only the crates of `Cargo.toml.tmpl`: `anyhow`, `axum`, `kit`, `serde`, `serde_json`, `sqlx`, `time`, `tokio`, `tracing`, `utoipa`, `utoipa-axum`, `uuid`, and dev `kit` with `testkit`. Generated tests use `kit::testkit::http` only (no `tower`, `http-body-util`, or `reqwest`). The kit re-exports what app code needs, such as `kit::mail::async_trait`.
- Jobs: `src/jobs/mod.rs` (managed, generator-owned) and `src/jobs/<snake>.rs` (scaffold). Mail: `src/mail/mod.rs` (managed, generator-owned), `src/mail/<snake>.rs`, and `templates/mail/`. The `jobs` and `mail` packs ship the zero-item lists, equal to the R2.15 planner output.
- Code that the packs ship uses runtime queries only. So a new app builds offline with an empty `.sqlx/`. Only generated resource code uses `query_as!`. This is decision 4 of the main loop.
- `openapi/bundle.gen.yaml` is class `seed`. The `openapi-emit` post-step rewrites it. Upgrades never merge it.
- Embed: the web build copies to `api/webdist/`. The `embedweb` feature of the app turns on `kit/embedweb`. `api/build.rs` (only with `frontend_mode == 'embed'`) writes the file table into `$OUT_DIR`. A missing or empty `api/webdist/` gives an empty table, so `cargo check` works before the web build.
- **Auth schema.** The auth tables of Rust apps use the spike schema (`sessions.user_id`, `data jsonb`, and the index), because the kit queries need it. ADR 0019 §4 already accepts the session break. The pre-seed copies the spike pairs to `crates/spinup-kit/tests/it/migrations/` as `00002_create_auth`, `00003_password_resets`, and `00004_oidc_identities` (after `00001_init`). R2.19 ships these bytes in the `auth` pack. It does not split the Go SQL.

### 4. `Cargo.toml.tmpl` and `Cargo.lock` of the `api` pack

- Both files and their fragment live in the overlay: `overlay/api/files/api/Cargo.toml.tmpl`, `overlay/api/files/api/Cargo.lock`, `overlay/api/pack.d/deps.toml`. They are main-loop files. The converter never touches them.
- `Cargo.toml.tmpl` declares every app feature for every pack set. Only the `default` list changes with the answers. Versions come from the render helper `crate("<name>")` (stack `crates`).
- `Cargo.lock` is one static file: the lock of the full app (all features), made with the kit `[patch.crates-io]` path. Cargo resolves a lock with all member features on, so one lock fits every pack set. `spinup module add` edits only the `default` list.
- The lock has the path form of the kit entry (no `source`, no `checksum`). It is valid for every patched app, whatever the kit path. Until R4, every app is patched, because crates.io has no `spinup-kit`. R4 switches the pack lock to the registry form, and `apply_replace` (`crates/spinup-newapp/src/kit.rs:13-37`) and the R2.10 `apply` phase then also rewrite the `spinup-kit` entry of `api/Cargo.lock`.
- `make rs-pack-lock` remakes the lock. Its proof step renders three apps and runs `cargo metadata --locked --offline` in each `api/`. The main loop runs it at the pre-seed, after each kit manifest change, and at the gate. `kit_manifest.rs` fails when the pack lock is stale: each package that the pack lock and the root lock both hold once has the same version, and the `spinup-kit` entry equals `crates.spinup-kit`.
- **Compile proof.** `make rs-app-check SET=<set>` (`scripts/rust/app-check.sh`) renders one app with `SPINUP_KIT_REPLACE`, runs `rustfmt --check --edition 2024` on every `.rs` file, and runs `cargo check --offline --locked --all-targets` in `api/` with `SQLX_OFFLINE=true`. With `RESOURCE=1` it first runs `g resource todo title:string done:bool 'due:date?'` (the no-database rule applies the plan) and copies `testdata/reference/todo-rs/.sqlx/*.json` into `api/.sqlx/`. R2.18 checks `small` and `embed`. R2.19 checks `full` (with `RESOURCE=1`), `auth`, `jobs`, `mail`, and `auth-mail`.
- Until the kit is on crates.io, a generated app builds only with `SPINUP_KIT_REPLACE`. Every R2 script, doc page, and the app gate set it through `SPINUP_KIT_DIR`.

### 5. The registry owner

`spinup.toml`, table `[app]`, key `registry_owner` (string). It is empty, or lowercase in the GitHub owner form `^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$`. The base overlay (R2.20) renders the lowercase second segment of `module` when `module` starts with `github.com/`, else `""`. `spinup build --push` (R3a) and `spinup deploy` read it through `spinup_project::config::App::registry_owner`. An empty value fails with `Try: set registry_owner in the [app] table of spinup.toml`.

### 6. Database commands and migrations

- The CLI stays synchronous, without tokio. It runs migrations with `sqlx-cli` 0.9.0 through `spinup-exec`. `sqlx-cli` must have the `sqlx-toml` feature (`--features postgres,rustls,sqlx-toml`). `make rs-tools-check` checks it.
- `db migrate up` runs two commands in `api/`: first `sqlx migrate run --source <kit>/migrations --config <tmp>/kit-migrations.toml`, then `sqlx migrate run --source migrations`. The temporary config holds `[migrate] table-name = "_spinup_kit_migrations"`. `<kit>` is the directory of `manifest_path` of the `spinup-kit` package from `cargo metadata --format-version 1 --locked` in `api/`. The command never adds `--offline`: lanes and scripts are offline through `CARGO_NET_OFFLINE`, and a crates.io kit (R4) can need a download. `--locked` keeps `api/Cargo.lock` unchanged. `--dry-run` prints the two command lines.
- `down`, `redo`, and `to <version>` act on the app migrations only (`sqlx migrate revert`, `--target-version`). `status` prints `sqlx migrate info` for the kit, then for the app.
- **The database URL** comes from one resolver, `spinup_project::env::database_url(root, lookup)`, in this order: `DATABASE_URL`, the file that `DATABASE_URL_FILE` names, the `DATABASE_URL` line of `<root>/api/.env`, the `DATABASE_URL` line of `<root>/.env`. This is the order of the Go app (`kit/config/config.go:40`, `kit/config/dotenv.go:14`). `db`, the post-step runner, and the no-database rule use it. Child processes get the value as `DATABASE_URL`. It never appears on a printed command line.
- `db prepare` runs `db migrate up`, then `cargo sqlx prepare --workspace -- --all-targets` in `api/` with `SQLX_OFFLINE=false`. `--all-targets` puts the query macros of test code into `.sqlx/` too. The `sqlx-prepare` post-step calls the same function (`spinup_db::prepare::run`).
- A failed `cargo build` whose output contains `spinup_project::env::SQLX_NO_CACHE` (`there is no cached data for this query`) adds `SQLX_PREPARE_TRY` (`Try: spinup db prepare`). The build post-step (R2.11) and `spinup build` (R2.16) use the two constants.
- `db new <name>` writes `api/migrations/<NNNNN>_<name>.up.sql` and `.down.sql`. `db lint` reads the `.up.sql` files only. `db seed` applies `api/seeds/*.sql` through `docker compose ... psql`, as Go does. `db psql`, `dump`, `restore`, `reset`, and `doctor` keep the Go compose commands.

### 7. Generators

- **Post-step order:** plan, rustfmt, apply and lock, `migrate`, `sqlx-prepare`, `build`, `openapi-emit`, `client` (ADR 0019 §4). Owners: R2.11 registers `build`. R2.15 registers `migrate` and `sqlx-prepare` (both in `migration/`, through `spinup_db`) and `openapi-emit`. R2.12 registers `client`.
- **No database.** When the resolver (§6) finds no database URL, the runner applies the plan and skips `migrate`, `sqlx-prepare`, `build`, and `openapi-emit`. It skips `client` only when `openapi-emit` was requested and skipped, so `g client` works without a database. It prints `Skipped post-steps: DATABASE_URL is not set` with the skipped names, and adds `Next: spinup db prepare` and `Next: spinup g openapi`. The exit code is 0. R2.11 adds this rule to `crates/spinup-gen/src/run/`. `run::Options` gets the field `database_url: Option<String>`. `Options::new` sets it from `database_url_from_process(root)`. `run()` never reads `std::env`. Tests set the field.
- **rustfmt (plan C).** The runner (R2.11) formats every planned `.rs` file before apply, so no generator can forget it. It stages the files in a temporary directory, adds empty files for declared child modules, and runs one `rustfmt --edition 2024` process through the runner. The working directory is `<root>/api` when it exists, so rustup reads `api/rust-toolchain.toml`. The staged paths are arguments. `--config-path` names `api/rustfmt.toml` or `api/.rustfmt.toml` when one exists. The runner writes `rustfmt <version>` (from `rustfmt --version`) into `lock.formatter` when the plan has a `.rs` file. The staging code allows `clippy::disallowed_methods` with a reason.
- **Fields.** `decimal` maps to `String` with `::numeric` and `::text` casts in the SQL. No decimal crate.
- **Scans.** `g job` and `g mailer` find existing items by file name, not by parsing Rust. There is no `syn`.
- **Templates.** The ten page templates come from the converter `-spike` output (`_inline/resource/*.tmpl`). The pre-seed copies them to `crates/spinup-gen-web/templates/resource/`. R2.12 owns them after that. The gen-app templates are hand-written minijinja (`crates/spinup-gen-app/templates/`), from the R0.10 spike.
- **`destroy`** removes the emptied directories with `spinup_vfs::apply::{emptied_dirs, remove_dirs}` (Go `dirs.go`). The gen crates ban `std::fs::remove_dir` and `remove_dir_all`.
- **`routes`** reads `api/openapi/bundle.gen.yaml`, the three kit routes (`/healthz`, `/readyz`, `/version`, source `kit`), and literal `.route("<path>", <method>(...))` calls in `api/src/routes.rs` (source `routes.rs:<line>`). It does not build the app.
- **`test`** runs `cargo nextest run --locked` in `api/`. `--cover` runs `cargo llvm-cov nextest --locked`. The Go flag `--race` is removed: Rust has no race detector.
- **`build`** runs `cargo build --release --locked` in `api/` and copies the binary to `api/bin/api`. It finds the binary through the cargo JSON artifact messages, so a shared `CARGO_TARGET_DIR` works.

### 8. Upgrade cargo phases

R2.10 fills all six slots of `Phases`, not only the cargo work:
- `before` and `after`: the codemod migrations (`spinup_migrate::registry`), as Go `phases.go` does.
- `apply`: the kit patch keep rule (ADR 0018 §3). An app with `[patch.crates-io] spinup-kit` keeps it.
- `deps`: skipped when `api/Cargo.toml` is absent. `toml_edit` edits of `api/Cargo.toml`: it raises each pin to `Stack.crates`, takes new dependencies and features of the new template, and raises `rust-version` to `Stack.rust.min`. The `[features] default` list follows the new render and keeps the entries that the user added. Then, for each raised direct pin, `cargo update -p <name>@<old> --precise <pin>`, so the lock lands on the pin as Go MVS does. It adds `--offline` only when `Options.offline` is set (`crates/spinup-upgrade/src/contract.rs:112-113`). Lanes and tests stay offline through `CARGO_NET_OFFLINE`. Then the web lockfile refresh of Go.
- `fix`: empty. It returns `false`.
- `verify`: `cargo clippy --locked --all-targets -- -D warnings`, then `cargo test --locked`, then the web checks and the local doctor of Go. Child cargo processes run with `CARGO_ENCODED_RUSTFLAGS`, `RUSTFLAGS`, and `LLVM_PROFILE_FILE` removed.
- `kept_manifest` keeps `api/Cargo.toml` and `api/Cargo.lock` out of the text merge.
- **Line endings.** The base pack already ships `.gitattributes` with `* text=auto eol=lf`. It overrides `core.autocrlf` for every text file. Generated apps add no other rule. R2.10 adds a test that runs an upgrade with `core.autocrlf=true` and this `.gitattributes`, and expects no modified file.

### 9. `deny.toml` and the kit graph

- `deny.toml` checks the whole workspace with `[graph] all-features = true`. It no longer bans `tokio`, `aws-lc-rs`, or `aws-lc-sys`. It keeps the bans on `openssl`, `openssl-sys`, and `native-tls`. It allows `0BSD` (lettre).
- `scripts/rust/cli-graph-check.sh` runs `cargo tree --locked --offline -p spinup -e normal,build --target all --prefix none --format '{p}'`. It fails when the output names `tokio`, `aws-lc-rs`, `aws-lc-sys`, `openssl`, `openssl-sys`, or `native-tls`. `make rs-deny` runs it.
- No CLI crate depends on the kit. `rust-layers.toml` gives the kit no workspace edge.
- A workspace build unifies features: `cargo build --workspace` links aws-lc into the `spinup` binary through `rustls`. So a release, `make rs-install`, and `make rs-script` build the binary with `-p spinup` only. No script or test uses a binary from a workspace build.

### 10. Golden trees for changed packs

- `crates/spinup-testutil` reads `testdata/golden-rs/filter.d/*.toml` after `testdata/golden/rust-filter.toml`. A case in `filter.d` wins. Its tree is `testdata/golden-rs/<name>`, and `skip_suffixes` does not apply.
- A unit that changes a pack writes `testdata/golden-rs/filter.d/<name>.toml` and the tree. It reports the stale lines. At the merge, the main loop deletes the line of `testdata/golden/rust-parity.txt` and the table of `rust-filter.toml`. Until then the overlap is allowed: the testutil test prints the stale names and does not fail.
- Shared trees have serial owners in wave order: `api` (R2.18, R2.20), `web-embed` (R2.18, R2.20, R2.22), `ci` (R2.20, R2.22). `web-variants` has one owner (R2.22). The main loop runs the full `make rs-golden` after each pack merge.
- `make rs-golden` also runs `-p spinup-gen-web -p spinup-gen-app`.

### 11. Parity for R2 scripts and pages

- R2 scripts are forks in `testdata/script/rust/`. The shared scripts (Go and Rust run the same file) are `gen_dashboard` and the R1 shared entries of `parity.txt` (`version`, `harness_mkversion`, `gen_picker`, `new_web`). Each fork starts with `# Rust twin of testdata/script/<name>.txtar (Go blob <sha>).` `make rs-twin-check` fails when the Go file changes.
- The pre-seed lists every R2 script name in `testdata/script/parity.txt`. Lanes report later changes. The main loop applies them.
- Lines that need a build after `g resource` use `[env:DATABASE_URL]`. Lanes of script units get `DATABASE_URL`.
- `make rs-script`, `rs-doctest`, and `parity` pass `SPINUP_KIT_DIR`, a shared app target (`target/apps`), and `CARGO_PROFILE_RELEASE_LTO=false` with `CARGO_PROFILE_RELEASE_CODEGEN_UNITS=16`. The script harness passes the two profile keys (decision 2 of the main loop).
- Scripts in one lane run one at a time (`SPINUP_SCRIPT_PARALLEL=1`). Every app package is `api`, so two scripts that share the app target race on `target/release/api` between the build and the copy (`spinup build`) or the run (`openapi-emit`). Each lane has its own target directory.
- Each doc page gets its own database, as each script does. The `rs-doctest` loop creates and drops it (`scripts/rust/page-db.sh`). Two pages that migrate one database fail on versions that their source does not have.
- The pre-seed adds the R2 command pages to `testdata/script/doc-parity.txt`. Rust blocks use `<!-- doctest: only rust -->`.

### 12. Stack versions and the reference app

- The Go file `images` gets `rust` (Alpine musl builder) and `distroless-static-debian13`, with digests. `gen-stack.sh` copies them. The Rust `crates` section gets `spinup-kit: 0.1.0`, `anyhow`, `time`, `tracing`, and `uuid`. No new action: workflows use `rustup` and the pinned `actions/cache`.
- The root lock holds the spike versions. After the online resolution, `scripts/rust/lock-align.sh` compares every package with `spikes-rs/r0-09-ref-web/Cargo.lock` and moves each difference in the same semver range back with `cargo update --precise`.
- `testdata/reference/todo-rs/` is a frozen copy of `spikes-rs/r0-09-ref-web` with its own workspace and lock. The root workspace excludes `testdata`.
- Reference parity: (1) every SQL text of the generated `todo` resource equals the `query` field of the matching file in `testdata/reference/todo-rs/.sqlx/` (R2.14 test). So the reference `.sqlx/` files serve the `cargo check` of `make rs-app-check SET=full RESOURCE=1`. (2) The spec that the gate app emits equals `todo-api/openapi.gen.yaml`, except `info` (R2.30, gate).

## Decisions taken by the main loop

1. Generated-app tests without Postgres use the Go skip rule (§2). This keeps Go parity. Without `DATABASE_URL`, `testkit::db::db()` prints a skip line and returns `None`. With `CI` set, it panics with a `Try:` line.
2. The two Go test-harness edits are accepted as test-only changes after `go-freeze-r0`, as R1.0a was. They change no product behavior. The doctest harness passes the Rust build variables to the page commands. The script harness passes the two release-profile keys. Each edit adds a Go test case.
3. The Windows proof of the kit waits for a push of `rust`. The user approves that push at the R2 gate. The fallback (exclude `-p spinup-kit` on Windows) also needs the approval of the user.
4. Pack code uses runtime queries. Only generated resource code uses `query_as!` (§3). This is the reading of the fixed decision "sqlx `query_as!` with committed `.sqlx/`".

## Consequences

- The R2 lanes can start after the pre-seed. Each lane compiles against the contracts and stubs from the first slot.
- The changed plan rows are in `rust-plan.md` §5 R2.
- `g resource` needs Postgres for its post-steps. Without it, the user runs `spinup db prepare` and `spinup g openapi` later.
- A new app builds only with `SPINUP_KIT_REPLACE` until the kit is on crates.io (R4).
- The CLI needs `sqlx-cli` with `sqlx-toml` for every database command.

## R2 results (added at the R2 gate, 2026-10-27)

The R2 gate passed locally, except item 11 (the Windows proof needs a push of `rust`) and the open disk question below. The checks:
- `make rs-build`, `rs-lint`, `rs-test` (1003 tests with Postgres, 87 of them `pg_` tests), `rs-deny`, `rs-digest`, `rs-golden` (56 trees), `rs-doctest`, and `rs-cover` (`spinup-manifest` 99, `spinup-merge` 85.4, `spinup-migrate` 94.9, `spinup-upgrade` 90.4 percent).
- `make rs-script`: the 25 parity scripts pass with the Rust binary. `make rs-app-gate`: `app_gate_api` (full app, `g resource todo`, clippy, tests, migrate, run, and the spec equal to the reference app except `info`) and `app_gate_web`.
- `make rs-app-check` for the seven sets (`full` with `RESOURCE=1`), `rs-ref-check`, `rs-kit-features`, `rs-pack-lock`, `rs-twin-check`, the `kit_manifest` test, `make oracle` with no diff, and `rs-convert-check`.
- `make rs-e2e`: the web and trace e2e scripts with the Rust binary. The trace goes from the web app to the Rust API in one Tempo trace.
- `make rs-devloop`: median 3484 ms and 3419 ms in two series of 10 handler-body edits (limit 5000 ms). See `docs/design/rust-spike/r2-devloop.md`.
- `make rs-vuln` and the full Go gate (`make build lint test golden script doctest`).
- Gate item 6: the Rust stack model and the template data have no Go sections, and every pack set renders with strict undefined.

### Disk (gate item 5, open)
`make rs-disk` counts the gate target, the run `CARGO_HOME`, and the seed target: 34.24 GB, under the 40 GB limit. The gate target is 26.7 GB (16.1 GB workspace `debug`, 7.1 GB app targets). During the run, two lane targets (8.7 to 14.8 GB `du` each, APFS clones of the 6.7 GB seed) existed next to the gate target. The logical `du` peak of all of them is above 40 GB. The volume never had less than 276 GB free. The user decides whether the limit counts logical `du` with clones, and whether lanes remove app targets after each merge.

### Recorded deviations from Go
- Kit core (R2.1): `ValidJson` rejects unknown JSON properties with the Go 400 text (the Go API ignored them). A wrong value type is 422 with the field. Kit routes answer 405 problems. The access log has a `route` field. The redactor works on flat span fields only. The Go server read, write, and idle timeouts are not ported (`axum::serve` has none). `version` prints the `/version` JSON.
- Kit database (R2.2): no migration hooks (kit migrations replace the Go job hook). The URL form only (no keyword/value DSN). `migration_status` lists applied rows only.
- Telemetry (R2.3): bad sampler, ratio, or header values fail with a `Try:` line (Go falls back). `host.name` comes from `HOSTNAME`. Log events go to the log export only. New metrics: `http.server.request.count` and the tokio runtime metrics.
- Auth (R2.5): an own session layer replaces tower-sessions (Max-Age 604800, as Go). The dummy hash runs after the start. A user delete cascades to the sessions. Hash slots default to 40 percent of the CPUs.
- Mail, reset, and OIDC (R2.7): `render` takes `mail::Templates`. `SmtpMailer` starts its pool at the first send. No file mailer. The ID token check has no expiry leeway.
- Jobs (R2.8, R2.19): a worker takes one queue, so `mail.send` runs on the default queue with 10 attempts. New metric `kit.job.duration`. A stop timeout returns cancelled jobs to `retryable` at once.
- Commands (R2.23, R2.24): `db prepare` replaces `sqlc`. `test` has no `--race`. `build` has no image flags until R3a. A kept Rust handler file stays in `handlers/mod.rs` and `routes_gen.rs`, so it must define `routes()`.
- Generators (R2.25, G1): `g handler` prints no route line. A `--shared` auth resource still needs a signed-in user. The generated Rust tests need Postgres (`pg_<plural>_crud`). The Go GOOS/GOARCH name rule for jobs is removed.
- Upgrade (R2.28): the pin-only deps rule applies only to an edited `api/Cargo.toml`. An unedited manifest takes the new render, as Go does with `go.mod`.
- CLI (G2): `spinup docs compat --json` has a `rust` key in place of `go`. A Rust stack file with a Go section is an error.

### Fixed at the gate
- `spinup db to 0 --i-know` with no applied migration is a no-op (it failed). An app binary stop no longer logs the cancelled dummy-hash task as an error. The app binary lists every command in its unknown-command error. Generator and help texts say "API", not "Go API".
- Stale tests and fixtures that the lane checks did not cover: the `spinup-newapp` generation test (Go `api` files), the destroy test (it read `DATABASE_URL` from the process), the Go docs site and the converter stack oracle (the two R2 images), and a 5 s timeout in the Grafana golden test (30 s).

### Due later
- Windows: `rust.yml` on Linux, macOS, and Windows with the kit (main-loop decision 3).
- `--frontend-mode embed` image lines (R3a.7). The kit-compat workflow (R4.9).
- The auth pack login and register pages still say "The Go API" in comments (converted Go pack text; R5 docs sweep).
