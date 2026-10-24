# spinup in Rust: the rewrite plan (`docs/design/rust-plan.md`)

Status: accepted (2026-10-24). R0 decides go or no-go. Date: 2026-10-24. Read `plan.md` and `architecture.md` first. This document extends `plan.md`. It does not replace it. Where this document and `plan.md` disagree about Rust work, this document wins.

## Context

We rewrite the CLI, the runtime library (`kit`), and the plugin SDK in Rust. We also change the generated API from Go to Rust. The web app (SvelteKit), PostgreSQL, Docker Compose deploy, and OpenTelemetry with Grafana LGTM stay as they are.

The project has no users and no release. We can break formats. We do not write a migration path for Go apps.

Go stays in the repository as the parity oracle until the cutover (R5). A Cargo workspace lives beside the Go code. At R5 we delete all Go code.

One `/orchestrate` run builds one milestone. R3 is too big for one run, so it has three runs (R3a, R3b, R3c).

### What "parity" means

- Files and golden trees are byte-equal with Go, for the packs that do not depend on Go.
- Console text is equal at regex level only. The scripts assert with regex. Cobra and clap print different `Usage:` text. Nobody may expect equal `Usage:` text.
- Archives compare by decoded tar contents. The zstd bytes differ.
- The docs site gets a new baseline, in `docs/site-rs/`.
- Algorithms (lock JSON, diff, quote, semver) are byte-equal. Go oracle fixtures prove this.

### Decisions this plan makes beyond the user's list

The main loop accepted each item on 2026-10-24. Section 11 records the answers.

1. **Lock schema 3.** Schema 2 has Go names (`formatter_go`, `kit.module`). A rename without a version bump breaks ADR 0006 §2. The Rust CLI writes schema 3 and rejects schemas 1 and 2. The `cmp spinup.lock` lines in the scripts compare a lock with an earlier lock from the same run. They do not compare with a fixed file. So schema 3 does not break the shared scripts.
2. **Converted packs are generated.** A converter reads the Go packs and writes minijinja packs into `crates/spinup-packs/packs/`. Rust-only changes live in `crates/spinup-packs/overlay/`. The overlay merge rule is in section 1. A test fails if re-conversion gives a diff.
3. **The converter is a Go program.** It uses `text/template/parse`, so it reads the real syntax tree. It is a throwaway tool and we delete it at R5. Per-pack conversion rules live in `scripts/rust/convert-templates/rules/<pack>.go`. The golden unit of a pack owns the rules file of that pack.
4. **Oracle fixtures from Go.** A Go test writes fixtures to `testdata/oracle/`. The Rust tests read them. R0.5 creates the generator and owns the lock, diff, and quote cases. R1.0b extends the generator and owns all other cases. One unit owns each case file.
5. **R0 lanes are offline too.** The R0 pre-seed writes one `spikes-rs/r0-NN-<name>/Cargo.toml` for each spike, with every candidate crate from the research, plus its `Cargo.lock`. The main loop runs `cargo fetch --locked` in each spike directory. A spike lane builds with `CARGO_NET_OFFLINE=true` and `--locked`. If a spike needs one more crate, it reports `blocked: <crate>`. The user chose this on 2026-10-24 in place of a network exception.
6. **One binary for Rust apps.** The worker is a subcommand (`api worker`). The queue tables ship in the kit migration set.
7. **R3 has three runs** (R3a, R3b, R3c).
8. **Formatter plan.** R0.10 decides. The default is: templates are written rustfmt-stable, and a golden test runs `rustfmt --check`. Render does no formatting. If dynamic code cannot stay stable, a `rustfmt` post-step runs and the lock hash covers the formatted bytes.
9. **R1 keeps all Go-facing text in the neutral packs.** R1 also renders the converted Go `api` pack, because most packs require it (see R1 scope). The `module` question and the README wording do not change in R1. R2 changes them. The flag name `--module` stays in R2, because about 20 shared scripts pass it. It feeds the crate slug.
10. **Go freeze timing.** The main loop tags `go-freeze-r0` at the R0 gate. Only four Go-side units may refactor Go after the tag: R1.0a, R1.0b, R3.0a, R3.0b. Everything else is a bug fix with a txtar or golden case. The main loop moves the final tag `go-parity-target` after R3.0b.
11. **Stack versions stay in sync.** `crates/spinup-stack/stackversions.yaml` (schema 2) shares sections with the Go file (schema 1). A script generates the shared sections from the Go file. A test fails on a mismatch. R1.5 ports `ci_pins_test` (digest pins, SHA pins, 14-day rule). In R1, schema 2 is additive: it keeps the Go sections (`go`, `go_modules`, `go_tools`) and adds `rust`, `crates`, `cargo_tools`. The main loop removes the Go sections in the R2 gate commit, after all overlays merge. A strict-undefined render proves that no pack still reads them.
12. **No behavior-version gates in the Rust kit.** The memory rule "no compat ceremony yet" holds before v1. The Rust kit drops `SPINUP_BEHAVIOR_VERSION`. The `compose.yml` overlay removes the variable (R2.20). R4.9 guards the public API only.
13. **Docs site.** The Rust `docs reference` writes `docs/site-rs/` until R5. The Go gate keeps `TestDocsSiteIsCurrent` on `docs/site/`. R5 swaps the directories.
14. **Doctest.** R1.0a adds `SPINUP_BIN` and two directives to the Go doctest: `<!-- doctest: only go -->` and `<!-- doctest: only rust -->`. The Rust pages run under the Go doctest runner until R5.

## Fixed decisions (from the user, not open)

| Area | Decision |
|---|---|
| Scope | CLI, `kit`, `plugin-sdk` in Rust. The generated API is Rust and replaces the Go API. |
| Migration | None for Go apps. The dogfood todo app is created again. |
| Repository | Same repository. Go is the oracle until R5. Then delete all Go. |
| HTTP | axum on tokio. |
| OpenAPI | Code-first (utoipa or aide). The app emits the spec. spinup commits it as `api/openapi/bundle.gen.yaml`. The web client generation does not change. |
| Database | sqlx `query_as!` with committed `.sqlx/`. `spinup db prepare` refreshes it. sqlx migrations are embedded in the app binary. goose goes away. |
| R0 | Time-boxed spike with a go/no-go report. |
| Hard no-go | (a) Handler edit to served response over about 5 s warm. (b) Two Rust lanes plus the gate overload the Mac, or use more than about 40 GB. |
| Soft risks | minijinja byte-equality. Comment-preserving YAML. These change the plan. They do not stop it. |
| Templates | minijinja replaces Go `text/template`. One-time conversion. Plugin packs change syntax. |
| Milestones | R0 spike, R1 CLI core, R2 kit and Rust API pack and generators, R3 operate commands, R4 plugins and release and docs, R5 cutover. |
| Parity | `SPINUP_BIN` in the Go testscript harness. The Rust golden test reads the same `testdata/golden` trees. R5 ports the harness to Rust. |
| Lanes | Offline. Crates are pre-seeded. Lanes cannot add crates. |
| Kit defaults | tower-sessions with a sqlx store, argon2, openidconnect, lettre, tracing and opentelemetry-otlp, own rate limiter (ADR 0016), own job queue on sqlx. |
| Registration | linkme distributed slices. |
| Plugin SDK | Rust crate only. Protocol stays JSON-RPC over stdio. |
| Publishing | `spinup-kit` and `spinup-plugin-sdk` go to crates.io as alpha. Each publish needs user approval. |
| Go freeze | Tag the parity target. Go gets bug fixes only. Each fix adds a txtar or golden case. |
| Defaults | GoReleaser with the Rust builder (cargo-zigbuild) and the ADR 0012 asset names. Keep the template archive format and the six platform targets. Per-lane `CARGO_TARGET_DIR`, shared sccache, delete after merge. |

The hard no-go limits for load average (14) and swap (2 GB) came from this plan. The user confirmed them on 2026-10-24.

## 1. Cargo workspace layout

### Rules

- The workspace root is `Cargo.toml` at the repository root. Crates live in `crates/<name>/`.
- The root `[workspace]` sets `exclude = ["spikes-rs"]`. Spike code lives in `spikes-rs/`. Each spike declares its own `[workspace]`. The directory `spikes/` is not used, because `spikes/bluegreen` is a Go app.
- The Go root module and `go.work` stay until R5.
- One crate holds one layer-cohesive group of Go packages. We do not make one crate for each Go package. Each crate makes its own test binary, and test binaries use disk.
- Each crate has one integration test binary: `tests/it/main.rs`. The pre-seed writes `main.rs` with all `mod` lines. Units add files such as `tests/it/golden_base.rs`. Units never add a second file in `tests/`.
- Every crate sets `[lints] workspace = true`, `rust-version = "1.99"`, and `edition = "2024"`.
- Each unit owns a sub-directory of a crate. The sub-directory has its own `mod.rs`. `lib.rs` lists only the top-level directories. This removes most file-set overlap between units.
- R0.13 measures disk use. The result may merge small crates.

### Overlay merge rule

The loader merges `crates/spinup-packs/packs/<pack>/` (converted) with `crates/spinup-packs/overlay/<pack>/` (Rust-only).

1. A file with the same path in the overlay replaces the converted file.
2. A file with a new path in the overlay is added.
3. The file `overlay/<pack>/remove.txt` lists converted paths to delete. The loader fails if a listed path does not exist.
4. Fragments in `overlay/<pack>/pack.d/*.toml` merge after the converted fragments, in file-name order.
5. An overlay cannot replace `pack.toml`. It uses fragments.
6. `make rs-convert-check` regenerates only `packs/`. It never touches `overlay/`.
7. `make rs-pack-tree` writes the merged tree to `target/pack-tree/`. The harness command `mkversion` uses it through `SPINUP_PACKS_SRC`.

### Layering rules, as crate rules

| Go rule | Rust enforcement |
|---|---|
| `internal/cli` holds only wiring. | `spinup-cli` may depend on any crate. No crate may depend on `spinup-cli`. A review rule says `spinup-cli` holds no logic. |
| Generators return a `vfs::Plan`. Only apply writes. | `spinup-gen*` crates have a `clippy.toml` with `disallowed-methods` for `std::fs::write`, `File::create`, `create_dir_all`, `remove_file`. Only `spinup-vfs` and `spinup-manifest` (atomic lock save) may write. |
| Every subprocess goes through `internal/exec`. | The root `clippy.toml` disallows `std::process::Command` and `tokio::process::Command`. Only `spinup-exec` allows them. |
| `merge`, `vfs`, `render` never import `cli`. | The file `docs/design/rust-layers.toml` lists the allowed crate edges. A test in `spinup-cli` reads `cargo metadata` and fails on any other edge. |
| Hook registries. | The hook slices for deploy live in `spinup-deploy`. `spinup-backup` and `spinup-monitor` register into them. `rust-layers.toml` allows the edges backup to deploy and monitor to deploy. |
| No CGO. | New rule: no system C library at runtime. C code that `cc` builds into the binary is allowed (zstd, ring). R0.14 checks this on all six targets. |
| Registration without a central list. | linkme slices. `crates/spinup/src/wire_gen.rs` lists `use spinup_x as _;` for each crate that registers items. A test compares the registered IDs with `crates/spinup/tests/expected_registry.txt`. |

### Crate table (CLI workspace)

"Depends on" names workspace crates only. "M" is the milestone that fills the crate.

| Crate | Replaces | Depends on | M |
|---|---|---|---|
| `spinup-ui` | `internal/ui` | none | R1 |
| `spinup-compat` | `internal/compat` | none | R1 |
| `spinup-vfs` | `internal/vfs` | none | R1 |
| `spinup-manifest` | `internal/manifest` (lock schema 3) | none | R1 |
| `spinup-stack` | `internal/stackversions` (schema 2) | none | R1 |
| `spinup-editors` | `internal/codemod` (YAML, TOML, JSON editors) | none | R1 |
| `spinup-exec` | `internal/exec` | none | R1 |
| `spinup-project` | `internal/project`, `internal/webtool` | ui, manifest, exec | R1 |
| `spinup-packs` | `internal/templates` (loader, overlay merge, `when`, embed) | vfs, manifest, stack | R1 |
| `spinup-tplstore` | `internal/templates/store` | packs, manifest, compat, stack, project | R1 |
| `spinup-render` | `internal/render`, `internal/modules` | packs, stack, manifest, vfs, project, ui | R1 |
| `spinup-merge` | `internal/merge` | exec, compat | R1 |
| `spinup-doctor` | `internal/doctor` registry, `composelint`, `plainsecrets` | exec, manifest, project, ui, editors | R1 |
| `spinup-plugin-api` | `internal/plugin/contract.go` and the `PluginSource` seam | manifest, vfs | R1 |
| `spinup-migrate` | `internal/migrate`, `migratetest`, codemod 0001 | exec, manifest, stack, packs, editors, project | R1 |
| `spinup-gen` | `internal/gen` contract, registry, run, fields, `Part` trait | vfs, manifest, exec, ui, project | R1 (contract, registry, run), R2 (fields, post-steps) |
| `spinup-upgrade` | `internal/upgrade` | merge, migrate, render, tplstore, plugin-api, doctor, project, manifest, vfs, exec, compat, editors, ui | R1 (engine), R2 (cargo phases) |
| `spinup-drift` | `internal/drift` | upgrade, doctor, tplstore, vfs | R1 |
| `spinup-newapp` | `internal/gen/newapp` | render, packs, tplstore, stack, manifest, vfs, exec, ui, plugin-api | R1 |
| `spinup-db` | `internal/db`, `db/ops`, `db/lint` | exec, project, ui | R2 |
| `spinup-build` | `internal/build` | exec, manifest, project, stack, ui | R2 (local build), R3a (image) |
| `spinup-appcmd` | `internal/apptest`, `routes`, `devloop` | exec, manifest, project, stack, gen, doctor, ui | R2 (routes, apptest), R3c (devloop) |
| `spinup-gen-web` | `gen/page`, `component`, `client`, `dashboard`, `alert`, resource pages and nav | gen, vfs, manifest, project, editors | R2 |
| `spinup-gen-app` | `gen/resource`, `model`, `migration`, `handler`, `job`, `mailer`, `openapi` (emit), `destroy` | gen, gen-web, db, render, vfs, manifest, project | R2 |
| `spinup-remote` | `internal/remote` | exec, project | R3a |
| `spinup-secrets` | `internal/secrets` | exec, project, editors | R3a |
| `spinup-server` | `internal/server` | exec, project, remote, doctor, ui | R3a |
| `spinup-deploy` | `internal/deploy` | remote, exec, secrets, project, manifest, drift, db, build, ui | R3a |
| `spinup-cloud` | `internal/cloud/digitalocean`, `cloudauth`, `browser` | none | R3b |
| `spinup-dofake` | `dofake` (axum, library and binary, test only) | none | R3b |
| `spinup-provision` | `internal/provision` | cloud, deploy, secrets, remote, server, editors, project | R3b |
| `spinup-infra` | `internal/infra`, `internal/tools` | exec, project, editors, stack | R3b |
| `spinup-backup` | `internal/backup`, `pgdump`, `walg`, `maintain/window` | deploy, remote, render, packs, project | R3c |
| `spinup-maintain` | `internal/maintain`, `pg`, `pgupgrade` | backup, deploy, remote, stack, project | R3c |
| `spinup-monitor` | `internal/monitor` | remote, deploy, project | R3c |
| `spinup-doctor-checks` | `doctor/checks`, `quick`, `clientdrift`, `remote` | doctor, backup, remote, gen, merge, compat | R3c |
| `spinup-plugin` | `internal/plugin/pack`, `render`, `rpc`, `scaffold` | plugin-api, plugin-sdk, gen, packs, render, doctor | R4 |
| `spinup-selfupdate` | `internal/selfupdate` | compat, exec | R4 |
| `spinup-ci` | `internal/ci` | gen, render, packs, doctor | R4 |
| `spinup-release` | `scripts/release/*` (bins: `check`, `templates`, `keygen`) | tplstore, stack, compat | R4 |
| `spinup-cli` | `internal/cli`, `internal/docsite` | all crates above | R1 onward |
| `spinup` | `cmd/spinup` (bin: `main.rs`, `wire_gen.rs`) | cli | R1 |
| `spinup-testutil` | `internal/testutil` golden helpers (dev-dependency) | render, packs, vfs | R1 |
| `spinup-testscript` | `internal/testutil/scripttest` | none | R5 |
| `spinup-doctest` | `internal/doctest` | none | R5 |

