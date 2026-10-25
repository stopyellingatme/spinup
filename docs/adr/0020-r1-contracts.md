# ADR 0020: R1 contracts (the Rust CLI core)

Status: accepted (2026-10-25)

## Context

R1 builds the Rust CLI core: `spinup new` (base, api, web), `check`, `diff`, `eject`, and `upgrade`. The plan is `docs/design/rust-plan.md` §5 R1. The R0 decisions are in ADR 0019.
The R1 units run in parallel lanes. This ADR fixes the contracts between them. The main loop writes the contract files in the R1 pre-seed commit. Units implement them and never edit them.
Go stays the parity oracle until R5. Go behavior does not change.

## Decisions

### 1. Contract files and seams

This section supersedes ADR 0006 §1. The Go contract files stay for the Go CLI until R5.

| File | Holds | Implemented by |
|---|---|---|
| `crates/spinup-exec/src/runner.rs` | `Cmd`, `Stdin`, `Output`, `Cancellation`, `Sinks`, `StopReason`, `ExitError`, `ExecError`, the `Runner` trait | R1.2 (`OsRunner`, `Fake`, `Display` for `Cmd`) |
| `crates/spinup-tplstore/src/source.rs` | `Version`, the `Source` trait, `SourceError`, `ENGINE` | R1.12 (`store`) |
| `crates/spinup-merge/src/contract.rs` | `Input`, `Merged`, the `Merger` trait, `MAX_SIZE` | R1.8 (`git`) |
| `crates/spinup-upgrade/src/contract.rs` | `Action`, `FileDecision`, `Plan`, `Options`, `Step`, `Phases`, `Extension`, the `EXTENSIONS` slice | R1.17 (planner), R1.18 (state machine, `--ui`), R2.10 (`cargo/`) |
| `crates/spinup-migrate/src/contract.rs` | `Stage`, `Ctx`, `Change`, `Migration`, the `MIGRATIONS` slice | R1.13 (registry, codemod 0001) |
| `crates/spinup-gen/src/contract.rs` | `PostStep`, `Input`, `Output`, `Command`, `Generator`, the `GENERATORS` slice | R1.10 (registry, runner, picker), R2 generators |
| `crates/spinup-plugin-api/src/lib.rs` | `Meta`, `GeneratorSpec`, `GenFile`, `Declared`, `Loaded`, `FetchRequest`, `PluginError`, the `PluginSource` trait | R1.10 (null source), R4 (`spinup-plugin`) |
| `crates/spinup-vfs/src/tree.rs` | `Tree`, `TreeFile`, `InvalidPath` | complete in the pre-seed |

`tree.rs` is a seventh contract file. The plan did not name it. A pack tree must cross the crates `spinup-packs`, `spinup-tplstore`, `spinup-render`, and `spinup-plugin-api`. `spinup-vfs` is the lowest crate that all of them can use. R1.3 owns the rest of `crates/spinup-vfs/src/`, but not `tree.rs`.

The contracts also name three types that units own. The pre-seed writes them as placeholders. The owning unit replaces the placeholder and keeps the path and the derives:

| Seam type | Owner | Keep |
|---|---|---|
| `spinup_vfs::plan::Plan` | R1.3 | `Debug`, `Clone`, `Default` |
| `spinup_manifest::lock::{Lock, Class}` | R1.4 | `Debug`, `Clone`; the three `Class` variants and their lock names |
| `spinup_stack::stack::Stack` | R1.5 | `Debug`, `Clone` |

`crates/spinup-cli/src/root.rs` has one more seam: `root::execute(args: Vec<OsString>) -> ExitCode`. `crates/spinup/src/main.rs` calls it. R1.21 keeps the path and the signature.

