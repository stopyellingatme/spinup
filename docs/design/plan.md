# spinup — a Buffalo-style full-stack CLI, built with /orchestrate

## Context

We want our own CLI, similar to go-buffalo. It creates and operates a complete stack: database, backend, frontend, deployment, monitoring, and maintenance. The stack is our own opinionated choice. The CLI and every generated app must be easy to upgrade and maintain over years.

Buffalo failed mostly on maintenance cost. It owned too many tools (an ORM, a migration DSL, an asset pipeline, and a plugin `PATH` scan), and generated apps had no safe upgrade path. This plan avoids those errors. We own only the glue and delegate every tool. The upgrade engine is a first-class part from the start.

The directory `/Users/king/Documents/code/spinup` is empty and is not a git repository. We build the CLI with the `/orchestrate` skill. One orchestrate run builds one milestone (M0 to M10).

How this plan was made:
1. One workflow did the research, wrote 3 independent designs, merged them, and ran a critic.
2. A second workflow checked the unit graph with 3 skeptics: orchestrate rules, coverage, and sequencing.

Design sources (save them into the repo during Step 0):
- Synthesis: `/private/tmp/claude-501/-Users-king-Documents-code-spinup/04018c6e-b578-4cf8-a98e-ee7c4f3830de/scratchpad/synthesis.md` → `docs/design/architecture.md`
- Critique: `…/scratchpad/critique.md` → `docs/design/critique.md`
- The "Critic resolutions" section below overrides the synthesis wherever the two disagree.

## Fixed decisions

| Area | Decision |
|---|---|
| CLI | Go, a single static binary, cobra, embedded templates. Module `github.com/stopyellingatme/spinup`. Get `stopyellingatme` from the user in Step 0. |
| Runtime library | `github.com/stopyellingatme/spinup/kit`, one module with no nested modules. Minor versions ship in lockstep with the CLI. Patch versions ship independently. |
| Backend | Go: stdlib `net/http` ServeMux, oapi-codegen strict server, pgx/v5, sqlc, goose, River, scs, slog. |
| Frontend | SvelteKit with adapter-node, Tailwind 4, shadcn-svelte, openapi-typescript and openapi-fetch. The Step 0 spike decides between SvelteKit 3 and 2. |
| Database | PostgreSQL 18 (16 and 17 allowed). |
| Deploy | Docker Compose on a VM over SSH. Caddy for TLS. Blue/green by Compose project. |
| Secrets | sops and age binaries, with checksums embedded in the CLI. |
| Backups | pgBackRest by default, behind a `Backend` interface. `pgdump` is the second backend. WAL-G comes in M10. Step 0 confirms the default. |
| Monitoring | OTel SDK sends data to Alloy, then to Loki, Tempo, Prometheus, and Grafana. The default profile is `lite`, and `full` is opt-in. |
| Upgrades | Hybrid model. Fat kit library (`go get`), plus a thin scaffold tracked in `spinup.lock` and updated by a 3-way merge. |
| Scope | Full depth. Six items come last in M10: executable plugins, self-update, OpenTofu, embed mode, bun/heyapi, and in-place pg_upgrade. |

The synthesis §3 lists all version pins. Treat every pin as unverified until Step 0 checks it.

## Architecture summary

Read `docs/design/architecture.md` for the full design. In short:

- **File classes:** `managed` (CLI regenerates it, with an extension point next to it), `scaffold` (3-way merged on upgrade), and `seed` (written once, never touched again). `eject <path>` changes a file from managed to scaffold.
- **Render** is a pure function of (template version, answers, plugin set, stack versions). Each template pack carries its own `stackversions` copy. The CLI embeds the last 3 template versions. It downloads older versions and verifies a signature against a key in the CLI.
- **Pack metadata:** `pack.toml` lists files, classes, `questions` (each question becomes a `new` flag and a prompt), and `when` conditions. The loader also merges `pack.d/*.toml` fragments, so later units can extend a pack without editing its `pack.toml`.
- **`spinup.lock`** (JSON) stores the template, CLI, and kit versions, the answers, the stack versions, the plugins, `files{class, template, hash, mode}`, tombstones, orphans, `applied_migrations`, and `behavior_version`.
- **Upgrade:** precheck → create the upgrade branch in a **git worktree** → plan → Before codemods → render OLD and NEW → decide each file with `git merge-file --diff3` → apply → bump only the `stackversions` pins → After codemods (`go fix` gets its own commit) → write the lock → verify → `--continue` or `--abort`.
- **Command groups:** Develop (`new dev generate destroy routes test`), Data (`db`), Ship (`build deploy rollback releases`), Operate (`server secrets backup monitor`), Maintain (`maintenance upgrade diff check doctor eject restore ci plugin tools cache`).
- **Exit codes:** 0 ok, 1 error, 2 usage, 3 conflicts remain, 4 drift found, 5 deploy lock held.
- **Generated app layout:** `api/` (Go), `web/` (SvelteKit), `deploy/`, `monitoring/`, `secrets/`, `.github/`, plus `spinup.toml` and `spinup.lock`.

## Critic resolutions (these override the synthesis)