The `PluginSource` trait lives in `spinup-plugin-api`. It has three methods: trust, fetch, and render. In R1, `spinup-newapp` and `spinup-upgrade` use a null implementation. In R4, `spinup-plugin` binds the real one.

### Crate table (kit and SDK)

| Crate | Path | Replaces | Notes |
|---|---|---|---|
| `spinup-kit` | `crates/spinup-kit` | Go `kit/*` | One crate with features: `auth`, `oidc`, `jobs`, `mail`, `telemetry`, `devtools`, `embedweb`, `apidocs`. Default features: app, config, db, httpx, logx, secrets, deployx. |
| `spinup-kit-testkit` | `crates/spinup-kit-testkit` | `kit/testkit`, `kit/auth/authtest` | A dev-dependency. It keeps testcontainers out of app builds. |
| `spinup-plugin-sdk` | `crates/spinup-plugin-sdk` | `plugin-sdk/sdk`, `conformance` | serde, serde_json, base64 only. |

Kit package dispositions:
- `kit/backup` and `kit/deprecation` are dropped. No pack wires them. Backup metrics come from the VM textfile (ADR 0011 §4).
- `kit/MIGRATION.json` moves to `crates/spinup-kit/`.
- The Go kit has no ETag layer. The Rust kit adds none. `spa` sets `Cache-Control` only.
- `kit/api`: R2.1 reads it and records its place in ADR 0021. The default is to fold it into `httpx`.

The names `spinup-kit` and `spinup-plugin-sdk` are free on crates.io (checked 2026-10-24). The name `spinup` has another owner. The CLI crate name, if we publish it, is `spinup-cli`. The binary name stays `spinup`. R0.14 confirms.

### Directory map

```
Cargo.toml                   workspace (main loop)
Cargo.lock                   (main loop)
rust-toolchain.toml          channel 1.99.1 (main loop)
crates/<name>/               all crates above
spikes-rs/r0-NN-<name>/      R0 spikes, standalone workspaces
testdata/oracle/             fixtures that Go writes and Rust reads
testdata/golden-rs/          golden trees for packs that change in Rust
testdata/reference/todo-rs/  frozen reference app (from R0, moved in R2)
testdata/corpus-rs/          frozen Rust app corpus (R2 gate)
docs/design/rust-spike/      one result file and one verdict.toml for each R0 question
docs/site-rs/                Rust docs site baseline until R5
```

## 2. Shared files (main loop only)

Only the main loop writes these files. A unit never edits them. A missing item means the unit reports `blocked: <item>`.

**Workspace and toolchain**
- `Cargo.toml` (root), every `crates/*/Cargo.toml`, `Cargo.lock`
- `rust-toolchain.toml`, `rustfmt.toml`, `clippy.toml` (root and per crate), `deny.toml`
- `.cargo/config.toml`, `.config/nextest.toml`
- `docs/design/rust-layers.toml`
- `scripts/rust/spike-check.sh`, `scripts/rust/no-go-check.sh`

**Module lists (the Rust twin of the stub `doc.go` rule)**
- `crates/*/src/lib.rs`, every top-level `mod.rs` of a crate, `crates/spinup-cli/src/cmd/mod.rs`, and every `tests/it/main.rs`.
- The pre-seed commit writes these files with all `mod` lines for the milestone.
- The pre-seed also writes a stub `mod.rs` for each unit sub-directory. A stub belongs to the unit whose file set contains it. That unit replaces the stub. A unit never edits a module list. A unit that needs a new module inside its own sub-directory edits its own `mod.rs`.

**Registration**
- `crates/spinup/src/wire_gen.rs`
- `crates/spinup/tests/expected_registry.txt`

**Contract files**

| Milestone | File |
|---|---|
| R1 | `crates/spinup-exec/src/runner.rs` (Runner trait, sync or async as R0.4 decides) |
| R1 | `crates/spinup-tplstore/src/source.rs` |
| R1 | `crates/spinup-merge/src/contract.rs` |
| R1 | `crates/spinup-upgrade/src/contract.rs` |
| R1 | `crates/spinup-migrate/src/contract.rs` |
| R1 | `crates/spinup-gen/src/contract.rs` |
| R1 | `crates/spinup-plugin-api/src/lib.rs` (includes `PluginSource`) |
| R3a | `crates/spinup-remote/src/runner.rs` |
| R3c | `crates/spinup-backup/src/registry.rs`, `crates/spinup-backup/src/window.rs` |

**Versions, packs, and content**
- `internal/stackversions/stackversions.yaml` (Go schema 1 until R5), `crates/spinup-stack/stackversions.yaml` (schema 2)
- `Makefile`, `scripts/tools-check.sh`, `scripts/bump-stack.sh`, `scripts/bin/`
- `crates/spinup-packs/packs.digest`
- `crates/spinup-packs/packs/` and `scripts/rust/convert-templates/` core files. Exception: a golden unit regenerates its own pack with `make rs-convert-packs PACK=<name>` and owns `scripts/rust/convert-templates/rules/<name>.go`.
- `crates/spinup-tplstore/trusted_keys.txt` (same bytes as the Go `TrustedKeys`)
- In the `api` pack: `Cargo.toml.tmpl`, `Cargo.lock`, `pack.d/deps.toml`
- In the `web` pack: `pack.d/{deps,modes,shadcn}.toml`, lockfiles, `ui/**`
- In the `monitoring` pack: `pack.d/vendor.toml` and the vendored dashboards
- `crates/spinup-kit/assets/scalar.js.gz` (vendored Scalar bundle)
- `.github/workflows/*`, `.github/actions/*`, `.goreleaser.yaml`, `Dockerfile.ci`
- `testdata/script/parity.txt`, `testdata/golden/rust-parity.txt`, `testdata/golden/rust-filter.toml`
- `CLAUDE.md`, `docs/design/plan.md`, `docs/design/rust-plan.md`

Go shared files (`go.mod`, `go.sum`, `go.work`, `deps_m*.go`, the Go contract files) stay main-loop only until R5.

## 3. Toolchain, Makefile, and check forms

### Toolchain

| Tool | Version | Note |
|---|---|---|
| Rust | 1.99.1 (edition 2024) | Pinned in `rust-toolchain.toml` with `rustfmt`, `clippy`, and the target list. The Mac default is 1.91.1. sqlx 0.9 needs 1.94 or newer. |
| cargo-nextest | 0.9.149 | Installed copy is 0.9.146. |
| sqlx-cli | 0.9.0 | Installed. It cannot install with `--locked`, so `tools-check` pins by version. |
| cargo-deny | 0.20.2 | Not installed. Replaces `govulncheck` and checks licenses. |
| sccache | verify in R0.13 | Not installed. |
| cargo-zigbuild, zig | 0.24.0, 0.16.0 | zig is installed. Release only. |
| cargo-chef | 0.1.78 | Generated app Dockerfile. |
| GoReleaser | 2.19.0 | The installed copy may be older. `tools-check` decides. |

The main loop runs `rustup toolchain install 1.99.1` once with network access. Lanes set `RUSTUP_TOOLCHAIN=1.99.1`. Lanes cannot install a toolchain.

### New Makefile targets (main loop writes them)

| Target | What it does |
|---|---|
| `make rs-lint` | `cargo fmt --check` and `cargo clippy --locked --all-targets -- -D warnings`. `CRATES="-p a -p b"` narrows both. No `cargo deny`. This is the lane lint. |
| `make rs-test` | `cargo nextest run --locked` plus `cargo test --doc`. `CRATES` narrows. `TEST='<filterset>'` passes a nextest filterset. The target always excludes `test(packs_digest)`. |
| `make rs-build` | `cargo build --locked --all-targets`. Gate only. |
| `make rs-deny` | `cargo deny --offline check bans licenses sources`. Gate only. |
| `make rs-golden` | Rust golden tests. `PACK=<name>` narrows. `FILTER='<filterset>'` narrows inside a pack. `UPDATE=1` rewrites. |
| `make rs-digest` | Runs the `packs_digest` test. Gate only. |
| `make rs-script` | Builds the Rust binary. Runs the Go harness with `SPINUP_BIN`. `RUN=<glob>` narrows. |
| `make rs-doctest` | Runs the Go `TestDocs` with `SPINUP_BIN`. Until R5. |
| `make rs-app-gate` † | Generates a Rust app. Builds and tests it. Docker and network. |
| `make rs-fetch` † | `cargo fetch --locked`. Main loop only. |
| `make rs-vuln` † | `cargo deny check advisories`. Gate only. |
| `make rs-cover` | `cargo llvm-cov`, 70 percent floor. R1: `merge`, `manifest`, `migrate`. R2 adds `upgrade`. |
| `make rs-tools-check` | Checks the Rust tool versions. |
| `make rs-convert-packs` | Runs the Go converter. `PACK=<name>` narrows. Writes `crates/spinup-packs/packs/`. |
| `make rs-convert-check` | Fails if re-conversion gives a diff. |
| `make rs-pack-tree` | Writes the merged pack tree (converted plus overlay) to `target/pack-tree/`. |
| `make rs-ref-check` | Runs `cargo check --offline --locked` with `SQLX_OFFLINE=true` on `testdata/reference/todo-rs/` with generated files overlaid. |
| `make rs-install` | Installs the Rust binary. Version comes from `SPINUP_VERSION` at build time. Fills `DEV_TEMPLATES` as `make install` does. |
| `make rs-bump-stack` | Rust twin of `bump-stack`. |
| `make oracle` | Runs the Go oracle generator. Writes `testdata/oracle/`. |
| `make dr` | New. Runs `bash testdata/dind/dr/run.sh`. The Makefile has no `dr` target today. Added in the R3a pre-seed. |
| `make parity` | Runs `make script`, then `make rs-script`. Prints one line per script. |

### Check forms

Add these to `CLAUDE.md`. The Go forms `T`, `S`, `G` stay until R5.
- `RT <crates> [:: <filterset>]` means `make rs-test CRATES="-p <crate> ..." TEST='<filterset>'`.
- `RG <pack> [:: <filterset>]` means `make rs-golden PACK=<pack> FILTER='<filterset>'`.
- `RS <name>` means `make rs-script RUN=<name>`.
- Every Rust unit check also implies `make rs-lint CRATES=<own crates>`. A lane does not run `rs-build` or `rs-deny`. Lane checks use nextest plus clippy, so each lane makes two artifact sets (check and test) and no third build set.

### Lane environment (from R1)

```
RUSTUP_TOOLCHAIN=1.99.1  RUSTUP_AUTO_INSTALL=0 (verify in R0.13)
CARGO_NET_OFFLINE=true   (and --locked on every command)
CARGO_HOME=<shared>      CARGO_TARGET_DIR=<lane dir>/target
CARGO_BUILD_JOBS=4       SQLX_OFFLINE=true
CARGO_INCREMENTAL and RUSTC_WRAPPER: set by the R0.13 result
LANE_NICE=10             max 2 lanes; the gate runs alone
```

If a unit needs a crate, a feature, or a version that is not in `Cargo.toml` and `Cargo.lock`, it stops. It reports `blocked: <crate>`. It does not run `cargo add`, `cargo update`, or `cargo install`. Postgres tests use `DATABASE_URL` and `#[sqlx::test]`. A lane never starts a database.

## 4. R0: Spike (time box: 3 weeks)

**Goal:** answer 14 questions with numbers. Then decide go or no-go.

