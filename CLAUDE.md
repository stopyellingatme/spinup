# spinup

spinup is a Buffalo-style CLI. It creates and operates a full stack: a Go API, a SvelteKit web app, PostgreSQL, a Docker Compose deploy to a VM, and OpenTelemetry monitoring with Grafana LGTM. Generated apps must stay easy to upgrade for years.

- CLI module: `github.com/stopyellingatme/spinup` (repository root)
- Runtime library ("kit"): `github.com/stopyellingatme/spinup/kit` (directory `kit/`, its own `go.mod`)
- Plugin SDK: `github.com/stopyellingatme/spinup/plugin-sdk` (directory `plugin-sdk/`, its own `go.mod`, ADR 0014 §1)
- `go.work` is committed. It joins the root module, `kit/`, and `plugin-sdk/`.

Read these before you change anything:
- `docs/design/plan.md`: the approved plan, the milestone unit graphs, and the critic resolutions. The critic resolutions override `architecture.md`.
- `docs/design/architecture.md`: the full design (command tree, layouts, lock format, upgrade algorithm).
- `docs/adr/`: decisions made after the design.

## Rust rewrite (in progress)

The CLI, the kit, the plugin SDK, and the generated API move to Rust. The plan is `docs/design/rust-plan.md`. It overrides `plan.md` for Rust work. The epic is the GitHub issue labeled `epic` and `rust-rewrite`.
- R0 is a spike with a go/no-go report. Spike code stays in `spikes-rs/r0-NN-<name>/`, each with its own workspace.
- The R0 decision is `GO with changes` (`docs/design/rust-spike/REPORT.md`). ADR 0019 (`docs/adr/0019-rust-rewrite.md`) records the decisions, the contract changes for Rust apps, and the Rust lane settings (`CARGO_BUILD_JOBS=2` per lane, no shared sccache).
- R0 spike lanes are offline too. The main loop pre-seeds each spike workspace with its candidate crates and runs `cargo fetch --locked`. A lane builds with `CARGO_NET_OFFLINE=true` and `--locked`. If it needs one more crate, it reports `blocked: <crate>`.
- Rust 1.99.1 (edition 2024) is pinned in `rust-toolchain.toml`. Lanes set `RUSTUP_TOOLCHAIN=1.99.1` and never install a toolchain.
- After the R0 gate (tag `go-freeze-r0`), Go gets bug fixes only. Each fix adds a txtar or golden case. Only the units R1.0a, R1.0b, R3.0a, and R3.0b may refactor Go.
- The Cargo workspace is the root `Cargo.toml`, with the crates in `crates/<name>/`. The R1 contracts (the contract files and seam types, lock schema 3, the engine marker, converted packs and overlays, stack schema 2, the plugin seam, registration and layering, the error text mapper, parity) are in `docs/adr/0020-r1-contracts.md`.

## Corrections to architecture.md (Step 0 verification, 2026-10-13)

