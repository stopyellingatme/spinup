# R0 go/no-go report

Status: final. All 14 spikes are merged, and `spike-check.sh` passes for each.
Unit: R0.15 (main loop). Plan: `docs/design/rust-plan.md` §4. Issue: #3. Date: 2026-10-24.
Sources: the 14 `*.verdict.toml` files in this directory, and the main-loop verification notes of run `20261024-144640`.

## Decision

**`GO with changes`.**

No spike has a `no-go` verdict. Nine spikes are `go`, and five spikes are `soft`. A `soft` verdict changes the plan but does not stop it.

Both hard no-go conditions pass:
- (a) The dev loop passes. A handler-body edit reaches the first correct response in a median of 3381 ms, against the limit of 5000 ms (R0.11). It passes only with the dev profile that the generated app must ship.
- (b) The lane cost passes. Two lanes plus the gate reach a peak 1-minute load of 10.52 (limit 14), 33.62 GB of disk by `du` (limit 40 GB), and no swap increase (limit 2 GB) (R0.13). It passes only with throttled lane settings. With the planned settings, the load reached 33.8 to 54.9.

The swap part of condition (b) counts the increase over the baseline. The user decided this on 2026-10-24. Other sessions already used 9.18 GB of swap before R0.13 started, so an absolute reading fails before any lane runs. The limit protects the machine from the lane workload. So `swap_gb` is the largest swap increase during the run, over the swap in use when the run starts. The disk limit and the load limit stay absolute.

The changes below are binding. R0.16 writes them into `rust-plan.md` and ADR 0019 before R1 starts.

## Spike results

| Spike | Verdict | Key numbers | Result |
|---|---|---|---|
| R0.1 YAML editing | soft | Fixtures passed, of 32: tree-sitter splice 17, `yaml-edit` 11, `yamlpath`+`yamlpatch` 9 | No crate reaches Go parity. Write an own splice editor in R1.5. |
| R0.2 minijinja | go | 96 templates compile (74 pack, 22 inline). 461 of 462 golden files equal (99.78%). `base`, `web`, `web-variants` 100% | minijinja 3.0.3 with `[[ ]]`, `[% %]`, `[# #]`. The one unequal file is Go source that Go formats with `go/format`. |
| R0.3 registration | soft | 6 of 6 macOS combinations. 6 of 6 Linux aarch64 combinations (main loop, Docker). Windows: 0 run | `linkme` plus `wire_gen.rs` and the registry ID test. `inventory` is the tested fallback. |
| R0.4 sync or async | soft | Stripped binary 1.92 MB against 3.84 MB. Clean build 15.6 s against 39.9 s. Warm build 4.3 s against 5.8 s | Sync CLI: threads, `ureq` 3.5.0, rustls with `ring`. |
| R0.5 format interop | go | Lock 6 cases, diff 262 cases, quote 267 cases: 100% equal. Go-signed archive verifies, tamper rejected | `RawValue` for Go numbers, a direct patience-diff port, `ed25519-dalek` + `zstd` + `tar`, `cap-std` with the Go path policy. |
| R0.6 reference API | go | `schema.d.ts` and heyapi output: 0 lines differ. Warm handler rebuild 1.68 s. Release binary 11.3 MB | axum 0.8, utoipa 6, `utoipa-axum` 0.3, sqlx 0.9. No aide. |
| R0.7 auth | go | Go rate-limit tables pass. 91 tests. argon2id 98 ms against 54 ms in Go. Release binary +7.1 MB | `oauth2` 5 + `jsonwebtoken` 11. Own `SessionStore`. |
| R0.8 jobs and OTel | go | 10 000 jobs, 50 kills: 0 lost, 0 completed twice (both runs). 7 of 9 RED panel queries have data | Own River-style queue, at-least-once. OpenTelemetry 0.33 crates pinned together. |
| R0.9 web | go | Web pack changes: 0. Playwright 14 of 14 and smoke 2 of 2, in 3 of 3 runs. Main loop ran it again: pass | The web pack works with the Rust API as it is. |
| R0.10 generator | go | `rustfmt` 44 ms per resource. Plan A breaks at a resource name of 10 characters | Plan C: one `rustfmt` run per generator run, hash after formatting. |
| R0.11 dev loop | go | Handler-body edit to the first correct response: median 3381 ms, p90 3645 ms (limit: 5000 ms). Default dev profile: 5458 ms. Go with air: 2043 ms. Load 2.1 to 7.0 | Passes only with the shipped dev profile: `debug = 0`, `strip = "symbols"`, dependencies at `opt-level = 1`. The main loop recomputed the median from the raw samples. |
| R0.12 size baseline | go | CLI 5.4 MB against 28.0 MB. `--help` 4.7 ms against 13.3 ms. API 9.3 MB against 26.9 MB. Linux idle RSS 7.3 MiB against 84 MiB. musl image 12.1 MB against 28.3 MB. Start to `/healthz`: 125 ms against 52 ms (9 ms without auth) | Smaller and lighter than Go. The full-kit start is slower, because the startup argon2 dummy hash runs on one thread. R2 moves the hash after startup. |
| R0.13 lane cost | soft | Throttled settings: load 7.06 to 10.52, disk 33.62 GB by `du` (5.1 GB real free-space drop), swap increase 0. Planned settings: load 33.8 to 54.9. 48 crates, 95 test binaries, 53k CLI lines | Lanes need `CARGO_BUILD_JOBS=2` and 4 test threads. sccache does not work across lane target directories. The main loop recomputed the peaks from the 5 s samples. |
| R0.14 release | soft | All six ADR 0012 archives build (main loop, after the gnullvm target). Archives 1.23 to 1.45 MB | GoReleaser Rust builder + `cargo-zigbuild`. `builder: prebuilt` is not available. |