**Rules for R0**
- The IDs are R0.1 to R0.14. There are no other question numbers.
- Each spike writes code to `spikes-rs/r0-NN-<name>/` (its own workspace, its own `Cargo.lock`, its own `CARGO_TARGET_DIR`).
- Each spike writes `docs/design/rust-spike/r0-NN-<name>.md` with five headings: Question, Method, Numbers, Verdict, Recommendation. The Verdict heading holds one line `Verdict: go`, `Verdict: soft`, or `Verdict: no-go`.
- Each spike also writes `docs/design/rust-spike/r0-NN-<name>.verdict.toml`. It has `verdict` and the numeric fields of the unit row. R0.11 has `median_ms` and `p90_ms`. R0.13 has `peak_disk_gb`, `load1_max`, and `swap_gb`.
- The check of every R0 unit is `scripts/rust/spike-check.sh r0-NN`. It verifies the five headings, the `Verdict:` line, the TOML fields, and then runs `cargo test` in the spike directory. R0.15 reads the TOML files, not prose.
- Spike lanes are offline. See "Decisions" item 5.
- Measurement spikes (R0.11, R0.12, R0.13) run alone. The machine must be idle (load average below 2). They run in the last waves.
- The R0 pre-seed holds `rust-toolchain.toml`, `spikes-rs/README.md`, `spike-check.sh`, the result-file template, `tools-check` additions, the Go converter skeleton, and one spike workspace (`Cargo.toml`, `Cargo.lock`, stub `src/`) for each of R0.1 to R0.14 with the candidate crates fetched.

**R0 pre-seed commit (`deps: pre-seed R0`):** install Rust 1.99.1, sccache, cargo-deny, cargo-chef, bacon, watchexec, and cargo-zigbuild. Create the 14 spike issues. The main loop tags `go-freeze-r0` at the R0 gate.

### R0 unit graph

| Unit | Question and outcome | Measurable result | Check | File set | Deps |
|---|---|---|---|---|---|
| R0.1 | **YAML editing.** Compare `yamlpath`+`yamlpatch`, `yaml-edit`, and a small splice editor on `tree-sitter-yaml`. Cover the six Go call sites: `.sops.yaml`, alert rules, the `codemod` example, compose edit, cloud-init (`userdata`, `userdata_rotate`), and `EditYAML`. | A table of candidate against fixture. Pass means byte-equal to the Go output on inputs in go-yaml style, with comments kept. At least 25 fixtures. One candidate must pass 100 percent on the five real call sites. Note the C build dependency (tree-sitter). | `spike-check r0-01` | `spikes-rs/r0-01-yaml/`, `docs/design/rust-spike/r0-01-*` | none |
| R0.2 | **minijinja conversion.** Write the Go converter (`scripts/rust/convert-templates/`, Go `text/template/parse`). Convert all 74 `.tmpl` files and the inline templates in the generators. Implement the Go-exact filters `quote`, `default`, `get`, `snake`, `kebab`, `camel`, `pascal`. | Golden files that are byte-equal, per pack. Target: 100 percent for `base`, `web`, `web-variants`. At least 99 percent over all neutral packs. All 74 templates compile. List each unequal file and its cause. List `[[`/`]]` clashes. | `spike-check r0-02`, then compare with `testdata/golden` | `scripts/rust/convert-templates/`, `spikes-rs/r0-02-minijinja/`, `docs/design/rust-spike/r0-02-*` | none |
| R0.3 | **linkme and wire list.** Build a slice test with a registering crate that the binary does not reference. Show the drop. Show the fix (`use x as _;`). Add the registry ID test. Try `inventory` as a fallback. | The registered ID set equals the expected set on macOS arm64, Linux (Docker), and Windows (CI runner), for debug, release, and LTO. The test fails when a wire line is removed. | `spike-check r0-03` on three OS | `spikes-rs/r0-03-linkme/`, `docs/design/rust-spike/r0-03-*` | none |
| R0.4 | **Sync or async.** Build the same small CLI core twice: sync (threads, `ureq`) and tokio (`reqwest`). Include a cancelable subprocess runner with process-group kill, a streaming redactor, a lock-refresh task, and a TLS check (`ring` or `aws-lc-rs`, six targets). | Binary size, build time (cold and warm), and glue lines for each. A decision for `spinup-exec` and the `Runner` traits. Windows behavior listed. | `spike-check r0-04` | `spikes-rs/r0-04-runtime/`, `docs/design/rust-spike/r0-04-*` | none |
| R0.5 | **Format interop.** (a) Lock canonical JSON byte-equal with Go, including `<`, `>`, `&` escapes, `[]` against `null`, and number precision. (b) Verify a Go-signed archive with `ed25519-dalek`. Read it with `zstd`. Write a deterministic tar+zstd. (c) Port `vfs/diff.go` and compare 200 oracle cases. (d) `cap-std` rooted paths, symlink escape, and Windows device names. | (a) 100 percent of oracle lock cases equal. (b) The Go archive verifies and decodes. (c) 100 percent of diff cases equal. (d) Same results as the Go `vfs` regression tests on macOS, Linux, and Windows. | `spike-check r0-05` | `spikes-rs/r0-05-interop/`, `internal/oracle/` (creates the generator, owns lock, diff, and quote cases), `testdata/oracle/`, `docs/design/rust-spike/r0-05-*` | none |
| R0.6 | **Reference app A.** Hand-write the todo API: axum, utoipa (aide as fallback), sqlx 0.9 `query_as!`, embedded migrations, `app openapi`, `app migrate`, `healthcheck`. Test the sqlx migrator against the kit queue tables. Compare the emitted OpenAPI 3.1 with the Go `bundle.gen.yaml`. Generate the TypeScript client with both openapi-typescript and heyapi, and diff both. Generate the app, add `[patch.crates-io]` with a path kit, and run `cargo build --locked --offline`. | Same operationIds, schema names (`Todo`, `TodoInput`, `TodoList`, `Problem`), status codes, and `application/problem+json` on 401, 404, and 422. Both `schema.d.ts` outputs differ from the Go golden only in a listed set of lines. Several methods on one path and `nest()` both work. `cargo sqlx prepare --check` passes. The lockfile test passes or the result states how the pack lock is made before the kit is published. | `spike-check r0-06` with `DATABASE_URL` | `spikes-rs/r0-06-ref-api/`, `docs/design/rust-spike/r0-06-*` | none |
| R0.7 | **Reference app B (auth).** Add the custom sqlx `SessionStore` (hashed token), argon2id, login lockout, cross-origin check, the rate limiter (replay the ADR 0016 table), password reset (lettre against Mailpit), and OIDC. Compare `openidconnect` and `oauth2`+`jsonwebtoken`. Record the `RUSTSEC-2023-0071` handling. | The cookie contract of ADR 0008 §3 holds (name `session`, HttpOnly, SameSite=Lax, Secure rule). The Go rate-limit table passes unchanged. Error texts equal `auth_ratelimit.txtar`. One OIDC crate choice, with a `cargo deny` result. | `spike-check r0-07` with `DATABASE_URL` | `spikes-rs/r0-07-ref-auth/`, `docs/design/rust-spike/r0-07-*` | R0.6 |
| R0.8 | **Reference app C (jobs and OTel).** Add the own job queue (River schema, `FOR UPDATE SKIP LOCKED`, `LISTEN/NOTIFY` with poll fallback, backoff, rescuer, periodic jobs, unique by args) and a chaos test. Add tracing, `opentelemetry-otlp`, and the HTTP metric layer. | Chaos test: kill the worker 50 times during 10 000 jobs. No job is lost. Retry follows the backoff. Metric names match the monitoring pack (`http_server_request_duration_seconds` with `http_route`, `http_request_method`, `http_response_status_code`, and `job=<app>-api`). The Grafana RED panel shows data against the LGTM image †. | `spike-check r0-08` with `DATABASE_URL`; panel check † | `spikes-rs/r0-08-ref-jobs-otel/`, `docs/design/rust-spike/r0-08-*` | R0.6 |
| R0.9 | **Reference app D (web).** Run the existing SvelteKit web pack (rendered by the Go CLI) against the reference API: session cookie through SSR and form action, `traceparent`, resource pages for `todo`, and the Playwright smoke from `testdata/e2e/web`. | The Playwright suite passes with the Rust API. Traceparent reaches Tempo. This re-confirms the ADR 0002 evidence against axum. | `spike-check r0-09` † (the main loop, or a lane with Docker access) | `spikes-rs/r0-09-ref-web/`, `docs/design/rust-spike/r0-09-*` | R0.6, R0.7, R0.8 |
| R0.10 | **Generator approach.** In the reference app, generate a resource (`todo`) with minijinja templates. Use generator-owned `routes_gen.rs` and `handlers/mod.rs`. Compare three formatting plans: (A) templates written rustfmt-stable, (B) `prettyplease`, (C) a `rustfmt` post-step. Test the first build after a new migration (`migrate`, `prepare`, `build` order). | For each plan: rendered bytes equal the bytes on disk after `cargo fmt`. The lock hash stays stable. A second run changes nothing. `cargo fmt --check` passes on the generated app. The result names one plan. | `spike-check r0-10` | `spikes-rs/r0-10-generator/`, `docs/design/rust-spike/r0-10-*` | R0.2, R0.6 |
| R0.11 | **Dev loop latency.** Measure a handler-body edit to the first correct served response, including restart and readiness. Use an app with at least 20 generated resources, linked with the full kit feature set (R0.7). Compare: default linker, `split-debuginfo=unpacked`, `debug="line-tables-only"`, `rust-lld`, dependency `opt-level`, `bacon`, `watchexec`, own `notify` supervisor, `listenfd`. Try cranelift (nightly) as an option only. Report signature and `#[utoipa::path]` edits, spec re-emit, client regeneration, and a migration edit as separate numbers. | Median and p90 of 10 warm handler-body edits for the best configuration, on the same day as the Go+air baseline (about 1 s). **Hard no-go (a): handler-body median above 5 s.** The other edit kinds do not count toward the limit. Report them. Report the cold start. | `spike-check r0-11` (idle machine) | `spikes-rs/r0-11-devloop/`, `docs/design/rust-spike/r0-11-*` | R0.6, R0.7, R0.8, R0.10 |
| R0.12 | **Size and start baseline.** Release builds with `lto`, `codegen-units=1`, `strip`, `panic=abort`: the CLI skeleton (clap, minijinja, toml, a TLS client, zstd, three embedded archives) and the reference API. Compare with Go: `bin/spinup` is 28 MB. | Binary size, idle RSS, time to first `/healthz`, image size on distroless static (musl) and distroless cc (glibc), cold and warm image build time. | `spike-check r0-12` (idle machine) | `spikes-rs/r0-12-baseline/`, `docs/design/rust-spike/r0-12-*` | R0.4, R0.6 |
| R0.13 | **Lane build cost and disk.** Simulate the heaviest case, the R2/R3 gate. Run two lanes and the gate at the same time. Each lane builds the CLI skeleton scaled to the planned crate count (about 45), with the exact lane check commands (clippy and nextest, no `rs-build`). Add 8 generated apps (`api`, `auth`, `jobs`, `mail`, three `gen-resource` trees, one corpus app) under one shared `CARGO_TARGET_DIR`. Add one linux-musl release build through `cargo-zigbuild` and one Docker image build. Run the `rs-ref-check` workload. Compare: plain per-lane target dirs, `sccache`, and APFS clone seeds (`cp -c`). Test whether the sccache key survives worktrees at different paths. Count test binaries. | **Hard no-go (b):** peak disk of all targets, `CARGO_HOME`, `~/.cargo/registry`, and caches above 40 GB, or load average (1 min) above 14, or swap above 2 GB. The user confirmed the load and swap limits. Report cold and warm times and `du` for each profile. State how the numbers extrapolate to 50k lines. Say whether crates must merge. | `spike-check r0-13` (idle machine) | `spikes-rs/r0-13-lanes/`, `docs/design/rust-spike/r0-13-*` | R0.1, R0.4, R0.6, R0.12 (if it reuses the CLI skeleton) |
| R0.14 | **Release.** Run `goreleaser release --snapshot --clean` with the Rust builder and cargo-zigbuild for six targets (linux, darwin, windows; amd64, arm64), offline. Check `windows/arm64` (gnullvm or `cargo-xwin`). Check `cargo-auditable` with syft. Check the TLS provider and the C dependencies. Check crates.io names. | Six archives with the ADR 0012 names. Cosign and SBOM steps still run. A list of targets that need a native runner. The names `spinup-kit`, `spinup-plugin-sdk`, `spinup-cli` are free. Describe the `builder: prebuilt` fallback if the Rust builder fails. | `spike-check r0-14`, `actionlint` | `spikes-rs/r0-14-release/`, `docs/design/rust-spike/r0-14-*` | R0.4 |
| R0.15 | **Go/no-go report.** The main loop reads the 14 verdict files. It writes the decision table, the plan changes, and the updated estimates. | The report names `GO`, `GO with changes`, or `NO-GO`. It decides: sync or async, YAML crate, formatter plan, OIDC crate, TLS provider, linkme or inventory, GoReleaser or prebuilt, utoipa or aide, pack lock before publish. | review | `docs/design/rust-spike/REPORT.md` | R0.1 to R0.14 |
| R0.16 | **ADR 0019** and the updated `rust-plan.md` (this file). | ADR 0019 lists the decisions. This document is up to date. | review | `docs/adr/0019-rust-rewrite.md`, `docs/design/rust-plan.md` | R0.15 |

R0.15 and R0.16 are main-loop work (a judge call). They are not lane work.

### R0 waves (2 lanes)

| Wave | Units |
|---|---|
| 1 | R0.1, R0.2 |
| 2 | R0.3, R0.4 |
| 3 | R0.5, R0.6 |
| 4 | R0.7, R0.8 |
| 5 | R0.10, R0.14 |
| 6 | R0.9 † |
| 7 (idle machine, one at a time, in this order) | R0.12, R0.11, R0.13 |
| 8 | R0.15, R0.16 (main loop) |

### R0 gate

