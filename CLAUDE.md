# spinup

spinup is a Buffalo-style CLI. It creates and operates a full stack: a Go API, a SvelteKit web app, PostgreSQL, a Docker Compose deploy to a VM, and OpenTelemetry monitoring with Grafana LGTM. Generated apps must stay easy to upgrade for years.

- CLI module: `github.com/stopyellingatme/spinup` (repository root)
- Runtime library ("kit"): `github.com/stopyellingatme/spinup/kit` (directory `kit/`, its own `go.mod`)
- `go.work` is committed. It joins the root module and `kit/`.

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
- `internal/doctest` has `TestDocs`.

## Toolchain

| Tool | Version | Note |
|---|---|---|
| Go | 1.27.2 | `/opt/homebrew/bin/go`. `GOTOOLCHAIN=local` always. Modules declare `go 1.26.0`. |
| Node | 24.x | `/opt/homebrew/opt/node@24/bin`. The Makefile puts it on `PATH`. Do not call a bare `node` in an interactive shell. It is a broken nvm function. |
| golangci-lint | 2.14.0 | config: `.golangci.yml` |
| goreleaser, actionlint, syft, cosign, promtool, sops, age, docker | see `scripts/tools-check.sh` | |

## Lane environment (orchestrate lanes)

- Lanes run with `GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOPROXY=off`. The module cache already holds every dependency of the milestone.
- If you need a module or npm package that is not in `go.mod` or the lockfile, stop. Report `blocked: <module>`. Do not add dependencies. Do not run `go get`, `go mod tidy`, `npm install`, or `pnpm add`.
- Checks marked † in the plan need Docker, Postgres, browsers, or network access. In a lane, run only their static part. The main loop runs the full check at the milestone gate.
- When a check needs Postgres, use `DATABASE_URL` from the environment. Never start your own database.

## Shared files (main loop only)

Only the main loop writes these files. A unit never edits them:
- `go.mod`, `go.sum`, `go.work`, `go.work.sum`, `kit/go.mod`, `kit/go.sum`, and every `deps_m*.go` file
- template-pack lockfiles (`go.sum`, `tools.sum`, `pnpm-lock.yaml` under `internal/templates/packs/`), plus `go.mod.tmpl`, `tools.mod.tmpl`, and `pack.d/deps.toml` in the `api` pack
- `internal/stackversions/stackversions.yaml`
- `Makefile`, `scripts/tools-check.sh`, `scripts/bump-stack.sh`
- `cmd/spinup/wire_gen.go`
- `CLAUDE.md`, `docs/design/plan.md`

The pre-seed commit creates stub files (`doc.go`, or a stub `main.go`) for the packages of a milestone. A stub belongs to the unit whose file set contains it. That unit replaces the stub.

## Rules for code

- **Registration.** Commands, doctor checks, generators, resource parts, upgrade flags, and deploy hooks register themselves with `Register(...)` in `init()`. No unit edits a central list. Packages outside `internal/cli` reach the binary through the blank imports in `cmd/spinup/wire_gen.go`.
- **Layering.** `internal/cli` holds only cobra wiring, not logic. Generators return a `vfs.Plan` and never write to disk. Only the apply step writes. Every subprocess call goes through `internal/exec`. `merge`, `vfs`, and `render` never import `cli`.
- **File classes.** Every template file is `managed`, `scaffold`, or `seed`, and its `pack.toml` (or a `pack.d/*.toml` fragment) declares the class. If you are not sure, use `managed` with an extension point.
- **Errors.** Every user-facing error says what failed and why, and ends with a `Try:` line. Every prompt has a flag. With no TTY, the CLI never prompts. It fails and names the missing flag.
- **Determinism.** A render is a pure function of the template version, the answers, the plugin set, and the stack versions. Do not use the clock, random values, or map iteration order in rendered output.
- **Docs.** Each command or feature unit adds its own page under `docs/` with runnable code blocks. `make doctest` runs them.
- **No CGO.** No custom DSL, no ORM, no telemetry from the CLI.

## Orchestrate lanes: commit discipline

Before you return:
1. Commit your work in your worktree. Start the commit message with `orch: `.
2. Leave no uncommitted changes.
3. Report your branch, the commit sha, the files you touched, the checks you ran with their results, and anything you did not do.

Write only inside your unit's declared file set, plus `_test.go` files in your own directories and the `testdata` paths that your row names.
