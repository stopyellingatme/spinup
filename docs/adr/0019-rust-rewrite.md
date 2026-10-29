# ADR 0019: Rust rewrite

Status: accepted (2026-10-24)

## Context

On 2026-10-24 the user decided to rewrite spinup in Rust: the CLI, the kit, the plugin SDK, and the generated API.
The reasons are CLI performance and size, type safety, and Rust generated apps.
The plan is `docs/design/rust-plan.md`. The epic is issue #2.

The plan started with R0, a spike of 14 questions with two hard no-go conditions.
All 14 spikes are done. The report is `docs/design/rust-spike/REPORT.md`. Its decision is `GO with changes`.
This ADR records the user decisions, the R0 results, and the changes to earlier ADRs.

## Decisions

### 1. Scope and the user decisions

- The CLI, the kit, and the plugin SDK move to Rust. The generated API is Rust (axum on tokio) and replaces the Go API.
- SvelteKit, PostgreSQL, Docker Compose, and OpenTelemetry with Grafana LGTM stay.
- There is no migration path for Go apps. The project has no users and no release.
- The work happens in the same repository, on the long-lived branch `rust`. Go stays as the parity oracle until R5. Then all Go code goes away.
- Lanes are offline. The main loop pre-seeds the crates. A lane cannot add a crate.
- `spinup-kit` and `spinup-plugin-sdk` go to crates.io as alpha versions. Each publish needs the approval of the user.
- The license is `MIT OR Apache-2.0` (`LICENSE-MIT`, `LICENSE-APACHE`). Each crate sets `license = "MIT OR Apache-2.0"`.
- The main loop tags `go-freeze-r0` at the R0 gate. After the tag, Go gets bug fixes only, and each fix adds a txtar or golden case.

The full list is in `rust-plan.md`, "Fixed decisions" and section 11.

### 2. The R0 result

The decision is `GO with changes`. Nine spikes are `go`, and five spikes are `soft`. No spike is `no-go`.

Both hard no-go conditions pass:

| Condition | Limit | Measured | Spike |
|---|---|---|---|
| (a) Handler-body edit to the first correct response, median, warm | 5 s | 3381 ms (p90 3645 ms) | R0.11 |
| (b) Two lanes plus the gate: peak disk | 40 GB | 33.62 GB by `du` (5.1 GB real free-space drop) | R0.13 |
| (b) Two lanes plus the gate: peak 1-minute load average | 14 | 10.52 | R0.13 |
| (b) Two lanes plus the gate: swap | 2 GB | 0 GB increase | R0.13 |

Condition (a) passes only with the dev profile in §4. Condition (b) passes only with the lane settings in §5.

The swap limit counts the increase over the swap in use when the run starts. The user decided this on 2026-10-24, because other sessions already held 9.18 GB of swap. The load limit stays absolute. The disk limit counts the real drop in free space since the run start (decided 2026-10-29, ADR 0021 "R2 results").

### 3. The R0 choices

| Question | Choice |
|---|---|
| Sync or async CLI | Sync. Threads, `ureq` 3.5.0, and a synchronous `Runner` trait. |
| YAML editing | An own editor on tree-sitter source spans (R1.5). No YAML crate reaches Go parity. |
| Formatter | Plan C. One `rustfmt --edition 2024` process in the plan step. The lock hash covers the formatted bytes. No `prettyplease`. |
| OIDC | `oauth2` 5 and `jsonwebtoken` 11. Not `openidconnect`. |
| TLS | The CLI uses rustls with `ring`. The kit uses rustls through reqwest 0.13, and the `aws_lc_rs` backend for `jsonwebtoken` when a pre-seed with network access makes the pack lock. |
| Registration | `linkme` slices, `wire_gen.rs`, and the registry ID test. `inventory` is the fallback. |
| Release | GoReleaser with the Rust builder and `cargo-zigbuild`, six targets. Windows arm64 uses `aarch64-pc-windows-gnullvm`. `builder: prebuilt` is not available in GoReleaser OSS. |
| OpenAPI | utoipa 6 with `utoipa-axum` 0.3. No aide and no schemars. The feature `preserve_order` stays off. |
| Pack lock before the kit is published | `[patch.crates-io]` with a path kit. The release job removes the patch and runs `cargo update -p spinup-kit`. |
| Templates | minijinja 3.0.3 with the delimiters `[[ ]]`, `[% %]`, and `[# #]`. |
| Job queue | An own River-style queue in the kit, at-least-once. Handlers must be idempotent. |