- All 14 result files and verdict files exist, and `spike-check.sh` passes for each.
- The two hard no-go conditions have numbers.
- The report has a decision.
- If the decision is `NO-GO`, stop. Keep the Go project. Archive the spikes.
- If the decision is `GO with changes`, edit this document before R1.
- The main loop tags `go-freeze-r0`.

## 5. Milestones R1 to R5

Unit rules are the same as `plan.md`. A unit writes only its file set. It may add test files (`tests/it/*.rs`, `#[cfg(test)]` modules) in its own sub-directories. It commits with the `orch:` prefix. A † mark means the check needs Docker, Postgres, browsers, or network. A lane runs only the static part of a † check. The main loop runs the full check at the gate. Units with † checks go to Claude lanes.

A "wave" is an ordering group. It is not a batch. Each milestone has an explicit lane schedule: two units at a time, in critical-path order.

### R1: CLI core. `spinup new` (base, api, web), `check`, `diff`, `eject`, and `upgrade` work in Rust. The neutral golden trees are byte-equal.

**R1 scope (read this first)**
- Nearly every Go golden test renders `base`+`api`+X, then selects the files of X. Most packs (`web`, `deploy`, `secrets`, `jobs`, `mail`, `tofu`, `backups`, `monitoring`) require `api`, directly or through `deploy`. So R1 renders the converted Go `api` pack, unchanged. R1 apps have a Go API. R2 replaces it.
- The `api` pack calls `gomod`, `image "golang"`, and `.Stack.GoModules`. So the R1 stack schema 2 keeps the Go sections (decision 11).
- R1 renders `.go` outputs without formatting. The Go render calls `format.Source`. R1 golden tests do not compare `.go` outputs. The lock hash covers the unformatted bytes.
- Each Rust golden test reproduces the file-selection logic of its Go test (for example `webOutput` and the `.sops.yaml` list). `rust-filter.toml` holds the selections, not only `web-embed`.
- `spinup new` in Go runs one subprocess: `git init`. So R1 needs no Go tooling.
- R1 parity is mostly golden-based. `parity.txt` lists only the scripts that are really due at R1: `version`, `harness_mkversion`, `gen_picker`, and the scripts that only need `new` with the converted `api` pack. Scripts that run `build`, `routes`, `db`, or `generate` for Go are due at R2 or later.

**Go-side units (Go lanes). They merge before the pre-seed.**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R1.0a | The harness runs a foreign binary. `SPINUP_BIN` skips the `go build` at `scripttest_test.go:59` and copies the binary as `spinup`. Conditions `[impl:go]` and `[impl:rust]`. Variables `SPINUP_PACKS_SRC` and `SPINUP_STACK_SRC` for `mkversion` (it copies both). `SPINUP_KIT_DIR`: in Rust mode, the harness rewrites `SPINUP_KIT_REPLACE` from `$SPINUP_REPO/kit` to `$SPINUP_KIT_DIR`. Pins for `CARGO_HOME` and `RUSTUP_HOME` (because `HOME=$WORK` breaks the rustup shim). `SPINUP_SCRIPT_PARALLEL`. `parity.txt` support. The Go doctest gets `SPINUP_BIN` and the two directives. The `.go` sample in the `_sample` pack becomes a non-Go file, with its golden and the Go `packs.digest` updated in the same unit. | `S version`, `S harness_mkversion`, `SPINUP_BIN=bin/spinup` with `S version`, `T ./internal/doctest/...`, `T ./internal/templates -run PacksDigest` | `internal/testutil/scripttest/`, `internal/doctest/`, `internal/templates/packs/_sample/`, `internal/templates/packs.digest`, `testdata/golden/_sample/` | none |
| R1.0b | Extend and freeze the R0.5 oracle generator. Add fixtures for `quote`, case words, semver and `compat`, `when`, `manifest.Hash`, `CleanPath`, and the `.modes` manifest format of `testutil.Golden`. | `T ./internal/oracle/...`, then `make oracle` gives no diff | `internal/oracle/`, `testdata/oracle/` (new case files only) | R0.5 |

**R1 pre-seed commit (`deps: pre-seed R1`), after R1.0a and R1.0b merge**
- Root `Cargo.toml` with all R1 crates. `[workspace.dependencies]` with exact versions and features for: clap 4.6.7, clap_complete, minijinja 3.0.3, toml, toml_edit, serde, serde_json, regex, sha2, hex, ed25519-dalek 3.0.0, tar, zstd, ureq or reqwest (per R0.4), cap-std, tempfile, walkdir, linkme, inquire, thiserror, anyhow, similar (tests only), and the YAML crate (per R0.1).
- `Cargo.lock`, `rust-toolchain.toml`, `.cargo/config.toml`, `clippy.toml` files, `deny.toml`, `.config/nextest.toml`.
- Stub `lib.rs`, stub unit `mod.rs` files, and `tests/it/main.rs` for every R1 crate. `wire_gen.rs`. `expected_registry.txt`. `cmd/mod.rs` with a stub file for each command.
- `crates/spinup-stack/stackversions.yaml` (schema 2, additive).
- `crates/spinup-tplstore/trusted_keys.txt`.
- The converted packs (`make rs-convert-packs`) and `packs.digest` (first run with `none`, then frozen).
- Contract files: `runner.rs`, `source.rs`, `merge/contract.rs`, `upgrade/contract.rs`, `migrate/contract.rs`, `gen/contract.rs`, `plugin-api/lib.rs`.
- Makefile `rs-*` targets, `tools-check` additions, and `.github/workflows/rust.yml` (fmt, clippy, nextest, and the parity subset on ubuntu, macos, and windows). Run `make rs-fetch`.
- Oracle fixtures from R1.0b.

**Wave 1 (no dependencies)**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R1.1 | `spinup-ui` and `spinup-compat`: error model, exit codes, `Try:` handling, JSON errors, `Prompter` trait. Semver, `CheckKit`, supported versions. | `RT spinup-ui spinup-compat` (oracle semver cases) | `crates/spinup-ui/src/`, `crates/spinup-compat/src/` | R1.0b |
| R1.2 | `spinup-exec`: OsRunner, process groups, cancel, streaming redaction, `ExitError`, `Fake`. | `RT spinup-exec` | `crates/spinup-exec/src/` (not `runner.rs`) | none |
| R1.3 | `spinup-vfs`: plan, `CleanPath`, apply with backups and rollback, patience diff, binary detection. | `RT spinup-vfs` (oracle diff cases, failure injection) | `crates/spinup-vfs/src/` | R1.0b |
| R1.4 | `spinup-manifest`: schema 3, canonical JSON, normalized hash, status, tombstones, orphans, atomic save. | `RT spinup-manifest` (oracle lock cases) | `crates/spinup-manifest/src/` | R1.0b |
| R1.5 | `spinup-stack` (schema 2 additive loader, strict decode, helpers, the sync test with the Go file, the `ci_pins_test` port) and `spinup-editors` (TOML, JSON, and the R0.1 YAML choice). | `RT spinup-stack spinup-editors` (codemod fixtures, ADR 0018 §6 blank-line rule) | `crates/spinup-stack/src/`, `crates/spinup-editors/src/` | R0 result |

**Wave 2**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R1.6 | `spinup-project`: `spinup.toml` model, root lookup, `ConfirmProtected`, webtool. | `RT spinup-project` | `crates/spinup-project/src/` | R1.1, R1.2, R1.4 |
| R1.7 | `spinup-packs`: loader, `pack.d`, overlay merge (with a fixture overlay), `when` evaluator, embed, validation, engine marker. The `packs_digest` test exists but is not a lane check. | `RT spinup-packs` (oracle `when` cases, overlay fixtures) | `crates/spinup-packs/src/` (not `packs/`, `overlay/`) | R1.3, R1.4, R1.5 |
| R1.8 | `spinup-merge`: `git merge-file --diff3 --diff-algorithm=histogram` through exec, git version check. | `RT spinup-merge` (real git) | `crates/spinup-merge/src/` (not `contract.rs`) | R1.1, R1.2 |
| R1.9 | `spinup-doctor`: registry, `composelint`, `plainsecrets`. | `RT spinup-doctor` (composelint cases from the Go tests) | `crates/spinup-doctor/src/` | R1.2, R1.5, R1.6 |
| R1.10 | `spinup-gen`: registry, runner (dry-run, diff, force, lock records), picker through `Prompter`. `spinup-plugin-api`: implementation of the null `PluginSource`. | `RT spinup-gen spinup-plugin-api` | `crates/spinup-gen/src/{registry,run,picker}/`, `crates/spinup-plugin-api/src/null_source/` | R1.3, R1.4, R1.6 |

**Wave 3**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R1.11 | `spinup-render`: minijinja environment (`[[ ]]` syntax, strict undefined, `keep_trailing_newline`), the 14 functions, answers validation, `modules` catalog and `Select`. | `RT spinup-render` (oracle quote and words cases) | `crates/spinup-render/src/` | R1.6, R1.7 |
| R1.12 | `spinup-tplstore`: archive reader and writer, signature check, trusted keys embedded from `trusted_keys.txt` (the build fails when the file is empty in release mode), cache, limits, `cache warm` logic, engine marker, `Try:` lines for Go-era archives. | `RT spinup-tplstore` (Go-signed archive from oracle) | `crates/spinup-tplstore/src/` (not `source.rs`) | R1.4, R1.5, R1.6, R1.7 |
| R1.13 | `spinup-migrate`: registry, `migratetest`, kit-notes parser, codemod 0001 (`sv migrate`). | `RT spinup-migrate` | `crates/spinup-migrate/src/` (not `contract.rs`) | R1.2, R1.5, R1.6, R1.7 |
| R1.14 | `spinup-testutil` (golden helper with the `.modes` format, `RepoRoot` on the workspace `Cargo.toml` marker) and golden tests for `_sample`, `base`, `secrets`, `tofu`. Owns the converter rules and the regenerated output for these four packs and for `api` (needed by all of them). | `RG _sample`, `RG base`, `RG secrets`, `RG tofu` | `crates/spinup-testutil/src/`, `crates/spinup-packs/tests/it/golden_{sample,base,secrets,tofu}.rs`, `scripts/rust/convert-templates/rules/{_sample,base,secrets,tofu,api}.go`, `crates/spinup-packs/packs/{_sample,base,secrets,tofu,api}/` | R1.11 |
| R1.15 | Golden tests for `deploy`, `backups`, `monitoring` (composelint inside; `go-runtime.json` stays unchanged). Owns the rules and output for these packs. | `RG deploy`, `RG backups`, `RG monitoring` | `crates/spinup-packs/tests/it/golden_{deploy,backups,monitoring}.rs`, `scripts/rust/convert-templates/rules/{deploy,backups,monitoring}.go`, `crates/spinup-packs/packs/{deploy,backups,monitoring}/` | R1.9, R1.11, R1.14 |
| R1.16 | Golden tests for `web`, `web-variants`, and `web-embed` (web half through `rust-filter.toml`). Owns the rules and output for `web`. | `RG web`, `RG web-variants`, `RG web-embed` | `crates/spinup-packs/tests/it/golden_web*.rs`, `scripts/rust/convert-templates/rules/web.go`, `crates/spinup-packs/packs/web/` | R1.11, R1.14 |

If a golden unit finds a defect in the converter core or in a pack it does not own, it reports `blocked: converter`. The main loop dispatches a converter-fix unit before the next schedule slot.

**Wave 4**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R1.17 | `spinup-upgrade` planner: per-file decisions, chain, `NextLock`, plan JSON, pack and plugin selection through traits, no cargo phases. The `Fix` slot is an empty phase hook in `cargo/mod.rs` (stub). | `RT spinup-upgrade :: test(plan) \| test(chain)` | `crates/spinup-upgrade/src/{plan,chain,source,lock}/` | R1.8, R1.9, R1.10, R1.11, R1.12, R1.13 |
| R1.18 | `spinup-upgrade` state machine: worktree, branch, `.spinup/upgrade.json`, `--continue`, `--abort`, exit 3, `--ui` extension, module add. | `RT spinup-upgrade :: test(run) \| test(worktree) \| test(module)` | `crates/spinup-upgrade/src/{run,state,worktree,module,ui}/` | R1.17 |
| R1.19 | `spinup-drift`: diff, check, eject, restore. | `RT spinup-drift` | `crates/spinup-drift/src/` | R1.9, R1.12, R1.17 |
| R1.20 | `spinup-newapp`: pack composition, questions to flags, lock writing, plugin seam (null `PluginSource`), `git init`, `SPINUP_KIT_REPLACE` as a `[patch.crates-io]` edit. | `RT spinup-newapp` | `crates/spinup-newapp/src/` | R1.10, R1.11, R1.12 |

**Wave 5 (wiring). Each wiring unit owns exact file names.**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R1.21 | `spinup-cli` framework: root command, global flags, groups, usage-error mapping to exit 2, `version`, `cache`, `docs compat` and `docs reference` (writes `docs/site-rs/`), completions, update notice. | `RT spinup-cli`, `RS version` | `crates/spinup-cli/src/{root,version,cache,docs}.rs`, `docs/site-rs/`, `docs/commands/{version,cache}.md` | R1.1, R1.6, R1.12 |
| R1.22 | Wiring for `new`, `upgrade`, `module`, `generate` (picker). | `RS gen_picker`, `RS new_web` | `crates/spinup-cli/src/cmd/{new,upgrade,module,generate}.rs`, `docs/commands/{new,upgrade,module}.md` | R1.18, R1.20, R1.21 |
| R1.23 | Wiring for `check`, `diff`, `eject`, `restore`. | `RS check`, `RS diff`, `RS eject` (as `parity.txt` allows) | `crates/spinup-cli/src/cmd/{check,diff,eject,restore}.rs`, `docs/commands/{check,diff,eject,restore}.md` | R1.19, R1.22 |
| R1.24 | R1 doctest blocks and directives for the engine pages. Pages that print console text use only regex-stable output. | `make rs-doctest` (R1 pages) | `docs/rust/engine.md` | R1.22, R1.23 |

**R1 lane schedule (two units at a time)**