`internal/stackversions/stackversions.yaml` is the only source for versions. It overrides the pins in `architecture.md` §3. These corrections matter most:
- Prompts use `charm.land/huh/v2`, and the TUI uses `charm.land/bubbletea/v2`. Do not use the `github.com/charmbracelet/...` paths.
- YAML editing uses `go.yaml.in/yaml/v3` (Node API). `gopkg.in/yaml.v3` is archived.
- TypeScript is 6.x. TypeScript 7 breaks the SvelteKit 3 peer ranges.
- The dev monitoring image is `grafana/otel-lgtm`, not `grafana/docker-otel-lgtm`.
- MinIO is archived. Tests use SeaweedFS for S3.
- cAdvisor is `ghcr.io/google/cadvisor`.
- The SvelteKit rules are in `docs/adr/0002-sveltekit-version.md`. The backup backend decision is in `docs/adr/0004-backup-backend.md`.
- The M1 contracts (`db migrate` through the app binary, the API lockfiles, the doc-test format) are in `docs/adr/0005-m1-contracts.md`.
- The M2 contracts (upgrade engine types, lock schema 2, template versions and archives, the upgrade worktree) are in `docs/adr/0006-m2-contracts.md`.
- The M3 contracts (generators, post-steps, the OpenAPI and sqlc layout, migrations, jobs) are in `docs/adr/0007-m3-contracts.md`.
- The M4 contracts (pnpm through corepack, the web layout, session and auth API, client generation, the dev loop) are in `docs/adr/0008-m4-contracts.md`.
- The M5 contracts (ssh access, the VM layout, the deploy lock and journal, hooks, image transport, secrets, production Compose, the test VM) are in `docs/adr/0009-m5-contracts.md`.
- The M6 contracts (telemetry configuration, the monitoring pack and profiles, monitor commands, web tracing) are in `docs/adr/0010-m6-contracts.md`.
- The M7 contracts (backup configuration, backends, in-place restore, backup metrics, the schedule, maintenance, the remote doctor, test storage) are in `docs/adr/0011-m7-contracts.md`.
- The M8 contracts (release assets, versions and signing, installing spinup, generated workflows, `spinup ci`) are in `docs/adr/0012-m8-contracts.md`.
- The M9 contracts (modules, mail and password reset, OIDC, pack plugins) are in `docs/adr/0013-m9-contracts.md`.
- The M10 contracts (tier-2 plugins and the plugin SDK, embed mode, bun and heyapi, OpenTofu, self-update, WAL-G, in-place pg_upgrade, the docs site) are in `docs/adr/0014-m10-contracts.md`.
- The M12 contracts (resource ownership, the API reference, navigation and home, the sign-in flow, resource pages with superforms) are in `docs/adr/0015-m12-contracts.md`. The vendored Scalar bundle `kit/apidocs/assets/` is a shared file (main loop only).
- The auth rate limit contract (endpoints, classes, client addresses, trusted proxies, the 429 answer) is in `docs/adr/0016-auth-rate-limit.md`.
- The DigitalOcean deploy contract (commands, the browser sign-in and token store, the API client, resources and tags, host key pins, provisioning steps, sslip.io, secret generation) is in `docs/adr/0017-digitalocean-deploy.md`.
- The changes from the first live DigitalOcean test (the public repository and the kit pin, the local kit build context, the kit replace on upgrade, `--confirm-env`, the fake API rules, the live test script) are in `docs/adr/0018-live-test-learnings.md`.

## Commands

The Makefile is the check contract. Run nothing else to prove a unit works.

| Command | What it does |
|---|---|
| `make build` | `go build ./...` in every module |
| `make test` | `go test -race`. `PKGS=./internal/x/...` narrows the root module. |
| `make lint` | `golangci-lint run` in every module (offline) |
| `make golden` | golden-tree tests (`TestGolden*`). `PACK=<name>` narrows, `UPDATE=1` rewrites. |
| `make script` | testscript files in `testdata/script/`. `RUN=<glob>` narrows. |
| `make app-gate` | generates apps and builds and tests them (Docker and network: milestone gate only) |
| `make doctest` | runs the code blocks in `docs/` |
| `make vuln` | `govulncheck` (network: milestone gate only) |
| `make tools-check` | checks the toolchain versions |
| `make e2e`, `make dind` | end-to-end suites (from M4 and M5) |

Rust targets (`rust-plan.md` §3). `CRATES="-p a -p b"` narrows to crates (default: the workspace).