The Rust contracts differ from Go in these points:
- **Sync runner (R0.4).** `Runner::run(&self, &Cmd, &Cancellation, Sinks) -> Result<Output, ExecError>` replaces `Run(ctx, Cmd)`. A shared `Cancellation` flag replaces the Go context. The CLI installs one signal handler that sets it. `Cmd.timeout` stays per command. The live sinks move from `Cmd` to the `Sinks` argument, so `Cmd` stays `Clone` and `PartialEq` for `Fake`. The provided method `Runner::output` runs without live sinks.
- **Exit codes with output.** `ExecError::Exit` holds the full `Output`. Callers that use the exit code as a result (`git merge-file`) read the output from the error. `ExecError::output()` returns it.
- **Typed errors.** `SourceError::Unavailable` replaces the sentinel `ErrVersionUnavailable`. `SourceError::Engine` is new (see §3). Hooks and phases return `anyhow::Result`.
- **`Version.packs`** is an `Arc<Tree>`, not an `fs.FS`. `Version.stack` is an `Arc<Stack>`.
- **Migrations read stack versions, not template versions.** `migrate::Ctx` has `old_stack` and `new_stack`, not `Old` and `New`. The crate table does not allow the edge `spinup-migrate` to `spinup-tplstore`, and the codemods compare only pins.
- **Answers** are `serde_json::Map<String, serde_json::Value>`. Without the `serde_json` feature `preserve_order`, the map sorts its keys, so every walk is deterministic.
- **Generators** get the parsed `clap::ArgMatches` of their subcommand and add flags with `args: fn(clap::Command) -> clap::Command`. This replaces the pflag set.
- **Post-steps** follow ADR 0019 §4: `Migrate`, `SqlxPrepare`, `Build`, `OpenApiEmit`, `Client`. `rustfmt` runs in the plan step and is not a post-step. Go had `openapi`, `sqlc`, `gofmt`, `client`. R1 registers no generator, so R1 runs no post-step.
- **Upgrade phases.** `Phases` keeps the six slots. The Go slot `GoFix` is `fix`. The phases live in the same crate (`spinup-upgrade/src/cargo/`), so the state machine calls `cargo::phases()`. No global setter is necessary. In R1, `phases()` returns empty slots.
- **The plugin seam.** `PluginSource` has three methods: `fetch` (resolve, fetch, hash), `trust` (allowlist and lock check), and `render` (a plan for one trusted plugin). The caller passes the declared plugins from `spinup.toml`, because `spinup-plugin-api` does not depend on `spinup-project`. The R1 null source returns no plugins for a project without plugins. It returns `PluginError::Unsupported` for a project that declares plugins (Go `ErrNoResolver`).

### 2. Lock schema 3

This section supersedes ADR 0006 §2. The Rust CLI writes `spinup.lock` schema 3. It rejects schema 1 and 2 locks with a `Try:` line. It has no migration path (ADR 0019 §1).

Schema 3 keeps every schema 2 field, with three changes:
- `formatter` replaces `formatter_go`. It holds `<tool> <version>` of the formatter of the generated source, such as `rustfmt 1.99.1`. It is an empty string when no file was formatted. R1 formats no file, because R1 renders the Go `api` pack without `go/format`.
- `kit.crate` replaces `kit.module`. It holds the package name of the kit. From R2 it is `spinup-kit`. In R1 it holds the Go kit module path, because the R1 `api` pack is the converted Go pack.
- `engine` is new. It holds the template engine of the template version that rendered the app: `minijinja`.

The lock hash covers the rendered bytes. In R1 these are the unformatted bytes. From R2 they are the bytes after `rustfmt` (ADR 0019 §3).

### 3. Template versions, packs, and stack versions

This section supersedes ADR 0006 §3 and ADR 0012 §2.

**Converted packs.** The Go converter `scripts/rust/convert-templates/` reads the Go packs in `internal/templates/packs/` with `text/template/parse` and writes minijinja packs to `crates/spinup-packs/packs/<pack>/` (`make rs-convert-packs`, `PACK=<name>` narrows). The delimiters are `[[ ]]`, `[% %]`, and `[# #]` (R0.2). Nobody edits the converted files by hand. `make rs-convert-check` fails when a new conversion differs.
- The converter core (`main.go`, `convert.go`, `packs.go`, `repository.go`, `rules/rules.go`) is main-loop only.
- A golden unit owns `rules/<pack>.go` of its pack. A rule registers a `Fix` function from `init`. The go tool ignores file names that start with `_`, so the rules of `_sample` are in `rules/sample.go`.
- The converter replaces each converted pack directory completely, so a file that the Go pack removes goes away too.