The critical path is R1.3/R1.4/R1.5, then R1.7, R1.11, R1.12, R1.17, R1.18, R1.22. Wiring units (R1.21 to R1.24) run one at a time, because they compile the whole tree.

| Slot | Units |
|---|---|
| 1 | R1.3, R1.4 |
| 2 | R1.5, R1.1 |
| 3 | R1.2, R1.7 |
| 4 | R1.6, R1.8 |
| 5 | R1.11, R1.9 |
| 6 | R1.12, R1.10 |
| 7 | R1.13, R1.14 |
| 8 | R1.15, R1.16 |
| 9 | R1.17, R1.20 |
| 10 | R1.18, R1.19 |
| 11 to 14 | R1.21, R1.22, R1.23, R1.24 (one at a time) |

### R1 gate

On `orch/<run-id>`, run in this order:
1. `make tools-check rs-tools-check`
2. `make rs-build rs-lint rs-test rs-deny`
3. `make rs-cover` (70 percent floor for `merge`, `manifest`, `migrate`. The `upgrade` floor starts at R2, when the Rust corpus exists.)
4. `make rs-digest`
5. `make rs-golden` for the parity set in `testdata/golden/rust-parity.txt`
6. `make rs-script` for the scripts that `parity.txt` marks as due at R1
7. `make rs-doctest`
8. `make rs-convert-check` and `make oracle` give no diff
9. `make rs-vuln` †
10. The full Go gate still passes (`make build lint test golden script doctest`). The Go oracle stays green. The `rust.yml` workflow is green on three operating systems.

The R1 exit test: a Rust-only upgrade of a `base`+`api`+`web` app, from a synthetic template version `0.0.1` to `0.0.2` (made by `mkversion` with `SPINUP_PACKS_SRC` and `SPINUP_STACK_SRC`). The conflict subject is a file of the converted Go `api` pack. The test covers a conflict, `--continue`, and `--abort`.

### R2: Kit and Rust API pack. `g resource` gives a working Rust CRUD API that equals the reference app.

**R2 pre-seed commit**
- All R2 crates in the workspace: `spinup-kit`, `spinup-kit-testkit`, `spinup-db`, `spinup-build`, `spinup-appcmd`, `spinup-gen-web`, `spinup-gen-app`.
- Crates: axum 0.8.9, tokio 1.54.1 or newer, tower-http 0.7.1, sqlx 0.9.0, utoipa 6.0.0, utoipa-axum 0.3.0, tower-sessions 0.15.0, argon2 0.6.0, the OIDC crates (per R0.7), lettre 0.11.23, opentelemetry 0.33.x with tracing-opentelemetry 0.34.0, garde 0.23.0, testcontainers (exact pair), and the others from the reference app.
- The `api` pack `Cargo.toml.tmpl`, `Cargo.lock` (produced as R0.6 and R0.15 decided), and `pack.d/deps.toml`. The vendored Scalar bundle.
- `stackversions.yaml`: the rust builder image, distroless static, new Action pins (toolchain action, rust-cache), and the kit crate pin.
- Move the code of R0.6 to R0.9 to `testdata/reference/todo-rs/` (frozen).
- Empty directories in `testdata/golden-rs/`.
- A new `spinup.toml` field for the registry owner. ADR 0021 fixes its name. The Go code reads this value from the `module` line in `api/go.mod`. The Rust code reads it from `spinup.toml`.

**Rule:** when an R2 unit changes a pack, its R1 parity entry in `rust-parity.txt` moves to `golden-rs` in the same unit. The same unit updates `parity.txt` for scripts that change.

**Kit units (lane work, offline)**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R2.1 | Kit core: `app` (routes, readiness, commands `migrate`, `openapi`, `healthcheck`, `worker`, shutdown), `config` (env loader with aggregated errors, `_FILE`, dotenv rules), `secrets`, `httpx` (Problem, `ValidJson`, layers), `logx`, `deployx`. Records the `kit/api` disposition. | `RT spinup-kit` (Go test cases ported) | `crates/spinup-kit/src/{app,config,secrets,httpx,logx,deployx}/` | none |
| R2.2 | Kit `db` (pool options, `in_tx`, migrator wrapper with `up/down/status/redo/to/version` and the exact output lines) and `spinup-kit-testkit` (`#[sqlx::test]` helpers, template databases, HTTP helpers). | `RT spinup-kit spinup-kit-testkit` † | `crates/spinup-kit/src/db/`, `crates/spinup-kit-testkit/src/` | R2.1 |
| R2.3 | Kit `telemetry`: tracing setup, OTLP over HTTP, HTTP metric layer, DB span layer, log redaction layer. | `RT spinup-kit :: test(telemetry)` | `crates/spinup-kit/src/telemetry/` | R2.1 |
| R2.4 | Kit `apidocs` (Scalar routes, CSP, cache), `devtools` (`/_dev`), `embedweb` (SPA rules, no ETag). | `RT spinup-kit :: test(apidocs) \| test(devtools) \| test(spa)` | `crates/spinup-kit/src/{apidocs,devtools,spa}/` | R2.1 |
| R2.5 | Kit `auth` core: session store, password hashing with a bounded pool, `Required` layer, `AuthUser` extractor, cross-origin check, register/login/logout/session routes, `testkit::as_user`. | `RT spinup-kit :: test(auth)` † | `crates/spinup-kit/src/auth/{store,password,routes,required}/` | R2.2 |
| R2.6 | Kit rate limit and client address (ADR 0016, port of the Go table tests). | `RT spinup-kit :: test(ratelimit) \| test(clientip)` | `crates/spinup-kit/src/auth/{ratelimit,clientip}/` | R2.1, R2.3 |
| R2.7 | Kit `mail` (lettre, minijinja templates, Log and Capture mailers) and auth `reset` and `oidc`. | `RT spinup-kit :: test(mail) \| test(reset) \| test(oidc)` † | `crates/spinup-kit/src/{mail,auth/reset,auth/oidc}/` | R2.5, R2.6 |
| R2.8 | Kit `jobs`: the own queue, kit migration, worker, periodic jobs, uniqueness, metrics. The `mail.send` job runs on the queue. | `RT spinup-kit :: test(jobs)` † | `crates/spinup-kit/src/jobs/` | R2.2, R2.3 |

**CLI-side units**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R2.9 | `spinup-db`: `db migrate` (runs the app binary), `NextMigrationName` (sqlx `up/down` pairs), `db lint` (up files only), `db new/status/redo/to/reset/seed/psql/dump/restore/doctor`, `db prepare`. | `RT spinup-db` | `crates/spinup-db/src/` | R1 gate |
| R2.10 | Upgrade cargo phases: `Cargo.toml` edits with `toml_edit`, `Cargo.lock` never merged as text, kit path and `[patch]` keep rule (ADR 0018 §3), `rust-version` raise, verify (`cargo check` or `clippy` and `test`, per R0.13), placeholder kit rule. | `RT spinup-upgrade :: test(deps) \| test(apply) \| test(verify)` | `crates/spinup-upgrade/src/cargo/` | R1.18 |
| R2.11 | `spinup-gen`: Rust field types, naming rules, post-steps (`sqlx-prepare`, `openapi-emit`, `client`, `rustfmt` or none per R0.10), retry hints. | `RT spinup-gen :: test(fields) \| test(poststeps)` | `crates/spinup-gen/src/{fields,poststeps}/` | R1.10 |
| R2.12 | `spinup-gen-web`: the resource `pages` part, `nav.gen.ts`, `page`, `component`, `client`, `dashboard`, `alert`. Checked against the existing neutral goldens. | `RT spinup-gen-web`, `RG gen-resource :: test(web) \| test(paths)` | `crates/spinup-gen-web/src/` | R2.11 |
| R2.13 | `gen-app` resource core: spec parsing, ownership, reserved names, migration SQL, `Part` registry, `Next:` lines. | `RT spinup-gen-app :: test(resource)` | `crates/spinup-gen-app/src/resource_core/` | R2.9, R2.11 |
| R2.14 | `gen-app` Rust templates: handlers (axum and `#[utoipa::path]`), `ToSchema` types, store module, `routes_gen.rs`, generated test. The output equals the todo module of the reference app. | `RT spinup-gen-app :: test(handlers)`, `RG gen-resource-rs`, `make rs-ref-check` | `crates/spinup-gen-app/src/resource_rs/`, `crates/spinup-gen-app/templates/`, `testdata/golden-rs/gen-resource-rs/` | R2.13 |
| R2.15 | `gen-app` other generators: `model`, `migration`, `handler`, `job`, `mailer`, `openapi` (emit), `destroy`. | `RT spinup-gen-app :: test(model) \| test(migration) \| test(handler) \| test(job) \| test(mailer) \| test(openapi) \| test(destroy)` | `crates/spinup-gen-app/src/{model,migration,handler,job,mailer,openapi,destroy}/` | R2.13 |
| R2.16 | `spinup-build`: local build, `--dry-run`, version injection through env, kit path context, registry owner from `spinup.toml`. | `RT spinup-build` | `crates/spinup-build/src/` | R2.1 |
| R2.17 | `spinup-appcmd`: `routes` (`api routes` through the app) and `apptest` (`cargo nextest`). | `RT spinup-appcmd :: test(routes) \| test(apptest)` | `crates/spinup-appcmd/src/{routes,apptest}/` | R2.1, R2.9 |

**Pack units (overlay and Rust-only content)**

Each unit writes only its own `overlay/<pack>/` directories and its own `testdata/golden-rs/<pack>/` subtrees. No unit edits `Cargo.toml.tmpl`, `Cargo.lock`, or `pack.d/deps.toml`.

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R2.18 | `api` pack: `remove.txt` for the Go files, `rust-toolchain.toml`, `.cargo/config.toml`, `src/{main,lib,routes,openapi}.rs`, `migrations/00001_init.{up,down}.sql`, `Dockerfile.api` (cargo-chef, musl, `SQLX_OFFLINE`, kit patch context), `compose.dev.yml`, embed feature. | `RG api`, `RG api-openapi` (Rust trees) | `crates/spinup-packs/overlay/api/`, `testdata/golden-rs/{api,api-openapi}/` | R2.1 |
| R2.19 | `auth`, `jobs`, `mail` packs: SQL for sqlx, `reset.rs`, worker subcommand wiring, minijinja mail templates. | `RG auth`, `RG jobs`, `RG mail` | `crates/spinup-packs/overlay/{auth,jobs,mail}/`, `testdata/golden-rs/{auth,jobs,mail}/` | R2.5, R2.7, R2.8, R2.18 |
| R2.20 | `base` (the `module` question stays and feeds the crate slug, new registry owner field), `deploy` (drops `SPINUP_BEHAVIOR_VERSION`), and `ci` (Rust `api` job, cargo cache, `sqlx prepare --check`, spec drift check). | `RG base`, `RG deploy`, `RG ci` (Rust trees) | `crates/spinup-packs/overlay/{base,deploy,ci}/`, `testdata/golden-rs/{base,deploy,ci}/` | R2.18 |
| R2.21 | `backups` `run.sh` (`_sqlx_migrations`) and `monitoring` (tokio runtime dashboard next to the unchanged Go dashboard until R5). | `RG backups`, `RG monitoring` (Rust trees) | `crates/spinup-packs/overlay/{backups,monitoring}/`, `testdata/golden-rs/{backups,monitoring}/` | R2.18 |
| R2.22 | `web` pack overlay: `embed.toml`, `vite.config.js` (copy target for the Rust embed path), `e2e.yml` (rust toolchain, `api migrate`, `api` run), and the "Go API" comments. | `RG web`, `RG web-embed` (Rust trees) | `crates/spinup-packs/overlay/web/`, `testdata/golden-rs/{web,web-embed}/` | R2.18 |

**Wiring and scripts**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R2.23 | CLI wiring for `db`, `generate` (resource and others), `destroy`. | `RT spinup-cli`, `RS db_cmds` | `crates/spinup-cli/src/cmd/{db,generate_rs,destroy}.rs`, `docs/commands/{db,generate-*,destroy}.md` | R2.9, R2.14, R2.15 |
| R2.24 | CLI wiring for `build`, `test`, `routes`. | `RT spinup-cli`, `RS routes` | `crates/spinup-cli/src/cmd/{build,test,routes}.rs`, `docs/commands/{build,test,routes}.md` | R2.16, R2.17, R2.23 |
| R2.25 | Script twins, family `gen_*` and `destroy`. Each script is shared with `[impl:rust]` blocks, or forked to `testdata/script/rust/`. Entries in `parity.txt`. | `RS gen_*`, `RS destroy` | the named `testdata/script/gen_*.txtar` and `destroy.txtar` files, `testdata/script/rust/gen_*.txtar` | R2.20, R2.23 |
| R2.26 | Script twins, family `auth_*`. | `RS auth_*` | the named `testdata/script/auth_*.txtar` files, `testdata/script/rust/auth_*.txtar` | R2.19, R2.23 |
| R2.27 | Script twins, family `new_api`, `embed`, `db_cmds`, `routes`. | `RS new_api`, `RS embed`, `RS db_cmds`, `RS routes` | the named files, `testdata/script/rust/` | R2.22, R2.24 |
| R2.28 | Script twins, family `upgrade_*` and `module_add`. | `RS upgrade_*`, `RS module_add` | the named files, `testdata/script/rust/` | R2.10, R2.24 |
| R2.29 | E2E port. `testdata/e2e/{web,trace,monitoring}/run.sh` and `testdata/e2e/trace/app` accept `SPINUP_BIN` and build the Rust API. | `bash testdata/e2e/web/run.sh` † (static part: `shellcheck`) | `testdata/e2e/` | R2.22, R2.24 |
| R2.30 | App gate and kit-compat workflow source: generate, build, test, `/healthz`, and `app openapi` equals the committed spec. | `make rs-app-gate` † | `testdata/script/rust/app_gate_api.txtar`, `testdata/script/rust/app_gate_web.txtar` | R2.18, R2.19, R2.27 |