| Command | What it does |
|---|---|
| `make rs-lint` | `cargo fmt --check` and `cargo clippy --locked --all-targets -- -D warnings`. `CRATES` narrows. This is the lane lint. |
| `make rs-test` | `cargo nextest run --locked` plus the doc tests. `CRATES` narrows, `TEST='<filterset>'` filters. It never runs `packs_digest`. |
| `make rs-golden` | Rust golden tests. `PACK=<name>` narrows, `FILTER='<filterset>'` narrows inside a pack, `UPDATE=1` rewrites. |
| `make rs-script` | builds the Rust binary and runs the Go script harness with `SPINUP_BIN` and `testdata/script/parity.txt`. `RUN=<glob>` narrows. |
| `make rs-doctest` | the Go `TestDocs` with `SPINUP_BIN` (until R5) |
| `make rs-build`, `make rs-deny`, `make rs-digest` | gate only: build all targets, `cargo deny` (bans, licenses, sources), the `packs_digest` test |
| `make rs-vuln`, `make rs-fetch` | network: `cargo deny check advisories` (gate), `cargo fetch --locked` (main loop) |
| `make rs-cover` | `cargo llvm-cov`, 70 percent floor (`COVER_CRATES`, R1: merge, manifest, migrate) |
| `make rs-convert-packs` | converts the Go packs into `crates/spinup-packs/packs/`. `PACK=<name>` narrows. |
| `make rs-convert-check` | fails when a new conversion differs from `crates/spinup-packs/packs/` |
| `make rs-pack-tree` | writes the merged pack tree (converted plus overlay) to `target/pack-tree/` |
| `make rs-tools-check` | checks the Rust tool versions |
| `make rs-install`, `make rs-ref-check` | `bin/spinup-rs` with its templates in `DEV_TEMPLATES_RS`; the reference app check (from R2) |
| `make oracle` | writes the Go oracle fixtures in `testdata/oracle/` again (the gate wants no diff) |
| `make parity` | runs the scripts with Go and with Rust, one line for each script |

Check forms that the plan uses:
- `T <pkgs>` means `go test -race <pkgs>`.
- `S <name>` means `make script RUN=<name>`.
- `G <pack>` means `make golden PACK=<pack>`.
- Every unit check also implies `make build lint`.
- `RT <crates> [:: <filterset>]` means `make rs-test CRATES="-p <crate> ..." TEST='<filterset>'`.
- `RG <pack> [:: <filterset>]` means `make rs-golden PACK=<pack> FILTER='<filterset>'`.
- `RS <name>` means `make rs-script RUN=<name>`.
- Every Rust unit check also implies `make rs-lint CRATES="-p <own crates>"`. A lane does not run `rs-build` or `rs-deny`.

Test contracts:
- `TestGolden*` tests read the environment variables `SPINUP_GOLDEN_PACK` (narrow to one pack) and `SPINUP_GOLDEN_UPDATE` (`1` rewrites the golden files).
- `internal/testutil/scripttest` has `TestScripts`. It builds the `spinup` binary and runs `testdata/script/*.txtar`. It reads `SPINUP_SCRIPT_RUN` (a glob) and skips `app_gate_*` scripts unless `SPINUP_APP_GATE=1`.
  Scripts can use `[env:NAME]` (true when `NAME` is set), `$SPINUP_REPO` (the repository root), and `mkversion <version> [overlay-dir]` (writes a synthetic template version under `$WORK/templates` for `SPINUP_TEMPLATES_DIR`; see the harness doc comment). Git commits work: the harness sets a git identity. With `DATABASE_URL` set, each script gets its own new database (created with `psql`, dropped at the end). `pnpm` (scripts/bin) uses the real home through `SPINUP_PNPM_HOME`, so offline installs work, and `DOCKER_CONFIG` points at the real `~/.docker`, so the docker CLI finds its context.
- `internal/doctest` has `TestDocs`.
- Rust: each crate has one integration test binary, `tests/it/main.rs`. The Rust golden tests read `SPINUP_GOLDEN_PACK` and `SPINUP_GOLDEN_UPDATE` too, and take their file selections from `testdata/golden/rust-filter.toml`. With `SPINUP_BIN` set, the script harness runs that binary, reads `parity.txt`, and supports `[impl:go]` and `[impl:rust]`.

## Toolchain