Other sessions shared the machine during every spike. R0.1 to R0.10 and R0.14 ran at a 1-minute load from 8 to 35. Use their timings to compare candidates, not as build budgets. R0.11, R0.12, and R0.13 ran alone, at a load from 2 to 10 outside their own peaks, and record the load with each series.

## The nine decisions

| Question | Decision | Evidence |
|---|---|---|
| Sync or async | **Sync CLI.** The `Runner` trait is synchronous. The kit and the generated API stay on axum and tokio (fixed decision 4). | R0.4: half the binary size, 2.5 times faster clean build. |
| YAML crate | **No crate.** An own editor on tree-sitter source spans in R1.5. Map serialization stays separate from node editing. | R0.1: the splice passes 17 of 32 fixtures and is the only candidate that completes a production family. |
| Formatter plan | **Plan C.** One `rustfmt --edition 2024` process in the plan step, before apply. The lock hash covers the formatted bytes. Keep the templates rustfmt-stable. Drop `prettyplease` and `syn` printing. | R0.10: 44 ms per resource. Stdin per file costs about 5 times more. |
| OIDC crate | **`oauth2` 5 + `jsonwebtoken` 11.** Not `openidconnect`. | R0.7: the only candidate that keeps the Go `email_verified` rule. `openidconnect` adds reqwest 0.12 and `rsa`. |
| TLS provider | **CLI: rustls with `ring`** (through `ureq`). **Kit: rustls through reqwest 0.13**, and the `aws_lc_rs` backend for `jsonwebtoken` when a pre-seed with network access makes the pack lock. | R0.4 and R0.7. The `aws_lc_rs` backend drops `rsa`, so both `deny.toml` ignores go away. Neither provider is pure Rust: both need a C toolchain. |
| linkme or inventory | **`linkme`**, with `wire_gen.rs` and the registry ID test before each merge. `inventory` stays as a tested fallback. | R0.3: no extra linker flags on macOS or Linux. Registration costs 160 bytes in release (`inventory`: 880). |
| GoReleaser or prebuilt | **GoReleaser with the Rust builder and `cargo-zigbuild`.** Windows arm64 uses the target `aarch64-pc-windows-gnullvm`, now in `rust-toolchain.toml`. | R0.14 and the main-loop rerun: six archives, six SBOMs, `checksums.txt`. The OSS GoReleaser rejects `builder: prebuilt`. |
| utoipa or aide | **utoipa 6 with `utoipa-axum` 0.3.** Remove aide and schemars from the plan. Do not turn on the utoipa feature `preserve_order`. | R0.6: both TypeScript clients are byte-equal with the clients from the Go spec. |
| Pack lock before publish | **`[patch.crates-io]` with a path kit.** The main loop makes the pack `Cargo.lock` offline. The release job publishes the kit, removes the patch, runs `cargo update -p spinup-kit`, and commits the new lock in the same release. | R0.6: `cargo build --locked --offline` passes with the patch. |

## Changes to the plan

**Lane environment (§3, from R0.13)**
- Lanes run with `CARGO_BUILD_JOBS=2` and `NEXTEST_TEST_THREADS=4`. The gate runs with `CARGO_BUILD_JOBS=4`. The planned setting (4 jobs and the default test threads in each lane) failed the load limit in every profile without clone seeds.
- Do not use a shared sccache. sccache 0.19.0 puts the absolute target path in the Rust cache key: a new lane target path got 0 hits of 1881. Leave `RUSTC_WRAPPER` unset, and leave `CARGO_INCREMENTAL` at the cargo default.
- At each milestone pre-seed, the main loop builds one whole-workspace seed target. Each new lane gets a `cp -c -R` APFS clone of it. This cuts the cold lane check to 42 to 62 s and the cold gate pass to about 135 s.
- The crates do not need to merge. At the measured growth rate, the `du` sum of the clone profile reaches 40 GB at about 100k lines. Check the real free-space drop, because `du` counts each clone in full.