### 4. Contract changes for generated Rust apps

- **Migrations.** The spinup CLI runs `db migrate` itself, with the sqlx migrator on the app `migrations/` directory at run time. It also runs the kit migrations from the kit package source. The app binary cannot compile until `.sqlx/` is current, so the CLI cannot use it for this step. The app binary keeps `migrate` for production.
- **`spinup db prepare`.** A new command. It runs the migrations on the development database, then `cargo sqlx prepare --workspace`. When `cargo build` fails with the `SQLX_OFFLINE` message, spinup adds `Try: spinup db prepare`.
- **Post-steps** of `g resource` and `g migration`, in this order: plan with rustfmt, apply and lock, `migrate up`, `sqlx prepare`, build, `openapi-emit`, `client`.
- **Offline macros.** Generated apps set `SQLX_OFFLINE = "true"` in `.cargo/config.toml`, and pin the toolchain with the `rustfmt` and `clippy` components.
- **Dev profile.** Generated apps ship `debug = 0`, `strip = "symbols"`, `[profile.dev.package."*"] opt-level = 1`, and `opt-level = 3` for `argon2` and `blake2`. The default profile fails the 5 s limit (5458 ms).
- **Kit tables.** The kit owns its tables through the migration table `_spinup_kit_migrations`. The kit migrations run before the app migrations.
- **Images.** The API image is a static musl build on `gcr.io/distroless/static-debian13:nonroot` (12.1 MB). It runs with `read_only: true`. The Compose health check runs `api healthcheck`.
- **Sessions.** A move from a Go app to a Rust app signs out every user, because the session data format changes. There is no migration (§1).

### 5. Lane environment for Rust milestones

- Lanes run with `CARGO_BUILD_JOBS=2` and `NEXTEST_TEST_THREADS=4`. The gate runs with `CARGO_BUILD_JOBS=4`. At most 2 lanes run, with `nice -n 10`, and the gate runs alone.
- No shared sccache. sccache 0.19.0 puts the absolute target path in the Rust cache key, so a lane at a new path gets no hits.
- At each milestone pre-seed, the main loop builds one whole-workspace seed target. Each lane gets an APFS clone (`cp -c -R`) of it.
- `RUSTC_WRAPPER` stays unset. `CARGO_INCREMENTAL` stays at the cargo default.

### 6. Superseded sections

- ADR 0001, Decision bullets 1 to 3 and Consequences: the CLI, the kit, and the generated API are Rust (§1). The upgrade model (file classes, `spinup.lock`, and the 3-way merge) stays. ADR 0020 changes the lock schema.
- ADR 0003, Images and health check: the Rust API image and its health check are in §4. The blue/green switch stays.

## Consequences

- R1 starts with the plan changes in `REPORT.md` and in `rust-plan.md`.
- The dev loop is about 1.3 s slower than Go with air (3381 ms against 2043 ms). The app crate rebuild and the macOS first-start check of each new binary are the two main costs.
- The Rust binaries are smaller and use less memory than Go: the CLI is 5.4 MB against 28.0 MB, and the API uses 7.3 MiB of memory at idle on Linux against 84 MiB.
- The full-kit API starts in 125 ms against 52 ms for Go, because the startup argon2 dummy hash runs on one thread. R2 moves the hash after the start.
- Lanes take longer at 2 build jobs. Plan more wall time per lane, not more lanes.
- These items stay open and do not block R1: native Windows runs (they need a CI workflow on `rust`, which the R1 pre-seed adds), a Mailpit run, and Linux dev-loop numbers.