1. Run the upgrade in a git worktree on `spinup/upgrade-<v>`. `--abort` deletes the worktree. Never run `reset --hard` on the user's branch.
2. When a `managed` file has drifted, 3-way merge it by default. `--force-managed` overwrites it. The CLI also offers `eject` or a move to the extension point.
3. Hash normalized content (LF line endings, one trailing newline). Record the formatter's Go version in the lock.
4. If the lock lists a file and the file is missing from disk, add an automatic tombstone. `check` warns when a managed file is missing.
5. Generators write registrations to generator-owned files (`routes_gen.go`). OpenAPI uses one file per resource in `api/openapi/paths/`, plus a bundle step. Users edit `routes.go` and `openapi.yaml`, and generators never touch them.
6. shadcn `ui/*` files are `seed`. The lock stores the registry version. `spinup upgrade --ui` is an opt-in step.
7. Install dev tools (air, sqlc, oapi-codegen, goose) through `api/tools.mod` with `go tool -modfile=tools.mod`. The app's `go.mod` stays clean.
8. `upgrade` bumps only the pins in `stackversions`. It never runs `go get -u`.
9. Deploy uses blue/green Compose projects (`<app>-blue` and `<app>-green`), and a Caddy reload switches the upstream. The M5.1 spike confirms this. The VM keeps a `deploy.state` journal. Add `deploy --resume`, `deploy status`, and `deploy unlock --force`.
10. Every destructive command calls one shared guard, `project.ConfirmProtected`, which checks the `protected` flag of the environment. The guard covers `deploy`, `db reset/seed`, `db migrate down`, `restore`, and `secrets rotate`. The user must retype the environment name.
11. The migration runner sets `lock_timeout` and `statement_timeout` by default. `db lint` enforces the two-release rule for contract steps.
12. Keep secrets on the VM in one `/opt/<app>/secrets` directory. Release snapshots store only the hashes of secret files. `secrets init` requires a recovery recipient. CI uses its own recipient. Alloy and `logx` remove secret values from logs.
13. Grafana uses its own auth, with the admin password in sops. `monitor open` reaches it through an SSH tunnel. Access through Caddy is opt-in. The heartbeat is the `monitoring.heartbeat_url` setting.
14. On Windows, `new`, `generate`, `build`, and `upgrade --plan` run natively. `dev`, `deploy`, and `server` need WSL2. Templates ship `.gitattributes` with `* text=auto eol=lf`.
15. The CLI sends no telemetry. Users can turn off the update check with `SPINUP_NO_UPDATE_CHECK=1`.
16. Add `cache warm` and `--offline`.
17. `kit/testkit` reads `DATABASE_URL` when it is set. It uses testcontainers only when the variable is missing.
18. Tier-2 plugins run with the full privileges of the user, and the docs must say so. A repo allowlist file controls which plugins can run.
19. Milestone changes:
    - The compose security lint, a minimal `ci.yml`, and the docs-test harness move into M1.
    - `backup now` moves into M5.
    - Start a dogfood app after M4.
    - Mark releases as `-alpha` until the lock schema is final in M2.
    - Freeze upgrade corpora only from v0.3.
    - Cap the per-PR matrix at the default answers. The full matrix runs nightly.
20. Step 0 checks the health of pgBackRest, before M5 starts to use it. If it had no release in 90 days, make `pgdump` plus WAL-G the primary backends. Record the result in ADR 0004.
21. Each feature unit adds its own `docs/` page. The doctest harness runs the code blocks in those pages, so no milestone leaves undocumented commands.

## How /orchestrate runs this project

### Constraints from the orchestrate skill, and the response to each

| Orchestrate rule | Response in this plan |
|---|---|
| Parallel worktrees need a git repo and a clean tree. | Step 0 runs inline. Every run starts from a clean, committed `main`. |
| Lanes never install dependencies. | The main loop runs a **pre-seed commit** before each run (see the run protocol). Lanes run with `GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off`. A lane that needs a missing dependency reports `blocked: <module>` and stops. |
| Verification runs only commands recorded in `CLAUDE.md`. | Step 0 records every `make` target and every check form below. |
| Shared files belong to exactly one unit. | Only the main loop writes `go.mod`, `go.sum`, `go.work`, pack lockfiles, `stackversions.yaml`, `Makefile`, and `cmd/spinup/wire_gen.go`. Units never edit them. |
| The orchestrator never merges, tags, or deploys. | Each run lands on `orch/<run-id>`. The user merges it into `main`. The user tags releases by hand. |

**Registration.** The root command, the doctor registries, the generator registry, the resource part registry, the upgrade flag hook, and the deploy hooks all use `Register(...)` from `init()`. Packages outside `internal/cli` reach the binary only through blank imports in `cmd/spinup/wire_gen.go`. The pre-seed commit writes these imports for every package of the milestone. It also creates a stub `doc.go` in each new package, so the build stays green before the lanes run.

**Lane-safe and gate-only checks.** A lane check uses only fakes, the injectable `exec.Runner`, and local files. Checks marked † need Docker, Postgres, browsers, or network access. For a † check, the lane runs only its static part (compile, `docker compose config`, `playwright test --list`, `actionlint`). The main loop runs the full check at the milestone gate. Units whose own check needs Postgres get a `DATABASE_URL` from the main loop, with one database for each lane.

**Check forms** (record them in `CLAUDE.md`):
- `T <pkgs>` means `go test -race <pkgs>`.
- `S <name>` means `make script RUN=<name>` (testscript files in `testdata/script/`).
- `G <pack>` means `make golden PACK=<pack>`.
- Every unit check also implies `make build lint`.
- `make lint` runs only `golangci-lint`, which works offline. `make vuln` runs `govulncheck` at the milestone gate.