| Tool | Version | Note |
|---|---|---|
| Go | 1.27.2 | `/opt/homebrew/bin/go`. `GOTOOLCHAIN=local` always. Modules declare `go 1.26.0`. |
| Node | 24.x | `/opt/homebrew/opt/node@24/bin`. The Makefile puts it on `PATH`. Do not call a bare `node` in an interactive shell. It is a broken nvm function. |
| pnpm | 12.11.2 | `scripts/bin/pnpm` runs it through corepack (no global install). The Makefile puts `scripts/bin` on `PATH`. Use `$REPO/scripts/bin/pnpm` outside make. Offline installs: `pnpm install --frozen-lockfile --offline`. |
| golangci-lint | 2.14.0 | config: `.golangci.yml` |
| goreleaser, actionlint, syft, cosign, promtool, sops, age, docker | see `scripts/tools-check.sh` | |
| Rust | 1.99.1 (edition 2024) | `rust-toolchain.toml`. Lanes set `RUSTUP_TOOLCHAIN=1.99.1` and never install a toolchain. |
| cargo-nextest, cargo-llvm-cov, cargo-deny, sqlx-cli | 0.9.146, 0.9.1, 0.20.2, 0.9.0 | see `scripts/rust/tools-check.sh` (`make rs-tools-check`) |

## Lane environment (orchestrate lanes)

- Lanes run with `GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off`. The module cache already holds every dependency of the milestone.
- If you need a module or npm package that is not in `go.mod` or the lockfile, stop. Report `blocked: <module>`. Do not add dependencies. Do not run `go get`, `go mod tidy`, `npm install`, or `pnpm add`.
- Checks marked † in the plan need Docker, Postgres, browsers, or network access. In a lane, run only their static part. The main loop runs the full check at the milestone gate.
- When a check needs Postgres, use `DATABASE_URL` from the environment. Never start your own database.

Rust lanes (ADR 0019 §5):
- `RUSTUP_TOOLCHAIN=1.99.1 RUSTUP_AUTO_INSTALL=0 CARGO_NET_OFFLINE=true`, and `--locked` on every cargo command. `CARGO_HOME` is the shared, pre-fetched cache of the run.
- `CARGO_BUILD_JOBS=2`, `NEXTEST_TEST_THREADS=4`, `nice -n 10`, at most 2 lanes. The gate runs alone with `CARGO_BUILD_JOBS=4`.
- Each lane has its own `CARGO_TARGET_DIR`, an APFS clone (`cp -c -R`) of the milestone seed target. No sccache: `RUSTC_WRAPPER` stays unset. `CARGO_INCREMENTAL` stays at the cargo default. `SQLX_OFFLINE=true`.
- If you need a crate, a feature, or a version that is not in `Cargo.toml` and `Cargo.lock`, stop. Report `blocked: <crate>`. Do not run `cargo add`, `cargo update`, `cargo install`, `rustup`, or `brew`.
- If the converter core or a pack that you do not own is wrong, report `blocked: converter`.

## Shared files (main loop only)

Only the main loop writes these files. A unit never edits them:
- `go.mod`, `go.sum`, `go.work`, `go.work.sum`, `kit/go.mod`, `kit/go.sum`, `plugin-sdk/go.mod`, and every `deps_m*.go` file
- template-pack lockfiles (`go.sum`, `tools.sum`, `pnpm-lock.yaml` under `internal/templates/packs/`), plus `go.mod.tmpl`, `tools.mod.tmpl`, and `pack.d/deps.toml` in the `api` pack
- `internal/stackversions/stackversions.yaml`
- `Makefile`, `scripts/tools-check.sh`, `scripts/bump-stack.sh`
- `cmd/spinup/wire_gen.go`
- `CLAUDE.md`, `docs/design/plan.md`
- the M2 contract files: `internal/templates/source.go`, `internal/merge/contract.go`, `internal/upgrade/contract.go`, `internal/migrate/contract.go` (see `docs/adr/0006-m2-contracts.md`)
- the M3 contract file `internal/gen/contract.go` (see `docs/adr/0007-m3-contracts.md`)
- the M7 contract files `internal/backup/registry.go` and `internal/maintain/window.go` (see `docs/adr/0011-m7-contracts.md`)
- the M9 contract file `internal/plugin/contract.go` (see `docs/adr/0013-m9-contracts.md`)
- the web pack fragments `pack.d/deps.toml`, `pack.d/modes.toml`, and `pack.d/shadcn.toml` with their sources (`web/package.json.tmpl`, `web/pnpm-lock.yaml`, `web/bun.lock`, `web/pnpm-workspace.yaml`, `web/src/lib/components/ui/**`), the monitoring pack fragment `pack.d/vendor.toml` with the vendored dashboards, and `scripts/bin/`