**The engine marker.** The converter writes `packs/ENGINE` with the line `minijinja`. Every template version (the embedded packs, an archive, the cache, `SPINUP_TEMPLATES_DIR`) holds it. A version without the marker is a Go-era version. The Rust store rejects it with `SourceError::Engine` and a `Try:` line.

**Overlays.** Rust-only changes live in `crates/spinup-packs/overlay/<pack>/`. The merge rule is in `rust-plan.md` §1. The loader (R1.7) merges the embedded packs and overlay. A template version outside the binary (an archive, the cache, `SPINUP_TEMPLATES_DIR`) holds the merged tree. In a merged tree, an overlay fragment `overlay/<pack>/pack.d/<name>.toml` lands as `pack.d/~overlay-<name>.toml`. The `~` sorts after the converted fragments, so a loader that reads `pack.d/` in file-name order applies the overlay fragments last. `make rs-pack-tree` (`scripts/rust/pack-tree.sh`) writes the merged tree to `target/pack-tree/`. The script tests use it through `SPINUP_PACKS_SRC`.

**Embedding.** `crates/spinup-packs/build.rs` embeds `packs/` and `overlay/` as two sorted lists of `(path, bytes)` in `$OUT_DIR/embedded.rs`. The file modes come from `pack.toml`, as in Go.

**Stack schema 2.** `crates/spinup-stack/stackversions.yaml` is schema 2. In R1 it is additive. The lines above its marker line are a copy of the Go file (schema 1), with `schema: 2`. `scripts/rust/gen-stack.sh` writes them, and `gen-stack.sh --check` fails when they are stale. The lines below the marker are the Rust sections `rust`, `crates`, and `cargo_tools`. The R2 gate removes the Go sections.

**Archives.** The archive format stays: `templates-v<version>.tar.zst`, a deterministic tar of `packs/...`, `stackversions.yaml`, and an optional `MIGRATION.json`, with an Ed25519 signature. The embedded archive set of the Rust store starts empty. It starts at the first Rust release. Archives compare by decoded tar contents, because the zstd bytes differ from Go.

**Trusted keys.** `crates/spinup-tplstore/trusted_keys.txt` holds the keys. It has the same keys as the Go `store.TrustedKeys` (none today). The format is one key for each line, 64 lowercase hex characters. A line that starts with `#` is a comment. A build with `SPINUP_RELEASE=1` fails when the file lists no key. "Release mode" means this variable, not the cargo `release` profile, so local `--release` builds work.

### 4. Plugin packs

This section supersedes ADR 0013 §4 for the template syntax and the contract. Plugin packs use the minijinja syntax of the converted packs. ADR 0023 fixes the final plugin syntax in R4. The allowlist, the hash, the cache, and the lock entries do not change. The contract is `PluginSource` (§1). In R1 the null source applies.

### 5. Registration and layering

This section supersedes ADR 0006 §5 for registration.

**Registration.** Registries are `linkme` distributed slices (R0.3). A unit registers an item in its own module. No unit edits a central list.

| Slice | Defined in | Items in R1 |
|---|---|---|
| `spinup_gen::contract::GENERATORS` | the gen contract | none (R1 has only the picker) |
| `spinup_migrate::contract::MIGRATIONS` | the migrate contract | codemod 0001 (R1.13) |
| `spinup_upgrade::contract::EXTENSIONS` | the upgrade contract | `--ui` (R1.18) |
| the doctor check slice | `spinup-doctor/src/registry.rs` (R1.9) | composelint, plainsecrets |
| the command slice | `spinup-cli/src/root.rs` (R1.21) | the R1 commands |

`crates/spinup/src/wire_gen.rs` has one `use <crate> as _;` line for each crate that defines or fills a slice. So the binary crate `spinup` depends on `spinup-cli` and on these crates. `crates/spinup-cli/src/registry.rs` lists the slices. The binary prints the registered IDs when `SPINUP_PRINT_REGISTRY=1` is set. The test `crates/spinup/tests/it/registry.rs` compares them with `crates/spinup/tests/expected_registry.txt` (one `<kind> <id>` line each, sorted, `#` lines ignored). All three files are main-loop only. When a merge adds a registered item, the main loop adds its ID. When a merge adds a slice, the main loop adds it to `registry.rs` and `wire_gen.rs`. Command IDs are not in the test, because the commands live in `spinup-cli`, which the binary always links.