**Unit rules.**
- A unit writes only its declared file set. It may add `_test.go` files inside its own directories.
- It may add the `testdata` paths named in its row. Golden and script file names are unique for each unit.
- It commits with the `orch:` prefix and leaves no uncommitted changes. It reports its branch, sha, files, and checks.
- When a row says "extends" an earlier file, that earlier file is in the unit's file set.

### Step 0 — Bootstrap (inline, before any orchestrate run)

1. **Toolchain.** The local Go is **1.19.1** (`darwin/amd64`). Install the pinned Go (1.27.x, minimum 1.26), and use an arm64 build if the Mac is Apple Silicon. Install and pin these tools:
   - golangci-lint, govulncheck, goreleaser, actionlint, syft, cosign
   - sops, age, promtool
   - Node 24 and pnpm, then the Playwright browsers
   - a git version that supports `merge-file --diff-algorithm`
   - Docker with privileged containers (for DinD)

   Add `make tools-check`, which fails when a tool or its version is wrong. Record the versions in `CLAUDE.md`.
2. Ask the user for `stopyellingatme`. Run `git init`, then create the default branch `main`.
3. **Pin verification.** Check every pin in synthesis §3 with `go list -m -versions`, `npm view`, and the registry and GitHub release APIs. Check the critic's doubts first: SvelteKit 3 stable, otel-lgtm 0.36.0, Go 1.27, the Grafana/pnpm/Renovate/Trivy pins, and the `charm.land` import paths for huh and Bubble Tea v2. Write `internal/stackversions/stackversions.yaml`, which holds:
   - versions
   - image digests
   - release-asset sha256 values for each platform (sops, age)
   - the GitHub Actions SHA table
4. **ADRs.** Write `docs/adr/0001-architecture.md`. Then do two checks and record the results:
   - **SvelteKit spike** → `0002-sveltekit-version.md`. Build a throwaway app in the scratchpad. Check that a Go `/api/session` cookie survives SSR and a form action, that `paths.origin` works, that the OTel server tracing sends a `traceparent` to Go, and that the Docker build passes. If SvelteKit 3 fails, pin SvelteKit 2.
   - **pgBackRest health** → `0004-backup-backend.md` (resolution 20).
5. **Modules.** Create the root `go.mod` (CLI) and `kit/go.mod`. Commit `go.work` so lane worktrees can resolve the kit.
6. **Makefile.** Write these targets: `build test lint vuln golden script app-gate doctest e2e dind tools-check bump-stack`. `e2e` and `dind` print "not implemented" and exit 0 until M4 and M5 fill them. `app-gate` runs every `testdata/script/app_gate_*.txtar`.
   - `bump-stack` regenerates the lockfiles of each template pack from `stackversions.yaml`: `go.sum`, `tools.sum`, and `pnpm-lock.yaml`. Only the main loop runs it.
7. **Documents.** Write `CLAUDE.md` with these parts:
   - the stack and the layout rules from synthesis §6.1
   - the registration convention, the file-class rule, and the docs rule (resolution 21)
   - the check forms and the lane environment variables

   Also write `.golangci.yml`, `.gitignore`, `MAINTAINERS.md`, `SECURITY.md`, `CODEOWNERS`, and `docs/design/{architecture,critique,plan}.md`. The file `plan.md` is a copy of this plan.
8. Make the M0 pre-seed commit (see the run protocol). Make the first commit on `main`.

### Run protocol (the main loop does this for every milestone)

1. **Pre-seed commit** on `main`, named `deps: pre-seed M<n>`:
   - Add the milestone's Go modules to each `go.mod`. Pin them with a `deps_m<n>.go` file under the `//go:build deps` tag, so that `go mod tidy` keeps them.
   - Write the `go.mod`, `tools.mod`, and `package.json` inputs of every template pack that the milestone touches. Run `make bump-stack`.
   - Run `go mod download` for every module, and share `GOMODCACHE` with the lanes.
   - Pull the milestone's Docker images by digest. Vendor the community dashboards (M6).
   - Write the `wire_gen.go` imports and the stub `doc.go` files.
2. Run `/orchestrate build milestone M<n> of docs/design/plan.md`. Use `/orchestrate plan …` first if you want to see the ledger.
3. **Milestone gate** on `orch/<run-id>`:
   - Run `make tools-check build lint vuln test golden script doctest app-gate`.
   - From M4, also run `make e2e`. From M5, also run `make dind`.
   - Run every † check.
4. The user reviews the branch and merges it into `main`.
5. **After M4 and every later milestone:** upgrade the dogfood app (a separate repo, created after M4) with the new CLI.

The "Row" values are hints for the `model-routing` skill. Orchestrate makes the final routing decision in its Phase 2. Units with † checks go to Claude lanes, because those lanes can reach the local Docker daemon.

### Milestone unit graphs