Rust shared files (`rust-plan.md` §2), main loop only as well:
- `Cargo.toml` (root), every `crates/*/Cargo.toml`, `Cargo.lock`, `rust-toolchain.toml`, `rustfmt.toml`, every `clippy.toml`, `deny.toml`, `.cargo/config.toml`, `.config/nextest.toml`, `docs/design/rust-layers.toml`, `crates/spinup-packs/build.rs`
- the module lists: every `lib.rs`, every top-level `mod.rs` of a crate, `crates/spinup-cli/src/cmd/mod.rs`, and every `tests/it/main.rs`
- the registration files `crates/spinup/src/wire_gen.rs`, `crates/spinup/tests/expected_registry.txt`, and `crates/spinup-cli/src/registry.rs`
- the R1 contract files: `crates/spinup-exec/src/runner.rs`, `crates/spinup-tplstore/src/source.rs`, `crates/spinup-merge/src/contract.rs`, `crates/spinup-upgrade/src/contract.rs`, `crates/spinup-migrate/src/contract.rs`, `crates/spinup-gen/src/contract.rs`, `crates/spinup-plugin-api/src/lib.rs`, and `crates/spinup-vfs/src/tree.rs` (see `docs/adr/0020-r1-contracts.md` §1)
- `crates/spinup-stack/stackversions.yaml` (schema 2), `crates/spinup-tplstore/trusted_keys.txt`, `crates/spinup-packs/packs.digest`
- `crates/spinup-packs/packs/` and the converter core in `scripts/rust/convert-templates/`. Exception: a golden unit converts its own pack with `make rs-convert-packs PACK=<name>` and owns `scripts/rust/convert-templates/rules/<pack>.go`.
- every other file in `scripts/rust/`, `.github/workflows/*`, `.github/actions/*`
- `testdata/script/parity.txt`, `testdata/golden/rust-parity.txt`, `testdata/golden/rust-filter.toml`, `docs/design/rust-plan.md`

The pre-seed commit creates stub files (`doc.go`, or a stub `main.go`) for the packages of a milestone. A stub belongs to the unit whose file set contains it. That unit replaces the stub.
In Rust, the stubs are module files and `mod.rs` files with a doc comment, plus a few seam placeholders (`spinup_vfs::plan::Plan`, `spinup_manifest::lock::{Lock, Class}`, `spinup_stack::stack::Stack`, `spinup_cli::root::execute`). Keep the path and the derives of a seam placeholder when you replace it. If you need a new module, add it inside your own module or directory. Never edit a module list.

## Rules for code

- **Registration.** Commands, doctor checks, generators, resource parts, upgrade flags, and deploy hooks register themselves with `Register(...)` in `init()`. No unit edits a central list. Packages outside `internal/cli` reach the binary through the blank imports in `cmd/spinup/wire_gen.go`.
  In Rust, they register into `linkme` distributed slices (ADR 0020 §5). `crates/spinup/src/wire_gen.rs` has one `use <crate> as _;` line for each crate that defines or fills a slice. The registry test compares the registered IDs with `crates/spinup/tests/expected_registry.txt`. If your unit registers an item, list its ID in your report. The main loop updates the expected list.