**Codemods** edit files only through `spinup-editors` (YAML, TOML, JSON). There is no Go AST editor.

**Layering.**
- `docs/design/rust-layers.toml` lists the allowed workspace edges. `[normal]` holds the normal and build edges of every crate. `[dev]` holds extra dev-dependency edges. The test `crates/spinup-cli/tests/it/layers.rs` reads `cargo metadata` and fails on any other edge. No crate depends on `spinup-cli`, except `spinup`.
- Three edges are new to the crate table of `rust-plan.md` §1. `spinup-tplstore` uses `spinup-vfs` for the pack tree. `spinup-testutil` uses `spinup-stack`, because the golden render loads the stack versions. `spinup` uses the crates of the wire list. The dev edges `spinup-packs` to `spinup-testutil` and `spinup-render` serve the golden tests.
- The root `clippy.toml` bans `std::process::Command` and `tokio::process::Command`. `crates/spinup-exec/clippy.toml` removes the ban for that crate. `crates/spinup-gen/clippy.toml` keeps the ban and adds the file-write functions (`std::fs::write`, `File::create`, `create_dir_all`, `remove_file`, and others). A test that must run a process (the registry test, the layer test) allows the lint with a `reason`.
- `deny.toml` bans `aws-lc-rs`, `aws-lc-sys`, `openssl`, `native-tls`, and `tokio` in the CLI graph.

### 6. Errors

Every user-facing error keeps the Go text, so the scripts match it by regex (§8).
- An error says what failed and why. It ends with one `Try:` line.
- Library crates return typed errors (`thiserror`) at contract boundaries, and `anyhow::Error` in hooks and phases. A context layer joins the texts with `: `, as Go `%w` does. The alternate form `{:#}` of an `anyhow::Error` gives the Go `err.Error()` text.
- The error text mapper is in `spinup-ui` (R1.1). It ports Go `RenderError`: it finds a `spinup_ui` error in the source chain (what, why, try, exit code), or it takes the `{:#}` text. It removes the `Try:` lines from the text and keeps one hint, with the Go rule. It prints `Error: <what>`, `  <why>`, and `  Try: <hint>`, or the JSON object `{"error":{"what","why","try","code"}}` with `--json`.
- The exit codes stay: 0 ok, 1 error, 2 usage, 3 conflicts, 4 drift, 5 locked. R1.21 maps clap usage errors to exit 2.

### 7. Editors

`spinup-editors` (R1.5) writes an own YAML editor on tree-sitter source spans (R0.1). It keeps comments where Go keeps them. The Compose writer keeps the Go behavior: it removes comments. This is not a parity exception. It closes the R0.1 question that `REPORT.md` left open. Byte-equality with go-yaml applies only to inputs in go-yaml style.

### 8. Parity

**What parity means.** Files and golden trees are byte-equal with Go. Console text is equal at regex level only: the scripts assert with regex, and nobody expects equal `Usage:` text from cobra and clap. Archives compare by decoded tar contents. The algorithms (lock JSON, diff, quote, case words, semver, `when`, the lock hash, `clean_path`) are byte-equal, and the Go oracle fixtures prove it.

**Scripts.** The Go harness runs a foreign binary with `SPINUP_BIN` (R1.0a). `testdata/script/parity.txt` lists the scripts that the Rust binary must pass: one name or glob for each line, `#` comments. A twin in `testdata/script/rust/` replaces a shared script of the same name. A shared script uses `[impl:go]` and `[impl:rust]` for the lines that differ. R1 lists `version`, `harness_mkversion`, `gen_picker`, `new_web`, `check`, `diff`, and `eject`. The scripts that run `build`, `db`, `routes`, Go `generate`, the upgrade dependency phase, or operate commands are due at R2 or later. `make rs-script` runs the list. `make parity` runs the Go and the Rust binaries and prints one line for each script.

**Golden trees.** `testdata/golden/rust-parity.txt` lists the golden trees that the Rust golden tests must match: `_sample`, `base`, `secrets`, `tofu`, `deploy`, `backups`, `monitoring`, `web`, `web-variants`, and `web-embed`. `testdata/golden/rust-filter.toml` holds the file selection of each Go golden test (packs, answers, `sub`, `prefixes`, `paths`, `exclude`, `mode`). R1 renders `.go` outputs without `go/format`, so the Rust tests skip them (`skip_suffixes`). When an R2 unit changes a pack, its entry moves to `testdata/golden-rs/`.