#### M0 — Foundations → `spinup version` runs, and the golden harness passes

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M0.1 | Cobra root (`Register` hook), global flags, exit codes, the `ui` renderer (TTY detection, `Try:` errors, `--json`), the getting-started card, and `version` | `T ./internal/ui/... ./internal/cli/...`, then `go run ./cmd/spinup version --json` | `cmd/spinup/main.go`, `internal/cli/{root,version}.go`, `internal/ui/` | – |
| M0.2 | In-memory `vfs`: stage, diff, dry-run, and atomic apply | `T ./internal/vfs/...` | `internal/vfs/` | – |
| M0.3 | `exec.Runner` with redaction, timeouts, and a fake | `T ./internal/exec/...` | `internal/exec/` | – |
| M0.4 | `manifest`: lock schema v1, normalized hashing, classes, tombstones, orphans, and a schema-migration hook | `T ./internal/manifest/...`, then fuzz for 10s | `internal/manifest/` | – |
| M0.6 | `stackversions` and `compat` loaders | `T ./internal/stackversions/... ./internal/compat/...` | `internal/stackversions/*.go`, `internal/compat/` | – |
| M0.9 | `project`: `spinup.toml` schema, root lookup, `--dir` and `--env`, environments, `ConfirmProtected`. Doctor `Check` interface with local and remote registries. | `T ./internal/project/... ./internal/doctor/` | `internal/project/`, `internal/doctor/registry.go` | – |
| M0.5 | `render`: `[[ ]]` templates, deterministic output, `go/format`, and output as a `vfs.Plan`. `pack.toml` and `pack.d` loader. `//go:embed all:packs` with a `_sample` pack. | `T ./internal/render/... ./internal/templates/` | `internal/render/`, `internal/templates/{embed,loader*}.go`, `internal/templates/packs/_sample/` | M0.2, M0.4, M0.6 |
| M0.7 | Harness for golden trees, determinism, and testscript | `G _sample`, then `S version` | `internal/testutil/`, `testdata/golden/_sample/`, `testdata/script/version.txtar` | M0.1, M0.5 |
| M0.8 | CLI repo CI (lint, test, golden, a Windows unit job), with actions pinned by SHA. Snapshot `.goreleaser.yaml`. | `goreleaser check`, then `actionlint` | `.github/workflows/ci.yml`, `.goreleaser.yaml` | – |

#### M1 — Tracer bullet → `new --api-only`, `go run`, and `/healthz` returns 200

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M1.1 | Kit `app`, `config` (`*_FILE`, `behavior_version`), `httpx`, and `logx` (with a no-op `Redactor` hook) | `T ./kit/app/... ./kit/config/... ./kit/httpx/... ./kit/logx/...` (includes an error-format golden test) | `kit/{app,config,httpx,logx}/` | – |
| M1.2 | Kit `db` (pgxpool, goose provider, advisory lock, timeouts) and `testkit` (`DATABASE_URL` first) | `T ./kit/db/... ./kit/testkit/...` with `DATABASE_URL` | `kit/db/`, `kit/testkit/` | – |
| M1.3 | Packs `base` (`spinup.toml`, `.gitattributes`, `.gitignore`, `.env.example`, README seed) and `api` (`main.go`, `routes.go`, `routes_gen.go`, empty `openapi/paths/`, `Dockerfile.api`, `compose.dev.yml`) | `G base`, then `G api` | `internal/templates/packs/{base,api}/` (not lockfiles), `testdata/golden/{base,api}/` | M1.1, M1.2 |
| M1.4 | `new` (generic composer from pack `questions`, huh prompts, `--non-interactive`), `build` (local), `db`, and `db migrate` (`down` needs `--i-know` and passes `ConfirmProtected`). Adds `SPINUP_KIT_REPLACE`. | `S new_api` | `internal/cli/{new,build,db,db_migrate}.go`, `internal/gen/newapp/`, `internal/db/migrate*.go`, `testdata/script/new_api.txtar`, `docs/commands/{new,build,db}.md`, `docs/policy/windows.md` | M0.9, M1.1–M1.3 |
| M1.5 | Compose security lint as a pure function, registered as a doctor check | `T ./internal/doctor/composelint/...` | `internal/doctor/composelint/` | – |
| M1.6 | Minimal `ci` pack with `ci.yml` | `G ci`, then `actionlint` on the golden output | `internal/templates/packs/ci/`, `testdata/golden/ci/` | – |
| M1.8 | Doctest harness that runs the code blocks in `docs/` | `T ./internal/doctest/...`, then `make doctest` | `internal/doctest/` | – |
| M1.7 | App gate for the API-only app: generate, build, test, and `/healthz` | `make app-gate` † | `testdata/script/app_gate_api.txtar` | M1.4 |

