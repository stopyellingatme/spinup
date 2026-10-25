# R0 go/no-go report

Status: draft. 13 of 14 spikes are merged. R0.13 is pending.
Unit: R0.15 (main loop). Plan: `docs/design/rust-plan.md` §4. Issue: #3.
Sources: the 14 `*.verdict.toml` files in this directory, and the main-loop verification notes of run `20261024-144640`.

## Decision

**Provisional: `GO with changes`.**

No merged spike has a `no-go` verdict. Nine spikes are `go`, and four spikes are `soft`.
Hard no-go condition (a), the dev loop, passes: the median is 3381 ms against the limit of 5000 ms (R0.11).
Condition (b), the lane cost, has no number yet. R0.13 measures it. If R0.13 fails its hard limit, the decision changes to `NO-GO`. The plan then stops, and the Go project stays.
The main loop writes the final decision when the R0.13 verdict file is merged.

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
| R0.13 lane cost | pending | Hard no-go (b): above 40 GB disk, load average above 14, or swap above 2 GB | |
| R0.14 release | soft | All six ADR 0012 archives build (main loop, after the gnullvm target). Archives 1.23 to 1.45 MB | GoReleaser Rust builder + `cargo-zigbuild`. `builder: prebuilt` is not available. |

Most timings come from a busy machine (1-minute load from 8 to 35). Use them to compare candidates, not as build budgets. R0.11, R0.12, and R0.13 measure on an idle machine.

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

R0.16 writes these changes into `rust-plan.md` and ADR 0019 before R1 starts.

**Contracts**
- ADR 0005 §1 changes for Rust. The spinup CLI runs the migrations itself, with the sqlx migrator at run time. The app binary cannot compile until `.sqlx/` is current (R0.10). The app binary keeps `migrate` for production.
- Add the command `spinup db prepare`. It runs migrate, then `cargo sqlx prepare`. A failed build with the `SQLX_OFFLINE` message adds `Try: spinup db prepare`.
- The post-step order of `g resource` and `g migration` is: plan with rustfmt, apply and lock, `migrate up`, `sqlx prepare`, build, `openapi-emit`, `client`.
- Generated apps set `SQLX_OFFLINE = "true"` in `.cargo/config.toml`, and pin the toolchain with the `rustfmt` and `clippy` components.
- The kit owns its tables through the migration table `_spinup_kit_migrations`. The kit migrations run first.

**Kit**
- The job queue is at-least-once (R0.8: 56 and 66 duplicate effects after kills). Handlers must be idempotent. R2.8 adds `complete_tx` for an exactly-once database effect.
- Each kit test that checks spans gets its own test binary with a global subscriber. A scoped subscriber races on the tracing call-site cache (52 of 60 runs failed before the fix).
- Sessions: tower-sessions sends `Max-Age=604799`, and Go sends 604800. R1 rounds up, or uses an own session layer of about 200 lines.
- argon2id runs on one thread: 98 ms against 54 ms in Go. R1 decides the hash slots with a load test. The dev profile sets `opt-level = 3` for `argon2` and `blake2`.
- R1 ports `kit/apidocs`. The kit crate carries its own copy of the Scalar bundle, because a published crate cannot read files outside its package.
- R3 replaces the "Go runtime" dashboard of the monitoring pack with tokio or process metrics.

**Release and policy**
- The repository needs a LICENSE before the first release. This is a user decision.
- The C dependency rule allows the required OS libraries and the C code of `ring`, `aws-lc-sys`, and `zstd-sys`. Other external C libraries stay prohibited.
- The crate names `spinup-kit`, `spinup-plugin-sdk`, and `spinup-cli` are free (checked 2026-10-24). `spinup` is taken.
- `verify.py` of R0.14 reads the wrong `dist/` sub-directory. Fix it when R4 ports the release check.

**Estimates**
- R1.5 (YAML editor): add 600 to 800 adapter lines, 200 to 350 test lines, and 3 to 5 engineering days (R0.1 planning estimate).
- R1: add the `apidocs` port and the session cookie fix.
- R2: start the argon2 dummy hash after startup (R0.12).
- R2: the app template ships the R0.11 dev profile. Without it, the dev loop fails the 5 s limit. Do not use rust-lld on macOS: it was 200 to 400 ms slower in every pair (R0.11).
- R2: the dev loop is about 1.3 s slower than Go with air (3381 ms against 2043 ms). The app crate rebuild (about 2.7 s, 147 sqlx macros) and the macOS first-start check of each new binary are the two main costs.
- R2.8: add `complete_tx`, job metrics, the trace link from insert to job, the cleaner, pause, cancel, and snooze.
- R3: add the Rust runtime dashboard.
- R0 itself: the 11 lane spikes finished in one day (2026-10-24), inside the time box of 3 weeks.

## Open before the R0 gate

- R0.13 (lane cost, hard no-go b).
- Native Windows runs for R0.3, R0.4, R0.5, and R0.14, and native execution of the Linux release binaries. These need a CI runner, so `rust` must be pushed. This is a user decision.
- A Mailpit run for mail (R0.7), and the RED panel check with 5xx traffic (R0.8). R1 can take both.
- The soft verdict of R0.3 changes to `go` only after the native Windows runs. R0.5 is `go`, but its native Windows check is also open.