**Oracle fixtures.** A Go test writes fixtures to `testdata/oracle/`, and the Rust tests read them. R0.5 made the generator. R1.0b extended and froze it. One unit owns each case file. `make oracle` writes the fixtures again, and the gate wants no diff. The `.modes` manifest of a golden tree keeps the Go format of `testutil.Golden`. R1.0b records it as oracle cases. `spinup-testutil` (R1.14) reads and writes the same bytes.

**Doc tests.** This supersedes ADR 0005 §3 for Rust pages. The Go doctest runner runs the Rust binary with `SPINUP_BIN` until R5. `<!-- doctest: only go -->` and `<!-- doctest: only rust -->` on the line before a block limit the block to one binary. Pages that print console text use regex-stable output only.

**The `--module` flag.** The flag name stays in R1 and R2, because about 20 shared scripts pass it. In R1 it is the Go module path of the `api` pack. From R2 it feeds the crate slug.

**The docs site.** The Rust `docs reference` writes `docs/site-rs/` until R5. The Go gate keeps `TestDocsSiteIsCurrent` on `docs/site/`. R5 swaps the directories.

### 9. CI workflows

`.github/workflows/rust.yml` is new. It runs on a push to `rust` and on pull requests to `rust`. A push needs the user.

| Workflow | R1 to R4 | R5 |
|---|---|---|
| `rust.yml` (new) | fmt and the stack file check; clippy and nextest on ubuntu, macOS, and Windows; the script parity subset and `rs-convert-check` on ubuntu and macOS; coverage; `cargo deny`; the R0.3, R0.4, and R0.5 spike checks on the three systems | becomes `ci.yml` |
| `ci.yml` | keep for Go | replaced by `rust.yml` |
| `docs.yml` | keep for Go (`docs/site/`) | rewrite for `docs/site-rs/` |
| `kit-compat.yml` | keep for Go | rewrite for the Rust kit (R4.9) |
| `upgrade-matrix.yml` | keep for Go | rewrite for `testdata/corpus-rs/` |
| `canary-pins.yml`, `canary-sveltekit.yml` | keep for Go | rewrite for cargo pins |
| `dind.yml`, `dr.yml` | keep for Go, add Rust jobs at R3a | rewrite |
| `monitoring.yml`, `nightly.yml` | keep for Go, add Rust jobs at R3c | rewrite |
| `bump.yml`, `shas.yml` | keep; the main loop ports the inline Go to `spinup-release` in R4 | rewrite |
| `release.yml` | rewrite at R4 for the Rust builder | keep |
| `.github/actions/prepare-tests` | keep | rewrite for cargo |

The script harness does not run on Windows (`scripts/ci/windows-excluded.txt`), so the script parity job runs on ubuntu and macOS. The golden parity tests run inside the nextest job on all three systems. The coverage job has `continue-on-error` until the R1 gate. CI installs `cargo-nextest` 0.9.146, `cargo-llvm-cov` 0.9.1, and `cargo-deny` 0.20.2 with `cargo install --locked`, the same pins as `scripts/rust/tools-check.sh`.

### 10. Module lists and stubs

- Each crate has one integration test binary, `tests/it/main.rs`. The pre-seed writes its `mod` lines and an empty stub for each test file that a unit row names.
- `lib.rs` lists only the top-level modules. A crate that one unit owns gets one stub file for each top-level module. A crate that several units share gets one directory with a stub `mod.rs` for each unit sub-directory.
- A stub belongs to the unit whose file set contains it. The unit replaces it. A unit that needs a new module adds it inside its own module or directory. It never edits `lib.rs`, a top-level `mod.rs`, `cmd/mod.rs`, or `tests/it/main.rs`.
- The crate doc of each `lib.rs` says which Go packages the crate replaces.

## Consequences