#### M2 — Upgrade spine → an edited v0.1-alpha app upgrades to a synthetic v0.2

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M2.1 | `Merger` interface and the gitmerge implementation. Exports `CheckGitVersion`. The merge table covers Go, YAML, JSON, Svelte, CRLF, binary files, and modes. | `T ./internal/merge/...` | `internal/merge/` | – |
| M2.3 | Codemod framework (Before and After stages, `Plan` and `Apply`, idempotent). Go AST helpers and comment-preserving YAML, TOML, and JSON editors. `MIGRATION.json` schema. | `T ./internal/migrate/... ./internal/codemod/...` (each fixture runs twice) | `internal/migrate/`, `internal/codemod/` | – |
| M2.6 | Template store: embeds the last 3 versions as zstd and downloads older ones with a signature check. Adds `cache warm` and `--offline`. | `T ./internal/templates/store/...` | `internal/templates/store/`, `internal/cli/cache.go` | – |
| M2.2 | Upgrade planner with a `TemplateSource` interface. Decision rules for each file (resolutions 2 and 4), plus renames, orphans, and new-path conflicts. | `T ./internal/upgrade/ -run Plan` | `internal/upgrade/plan*.go`, `internal/upgrade/source.go` | M2.1 |
| M2.4 | Upgrade state machine: worktree, branch, chaining, `--plan` (shows `MIGRATION.json`), `--continue`, `--abort`, exit 3, and a flag hook for later flags | `S upgrade_flow` | `internal/upgrade/{state,worktree,chain}*.go`, `internal/cli/upgrade.go`, `testdata/script/upgrade_flow.txtar` | M2.2, M2.6 |
| M2.5 | `diff`, `check` (exit 4), `doctor`, `eject`, and `restore`. Checks for the git version, a kit minor mismatch, and a missing managed file. | `S diff`, `S check`, `S eject` | `internal/cli/{diff,check,doctor,eject,restore}.go`, `internal/doctor/checks/`, `testdata/script/{diff,check,eject}.txtar` | M0.9, M2.2, M2.6 |
| M2.8 | Apply step, the pin-only dependency step, `go fix` in its own commit, and verify through `Runner` | `S upgrade_apply` | `internal/upgrade/{apply,deps,verify}*.go`, `testdata/script/upgrade_apply.txtar` | M2.3, M2.4 |
| M2.7 | Upgrade matrix and corpus format. Lock schema test (v1 to v2). A test that fails when a template changes with no migration entry. A coverage gate. A PR workflow. | `T ./internal/upgrade/ -run Matrix`, then `actionlint` | `internal/upgrade/matrix_test.go`, `testdata/corpus/`, `.github/workflows/upgrade-matrix.yml` | M2.5, M2.6, M2.8 |

#### M3 — Data and generators → `g resource post title:string` and `db migrate` give a working CRUD API with tests

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M3.0 | Generator registry and runner: `--dry-run`, `--diff`, `--force`, lock records, post-steps, `Next:` lines, and the picker | `T ./internal/gen/registry/... ./internal/gen/run/...`, then `S gen_picker` | `internal/gen/{registry,run}/`, `internal/cli/generate.go`, `testdata/script/gen_picker.txtar` | – |
| M3.1 | Field type table (SQL, Go, OpenAPI, TS) and the field parser | `T ./internal/gen/fields/...`, then fuzz for 10s | `internal/gen/fields/` | – |
| M3.2 | `db status/redo/new/to/seed/reset/psql/dump/restore/doctor/sqlc`. `db doctor` warns about PG 14. | `S db_cmds` | `internal/cli/db_{status,redo,new,to,seed,reset,psql,dump,restore,doctor,sqlc}.go`, `internal/db/ops/`, `testdata/script/db_cmds.txtar` | – |
| M3.7 | `db lint` with the two-release rule | `T ./internal/db/lint/...` | `internal/db/lint/`, `internal/cli/db_lint.go` | – |
| M3.3 | Split OpenAPI files, the bundle step, oapi-codegen wiring, and `g openapi`. Extends the M1.3 api pack. | `G api-openapi` | `packs/api/{openapi/,oapi-codegen.yaml.tmpl,generate.go.tmpl,pack.d/openapi.toml}`, `internal/gen/openapi/`, `testdata/golden/api-openapi/` | M3.0 |
| M3.4 | Generators `resource` (with a part registry), `model`, `handler`, and `migration`. They rewrite `routes_gen.go`. | `S gen_resource`, then `make app-gate` † | `internal/gen/{resource,model,handler,migration}/`, `testdata/script/gen_resource.txtar`, `testdata/golden/gen-resource/` | M3.0, M3.1, M3.3 |
| M3.5 | `destroy` (deletes only files whose hash matches the lock), `test`, and `routes` | `S destroy`, `S routes` | `internal/cli/{destroy,test,routes}.go`, `testdata/script/{destroy,routes}.txtar` | M3.4 |
| M3.6 | Kit `jobs` (River wrapper), the `jobs` pack (`cmd/worker/main.go`), and `g job` | `T ./kit/jobs/... ./internal/gen/job/...`, then `G jobs` | `kit/jobs/`, `packs/jobs/`, `internal/gen/job/`, `testdata/golden/jobs/` | M3.0 |