**Contracts**
- ADR 0005 §1 changes for Rust. The spinup CLI runs the migrations itself, with the sqlx migrator at run time. The app binary cannot compile until `.sqlx/` is current (R0.10). The app binary keeps `migrate` for production.
- Add the command `spinup db prepare`. It runs migrate, then `cargo sqlx prepare`. A failed build with the `SQLX_OFFLINE` message adds `Try: spinup db prepare`.
- The post-step order of `g resource` and `g migration` is: plan with rustfmt, apply and lock, `migrate up`, `sqlx prepare`, build, `openapi-emit`, `client`.
- Generated apps set `SQLX_OFFLINE = "true"` in `.cargo/config.toml`, and pin the toolchain with the `rustfmt` and `clippy` components.
- The generated app ships this dev profile: `debug = 0`, `strip = "symbols"`, and `[profile.dev.package."*"] opt-level = 1`, plus `opt-level = 3` for `argon2` and `blake2`. Without it, the dev loop fails the 5 s limit (5458 ms). It halves the target size, and it doubles the cold build (about 125 s against 55 s).
- The kit owns its tables through the migration table `_spinup_kit_migrations`. The kit migrations run first.

**Kit**
- The job queue is at-least-once (R0.8: 56 and 66 duplicate effects after kills). Handlers must be idempotent. R2.8 adds `complete_tx` for an exactly-once database effect.
- Each kit test that checks spans gets its own test binary with a global subscriber. A scoped subscriber races on the tracing call-site cache (52 of 60 runs failed before the fix).
- Sessions: tower-sessions sends `Max-Age=604799`, and Go sends 604800. R1 rounds up, or uses an own session layer of about 200 lines.
- argon2id runs on one thread: 98 ms against 54 ms in Go. R1 decides the hash slots with a load test. The kit computes the startup dummy hash after the start (R0.11 option `defer_dummy_hash`, R0.12 start time).
- R1 ports `kit/apidocs`. The kit crate carries its own copy of the Scalar bundle, because a published crate cannot read files outside its package.
- R3 replaces the "Go runtime" dashboard of the monitoring pack with tokio or process metrics.

**Release and policy**
- License: `MIT OR Apache-2.0`, in `LICENSE-MIT` and `LICENSE-APACHE` at the repository root (chosen 2026-10-25, the user delegated the choice). The release archives take `LICENSE*`. Each crate sets `license = "MIT OR Apache-2.0"`.
- The C dependency rule allows the required OS libraries and the C code of `ring`, `aws-lc-sys`, and `zstd-sys`. Other external C libraries stay prohibited.
- The crate names `spinup-kit`, `spinup-plugin-sdk`, and `spinup-cli` are free (checked 2026-10-24). `spinup` is taken.
- `verify.py` of R0.14 reads the wrong `dist/` sub-directory. Fix it when R4 ports the release check.
- Do not name a file `*.go` outside a Go package while Go is in the repository. `go build ./...` reads it (R0.12 had `docker/Dockerfile.go`).

**Estimates**
- R1.5 (YAML editor): add 600 to 800 adapter lines, 200 to 350 test lines, and 3 to 5 engineering days (R0.1 planning estimate).
- R1: add the `apidocs` port and the session cookie fix.
- R2: the dev loop is about 1.3 s slower than Go with air (3381 ms against 2043 ms). The app crate rebuild (about 2.7 s, 147 sqlx macros) and the macOS first-start check of each new binary (about 17 ms per MB) are the two main costs. Do not use rust-lld on macOS: it was 200 to 400 ms slower in every pair.
- R2.8: add `complete_tx`, job metrics, the trace link from insert to job, the cleaner, pause, cancel, and snooze.
- R3: add the Rust runtime dashboard.
- Lanes at 2 build jobs take longer than at 4. Plan more wall time per lane, not more lanes.
- R0 itself: the 14 lane spikes finished in one day (2026-10-24), inside the time box of 3 weeks.

## Open items (carried into R1)

These items do not block the R0 gate. The gate needs the 14 result files, the two hard no-go numbers, and a decision.
- Native Windows runs for R0.3, R0.4, R0.5, and R0.14, and native execution of the Linux release binaries. These need a CI runner, so `rust` must be pushed. This is a user decision.
- The soft verdict of R0.3 changes to `go` only after the native Windows runs. R0.5 is `go`, but its native Windows check is also open.
- A Mailpit run for mail (R0.7), and the RED panel check with 5xx traffic (R0.8).
- Linux numbers for the dev loop (R0.11), and a Docker build that compiles inside the image (R0.12, R0.13: offline lanes cannot fetch the musl target).
- R0.13 ran each setting once or twice, on generated CLI code. Repeat the lane-cost check at the R1 gate with the real crates.