- **Layering.** `internal/cli` holds only cobra wiring, not logic. Generators return a `vfs.Plan` and never write to disk. Only the apply step writes. Every subprocess call goes through `internal/exec`. `merge`, `vfs`, and `render` never import `cli`.
  In Rust: `spinup-cli` holds only clap wiring. No crate depends on `spinup-cli`. `docs/design/rust-layers.toml` lists the allowed crate edges, and a test fails on any other edge. The root `clippy.toml` bans `std::process::Command`: only `spinup-exec` runs subprocesses. `crates/spinup-gen/clippy.toml` also bans the file-write functions. Only `spinup-vfs` and `spinup-manifest` (the atomic lock save) write project files.
- **File classes.** Every template file is `managed`, `scaffold`, or `seed`, and its `pack.toml` (or a `pack.d/*.toml` fragment) declares the class. If you are not sure, use `managed` with an extension point.
- **Errors.** Every user-facing error says what failed and why, and ends with a `Try:` line. Every prompt has a flag. With no TTY, the CLI never prompts. It fails and names the missing flag.
- **Determinism.** A render is a pure function of the template version, the answers, the plugin set, and the stack versions. Do not use the clock, random values, or map iteration order in rendered output.
- **Pack changes.** `TestPacksDigest` guards `internal/templates/packs.digest`. If you change a template pack, run `SPINUP_GOLDEN_UPDATE=1 go test ./internal/templates -run PacksDigest`. Then replace `REVIEW` with the ID of the codemod that migrates existing apps, or with `none` when existing apps need no codemod (a new file, or a change the 3-way merge handles). See `docs/contributing/upgrade-matrix.md`.
  In Rust, the `packs_digest` test guards `crates/spinup-packs/packs.digest` (`make rs-digest`, gate only, `UPDATE=1` rewrites). The main loop updates it.
- **Converted packs.** `crates/spinup-packs/packs/` is generated from the Go packs by `make rs-convert-packs`. Never edit it by hand. Fix a conversion in `scripts/rust/convert-templates/rules/<pack>.go` (your own pack only). Rust-only changes go to `crates/spinup-packs/overlay/<pack>/` with the overlay merge rule of `rust-plan.md` §1: same path replaces, new path adds, `remove.txt` deletes, `pack.d` fragments merge last, and `pack.toml` cannot be replaced.
- **Go-era formats.** The Rust CLI writes lock schema 3 and rejects schema 1 and 2 locks. It rejects a template version without the engine marker `packs/ENGINE`. Each rejection ends with a `Try:` line. There is no migration from Go apps.
- **Parity.** Files and golden trees are byte-equal with Go. Console text is equal at regex level only: never expect equal `Usage:` text. The algorithms (lock JSON, diff, quote, case words, semver, `when`, the lock hash, `clean_path`) must match the Go oracle fixtures in `testdata/oracle/`. Never edit an oracle fixture by hand: `make oracle` writes them from Go.
- **Docs.** Each command or feature unit adds its own page under `docs/` with runnable code blocks. `make doctest` runs them.
- **No CGO.** No custom DSL, no ORM, no telemetry from the CLI.
  In Rust: no system C library at runtime. C code that `cc` builds into the binary is allowed (zstd, ring). The CLI uses rustls with `ring`. `deny.toml` bans `aws-lc`, `openssl`, and `native-tls` in the CLI graph.

## Orchestrate lanes: commit discipline

Before you return:
1. Commit your work in your worktree. Start the commit message with `orch: `.
2. Leave no uncommitted changes.
3. Report your branch, the commit sha, the files you touched, the checks you ran with their results, and anything you did not do.

Write only inside your unit's declared file set, plus `_test.go` files in your own directories and the `testdata` paths that your row names.
In Rust, add tests as `#[cfg(test)]` modules in your own files, or as the `tests/it/<file>.rs` that your row names. Never add a second file in `tests/`.