#### M4 — Dev loop and frontend → `spinup dev` gives a login flow in the browser

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M4.1 | `web` pack: the SvelteKit version from ADR 0002, Tailwind, shadcn as seed (registry version in the answers), the cookie `handleFetch`, `paths.origin`, the Vite proxy, `Dockerfile.web`, an `onlyBuiltDependencies` allowlist, and an instrumentation stub. An After codemod runs `sv migrate`. | `G web`, then `make app-gate` † | `packs/web/` (not lockfiles), `internal/migrate/web_svmigrate.go`, `testdata/golden/web/`, `testdata/script/app_gate_web.txtar` | – |
| M4.2 | `g client` (openapi-typescript), and a doctor check for client drift | `T ./internal/gen/client/... ./internal/doctor/clientdrift/...` | `internal/gen/client/`, `internal/doctor/clientdrift/` | – |
| M4.3 | Kit `auth`: argon2id, scs with pgxstore, session rotation, lockout, CSRF, a CSP default, and `/api/session`. The `auth` pack adds login pages. | `T ./kit/auth/...`, then `G auth` | `kit/auth/`, `packs/auth/`, `testdata/golden/auth/` | M4.1 |
| M4.4 | `devloop` supervisor: process groups, log mux, port allocator, air through `tools.mod`, regeneration on SQL or OpenAPI changes. `doctor --quick` checks. | `T -race ./internal/devloop/... ./internal/doctor/quick/...` | `internal/devloop/`, `internal/cli/dev.go`, `internal/doctor/quick/` | – |
| M4.5 | Kit `devtools` page `/_dev`, compiled in only for the dev environment | `T ./kit/devtools/...` | `kit/devtools/` | – |
| M4.6 | `g page`, `g component`, and resource pages through the part registry | `S gen_page`, then `G gen-resource` (updates the M3.4 golden files) | `internal/gen/{page,component}/`, `internal/gen/resource/pages*.go`, `testdata/script/gen_page.txtar`, `testdata/golden/gen-resource/` | M4.1, M4.2 |
| M4.8 | `upgrade --ui` through the M2.4 flag hook | `S upgrade_ui` | `internal/cli/upgrade_ui.go`, `internal/upgrade/ui*.go`, `testdata/script/upgrade_ui.txtar` | M4.1 |
| M4.7 | Playwright smoke (login, session across SSR), `nightly.yml`, and the SvelteKit canary | `playwright test --list`, then `actionlint`. Gate: `make e2e` † | `testdata/e2e/web/`, `.github/workflows/{nightly,canary-sveltekit}.yml` | M4.1–M4.6 |

#### M5 — Deploy, server, and secrets → zero-downtime deploy and rollback on a DinD VM

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M5.1 | Blue/green spike. Pass: zero failed requests out of 1,000 during the switch. Fallback: rolling scale. ADR 0003 ends with the line `strategy: bluegreen\|rolling`. | `bash spikes/bluegreen/run.sh` † | `spikes/bluegreen/`, `docs/adr/0003-deploy-strategy.md` | – |
| M5.2 | `remote` SSH wrapper (ControlMaster, fakes), the deploy lock with a TTL, the `deploy.state` journal, and `PreDeploy` and `PostRelease` hook registries | `T ./internal/remote/... ./internal/deploy/` | `internal/remote/`, `internal/deploy/{lock,journal,hooks}.go` | – |
| M5.3 | `secrets` group over sops and age, with recovery and CI recipients. Kit `secrets`. The `secrets` pack (`.sops.yaml`, seeds). A doctor check for plaintext secrets. | `T ./internal/secrets/... ./kit/secrets/...`, then `S secrets` | `internal/secrets/`, `internal/cli/secrets.go`, `kit/secrets/`, `packs/secrets/`, `internal/doctor/plainsecrets/`, `testdata/script/secrets.txtar` | M0.9 |
| M5.10 | `tools install` with the sha256 values from `stackversions` | `T ./internal/tools/...` | `internal/tools/`, `internal/cli/tools.go` | – |
| M5.9 | `build --push`: buildx, GHCR, provenance and SBOM, and a `--platform` default from the environment | `T ./internal/build/...` | `internal/build/`, `internal/cli/build_image.go` | M0.9 |
| M5.4 | `deploy` pack: Compose for the ADR 0003 strategy, Caddyfile and `Caddyfile.d/`, `Dockerfile.postgres`, cloud-init, and `environments/*.toml` | `G deploy`, composelint on the output, then `docker compose config` | `packs/deploy/`, `testdata/golden/deploy/` | M5.1 |
| M5.5 | `server bootstrap`, `server audit` (scans `docker inspect` for secrets), `logs`, and `ssh`. A doctor check recommends `lite` below 4 GB of RAM. | `T ./internal/server/...` | `internal/server/`, `internal/cli/server.go` | M5.2, M5.4 |
| M5.6 | Deployer flow: `--plan`, `ConfirmProtected`, hook calls, and `deploy status`, `resume`, and `unlock` | `T ./internal/deploy/ -run Flow` | `internal/deploy/flow*.go`, `internal/cli/deploy.go` | M5.2–M5.4, M5.9 |
| M5.11 | `rollback`, `releases`, and kit `deployx` | `T ./internal/deploy/ -run 'Rollback\|Releases' ./kit/deployx/...` | `internal/deploy/{rollback,releases}*.go`, `internal/cli/{rollback,releases}.go`, `kit/deployx/` | M5.6 |
| M5.7 | `Backend` interface, pgBackRest, `backup` and `backup now`, a `PreDeploy` hook, and the `backups` pack (`pgbackrest.conf`) | `T ./internal/backup/... ./kit/backup/...` | `internal/backup/{backend,pgbackrest,now,hook}.go`, `internal/cli/backup.go`, `kit/backup/`, `packs/backups/` (not `systemd/`) | M5.2, M5.4 |
| M5.8 | DinD suite: bootstrap, deploy, a failed health check that triggers an automatic rollback, `rollback`, lock contention, and `secrets push` | `actionlint`. Gate: `make dind` † | `testdata/dind/`, `.github/workflows/dind.yml` | M5.5, M5.7, M5.11 |

