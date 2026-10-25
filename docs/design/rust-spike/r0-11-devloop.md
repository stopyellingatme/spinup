# R0.11 Dev loop latency

Issue: #19 (label `hard-no-go`). Spike code: `spikes-rs/r0-11-devloop/`.

## Question

How long does it take from a handler-body edit to the first correct served response, with the rebuild, the restart, and the readiness wait? Use an app with at least 20 generated resources and the full kit. Hard no-go (a): the median of the best configuration is above 5 s.

## Method

The workspace has four members:

- `kit` and `todo-api`: copies of the R0.12 full-kit app (R0.6, the R0.7 auth stack, and the R0.8 job queue and telemetry). `serve` starts auth, OIDC, the reset routes (with `SMTP_URL`), telemetry, and the session layer.
- `gen`: a copy of the R0.10 generator (plan C, one `rustfmt` run per resource). It wrote 20 resources into `todo-api` from `gen/resources.txt`: todo, note, project, tag, comment, customer, invoice, product, order, supplier, warehouse, shipment, event, ticket, article, category, review, coupon, employee, and meeting. They use every R0.10 field type. The app has 5,224 lines in `handlers/` and `models/`, 41 paths, 62 schemas, and 147 sqlx queries in `.sqlx/`.
- `supervisor`: the own dev loop. It watches `todo-api/src` and `kit/src` with `notify` 8.2 (FSEvents), waits 10 ms for quiet, and runs `cargo build`. After a good build it sends SIGTERM to the old app, starts the new binary, and polls `GET /healthz` every 2 ms. With `--listenfd` it binds the port once and passes the socket as fd 3 (`LISTEN_FDS=1`). `todo-api` takes the socket with the `listenfd` crate. `--events` writes a time stamp for each step.

Changes to the copied code:

- The kit option `auth::Options::defer_dummy_hash` computes the startup argon2 dummy hash on a blocking thread after the start. A login that comes first waits for it. `todo-api` turns it on with `AUTH_DEFER_DUMMY_HASH=1`.
- `todo-api/build.rs` has `cargo:rerun-if-changed=migrations`, so a new migration file rebuilds the embedded migrator.
- The auth migrations are `00002` to `00004`. The resource migrations are `00005` to `00024`.
- The scaffold file `handlers/todos.rs` has one hand edit: `GET /api/todos/greeting`. Its body runs a `query_scalar!` count and returns a message with a marker, such as `mark-0003`.

`measure.py` drives every series. A sample starts just before the script writes the edited file. It ends at the first `200` answer from `/api/todos/greeting` whose body has the new marker. The script polls with a new connection every 5 ms. Each series has 1 warm-up edit, then 10 measured edits, with 1.5 s of quiet between edits. The script waits while the 1-minute load average is 4 or more, and it records `uptime` before and after each series. The raw records are in `spikes-rs/r0-11-devloop/measurements/results.jsonl`.

The edit kinds:

- Handler body: the marker in the `format!` literal changes.
- Signature: a parameter `_headers: axum::http::HeaderMap` is added or removed, and the marker changes.
- `#[utoipa::path]`: the `summary` changes. The supervisor runs `todo-api openapi` after the build (`--post-build`). The sample ends when `openapi.gen.yaml` has the new summary and the greeting has the new marker.
- Migration: a new migration file adds a column to `todos`. Then the R0.10 order runs: `sqlx migrate run`, `cargo sqlx prepare --workspace`, `cargo build`, restart, and the first response.
- Client: `openapi-typescript` 7.13 on the emitted 20-resource spec (the dogfood app `web/node_modules`, read only).

Build variants (each in its own target directory, set through `CARGO_PROFILE_DEV_*`, `RUSTFLAGS`, or `--config`):

| Variant | Settings |
|---|---|
| default | the dev profile (macOS: `split-debuginfo = "unpacked"` is already the default), argon2 and blake2 at `opt-level = 3` as in R0.7 |
| unpacked | `split-debuginfo = "unpacked"`, explicit |
| line-tables | `debug = "line-tables-only"` |
| nodebug | `debug = 0` |
| lld | `-C link-arg=-fuse-ld=<sysroot>/.../gcc-ld/ld64.lld` (rust-lld, LLD 23.1.1). The default linker is Apple ld-1267. |
| deps-opt1 | `profile.dev.package."*".opt-level = 1` (dependencies only, not `kit` or `todo-api`) |
| lt-lld, nodebug-lld | the combinations |
| lean | line-tables + rust-lld + deps-opt1 |
| lean-strip | `debug = 0`, `strip = "symbols"`, rust-lld, deps-opt1 |
| ltd | line-tables + deps-opt1 |
| ltd-stripdbg | `debug = 0`, `strip = "debuginfo"`, deps-opt1 |
| ltd-strip | `debug = 0`, `strip = "symbols"`, deps-opt1 |