- The units of R1 can start in parallel. Each unit compiles against the contracts and the seam placeholders from the first slot.
- R1.3 must not write `tree.rs`, R1.4 must keep the `Lock` and `Class` paths, R1.5 must keep the `Stack` path, and R1.21 must keep `root::execute`. The main loop checks this at each merge.
- The file set of R1.14 names `rules/_sample.go`. The go tool ignores that name, so R1.14 writes `rules/sample.go`.
- A merge that registers an item also changes `expected_registry.txt`. The main loop makes that change in the merge commit.
- The Go sections stay in the Rust stack file until the R2 gate.
- The native Windows runs of R0.3, R0.4, and R0.5 happen when the user pushes `rust`.

## R1 results (added at the R1 gate, 2026-10-25)

The R1 gate passed locally: the Rust build, lint, tests, and `cargo deny`; coverage 95.5 percent for `merge`, `manifest`, and `migrate`; the packs digest; every golden tree of `testdata/golden/rust-parity.txt` byte-equal with Go; the 9 parity scripts against the Rust binary, including the exit test `upgrade_flow` (conflict, `--continue`, `--abort`); the 12 R1 doc pages; `make rs-convert-check`; `make oracle` with no diff; and the full Go gate.

### Crate edges added during R1
- `spinup-migrate -> spinup-compat` (Go `migrate` compares versions with `compat`).
- `spinup-upgrade -> spinup-packs` (the real `PackRenderer`; `spinup-drift` renders through it).
- `spinup-drift -> spinup-manifest, spinup-project, spinup-ui, spinup-exec, spinup-compat`.
- Dev edges: `spinup-packs` tests use `spinup-doctor` (composelint) and `spinup-exec` (the optional `docker compose config`, `caddy adapt`, and `promtool` checks).

### Recorded deviations from Go
- YAML editor (R1.5): 29 of 32 R0.1 fixtures are byte-equal with go-yaml. In `alert-01`, `codemod-delete`, and `codemod-insert`, the Rust editor keeps a comment that Go drops or moves. The value writer equals go-yaml v3.0.5 on about 340 oracle cases. The Compose writer removes comments like Go (user decision).
- `composelint` (R1.9): `spinup:allow` comments have the Go scope. A comment that cannot be mapped to an item suppresses nothing. Parser error texts differ from Go.
- Plan JSON (R1.17): a tombstone-only path has the class `seed` (Go: an empty class).
- Drift (R1.19): the diff hunks come from the `spinup-vfs` patience diff (Go drift used go-internal/diff). The `diff` parity script passes. Safe reads use `std` with a canonical-parent check.
- CLI (R1.21): the version JSON has a `rust` key in place of `go`. The update notice and `version --check` are inactive until R4. A second SIGINT or SIGTERM exits with code 130.
- Migrate (R1.13): errors in place of Go panics. Kit-notes field names match exactly (Go matched without case).

### Due later
- `upgrade_apply` (the kit replace of the upgrade apply phase) and `upgrade_generated` (`spinup g resource`) are due at R2 (R2.10, R2.11).
- Go `TestBunWorkflows` needs the `ci` pack golden helpers (R4).

### Windows (rust.yml run 40232030925, 2026-10-25: all jobs green on Linux, macOS, and Windows)
- `spinup-vfs` compares only the owner-write bit on Windows (it stores only the read-only attribute), so it plans no other mode change there, and a mode-only change sets the attribute by path. Before, every unchanged file became a mode-only change, and setting attributes through a read-only handle failed with "Access is denied".
- `spinup-vfs` updates move the original into the backups first and rename into a free name, as Go does. Renames and removals retry for up to 2 s on Windows on "access denied" or a sharing violation, and an apply error names the failing step.
- `spinup_vfs::path::canonical` returns plain paths on Windows (not `\\?\C:\...`), because `git worktree add` rejects verbatim paths. `spinup-upgrade` and `spinup-drift` use it.
- Ignored on Windows, with the reason in the test: the Grafana entrypoint golden test (POSIX `sh`; Go excludes `templates` on Windows) and the R0.5 probe case `parent-absolute-inside` (the production `spinup-vfs` passes the same cases).
- R2 topic: with `core.autocrlf=true` (the Windows default of many Git installs), an upgrade can show LF files as modified. The upgrade tests set `core.autocrlf=false`. Go behaves the same. Decide in R2 whether generated apps add a `.gitattributes` rule.

