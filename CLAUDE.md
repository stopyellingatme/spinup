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

Check forms that the plan uses:
- `T <pkgs>` means `go test -race <pkgs>`.
- `S <name>` means `make script RUN=<name>`.
- `G <pack>` means `make golden PACK=<pack>`.
- Every unit check also implies `make build lint`.

Test contracts:
- `TestGolden*` tests read the environment variables `SPINUP_GOLDEN_PACK` (narrow to one pack) and `SPINUP_GOLDEN_UPDATE` (`1` rewrites the golden files).
- `internal/testutil/scripttest` has `TestScripts`. It builds the `spinup` binary and runs `testdata/script/*.txtar`. It reads `SPINUP_SCRIPT_RUN` (a glob) and skips `app_gate_*` scripts unless `SPINUP_APP_GATE=1`.
  Scripts can use `[env:NAME]` (true when `NAME` is set), `$SPINUP_REPO` (the repository root), and `mkversion <version> [overlay-dir]` (writes a synthetic template version under `$WORK/templates` for `SPINUP_TEMPLATES_DIR`; see the harness doc comment). Git commits work: the harness sets a git identity. With `DATABASE_URL` set, each script gets its own new database (created with `psql`, dropped at the end). `pnpm` (scripts/bin) uses the real home through `SPINUP_PNPM_HOME`, so offline installs work, and `DOCKER_CONFIG` points at the real `~/.docker`, so the docker CLI finds its context.
- `internal/doctest` has `TestDocs`.

## Toolchain

| Tool | Version | Note |
|---|---|---|
| Go | 1.27.2 | `/opt/homebrew/bin/go`. `GOTOOLCHAIN=local` always. Modules declare `go 1.26.0`. |
| Node | 24.x | `/opt/homebrew/opt/node@24/bin`. The Makefile puts it on `PATH`. Do not call a bare `node` in an interactive shell. It is a broken nvm function. |
| pnpm | 12.11.2 | `scripts/bin/pnpm` runs it through corepack (no global install). The Makefile puts `scripts/bin` on `PATH`. Use `$REPO/scripts/bin/pnpm` outside make. Offline installs: `pnpm install --frozen-lockfile --offline`. |
| golangci-lint | 2.14.0 | config: `.golangci.yml` |
| goreleaser, actionlint, syft, cosign, promtool, sops, age, docker | see `scripts/tools-check.sh` | |

## Lane environment (orchestrate lanes)

- Lanes run with `GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off`. The module cache already holds every dependency of the milestone.
- If you need a module or npm package that is not in `go.mod` or the lockfile, stop. Report `blocked: <module>`. Do not add dependencies. Do not run `go get`, `go mod tidy`, `npm install`, or `pnpm add`.
- Checks marked † in the plan need Docker, Postgres, browsers, or network access. In a lane, run only their static part. The main loop runs the full check at the milestone gate.
- When a check needs Postgres, use `DATABASE_URL` from the environment. Never start your own database.

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

The pre-seed commit creates stub files (`doc.go`, or a stub `main.go`) for the packages of a milestone. A stub belongs to the unit whose file set contains it. That unit replaces the stub.

## Rules for code

- **Registration.** Commands, doctor checks, generators, resource parts, upgrade flags, and deploy hooks register themselves with `Register(...)` in `init()`. No unit edits a central list. Packages outside `internal/cli` reach the binary through the blank imports in `cmd/spinup/wire_gen.go`.
- **Layering.** `internal/cli` holds only cobra wiring, not logic. Generators return a `vfs.Plan` and never write to disk. Only the apply step writes. Every subprocess call goes through `internal/exec`. `merge`, `vfs`, and `render` never import `cli`.
- **File classes.** Every template file is `managed`, `scaffold`, or `seed`, and its `pack.toml` (or a `pack.d/*.toml` fragment) declares the class. If you are not sure, use `managed` with an extension point.
- **Errors.** Every user-facing error says what failed and why, and ends with a `Try:` line. Every prompt has a flag. With no TTY, the CLI never prompts. It fails and names the missing flag.
- **Determinism.** A render is a pure function of the template version, the answers, the plugin set, and the stack versions. Do not use the clock, random values, or map iteration order in rendered output.
- **Pack changes.** `TestPacksDigest` guards `internal/templates/packs.digest`. If you change a template pack, run `SPINUP_GOLDEN_UPDATE=1 go test ./internal/templates -run PacksDigest`. Then replace `REVIEW` with the ID of the codemod that migrates existing apps, or with `none` when existing apps need no codemod (a new file, or a change the 3-way merge handles). See `docs/contributing/upgrade-matrix.md`.
- **Docs.** Each command or feature unit adds its own page under `docs/` with runnable code blocks. `make doctest` runs them.
- **No CGO.** No custom DSL, no ORM, no telemetry from the CLI.

## Orchestrate lanes: commit discipline

Before you return:
1. Commit your work in your worktree. Start the commit message with `orch: `.
2. Leave no uncommitted changes.
3. Report your branch, the commit sha, the files you touched, the checks you ran with their results, and anything you did not do.

Write only inside your unit's declared file set, plus `_test.go` files in your own directories and the `testdata` paths that your row names.