Drivers: `own` (the supervisor), `listenfd` (the supervisor with `--listenfd`), `bacon --headless` with the job in `bacon.toml` (`cargo run`, `kill_then_restart`), and `watchexec --restart --debounce 10ms -- cargo run`. `defer` in a series name means `AUTH_DEFER_DUMMY_HASH=1`.

Go baseline (same day, same machine): a copy of the dogfood app `/Users/king/Documents/code/todo` (read only; the copy is in the scratch directory). I generated 19 more resources with the Go `spinup g resource --api-only` from this worktree, so the Go app also has 20 resources, with auth. I added the same greeting handler in `internal/handlers/greeting.go`. `go tool -modfile=tools.mod air` (air 1.67.4) runs with the managed `.air.toml` of the api pack (`delay = 300`). This is the command that `spinup dev` runs. `air` is not on `PATH`, but the tool module is in the module cache, so the real `air` ran.

Cranelift: not measured. The nightly toolchain is installed (rustc 1.93.0-nightly, 2025-11-10), but it has no `rustc-codegen-cranelift` component. That nightly is also older than the sqlx 0.9 minimum (1.94). The brief does not allow an install.

The first-start checks ran on fresh copies of each binary, at a 1-minute load of 3.5 to 3.9.

Machine: Apple M1 Max, 10 cores, 64 GB, macOS (Darwin 25.4.0). Rust 1.99.1, Go 1.27.2, PostgreSQL 18 on 127.0.0.1:55433 (database `spinup_r11` for Rust, `spinup_r11_go` for Go). `CARGO_BUILD_JOBS=4`. No other lane ran. Other sessions kept the 1-minute load average between 2.1 and 7.0. The load before and after each series is in the tables.

## Numbers

### Handler-body edits, 10 warm edits per series (ms)

The step columns are medians from the supervisor events. "Ready" is from the spawn of the new binary to the first `200` from `/healthz`. "Resp" is from ready to the first correct greeting.

| Series | Median | p90 | Min to max | Build | Ready | Resp | Load 1 min before / after |
|---|---:|---:|---:|---:|---:|---:|---:|
| own-default | 5458 | 5584 | 5291 to 5856 | 3756 | 1616 | 46 | 3.68 / 5.17 |
| own-unpacked | 5393 | 5501 | 5308 to 5945 | 3684 | 1634 | 44 | 3.71 / 3.47 |
| own-line-tables | 4501 | 4551 | 4442 to 5005 | 2874 | 1538 | 38 | 3.27 / 3.66 |
| own-nodebug | 4580 | 4693 | 4530 to 4702 | 3098 | 1404 | 38 | 3.66 / 5.51 |
| own-lld | 5625 | 5683 | 5541 to 5861 | 3996 | 1548 | 44 | 3.69 / 3.41 |
| own-deps-opt1 | 4652 | 4735 | 4547 to 5034 | 3472 | 1136 | 18 | 3.78 / 2.95 |
| own-default-defer | 5377 | 5432 | 5311 to 5704 | 3760 | 1503 | 47 | 2.95 / 4.24 |
| listenfd-default | 5452 | 5482 | 5370 to 5665 | 3762 | 1596 | 44 | 3.44 / 4.05 |
| bacon-default | 5397 | 5508 | 5313 to 5520 | not split | not split | not split | 3.80 / 3.52 |
| watchexec-default | 5428 | 5503 | 5338 to 5710 | not split | not split | not split | 3.32 / 2.64 |
| own-lt-lld | 4739 | 5143 | 4640 to 5156 | 3180 | 1496 | 40 | 2.67 / 3.03 |
| own-lt-lld-defer | 4658 | 4702 | 4610 to 4730 | 3193 | 1394 | 37 | 3.02 / 4.02 |
| listenfd-lt-lld-defer | 4674 | 4716 | 4615 to 4877 | 3213 | 1392 | 38 | 3.86 / 6.95 |
| own-nodebug-lld-defer | 4922 | 5229 | 4849 to 5551 | 3524 | 1325 | 39 | 3.56 / 2.73 |
| own-lean-defer | 3928 | 3994 | 3849 to 4136 | 2851 | 1004 | 19 | 2.67 / 3.56 |
| listenfd-lean-defer | 3917 | 3962 | 3861 to 4198 | 2870 | 993 | 16 | 3.52 / 4.15 |
| own-lean-strip-defer | 3668 | 3759 | 3609 to 3788 | 2937 | 682 | 17 | 3.98 / 3.47 |
| listenfd-lean-strip-defer | 3630 | 3646 | 3574 to 3657 | 2915 | 667 | 16 | 3.47 / 2.50 |
| listenfd-ltd-defer | 3734 | 3849 | 3679 to 3993 | 2661 | 1014 | 16 | 3.85 / 3.50 |
| listenfd-ltd-stripdbg-defer | 3718 | 3747 | 3674 to 3758 | 2728 | 916 | 19 | 3.46 / 3.41 |
| **listenfd-ltd-strip-defer** | **3381** | **3645** | 3335 to 3682 | 2655 | 674 | 16 | 3.29 / 2.44 |
| best-repeat (same settings) | 3390 | 3433 | 3350 to 3445 | 2685 | 665 | 16 | 2.24 / 2.92 |
| own-ltd-strip (no listenfd, no defer) | 3490 | 3541 | 3415 to 3588 | 2668 | 765 | 18 | 2.44 / 2.10 |
| Go, air, 20 resources | 2043 | 2050 | 2024 to 2095 | about 1200 | not split | not split | 3.51 / 5.28 |