#### M6 — Monitoring → web and API spans appear in Tempo, logs in Loki, and a test alert fires

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M6.1 | Kit `telemetry` (OTel wrappers, slog bridge). The `logx` redactor is registered. Adds `docs/data-handling.md`. | `T ./kit/telemetry/... ./kit/logx/...` | `kit/telemetry/`, `kit/logx/redact.go`, `docs/data-handling.md` | – |
| M6.2 | `monitoring` pack with profiles `lite`, `full`, and `none`. Alloy with a redaction stage. Vendored dashboards, RED and backup dashboards, alerts, and the heartbeat. | `G monitoring`, `promtool check rules` on the output, then composelint | `packs/monitoring/`, `testdata/golden/monitoring/` | – |
| M6.3 | `monitor open/status/logs/trace/alerts/test-alert/silence/dashboards sync`, and the annotation `PostRelease` hook | `T ./internal/monitor/...` | `internal/monitor/`, `internal/cli/monitor.go` | – |
| M6.6 | `g dashboard` and `g alert` (they write the `custom/` folders) | `S gen_dashboard` | `internal/gen/{dashboard,alert}/`, `testdata/script/gen_dashboard.txtar` | M6.2 |
| M6.4 | SvelteKit server tracing (extends the M4.1 stub), and an e2e test of `traceparent` from Node to Go | `G web`. Gate: e2e trace test † | `packs/web/src/instrumentation*`, `packs/web/pack.d/tracing.toml`, `testdata/e2e/trace/` | M6.1 |
| M6.5 | Stack boot test with a RAM budget for each profile | `actionlint`. Gate: boot test † | `testdata/e2e/monitoring/`, `.github/workflows/monitoring.yml` | M6.2 |

#### M7 — Backups and maintenance → a verified restore runs on schedule, and the DR drill passes

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M7.1 | `pgdump` backend | `T ./internal/backup/pgdump/...` | `internal/backup/pgdump/` | – |
| M7.2 | `backup verify/restore --into new\|in-place/prune`, pgBackRest `Verify`/`Restore`/`Expire`, and `backup.Open` (backend selection). `restore` calls `ConfirmProtected` and takes the deploy lock. | `T ./internal/backup/...` | `internal/backup/{open,verify,restore,prune}*.go`, `internal/cli/backup_{verify,restore,prune}.go` | – |
| M7.2b | `backup schedule` (systemd timers and `run.sh` on the VM) and the backup metrics file that Alloy reads (ADR 0011 §4, §5) | `T ./internal/backup/...`, then `G backups`, `G monitoring` | `internal/backup/{metrics,schedule}*.go`, `internal/cli/backup_schedule.go`, `packs/backups/` (new files), the monitoring pack's Alloy, Compose, rules, and backups dashboard, `testdata/golden/{backups,monitoring}/` | – |
| M7.3 | `maintenance` group: `patch`, `vuln-scan`, `prune`, `vacuum`, `certs`, and `window` | `T ./internal/maintain/...` | `internal/maintain/` (not `pg/`), `internal/cli/maintenance.go` | – |
| M7.4 | `pg-minor-upgrade`, and `pg-major-upgrade` (dump/restore swap) | `T ./internal/maintain/pg/...` | `internal/maintain/pg/`, `internal/cli/maintenance_pg.go` | M7.1, M7.3 |
| M7.5 | Remote doctor checks, and `renovate.json` through a `pack.d` fragment of the base pack | `T ./internal/doctor/remote/...`, then `G base` | `internal/doctor/remote/`, `packs/base/{renovate.json.tmpl,pack.d/renovate.toml}` | – |
| M7.6 | DR drill: destroy the VM, start a new one, restore from S3 (SeaweedFS; MinIO is archived), and assert the RTO | `bash testdata/dind/dr/run.sh` †, then `actionlint` | `testdata/dind/dr/`, `.github/workflows/dr.yml` | M7.1, M7.2 |

The M7 pre-seed (ADR 0011) adds the contract files `internal/backup/registry.go` and `internal/maintain/window.go`, the `[backups]` and `[maintenance]` keys of `spinup.toml`, and `registerBackup` in `internal/cli/backup.go`.

#### M8 — CI generation and release → a generated repo is green on GitHub, and a `-rc` builds

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M8.1 | Full `ci` pack (replaces the M1.6 pack): `ci`, `e2e`, `deploy`, `backup-verify`, and `upgrade-check` workflows, with the SHA table and least-privilege permissions | `G ci`, then `actionlint` on the output | `packs/ci/`, `internal/ci/`, `testdata/golden/ci/` | – |
| M8.1b | Playwright e2e scaffold in the web pack (`playwright.config.ts`, a smoke test) that `e2e.yml` runs | `G web`, `G auth`, then `playwright test --list` | `packs/web/pack.d/e2e.toml` and its files, `testdata/golden/{web,auth}/` | – |
| M8.2 | `ci generate` and `ci verify`. CLI repo workflows: `bump`, `shas`, `canary-pins`, and `kit-compat` (the last frozen app built against the new kit) | `S ci_verify`, then `actionlint` | `internal/cli/ci.go`, `.github/workflows/{bump,shas,canary-pins,kit-compat}.yml`, `testdata/script/ci_verify.txtar` | M8.1 |
| M8.3 | Release pipeline (extends M0.8): a script that enforces lockstep minors, the kit tag, syft SBOM, cosign keyless signing, attestation, assets (templates tarball, `compat.json`, `stack-versions.json`), the Homebrew tap, `install.sh`, the GHCR CI image, and a CHANGELOG template | `goreleaser release --snapshot --clean`, then `actionlint` | `.goreleaser.yaml`, `scripts/release/`, `install.sh`, `Dockerfile.ci`, `.github/workflows/release.yml`, `CHANGELOG.md` | – |