**R2 lane schedule.** Kit units first: (R2.1, R2.9 is blocked on R1 so use R2.1 with R2.11), (R2.2, R2.3), (R2.4, R2.5), (R2.6, R2.8), (R2.7, R2.12), (R2.9, R2.16), (R2.13, R2.17), (R2.14, R2.15), (R2.10, R2.18), (R2.19, R2.20), (R2.21, R2.22). Then R2.23, R2.24 one at a time. Then (R2.25, R2.26), (R2.27, R2.28), (R2.29, R2.30).

### R2 gate

1. `make rs-build rs-lint rs-test rs-deny rs-digest rs-golden rs-script rs-doctest rs-cover` (the `upgrade` floor of 70 percent applies now)
2. `make rs-app-gate` †: generate a full app (auth, jobs, mail), run `g resource todo`, build, test, run, and compare with `testdata/reference/todo-rs/`.
3. The e2e scripts from R2.29 †.
4. Re-measure the dev loop with the real kit and compare with the R0.11 number. The limit of 5 s for handler-body edits still applies. If it fails, stop and open an R0.11 re-run.
5. Re-measure disk use with the R0.13 commands. The 40 GB limit still applies.
6. The main loop removes the Go sections from the stack schema and runs the strict-undefined render.
7. `make rs-vuln` †. The Go gate still passes.
8. Freeze a generated app as `testdata/corpus-rs/0.1.0/` for the upgrade matrix.

### R3: Operate commands (three runs)

All three runs use shared shell scripts and the transcript oracle.

**R3.0 Go-side prep (before R3a, Go lanes). These are allowed Go refactors (decision 10).**

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R3.0a | Move every embedded VM shell script (lock, journal, probe, rotate, release-exists, read-file, metrics merge, restore wait loop, pg_upgrade helpers) to shared `.sh` files under `internal/deploy/sh/` and siblings. Both languages embed the same bytes. | `T ./internal/deploy/... ./internal/backup/... ./internal/maintain/...` | `internal/{deploy,backup,maintain}/sh/`, the Go loaders | none |
| R3.0b | Transcript oracle: record (argv, stdin, uploaded bytes) of the fake Runner for fixed scenarios: deploy happy path, resume, rollback, lock takeover, backup now/verify/restore, pg major upgrade plan. | `T ./internal/transcript/...` | `internal/transcript/`, `testdata/transcripts/` | R3.0a |

After R3.0b the main loop moves the tag `go-parity-target`.

**R3a: remote, secrets, deploy, server, image build**

Pre-seed: R3a crates, the `Remote` trait (`runner.rs`), `ssh-key`, the `make dr` target. Re-run `make rs-fetch`.

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R3a.1 | `spinup-remote`: ssh argv, `Quote`, ControlPath limit, pin files, atomic upload, `Fake`. | `RT spinup-remote` | `crates/spinup-remote/src/` (not `runner.rs`) | none |
| R3a.2 | `spinup-secrets`: sops and age wrappers, `.sops.yaml` edit, push. | `RT spinup-secrets`, `RS secrets` | `crates/spinup-secrets/src/` | R3a.1 |
| R3a.3 | `spinup-build` image part: `--image`, registry owner from `spinup.toml`, kit path context. | `RT spinup-build :: test(image)` | `crates/spinup-build/src/image/` | R2 gate |
| R3a.4 | `spinup-deploy` lock, journal, `status`, `unlock`, `rollback`, `releases`, `--plan` diff. Shared `sh/` embeds. | `RT spinup-deploy :: test(lock) \| test(journal) \| test(rollback) \| test(releases) \| test(plan)` | `crates/spinup-deploy/src/{lock,journal,rollback,releases,plan}/` | R3a.1 |
| R3a.5 | `spinup-deploy` flow: preflight, transport, upload, edge, migrate, switch, smoke, record, hooks as linkme slices. | `RT spinup-deploy :: test(flow) \| test(switch) \| test(hooks)` (transcripts) | `crates/spinup-deploy/src/{flow,switch,hooks,transport,steps}/` | R3a.2, R3a.3, R3a.4, R3.0b |
| R3a.6 | `spinup-server`: bootstrap, audit, ssh, logs, key validation. | `RT spinup-server` | `crates/spinup-server/src/` | R3a.1 |
| R3a.7 | CLI wiring: `deploy`, `rollback`, `releases`, `secrets`, `server`, `build --image`. | `RT spinup-cli`, `RS secrets` | `crates/spinup-cli/src/cmd/{deploy,rollback,releases,secrets,server,build_image}.rs`, `docs/commands/{deploy,rollback,releases,secrets,server}.md` | R3a.2, R3a.3, R3a.5, R3a.6 |
| R3a.8 | dind and dr scripts for the Rust CLI. They build the Rust app with `cargo zigbuild`. A Go-oracle copy stays. | `shellcheck testdata/dind/run.sh testdata/dind/dr/run.sh` (static), `make dind` † | `testdata/dind/{run.sh,dr/run.sh}`, `testdata/dind/fixtures/broken-readyz-rs.patch` | R3a.7 |

R3a gate: all `rs-*` targets, then `make dind` † (deploy, broken-readyz rollback, lock contention, secrets push) and `make dr` †.

**R3b: cloud, provision, infra, tools**

Pre-seed: `reqwest` or `ureq` (per R0.4), `md-5`, a DNS lookup choice (`hickory` or `std`). Re-seed `Cargo.lock` and re-run `make rs-fetch`.

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R3b.1 | `spinup-cloud`: DO client (retry matrix), `cloudauth` (PKCE loopback, token store, lock), `browser`. | `RT spinup-cloud` | `crates/spinup-cloud/src/` | none |
| R3b.2 | `spinup-dofake`: axum fake with the fidelity rules (ADR 0018 §5). Library and a binary for dind. | `RT spinup-dofake` | `crates/spinup-dofake/src/` | none |
| R3b.3 | `spinup-provision`: preflight, token, ssh key, firewall, host key and rotation, droplet, DNS, user data (YAML edit), secrets generation, handoff. | `RT spinup-provision` | `crates/spinup-provision/src/` | R3b.1, R3b.2, R3a gate |
| R3b.4 | `spinup-infra`: OpenTofu runner. `tools install/list` with checksums. | `RT spinup-infra` | `crates/spinup-infra/src/` | none |
| R3b.5 | CLI wiring for `deploy digital-ocean`, `infra`, `tools`, `destroy` (cloud). | `RS deploy_digitalocean` (the Go harness starts the Go dofake) | `crates/spinup-cli/src/cmd/{deploy_do,infra,tools}.rs`, `docs/commands/{infra,tools}.md` | R3b.3, R3b.4 |
| R3b.6 | Dind step 7 uses the Rust dofake binary. The Go copy stays. | `shellcheck` (static), `make dind` † | `testdata/dind/{run.sh,fixtures/}` (step 7 files only) | R3b.2, R3b.5 |

R3b gate: `RS deploy_digitalocean`, dind step 7 †. No live DigitalOcean test.

**R3c: backup, maintain, monitor, doctor, dev loop**

Pre-seed: `x509-parser` or `rustls`, `notify`, and the `time` or `chrono` choice. Re-seed `Cargo.lock` and re-run `make rs-fetch`.

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R3c.1 | `spinup-backup`: engine, pgBackRest, `pgdump`, `walg`, restore, schedule render, metrics, pre-deploy hook, `_sqlx_migrations` queries, `window`. | `RT spinup-backup` (transcripts) | `crates/spinup-backup/src/` (not `registry.rs`, `window.rs`) | R3a gate |
| R3c.2 | `spinup-maintain`: patch, vuln-scan, prune, vacuum, certs, `pg`, `pgupgrade`. | `RT spinup-maintain` | `crates/spinup-maintain/src/` | R3c.1 |
| R3c.3 | `spinup-monitor`: open (tunnel), status, logs, trace, alerts, silence, dashboards sync, annotation hook. | `RT spinup-monitor` | `crates/spinup-monitor/src/` | R3a gate |
| R3c.4 | `spinup-appcmd` dev loop: supervisor, ports, watcher. | `RT spinup-appcmd :: test(devloop)` | `crates/spinup-appcmd/src/devloop/` | R2 gate |
| R3c.5 | `spinup-doctor-checks`: local checks (`kit-version` from `Cargo.toml`), quick checks (Rust toolchain), `clientdrift`, spec drift, `.sqlx` drift, remote doctor. | `RT spinup-doctor-checks` | `crates/spinup-doctor-checks/src/` | R3c.1 |
| R3c.6 | CLI wiring for `backup`, `monitor`, `maintenance`, `doctor`, `dev`. | `RT spinup-cli` | `crates/spinup-cli/src/cmd/{backup,monitor,maintenance,doctor,dev}.rs`, `docs/commands/{backup,monitor,maintenance,doctor,dev}.md` | R3c.1 to R3c.5 |

R3c gate: `make dind` †, `make dr` † (disaster recovery drill with SeaweedFS), the e2e scripts †, and the monitoring e2e †.

### R4: Plugins, release, docs

**Pre-seed:** `spinup-plugin`, `spinup-plugin-sdk`, `spinup-selfupdate`, `spinup-ci`, `spinup-release` in the workspace. Release crates. GoReleaser config update. Plugin fixture sets (`testdata/plugins-jinja/`). Re-run `make rs-fetch`.

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R4.1 | `spinup-plugin-sdk`: protocol types, `serve`, bounded line reader, `conformance`. | `RT spinup-plugin-sdk` | `crates/spinup-plugin-sdk/src/` | none |
| R4.2 | `spinup-plugin` pack manager: fetch (hardened git), tree snapshot and hash, cache, allowlist, history, range grammar, `plugin.toml`, trust flags. It binds the real `PluginSource`. | `RT spinup-plugin :: test(pack)` | `crates/spinup-plugin/src/pack/` | R4.1 |
| R4.3 | `spinup-plugin` render adapter and overlay (minijinja), `rpc` host, `scaffold` (Rust `plugin new`). | `RT spinup-plugin :: test(render) \| test(rpc) \| test(scaffold)` | `crates/spinup-plugin/src/{render,rpc,scaffold}/` | R4.1, R4.2 |
| R4.4 | `spinup-selfupdate`: release resolution, verify, atomic replace, daily notice. Test seam env vars for the API and download base. | `RT spinup-selfupdate` | `crates/spinup-selfupdate/src/` | none |
| R4.5 | `spinup-ci`: `ci generate`, `ci verify` (anchors and merge keys, line numbers). | `RT spinup-ci`, `RS ci_verify` | `crates/spinup-ci/src/` | none |
| R4.6 | `spinup-release`: `check`, `templates`, `keygen`. Same asset formats. The release check fails when `trusted_keys.txt` is empty. | `RT spinup-release` | `crates/spinup-release/src/` | none |
| R4.7 | CLI wiring: `plugin`, `self-update`, `ci`, `docs`. Regenerated `docs/site-rs/` baseline. | `RT spinup-cli`, `RS plugin`, `RS plugin_render`, `RS plugin_new` | `crates/spinup-cli/src/cmd/{plugin,selfupdate,ci,docs_cmd}.rs`, `docs/site-rs/`, `docs/commands/{plugin,self-update,ci}.md` | R4.2 to R4.6 |
| R4.8 | New txtar for areas without a black-box test: `tools`, `self-update`, `docs compat`, `doctor --json`, `test --dry-run`. Added to the Go harness first as Go oracle (a Go-freeze exception: test only), then run on Rust. | `S` and `RS` for each | `testdata/script/{tools,selfupdate,docs,doctor,test}.txtar` | R4.4, R4.7 |
| R4.9 | Kit API guard: `cargo-semver-checks` or `cargo-public-api` baseline. Policy doc rewrite for the public API only (decision 12). | CI step, `docs/policy/kit-compat.md` | `docs/policy/kit-compat.md`, `crates/spinup-kit/api/` | R2 gate |
| R4.10 | Rust variants of `examples/plugin` and of the plugin fixtures `testdata/plugins/*` (the exec fixture is a prebuilt binary built from Rust). | `RS plugin`, `RS plugin_render` | `examples/plugin-rs/`, `testdata/plugins-rs/` | R4.1, R4.3 |

**Main-loop items in R4 (not lane work):** `.goreleaser.yaml`, `Dockerfile.ci`, workflows, `deny.toml`, `stackversions.yaml` pins, and the port of the inline Go programs in `bump.yml` and `shas.yml` to a `spinup-release` subcommand.

### R4 gate

1. All `rs-*` targets.
2. `goreleaser release --snapshot --clean` with the Rust builder, offline.
3. `make parity`: every script in `parity.txt` that is due passes.
4. Self-update test: the last Go build updates itself from a local fake release that holds the Rust archive (same asset names).
5. `cargo publish --dry-run` for `spinup-kit` and `spinup-plugin-sdk`. The real publish needs user approval and is not a lane action.

Before the first Rust release, `make rs-script` and the self-update test use synthetic versions from `mkversion` and the dev templates directory. The embedded archive set of the Rust store starts at the first Rust release.

### R5: Cutover

R5 starts only after `make parity` is green for every script.

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| R5.1 | `spinup-testscript`: txtar parser and the same dialect (`exec`, `!`, `stdout`, `stderr`, `grep`, `cmp`, `exists`, `env`, `cd`, `cp`, `rm`, `mkdir`, `kill`, `wait`, `defer`, `skip`, `stop`, `[env:NAME]`, `[exec:NAME]`, background `&`) and the harness commands `dofake start` and `mkversion`. | `RT spinup-testscript` (harness self-test), then all 33 scripts through it | `crates/spinup-testscript/src/`, `crates/spinup-cli/tests/it/scripts.rs` | R3b.2 |
| R5.2 | `spinup-doctest`: parser, matcher, runner. Pages that describe the Go layout are rewritten. | `RT spinup-doctest`, `make rs-doctest` | `crates/spinup-doctest/src/`, `docs/rust/` | R5.1 |
| R5.3 | dind and dr scripts use the Rust dofake and cargo-zigbuild. Remove the Go-oracle copies. | `make dind` †, `make dr` † | `testdata/dind/` | R5.1 |
| R5.4 | Docs sweep: every page, the ADR index, `README`, `CONTRIBUTING`, `docs/policy/windows.md`, `docs/contributing/{release,upgrade-matrix,dind,v1-checklist}.md`. These pages quote Go commands and must stay valid for Go until now. | `make rs-doctest` | `docs/`, `README.md` | R5.2 |
| R5.5 | Delete Go (main loop). See the deletion list below. Rename `rs-*` targets to the plain names. Rewrite `CLAUDE.md` and the workflows. Move the pack overlay into the final pack tree. Remove the converter, the `rs-convert-*` targets, and `make oracle`. Swap `docs/site-rs/` into `docs/site/`. | `scripts/rust/no-go-check.sh`, then `make tools-check build lint test golden script doctest app-gate` on a Rust-only tree | many (main loop) | R5.1 to R5.4 |
| R5.6 | First Rust release candidate: `goreleaser --snapshot`, and the self-update test from the last Go release. | Run the R4 gate again. `make parity` is no longer needed. | release files | R5.5 |