The p90 is the nearest-rank value (the 9th of 10 samples). In each own series, file detection took 10 to 14 ms, the debounce 10 to 15 ms, and the stop of the old app 3 to 10 ms. Each sample had exactly one build.

**Best configuration:** `debug = 0`, `strip = "symbols"`, dependencies at `opt-level = 1`, the default Apple linker, the own supervisor with `--listenfd`, and the deferred dummy hash. Its two series give 20 samples. The combined median is 3381 ms. The verdict file uses the first series p90 (3645 ms), because it is the higher of the two. The p90 of all 20 samples is 3450 ms.

The Go baseline is 2043 ms: about 1.2 s `go build`, about 0.54 s first start of the new 37.6 MB binary, and the 300 ms air delay. Rust with the best configuration is 1.34 s (66 %) slower than Go.

### Why the start takes so long on macOS

macOS checks each new executable at its first start. The second start of the same file is fast. A copy of the default debug binary (82 MB) took 1.40 s for `--help` on the first run and 0.00 s on the second run. The time grows with the file size, about 17 ms per MB:

| Binary | Size | First `--help` |
|---|---:|---:|
| default | 82.1 MB | 1.40 s |
| nodebug (`debug = 0`) | 69.7 MB | 1.24 s |
| lt-lld | 74.7 MB | 1.28 s |
| deps-opt1 | 53.5 MB | 0.97 s |
| nodebug, stripped with `strip` | 42.1 MB | 0.86 s |
| ltd-strip (the best), 3 fresh copies | 29.5 MB | 0.58 to 0.67 s |
| Go API, 20 resources | 37.6 MB | 0.54 s |

This check is most of the "Ready" column. The app start itself is short. With an already checked binary, the default build took 138 to 142 ms to the first `/healthz` with the dummy hash at start, and 46 to 56 ms with the deferred hash. So the deferred hash saves about 90 ms, and `listenfd` saves less than the 2 ms poll step. The "Resp" column falls from 46 ms to 16 ms with dependencies at `opt-level = 1`. The first query opens the first database connection, and the unoptimized sqlx and tokio code is slow.

### Build time of the warm edit

`cargo build --timings` for one body edit in the default variant: 0.26 s cargo, 2.63 s the `todo-api` library (incremental, with the expansion of 147 sqlx macros and the utoipa macros), and 0.71 s the `todo-api` binary with the link. rust-lld made every warm build slower than the Apple linker by 200 to 400 ms (5625 ms against 5458 ms, and 4739 ms against 4501 ms). `debug = "line-tables-only"` or `debug = 0` cut 0.6 to 0.9 s from the build. `split-debuginfo = "unpacked"` is the macOS default, so it changed nothing.

### Other edit kinds (best configuration unless named)

These numbers do not count toward the limit.

| Edit kind | Median | p90 | n | Load 1 min before / after | Note |
|---|---:|---:|---:|---:|---|
| Signature edit | 4247 ms | 4283 ms | 10 | 2.92 / 4.81 | build 3521 ms |
| `#[utoipa::path]` edit with spec re-emit | 3422 ms | 3487 ms | 10 | 3.50 / 3.40 | build 2659 ms, emit 672 ms (it includes the first-start check), ready 43 ms |
| Spec emit alone (`todo-api openapi`, 144,815 bytes) | 46 ms | 1485 ms | 5 | 3.46 / 3.74 | the slow run is the first start of a new binary |
| Client regeneration (`openapi-typescript`, 216,762 bytes out) | 430 ms | 545 ms | 5 | 3.46 / 3.74 | |
| Migration edit (migrate, prepare, build, restart, response) | 6466 ms | 54004 ms | 3 | 3.74 / about 4.7 | see below |
| Cold start, best variant | 126.5 s | | 1 | 3.48 / 5.68 | 125.7 s build, 782 ms start to the first greeting, target 1.18 GB |
| Cold start, default variant | 62.6 s | | 1 | 3.56 / 5.91 | 60.9 s build, 1704 ms start to the first greeting, target 2.33 GB |