The M8 pre-seed adds ADR 0012 (release assets, signing, the installer, the generated workflows, and `spinup ci`) and the stub package `internal/ci`.

#### M9 — Modules and plugins → a third-party pack adds a generator and upgrades cleanly

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M9.1 | Module registry, `module list`, and `module add` (through the upgrade engine) | `T ./internal/modules/...`, then `S module_add` | `internal/modules/`, `internal/cli/module.go`, `internal/upgrade/module*.go` | – |
| M9.2 | Kit `mail`, the `mail` pack, `g mailer`, and password reset through an auth `pack.d` fragment | `T ./kit/mail/... ./internal/gen/mailer/...`, then `G mail` | `kit/mail/`, `packs/mail/`, `packs/auth/pack.d/reset.toml` with its templates, `internal/gen/mailer/`, `testdata/golden/mail/` | – |
| M9.3 | OIDC in kit `auth` | `T ./kit/auth/ -run OIDC` | `kit/auth/oidc*.go`, `packs/auth/pack.d/oidc.toml` with its templates | M9.2 |
| M9.4 | Tier-1 pack plugins: fetch, hash, allowlist, lock, and `plugin add/remove/list/verify/info/update` | `T ./internal/plugin/...`, then `S plugin` | `internal/plugin/pack/`, `internal/cli/plugin.go`, `testdata/script/plugin.txtar`, `testdata/plugins/`, `docs/policy/plugins.md` | – |
| M9.5 | Plugin packs in `new`, render, `upgrade`, and declarative generators; the plugin matrix test | `T ./internal/upgrade/ -run MatrixPlugin`, then `S plugin_render` | `internal/plugin/render/`, the upgrade source and plan files, `internal/upgrade/matrix_plugin_test.go`, `testdata/script/plugin_render.txtar` | M9.1, M9.4 |

The M9 pre-seed adds ADR 0013, the plugin contract `internal/plugin/contract.go`, go-mail, go-oidc, and oauth2 in the kit and the app lockfiles, and stub packages.

#### M10 — Deferred full-depth items (last, and the core does not depend on them)

| Unit | Outcome | Check | File set | Deps |
|---|---|---|---|---|
| M10.1 | JSON-RPC host for tier-2 plugins, and the `plugin-sdk` module (the pre-seed writes its `go.mod`) | `T ./internal/plugin/rpc/...`, then `cd plugin-sdk && go test ./...` | `internal/plugin/rpc/`, `plugin-sdk/` (not `go.mod` or `go.sum`) | – |
| M10.2 | Conformance suite with malicious cases, an example plugin, `plugin new`, and `g plugin` | `T ./plugin-sdk/conformance/...`, then `S plugin_new` | `plugin-sdk/conformance/`, `examples/plugin/`, `internal/cli/plugin_new.go`, `internal/gen/plugin/`, `testdata/script/plugin_new.txtar` | M10.1 |
| M10.3 | `--frontend-mode=embed` (questions in `pack.toml`) | `G web-embed` | `packs/web-embed/`, `testdata/golden/web-embed/` | – |
| M10.4 | `--pm bun` and `--client heyapi` | `G web-variants` | `packs/web/variants/`, `packs/web/pack.d/variants.toml`, `testdata/golden/web-variants/` | – |
| M10.5 | OpenTofu pack for hcloud | `G tofu` | `packs/tofu/`, `testdata/golden/tofu/` | – |
| M10.6 | `self-update` with cosign verification and Homebrew detection | `T ./internal/selfupdate/...` | `internal/selfupdate/`, `internal/cli/selfupdate.go` | – |
| M10.7 | WAL-G backend | `T ./internal/backup/walg/...` | `internal/backup/walg/` | – |
| M10.8 | In-place `pg_upgrade` | `T ./internal/maintain/pgupgrade/...` | `internal/maintain/pgupgrade/`, `internal/cli/maintenance_pgupgrade.go` | – |
| M10.9 | Docs site from `compat/` and `stackversions`, and `docs compat` | `make doctest` | `docs/site/`, `internal/cli/docs.go` | – |

Note: `packs/` means `internal/templates/packs/`.

**M11 — Stabilize** runs without orchestrate. Keep two minors with no breaking kit change. Pass the deprecation tests. The dogfood app must upgrade across two or more minors. Then freeze v1.0.

## Verification

- **Per unit:** the orchestrate Phase 5 runs the unit's Check column, plus `make build lint`. Lanes do not self-certify.
- **Per milestone:** the gate in step 3 of the run protocol must pass before the user merges `orch/<run-id>`. From M2, the upgrade matrix runs on every PR.
- **End to end at v0.x:**
  1. `spinup new demo && cd demo && spinup dev`, then log in through the browser.
  2. `spinup g resource post title:string` gives a CRUD page.
  3. `spinup deploy --env staging` deploys to the DinD VM.
  4. Kill a container, and see the alert in Grafana.
  5. `spinup backup verify` passes.
  6. Build the CLI at v+1 and run `spinup upgrade` on `demo` with local edits. Expect only the planned conflicts, then a green verify.
- **Corpus:** from v0.3, freeze the output of each release in `testdata/corpus/`. Nightly runs upgrade every supported corpus to `main`.