**R5.5 deletion list.** Delete these Go items and the files that go with them:
- `cmd/`, `internal/` (including `internal/deps` and the Go oracle and transcript code), `kit/`, `plugin-sdk/`, `go.work`, `go.work.sum`, `go.mod`, `go.sum`, `.golangci.yml`
- Go scripts, the Go converter, the Go-only trees in `testdata/golden`, and the Go `api` pack files with `testdata/golden/api`
- `examples/plugin`, `testdata/plugins/exec-hello/src/go.mod` and its Go source
- `spikes/bluegreen/app/go.mod` and the Go app
- `testdata/dind/fixtures/broken-readyz.patch`, `testdata/dind/dofake/main.go`
- `testdata/corpus/0.0.0-dev/api` and `testdata/corpus/locks/{v1,v2}.json`
- `internal/plugin/scaffold/templates/go.mod.txt`
- `testdata/e2e/{web,trace,monitoring}/run.sh` lines that build `./cmd/spinup` and `./cmd/api`
- the Go sources of `scripts/release/{check,keygen,templates}`
- `scripts/ci/{warm-cache.sh,windows-packages.sh,windows-excluded.txt}` (rewrite for cargo)
- the Go stage of `Dockerfile.ci`
- the `kit/` references in `release.yml` (`kit/go.sum`, `kit/$RELEASE_TAG`)
- `docs/policy/kit-compat.md` Go text

The script `scripts/rust/no-go-check.sh` replaces the old grep. It runs `git ls-files | grep -E '\.go(\.tmpl|\.txt)?$|go\.(mod|sum|work)'` and a content grep for `go build|go test|go mod|golangci|setup-go` outside `docs/adr`. Both must give no output.

### R5 gate

- `no-go-check.sh` gives no output.
- `make tools-check build lint vuln test golden script doctest app-gate e2e dind dr` all pass (unprefixed, now cargo).
- A fresh clone builds with `make build` and `CARGO_NET_OFFLINE=true` after `make fetch`.
- The user reviews the branch and merges it.

## 6. CI workflows

The main loop adds `rust.yml` in the R1 pre-seed. Each workflow has one disposition. ADR 0020 holds the complete table. The main loop fills any workflow that this table does not name from `ls .github/workflows`.

| Workflow | R1 to R4 | R5 |
|---|---|---|
| `rust.yml` (new) | fmt, clippy, nextest, parity subset on ubuntu, macos, windows | becomes `ci.yml` |
| `kit-compat.yml` | keep for Go | rewrite for the Rust kit (R4.9) |
| `upgrade-matrix.yml` | keep for Go | rewrite for `testdata/corpus-rs/` |
| `canary-*.yml` | keep for Go | rewrite for cargo pins |
| `dind.yml`, `dr.yml` | keep for Go, add Rust jobs at R3a | rewrite |
| `monitoring.yml`, `nightly.yml` | keep for Go, add Rust jobs at R3c | rewrite |
| `bump.yml`, `shas.yml` | keep; main loop ports the inline Go to `spinup-release` in R4 | rewrite |
| `release.yml` | rewrite at R4 for the Rust builder | keep |
| `.github/actions/prepare-tests` | keep | rewrite for cargo |

## 7. ADR changes

New ADRs supersede the named sections. The old ADR text stays. Each old section gets a one-line note: "Superseded by ADR 00NN".

| New ADR | Written at | Supersedes (old ADR and section) |
|---|---|---|
| **0019 Rust rewrite** | R0.16 | 0001 Decision bullets 1 to 3 and Consequences. 0003 Images and health check. States the user decisions and the R0 results. |
| **0020 R1 contracts** | start of R1 | 0006 §1 (contract files in Rust), §2 (lock schema 3, fields `formatter`, `kit.crate`, `engine`), §3 (engine marker, minijinja packs, stack schema 2 additive, archives restart at the first Rust release), §5 (linkme registration, no Go editor). 0005 §3 (doctest uses `SPINUP_BIN`). 0013 §4 (minijinja plugin packs). 0012 §2 (`TrustedKeys` in Rust). Also: sync or async, error text mapper, the parity rules and `parity.txt`, regex-level console parity, oracle fixtures and the `.modes` format, converted-pack and overlay rules, the `--module` flag name, the workflow table, `docs/site-rs/`. |
| **0021 R2 contracts** | start of R2 | 0005 §1 (`db migrate` runs the Rust binary), §2 (Cargo files, kit pin, path or patch), 0007 §1 to §6, 0008 §3 to §6, 0010 §1 and §2, 0013 §2 and §3, 0014 §2, 0015 §1 and §2, 0016 §3 and §4, 0018 §1 to §3. Also: the wire contract (schema names, operationIds, Problem, pagination), the formatter decision, the OpenAPI 3.1 choice, the kit package dispositions, the registry owner field, no behavior-version gates. |
| **0022 R3 contracts** | start of R3a | 0009 §1 to §4, 0011 §2, §3, §6 (table `_sqlx_migrations`), 0014 §6 and §7, 0017 §2, §3, §6, §10, 0018 §4 to §7. Also: shared shell scripts and the transcript oracle. |
| **0023 R4 contracts** | start of R4 | 0014 §1, §5, §8 (docs site baseline, field `rust`), 0012 §1 to §4 (Rust builder, `cargo-auditable`), 0013 §4 (final plugin syntax). Also: kit API guard, crates.io alpha publish rule. |
| **0024 Cutover** | start of R5 | The Go freeze, the deletion list, the new `Makefile` and `CLAUDE.md` contract, the first Rust release rules. |

`docs/design/architecture.md` §6.2, §7, §9 step 8, §11, §12 get a header note: "Go details superseded by ADR 0019 to 0021".

## 8. CLAUDE.md changes by stage

The main loop makes each change. Keep the style of the current file.

| Stage | Change |
|---|---|
| **R0** | Add a pointer to `docs/design/rust-plan.md` and ADR 0019. Add the rule that R0 spike lanes are offline with pre-seeded spike workspaces (decision 5). Add the Rust row to the toolchain table. Add the rule that spike code stays in `spikes-rs/`. Add the Go freeze rule (decision 10). |
| **R1** | Add the `rs-*` commands and the check forms `RT`, `RG`, `RS`. Add the Rust lane environment (offline, `--locked`, per-lane target dir, `blocked: <crate>` rule). Add the Rust shared files list (section 2). Rewrite the Registration rule: linkme slices plus `wire_gen.rs`. Rewrite the Layering rule with the crate rules and the `clippy.toml` bans. Replace the No CGO rule. Add the rules: Go-era formats are rejected, converted packs are generated, overlay merge, oracle fixtures, console text is regex-equal. Update the "Pack changes" rule for the Rust digest. Keep every Go rule until R5. |
| **R2** | Add the kit rules: features, the wire contract, metric names, the migration style, and the app CLI contract (`migrate`, `openapi`, `healthcheck`, `worker`). Add the Rust API pack file classes. Replace the Corrections lines about Go versions. Add ADR 0021. Update the Docs rule for Rust output. |
| **R3** | Add the shared-script rule (`sh/` files used by both languages) and the transcript oracle. Add the Remote trait contract file. Add ADR 0022. Add the R3 run split. |
| **R4** | Add the release rules for the Rust builder and crates.io (every publish needs user approval). Add the plugin template syntax. Add ADR 0023. |
| **R5** | Remove all Go content: toolchain row, commands, `T/S/G` forms, Go lane variables, Go shared files, Go contract files, the dual-run `parity` rule. Rename `rs-*` to plain names. Replace "Go" with "Rust" in the first paragraph. Add ADR 0024. Update the memory notes (the dogfood app and Go cache notes). |

## 9. GitHub issue set

**Labels:** `rust-rewrite`, `epic`, `milestone`, `spike`, `hard-no-go`, `soft-risk`, `blocked-on-user`.
**Issues:** 1 epic, 6 milestone issues (R0 to R5), 14 spike issues (R0.1 to R0.14). Total 21. The R3 issue has three checklist items (R3a, R3b, R3c). The full bodies are in the "Issue set" section of the report.

## 10. Top risks and mitigations

| # | Risk | Mitigation |
|---|---|---|
| 1 | **Dev loop too slow** with axum, sqlx macros, and utoipa (hard no-go a). | R0.11 measures it on a 20-resource app with the full kit. Options: `split-debuginfo`, `line-tables-only`, dependency `opt-level`, `bacon` or `watchexec`, `listenfd`. Re-measure at the R2 gate. If it fails, stop. |
| 2 | **Lane builds too heavy** (hard no-go b). About 45 crates, many test binaries, C code in zstd and ring, up to 8 generated apps in the gate. | R0.13 simulates the gate with the exact lane commands. `CARGO_BUILD_JOBS=4`, nice, max 2 lanes, serial gate. Lanes run clippy and nextest only. Per-lane target dirs deleted after merge. One test binary per crate. Merge small crates if the numbers say so. |
| 3 | **Toolchain drift.** The Mac default is 1.91.1. sqlx 0.9 needs 1.94. Clippy lints change. | Pin 1.99.1 in `rust-toolchain.toml`. Bump only in a dedicated commit. Lanes cannot install toolchains. |
| 4 | **minijinja byte-equality and 3.0 churn.** Whitespace rules and `[[ ]]` clashes. | Go converter on the real syntax tree. Custom filters. Pin 3.0.3. Soft risk: if under 99 percent, the plan accepts a documented list of differences and new golden files for those packs. |
| 5 | **YAML editing has no mature crate.** | R0.1 tests three options. Fallback: hand-written splice editor. The write surface is small (six call sites). Do not promise byte-equality with go-yaml for inputs outside its style. |
| 6 | **Own job queue.** At-least-once, crash recovery, periodic jobs, lost `NOTIFY`. | River schema as the reference. Rescuer and poll fallback. Chaos test in R0.8 and again in R2.8. Fallback: `graphile_worker`. |
| 7 | **Dependency health.** `openidconnect` is old and pulls `rsa` (RUSTSEC-2023-0071). The official tower-sessions sqlx store is stale. OpenTelemetry crates break every few months. | Own `SessionStore`. R0.7 chooses the OIDC crate and records the advisory ignore in `deny.toml`. Pin all OpenTelemetry crates together. Check release dates at each pre-seed (the 14-day rule). |
| 8 | **Code-first OpenAPI drift.** utoipa 6 emits OpenAPI 3.1. Client output changes. The spec needs a compiled app. | R0.6 diffs both clients. A golden for the emitted `bundle.gen.yaml` comes before any fragment golden. Keep aide as a fallback. ADR 0021 holds the wire contract. |
| 9 | **Formatting breaks the lock hash.** There is no in-process rustfmt. | R0.10 decides. Default: rustfmt-stable templates plus a `rustfmt --check` golden. Otherwise a `rustfmt` post-step with the hash taken after formatting. |
| 10 | **Parity is weaker than it sounds.** Scripts compare console text by regex. Cobra and clap text differ. zstd bytes differ. Go-only scripts cannot be shared. | Files and goldens are byte-equal. Console text is regex-equal. The docs site gets a new baseline. Archives compare by decoded tar. Forked scripts use `[impl:rust]`. Oracle fixtures cover the algorithms. ADR 0020 states this. |
| 11 | **Release builds for six targets.** The GoReleaser Rust builder is young. Windows arm64 is not in its default list. | R0.14 tests all six targets. Fallback: `builder: prebuilt` with a cargo matrix. Native Windows runner if needed. `cargo-auditable` for SBOMs. |
| 12 | **R3 size** (about 19k Go lines plus tests). | Three runs. Shared shell scripts. The transcript oracle makes the orchestration logic byte-checkable without a VM. |
| 13 | **Go freeze costs.** Every Go fix needs a port. | Each Go fix adds a txtar or golden case. Neutral packs are converted by script, so pack fixes flow to Rust through `make rs-convert-packs`. Only four Go-side units may refactor Go. |
| 14 | **linkme drops crates that nothing references.** | `wire_gen.rs` plus the registry ID test. R0.3 proves it on three operating systems. Fallback: `inventory`. |
| 15 | **Unexpected approvals.** Publishing to crates.io and the merge of each branch need the user. | The epic lists them. Lanes never publish, tag, or merge. |
| 16 | **Converter and pack defects found late.** | Golden units own their converter rules. A converter-core defect gives `blocked: converter`. The main loop dispatches a fix before the next slot. |
| 17 | **Lockfile fails under `[patch]`.** A registry-made `Cargo.lock` may fail `--locked` with a path kit. | R0.6 tests it. The pack lock uses a path kit until the kit is published. The pre-seed after publish regenerates it. |
| 18 | **Go leftovers at cutover.** | The R5.5 deletion list and `no-go-check.sh`. |

## 11. Answers to the open questions (2026-10-24)