Migration edit, per sample: (1) 54.0 s: migrate 50 ms, `cargo sqlx prepare` 49.8 s, build 3.4 s, restart and response 796 ms. The first prepare in a target directory runs a cold `cargo check` of the workspace. (2) 6.5 s: migrate 27 ms, prepare 2.6 s, build 3.1 s, restart 792 ms. (3) 6.3 s: migrate 25 ms, prepare 2.4 s, build 3.0 s, restart 867 ms. The warm migration edit is about 6.4 s.

Full builds of each variant from a warm `CARGO_HOME` (load 3.0 to 5.8, caused by these builds): 58 to 64 s without the dependency `opt-level`, 125 to 144 s with it.

## Verdict

Verdict: go

The best configuration serves a handler-body edit in a median of 3381 ms (p90 3645 ms). This is below the 5 s limit on a 20-resource app with the full kit, with the machine at a 1-minute load of 2.2 to 3.3. The default dev profile does not pass: 5458 ms median, with bacon, watchexec, and the own supervisor all between 5397 and 5458 ms. So the generated app must ship the profile settings. The margin to the limit is about 1.6 s. Go with air is 2043 ms on the same day, so Rust is about 1.3 s slower. Two thirds of the Rust time is the incremental build of the app crate (about 2.7 s). The other large part is the macOS check of each new binary (about 0.6 s for 29.5 MB, and 1.4 s for the default 82 MB).

## Recommendation

- Generated apps (R2 app template): set the dev profile in the app `Cargo.toml`: `[profile.dev] debug = 0` and `strip = "symbols"`, and `[profile.dev.package."*"] opt-level = 1`. Keep `opt-level = 3` for argon2 and blake2. If the team wants symbol names in panic backtraces, use `strip = "debuginfo"` (3718 ms) or `debug = "line-tables-only"` (3734 ms). Both still pass. Do not set rust-lld on macOS: it was slower than the Apple linker in every pair.
- The cost: a cold build takes about 2 times longer with the dependency `opt-level` (126 s against 61 s here), but the target directory is half the size (1.18 GB against 2.33 GB). Record this for R0.13.
- `spinup dev` (R3): use the own supervisor (`notify`, build, SIGTERM, start, readiness poll). bacon and watchexec gave the same latency as the supervisor (within 60 ms), but they restart through `cargo run`, they kill the app before the build, and they cannot run the spec emit and client post-steps in order. The supervisor can. Keep `--listenfd` as an option: it keeps connections in the backlog during a restart, but it does not change the latency. Measure the readiness with `/healthz`.
- Kit (R2): compute the startup dummy hash after the start (`defer_dummy_hash`, as in this spike, or the R0.12 proposal). It saves about 90 ms on each restart in debug builds. Open the first database connection at start (a pool minimum of 1), so the first request does not pay for it.
- On macOS, the first-start check of each new binary costs about 17 ms per MB. Smaller dev binaries help. `spinup doctor` can tell macOS users to add the terminal to "Developer Tools" in System Settings, Privacy and Security, which removes this check. I did not change system settings, so this spike did not measure that case. The R2 gate re-measure must use the same settings as here, or say which ones changed.
- The other edit kinds: a signature edit is about 0.9 s slower than a body edit (4.2 s). A `#[utoipa::path]` edit with spec re-emit is 3.4 s, because the emit runs the new binary first and so takes the macOS check in place of the restart. Client regeneration adds about 0.4 s. A warm migration edit takes about 6.4 s, and `cargo sqlx prepare` (about 2.5 s) is the step to remove. The first prepare in a new target directory took 50 s.
- If the app crate grows past 20 resources, the incremental build grows with the macro count. R2 can put the resources in a separate crate, so an edit in a hand-written module does not expand the generated macros again. This spike did not measure that split.
- Changes to `rust-plan.md`: the R0.11 row passes with 3381 ms (p90 3645 ms) against the Go+air 2043 ms on the same day. Add the dev profile above to the app template unit. The R2 gate re-measure uses `measure.py` with the `best-repeat` series.
- Not done: Cranelift (no nightly component, and the installed nightly is older than the sqlx minimum), the Developer Tools exception, Linux numbers, and an edit in `kit/src` (the kit is a path dependency here, but it is a registry crate in a real app).
- To run the measurements again: create the database `spinup_r11` on 55433, run `sqlx migrate run --source todo-api/migrations` and `todo-api migrate up`, then `measure.py prepare <variant>` and `measure.py series <name>` (see the doc comment of `measure.py`). The Go series needs a copy of a Go app with `internal/handlers/greeting.go` and `R11_GO_DATABASE_URL`.