1. Lock schema 3: accepted (decision 1). The project is pre-v1, so there is no compat path.
2. The R0 network exception: rejected by the user. Spike workspaces are pre-seeded and lanes stay offline (decision 5).
3. Three runs for R3: accepted.
4. The converted-pack and overlay model: accepted.
5. Issue count: the user accepted 21 issues (the epic, 6 milestones, and 14 spikes).
6. No `SPINUP_BEHAVIOR_VERSION` in the Rust kit: accepted (decision 12).
7. The Go freeze tag at the R0 gate with four allowed refactor units: accepted (decision 10).
8. The hard no-go limits (40 GB disk, load average 14, swap 2 GB): confirmed by the user.

---

# Appendix A: Issue set

## Epic

**Title:** Rewrite spinup in Rust (CLI, kit, plugin SDK, generated API)

**Body:**

Goal: Rewrite the CLI, the `kit` library, and the plugin SDK in Rust. The generated API changes from Go to Rust. The web app (SvelteKit), PostgreSQL, Docker Compose deploy, and OpenTelemetry with Grafana LGTM do not change.

Plan: `docs/design/rust-plan.md`. Decisions: the 17 user decisions and the 14 plan decisions in that file.

Rules:
- Use `/orchestrate`. One run for each milestone. R3 has three runs (R3a, R3b, R3c).
- Go stays as the parity oracle until R5. The Go freeze tag is `go-freeze-r0`. Go gets bug fixes only, and each fix adds a txtar or golden case. Four Go-side units may refactor Go: R1.0a, R1.0b, R3.0a, R3.0b.
- There is no migration path for Go apps.
- Parity means: files and goldens are byte-equal. Console text is regex-equal. Archives compare by decoded contents.

Hard no-go conditions:
- (a) A handler-body edit to a served response takes more than about 5 s warm.
- (b) Two Rust lanes plus the gate overload the Mac, or use more than about 40 GB of disk. The measures are load average above 14 and swap above 2 GB (confirmed by the user).

Soft risks (the plan changes, work continues): minijinja byte-equality, comment-preserving YAML editing.

Checklist:
- [ ] R0 Spike and go/no-go
- [ ] R1 CLI core
- [ ] R2 Kit, Rust API pack, and generators
- [ ] R3 Operate commands (R3a, R3b, R3c)
- [ ] R4 Plugins, release, and docs
- [ ] R5 Cutover

Approvals needed from the user: each crates.io publish, each push, tag, and release. Lock schema 3 and the no-go limits are already accepted.

**Acceptance criteria:**
- All six milestone issues are closed.
- The R5 gate passes.
- The repository has no Go code. `scripts/rust/no-go-check.sh` gives no output.
- If the R0 decision is `NO-GO`, close the epic with `REPORT.md` linked.

## Milestone issues

### R0 Spike and go/no-go

**Body:** Answer 14 questions with numbers. Child issues: R0.1 to R0.14. The main loop writes the report (R0.15) and ADR 0019 (R0.16). Time box: 3 weeks. The waves and the offline spike workspaces are in section 4 of the plan.

**Acceptance criteria:**
- 14 result files and 14 `verdict.toml` files exist. `spike-check.sh` passes for each.
- `docs/design/rust-spike/REPORT.md` names `GO`, `GO with changes`, or `NO-GO`.
- Both hard no-go conditions have numbers.
- ADR 0019 is merged. `rust-plan.md` is updated.
- The tag `go-freeze-r0` exists.
- If `NO-GO`: the epic is closed with the report.

### R1 CLI core

**Body:** Build the Rust engine: render, vfs, merge, lock, upgrade, drift, newapp, and the commands `new`, `upgrade`, `module`, `generate` (picker), `check`, `diff`, `eject`, `restore`. R1 renders the converted Go `api` pack. Go-side units R1.0a and R1.0b run first.

**Acceptance criteria:**
- All R1 units are merged. The R1 gate passes.
- Golden trees for `_sample`, `base`, `secrets`, `tofu`, `deploy`, `backups`, `monitoring`, `web`, `web-variants`, and the web half of `web-embed` are byte-equal with Go.
- The scripts that `parity.txt` marks due at R1 pass with `SPINUP_BIN`. Console text is regex-equal.
- The exit test passes: a Rust-only upgrade of a `base`+`api`+`web` app with a conflict, `--continue`, and `--abort`.
- The coverage floor holds for `merge`, `manifest`, `migrate`.
- `rust.yml` is green on ubuntu, macos, and windows. The Go gate is green.
- ADR 0020 is merged.

### R2 Kit, Rust API pack, and generators

**Body:** Build the `spinup-kit` crate, the Rust `api`, `auth`, `jobs`, `mail` packs, the overlays for other packs, the Rust-aware generators, and the commands `db`, `generate`, `destroy`, `build`, `test`, `routes`.

**Acceptance criteria:**
- Kit features are complete: app, config, db, httpx, logx, telemetry, auth, oidc, mail, jobs, apidocs, devtools, embedweb.
- `spinup new` creates a Rust app. `g resource todo` output equals `testdata/reference/todo-rs/`.
- `make rs-app-gate` and the e2e scripts pass.
- The wire contract holds against the SvelteKit pages.
- Handler-body dev loop median is at most 5 s. Disk use is at most 40 GB.
- The `upgrade` coverage floor holds. `testdata/corpus-rs/0.1.0/` exists.
- The Go sections are removed from the stack schema.
- ADR 0021 is merged.

### R3 Operate commands (R3a, R3b, R3c)

**Body:** Build `deploy`, `rollback`, `releases`, `secrets`, `server`, `deploy digital-ocean`, `infra`, `tools`, `backup`, `monitor`, `maintenance`, `doctor`, and `dev`. Go-side prep R3.0a and R3.0b run first. Checklist: R3a (remote, secrets, deploy, server, image), R3b (cloud, provision, infra), R3c (backup, maintain, monitor, doctor, dev loop).

**Acceptance criteria:**
- All operate commands work in Rust.
- Transcript oracle cases are equal. Both languages embed the same shell scripts.
- `make dind` and `make dr` pass with the Rust CLI.
- `deploy_digitalocean` passes with the Rust CLI and the Go dofake.
- The R3a, R3b, and R3c gates pass.
- ADR 0022 is merged.

### R4 Plugins, release, and docs

**Body:** Build the Rust plugin SDK, the plugin manager, self-update, `ci`, the release tools, the kit API guard, and the Rust release pipeline.

**Acceptance criteria:**
- The Rust SDK and conformance kit work. `plugin new` scaffolds Rust.
- Self-update from the last Go build to a Rust archive works.
- `goreleaser release --snapshot --clean` passes offline.
- `docs/site-rs/` baseline is regenerated.
- The kit API guard runs in CI.
- `cargo publish --dry-run` passes for `spinup-kit` and `spinup-plugin-sdk`. The real publish waits for user approval.
- ADR 0023 is merged.

### R5 Cutover

**Body:** Port the testscript and doctest harnesses to Rust. Delete all Go code. Rename the Makefile targets. Rewrite `CLAUDE.md`.

**Acceptance criteria:**
- The testscript and doctest harnesses are in Rust. All 33 scripts pass through the Rust harness.
- `scripts/rust/no-go-check.sh` gives no output.
- Unprefixed `make` targets use cargo. `CLAUDE.md` is rewritten.
- The first Rust release candidate builds. The self-update test from the last Go release passes.
- ADR 0024 is merged. The user reviews and merges the branch.

## Spike issues

Each spike issue has the labels `rust-rewrite` and `spike`. The result goes to `docs/design/rust-spike/r0-NN-<name>.md` with a `verdict.toml`. The check for every spike is `scripts/rust/spike-check.sh r0-NN`.

### R0.1 YAML editing
**Body:** Compare `yamlpath`+`yamlpatch`, `yaml-edit`, and a splice editor on `tree-sitter-yaml`. Cover the six Go call sites. Label `soft-risk`.
**Acceptance criteria:**
- At least 25 fixtures run against three candidates.
- One candidate passes 100 percent of the five real call sites, with comments kept.
- The result states the C dependency cost.
- If none passes: the plan adds a hand-written splice editor (about 300 lines) to R1.5.

### R0.2 minijinja conversion
**Body:** Write the Go converter. Convert 74 templates and the inline templates. Label `soft-risk`.
**Acceptance criteria:**
- All 74 templates and the inline templates compile.
- `base`, `web`, and `web-variants` are 100 percent byte-equal. All neutral packs are at least 99 percent.
- Each unequal file has a cause. The `[[`/`]]` clashes are listed.
- The delimiter choice is final.

### R0.3 linkme and the wire list
**Body:** Prove registration of crates that the binary does not reference.
**Acceptance criteria:**
- The registered ID set test passes on macOS, Linux, and Windows, in debug, release, and LTO.
- The dropped-crate case is shown and fixed with `use x as _;`. The test fails when a wire line is removed.
- The `inventory` fallback is tested.

### R0.4 Sync or async
**Body:** Build the same CLI core in sync and tokio forms.
**Acceptance criteria:**
- Binary size, build time, and glue size are compared.
- The cancelable runner, the redactor, the lock-refresh task, and the TLS provider check on six targets are done.
- One decision is recorded for the `Runner` traits.

### R0.5 Format interop
**Body:** Prove lock JSON, archive, signature, diff, and `cap-std` parity with Go. Create the Go oracle generator.
**Acceptance criteria:**
- 100 percent of oracle lock cases and 200 diff cases are equal.
- A Go-signed archive verifies and decodes. A deterministic tar+zstd is written.
- `cap-std` results equal the Go `vfs` regression tests on macOS, Linux, and Windows.

### R0.6 Reference app A (API and OpenAPI)
**Body:** Hand-write the todo API with axum, utoipa, and sqlx.
**Acceptance criteria:**
- The spec has the same operationIds, schema names, status codes, and `application/problem+json` on 401, 404, and 422.
- Both generated clients (openapi-typescript and heyapi) differ from the Go golden only in a listed set of lines.
- `nest()` and several methods on one path work. `cargo sqlx prepare --check` passes.
- `cargo build --locked --offline` passes with a `[patch.crates-io]` path kit, or the result states how the pack lock is made before publish.

### R0.7 Reference app B (auth)
**Body:** Add sessions, argon2id, lockout, cross-origin check, rate limiter, password reset, and OIDC.
**Acceptance criteria:**
- The cookie contract of ADR 0008 §3 holds.
- The Go rate-limit table passes unchanged. Error texts equal `auth_ratelimit.txtar`.
- One OIDC crate is chosen, with a `cargo deny` result and the `RUSTSEC-2023-0071` handling.

### R0.8 Reference app C (jobs and OTel)
**Body:** Build the own job queue and the telemetry layers.
**Acceptance criteria:**
- The chaos test kills the worker 50 times during 10 000 jobs. No job is lost. Retry follows the backoff.
- Metric names match the monitoring pack.
- The Grafana RED panel shows data †.

### R0.9 Reference app D (web)
**Body:** Run the existing SvelteKit web pack against the reference API. The main loop, or a lane with Docker access, runs this unit †.
**Acceptance criteria:**
- The Playwright suite passes with the Rust API.
- Traceparent reaches Tempo.
- The ADR 0002 evidence is re-confirmed against axum.

### R0.10 Generator approach and formatter
**Body:** Generate a resource with minijinja and compare three formatting plans.
**Acceptance criteria:**
- Three plans (rustfmt-stable templates, `prettyplease`, `rustfmt` post-step) are compared. One is chosen.
- The lock hash is stable. A second run changes nothing. `cargo fmt --check` passes on the generated app.
- The first build after a new migration works with the documented order.

### R0.11 Dev loop latency
**Body:** Measure the handler-body edit to the first correct served response on a 20-resource app with the full kit. Label `hard-no-go`.
**Acceptance criteria:**
- Median and p90 of 10 warm edits are recorded for the best configuration, on the same day as the Go+air baseline.
- Hard no-go if the handler-body median is above 5 s.
- Signature edits, spec re-emit, client regeneration, migration edits, and cold start are reported as separate numbers.

### R0.12 Size and start baseline
**Body:** Measure release size, RSS, start time, and image size for the CLI and the API.
**Acceptance criteria:**
- Sizes, RSS, start time, and image sizes (musl and glibc) are recorded.
- The comparison with Go (28 MB CLI) is stated.
- A static musl image works with `read_only` and `healthcheck`.

### R0.13 Lane build cost and disk
**Body:** Simulate two lanes plus the gate with the exact lane commands. Label `hard-no-go`.
**Acceptance criteria:**
- The workload includes the scaled CLI skeleton, 8 generated apps, a musl release build, and a Docker image build.
- Hard no-go if peak disk is above 40 GB, load average is above 14, or swap is above 2 GB.
- Plain target dirs, `sccache`, and clone seeds are compared. The sccache key across worktree paths is tested.
- The result states the extrapolation to 50k lines and the crate merge advice.

### R0.14 Release
**Body:** Build six targets with the Rust builder, offline.
**Acceptance criteria:**
- Six archives with ADR 0012 names build, or the `builder: prebuilt` fallback is shown.
- The `windows/arm64` answer, the SBOM answer, and the TLS provider answer are recorded.
- The crates.io names `spinup-kit`, `spinup-plugin-sdk`, and `spinup-cli` are checked.

---

# Appendix B: Rejected critiques of the draft

1. **Add an R1 stub `api` pack with its own tree `golden-rs/api-stub` (second critique, item 1).** Rejected because the converted Go `api` pack gives byte-exact goldens for every pack that requires it. A stub needs a second tree and does not test the real pack chain. The accepted fix is the additive stack schema (first critique, item 1).
2. **Build a small R1 corpus from the stub `api` pack for the `upgrade` coverage floor (second critique, item 2).** Rejected because there is no stub pack. The accepted fix defers the `upgrade` floor to R2, when `testdata/corpus-rs/` exists.
3. **Move the docs sweep for `docs/policy/windows.md` and `docs/contributing/*` before R5 (second critique, item 30).** Rejected in timing. Go still ships until R5, so these pages must stay valid for Go. The plan now names the pages in R5.4.
4. **Make `kit/api` a fixed disposition (second critique, item 15).** Not decided. The critique names the gap but not the right answer, and I did not verify the content of `kit/api`. R2.1 records the disposition in ADR 0021, with "fold into `httpx`" as the default.
