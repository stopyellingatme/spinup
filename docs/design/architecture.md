# spinup: unified final plan (2026-10-13)

## 1. Scores

| Design | Maintainability | DX | Ops | Feasibility | Reason |
|---|---|---|---|---|---|
| maintainability-first | 9 | 6 | 6 | 8 | It has the best backbone: a small surface, git-only merge, a frozen upgrade corpus, and a canary job. Its dev UX and ops depth are thin. |
| dx-first | 7 | 9 | 7 | 6 | It has the best command UX: "Try:" errors, a picker, a `/_dev` page, and `upgrade --continue/--abort`. It owns more code: a watcher, a Go merge fallback, and an embedded sops library. |
| ops-first | 7 | 7 | 9 | 6 | It has the best deploy, rollback, backup, and alert detail, and the best exit codes. It is the widest plan, with nested modules and many dependencies. |

**Backbone:** maintainability-first, which sets the file layout, upgrade engine, and test spine. **Graft from dx-first:** UX rules, `upgrade --continue/--abort`, upgrade branches, and the `/_dev` page. **Graft from ops-first:** environments, exit codes, the pre-deploy backup, `deployx`, the `maintenance` group, and the alert list.

## 2. Conflict resolutions (final)

| Conflict | Decision | Reason |
|---|---|---|
| Merge engine | Use `git merge-file --diff3 --diff-algorithm=histogram`. Keep a `Merger` interface. Ship no Go fallback in v1. | Git is already required for the clean-tree check. This removes a low-activity dependency. `doctor` fails if `git` is missing. |
| Hot reload | Run `air` as a `go tool` directive in the app. The fallback is `wgo`. | The CLI owns no watcher. The `dev` supervisor only starts and stops processes. |
| Runtime modules | One module `github.com/stopyellingatme/spinup/kit`, with no nested modules. | This avoids version skew (the Buffalo lesson). Go module pruning keeps unused dependencies out of the build. |
| Release coupling | Lockstep. CLI `vX.Y.Z` and `kit/vX.Y.Z` ship from one tag run. | This removes the compatibility matrix. A patch may ship alone. The CLI refuses a kit with a different minor. |
| Secrets tools | Use the `sops` and `age` binaries, installed by `spinup tools install` with checksum checks. | The sops library has a large dependency tree. |
| BASE source | Re-render the old template. Do not store BASE in the project. | The render is a pure function. |
| Template retention | Embed the last 3 template versions as zstd tarballs. Download older ones from the release and verify the checksum. | This controls binary size. |
| File classes | `managed`, `scaffold`, `seed`. `eject` turns managed into scaffold. | The runtime kit is class A and is not a file. |
| Project layout | `api/` and `web/` subdirectories, with one root `spinup.toml`. | The two toolchains stay separate. |
| Metrics store | Prometheus. Mimir is optional and later. | It is simpler on one VM. |
| Collector | Grafana Alloy. | It is an OTel distribution and is in the research. |
| Frontend mode | `node` (adapter-node) is the default. `embed` comes in M10. | SSR works in `node` mode. |
| Format step | Go output goes through `go/format`. Other files are written final by the template and are not formatted. | This keeps BASE and THEIRS stable. |
| Windows | Best effort for `new`, `generate`, `build`. | `ssh` and `git` paths are not tested there. |
| Plugins | Template packs first. Executable plugins use JSON-RPC over stdio. Declare them in `spinup.toml` with a checksum. | Never scan `PATH`. |
| Behavior gating | `behavior_version` in the lock and `spinup.toml`. | A kit bump never changes behavior silently. |
| Go versions | The app has `go 1.26` and `toolchain go1.27.2`. CI tests 1.26 and 1.27. | This follows the support policy. |
| SvelteKit 3 age | Go now with exact pins and a nightly canary job. | Kit 3.0.1 is 7 days old at this date. The user chose SvelteKit. |

## 3. Stack manifest (pinned in `stackversions.yaml`; re-verify on the day of the first commit)

| Area | Tool | Version | Role |
|---|---|---|---|
| Language | Go | 1.27.2 (min 1.26) | CLI and API |
| CLI | cobra | v1.10.2 | commands |
| CLI | huh | v1.0.0 | prompts |
| CLI | bubbletea/v2 | v2.1.0 | `dev` and `monitor` views only |
| Release | GoReleaser | v2.18.3 | builds, SBOM, cosign |
| API | `net/http` ServeMux | stdlib | router |
| API | oapi-codegen | v2.8.0 | strict server |
| DB | PostgreSQL | 18.6 | database (16 and 17 allowed) |
| DB | pgx/v5 | v5.11.0 | driver (above the CVE-2026-41889 fix) |
| DB | goose/v3 | v3.29.0 | SQL migrations |
| DB | sqlc | v1.31.1 | typed queries |
| Jobs | River | v0.49.0 | wrapped, exact pin |
| Auth | scs/v2 | v2.9.0 | sessions |
| Auth | go-oidc/v3 and x/oauth2 | v3.21.0, v0.37.0 | OIDC |
| Mail | go-mail | v0.8.1 | SMTP |
| Test | testcontainers-go | v0.44.0 | integration tests |
| Dev reload | air | v1.67.4 | `go tool air` |
| Telemetry | opentelemetry-go | v1.47.0 | SDK |
| Telemetry | otelhttp, otelpgx | v0.72.0, v0.13.0 | instrumentation |
| Frontend | SvelteKit | 3.0.1 | web |
| Frontend | Svelte, Vite | 5.57.x, 8.3.x | web |
| Frontend | adapter-node | 6.0.0 | web runtime |
| Frontend | Node | 24 LTS (floor 22.17) | Dockerfile |
| Frontend | TypeScript | 6 or later | types |
| Frontend | pnpm | 12.11.2 | package manager |
| Frontend | Tailwind | 4.3.3 | CSS |
| Frontend | shadcn-svelte, bits-ui | 1.7.0, 2.19.5 | UI |
| Client | openapi-typescript, openapi-fetch | 7.13.0, 0.17.0 | typed client |
| Client | hey-api | 0.99.0 | opt-in only |
| Test | vitest, Playwright | 5.0.4, 1.64.0 | web tests |
| Deploy | Docker Compose | v5.6.0 | on the VM |
| Deploy | Caddy | 2.11.7 | proxy and TLS (digest pin) |
| Secrets | sops, age | 3.13.3, 1.3.2 | binaries |
| Backup | pgBackRest | 2.59.3 | default backend |
| Backup | WAL-G | v3.0.9 | second backend, after v1 |
| Monitoring | Alloy | 1.20.1 | collector |
| Monitoring | Loki, Tempo | 3.7.8, 3.1.0 | logs, traces |
| Monitoring | Prometheus, Grafana | 3.15.0, 13.2.4 | metrics, UI |
| Dev monitoring | grafana/docker-otel-lgtm | 0.36.0 | dev only, digest pin |
| Provision | OpenTofu, hcloud provider | 1.13.1, 1.70.0 | optional pack |
| Maintenance | Renovate | 44.x | config generated |
| Maintenance | Trivy | 0.75.0 | pinned container, not the action |

Rules: pin all images by tag and digest. Never use `latest`. Pin all GitHub Actions by full commit SHA. A weekly job opens a PR that bumps `stackversions.yaml`. The same file feeds templates, `doctor`, and the docs.

## 4. Principles

1. Own the glue, not the tools. We own the render engine, the lock and merge orchestration, the codemod list, the deployer, and the plugin host.
2. Keep the scaffold thin and the kit fat. If two apps differ only by config values, the code belongs in the kit.
3. Make a file class decision before you write any template.
4. A render is a pure function of (template version, answers, plugin set).
5. No CGO. No custom DSL. No custom ORM. No JS bundler config in the CLI.
6. Every error says what failed, why, and a `Try:` line.
7. Every prompt has a flag. With no TTY the CLI never prompts. It fails and names the missing flag.
8. Core works with zero plugins.

## 5. Command tree

Global flags: `--dir/-C`, `--env`, `--yes/-y`, `--non-interactive`, `--json`, `--dry-run`, `--verbose`, `--no-color`.

Exit codes: 0 ok, 1 error, 2 usage, 3 conflicts remain, 4 drift found (`check`), 5 deploy lock held.

Command groups: Develop, Data, Ship, Operate, Maintain. `spinup` with no arguments prints a getting-started card. `spinup g` with no kind opens a picker.

```
new <name>        --module --frontend-mode node|embed --client openapi-fetch|heyapi --pm pnpm|bun
                  --monitoring full|lite|none --modules backups,ci,mail,jobs,auth --no-git --template-version
dev               --no-web --no-infra --lgtm --port --watch air|wgo
generate|g <kind> resource|model|handler|migration|job|mailer|page|component|client|openapi|dashboard|alert|plugin
                  --dry-run --diff --force
destroy <kind> <name>        removes only files whose hash still matches the lock
db                migrate up|down|status|redo|new|to | seed | reset(dev only) | psql | dump | restore | lint | doctor | sqlc
build             --target api|web|all --push --tag --platform
test              --api --web --e2e --contract --watch
routes
deploy            --env --tag --plan --skip-migrate --skip-backup --lock-timeout --no-auto-rollback --allow-destructive
rollback          --env --to <release> --with-db-plan
releases          list|show|prune
server            bootstrap|audit|ssh|logs <svc>|exec|ps|reboot
secrets           init|edit|get|set|rotate|rekey|push|diff
backup            now|list|verify|restore --to <time> [--into new|in-place]|schedule|status|prune
monitor           open|status|logs|trace|alerts|test-alert|silence|dashboards sync
maintenance       patch|vuln-scan|prune|vacuum|pg-minor-upgrade|pg-major-upgrade|certs|window on|off
ci                generate|verify
upgrade           --plan|--apply|--to <v>|--continue|--abort|--allow-dirty|--force-managed|--no-verify|--commit-conflicts
diff | check | doctor [--fix --remote --quick]
restore <path> | eject <path>
plugin            list|add|remove|update|verify|info|new
tools             install|list           (checksum-verified sops, age, and similar)
self-update [--check --channel stable|next] | version [--check] | docs compat | completion
```

`db migrate down` is blocked in production without `--i-know`. `rollback` never runs `down`.

## 6. Repository layouts

### 6.1 CLI repository (one repo, two Go modules)

```
spinup/
  go.mod                         # module github.com/stopyellingatme/spinup (CLI)
  cmd/spinup/main.go             # build info, calls cli.Execute
  internal/
    cli/       # cobra wiring, no logic
    ui/        # TTY detection, huh, error renderer with "Try:", --json
    project/   # find root, load spinup.toml
    manifest/  # spinup.lock schema, hashing, classes, tombstones, schema migration
    vfs/       # in-memory FS (~200 lines): stage, diff, dry-run, atomic apply
    render/    # text/template with [[ ]], deterministic output, go/format
    templates/ # embed: zstd tarballs per version, pack.toml per pack, renames.toml
    gen/       # generator registry
    modules/   # built-in packs: backups ci secrets mail jobs auth (same format as plugins)
    merge/     # Merger interface, gitmerge impl
    upgrade/   # planner, chain, renames, orphans, tombstones, verify
    migrate/   # codemods NNNN_slug.go
    codemod/   # go/ast helpers, comment-preserving yaml/toml/json editors
    devloop/   # process supervisor, log mux, port allocator
    db/        # goose glue, sqlc runner, lint rules
    deploy/ remote/ server/ secrets/ backup/ monitor/ maintain/ ci/
    plugin/    # host, protocol, checksum store
    doctor/    # check registry
    exec/      # the only subprocess wrapper: redaction, timeouts, JSON parse
    selfupdate/ compat/ stackversions/ testutil/
  kit/                           # own go.mod: github.com/stopyellingatme/spinup/kit
  plugin-sdk/                    # own go.mod: protocol types, Serve(), conformance kit
  testdata/{golden,corpus,migrations}/
  docs/  .goreleaser.yaml  .github/workflows/  MAINTAINERS.md  SECURITY.md  CODEOWNERS
```

Rules: `internal/cli` holds no logic. Generators return a `vfs.Plan` and never write to disk. Only `render.Apply` writes. All subprocess calls go through `internal/exec`. `merge`, `vfs`, and `render` do not import `cli`.

### 6.2 Generated app

```
myapp/
  spinup.toml           scaffold   app config: envs, modules, ports, profile, behavior_version, [[plugins]]
  spinup.lock           CLI-owned  JSON manifest, committed, never hand-edited
  .env.example  .gitignore  README.md(seed)
  api/
    go.mod              kit import plus tool directives: sqlc, oapi-codegen, goose, air
    cmd/api/main.go     scaffold   ~30 lines: kit/app.New(...).Run()
    cmd/worker/main.go  scaffold   if jobs module is on
    internal/{app/routes.go(scaffold; has `// spinup:routes` marker), handlers/, store/gen/(managed), jobs/, mail/}
    internal/gen/api.gen.go        managed
    db/{migrations/*.sql (seed, then user-owned), queries/*.sql (scaffold)}
    openapi.yaml        scaffold   contract, source of truth
    sqlc.yaml  oapi-codegen.yaml   managed        .air.toml  managed
  web/
    package.json (exact pins, imports "#lib/*", packageManager)   vite.config.js (sveltekit({...}) options)
    src/{hooks.server.ts, instrumentation.server.ts}              scaffold
    src/lib/api/client.ts scaffold   src/lib/api/schema.d.ts managed
    src/lib/components/ui/*  scaffold (shadcn copies, tracked one by one)   components.json scaffold
    src/routes/healthz/+server.ts managed      src/routes/** user-owned
    tests/ scaffold    Dockerfile managed
  deploy/
    compose.yml managed (includes compose.override.yml)   compose.override.yml seed
    compose.dev.yml managed    Caddyfile managed (imports Caddyfile.d/*.caddy)   Caddyfile.d/ seed
    Dockerfile.api  Dockerfile.postgres(pgBackRest)  managed
    environments/{staging,production}.toml scaffold
    cloud-init/user-data.yaml.tmpl managed     infra/tofu/ optional pack
  monitoring/
    alloy/ loki/ tempo/ prometheus/ blackbox/            managed
    grafana/provisioning/{datasources,dashboards,alerting}/  managed
    grafana/dashboards/stack/ managed     grafana/dashboards/custom/ seed, CLI never touches
    alerts/custom.yml seed
  secrets/{env}.enc.yaml seed     .sops.yaml scaffold
  .github/workflows/{ci,e2e,deploy,backup-verify,upgrade-check}.yml  managed
  renovate.json scaffold (user extension block)
  .spinup/ git-ignored: template cache, last plan, dev.json
```

Extension points for managed files: `compose.override.yml`, `Caddyfile.d/`, `dashboards/custom/`, `alerts/custom.yml`, `spinup.toml`. If the user edits a managed file, `check` warns and suggests `spinup eject <path>`.

## 7. Runtime library `github.com/stopyellingatme/spinup/kit`

| Package | Content |
|---|---|
| `app` | `New(opts...)`, `Run(ctx)`, graceful shutdown, `/healthz`, `/readyz`, `/version` |
| `config` | typed env loader, `*_FILE` secrets, `behavior_version` gate |
| `httpx` | ServeMux helpers, request ID, recover, CORS, CSRF, rate limit, security headers, problem+json, OpenAPI validator hook |
| `db` | pgxpool, tx helper, otelpgx, goose provider, migration runner with advisory lock |
| `auth` | argon2id, scs sessions with pgxstore, OIDC, `GET /api/session` |
| `jobs` | River wrapper, periodic jobs, `rivermigrate` hook |
| `mail` | `Mailer` interface, SMTP, templates, job-based send |
| `telemetry` | all OTel imports, OTLP exporters, slog bridge, otelhttp wrapper |
| `logx` | slog JSON with trace correlation |
| `backup` | pg_dump helpers, S3 upload, last-backup-age metric from pgBackRest `info` |
| `secrets` | file, env, and sops-decrypted loaders |
| `deployx` | the deploy contract: health, version, migration-state endpoints |
| `devtools` | `/_dev` page: routes, masked config, last 50 requests with trace links, pending migrations, mail previews. Compiled out unless `SPINUP_ENV=dev`. |
| `testkit` | testcontainers Postgres, template-DB cloning, HTTP helpers |
| `x/`, `internal/` | unstable APIs, private code |

Policy:
- Stay on v0.x until two minors pass with no breaking change. Avoid `/v2`. If it is unavoidable, ship an import-rewrite codemod.
- Deprecate with `// Deprecated:` plus `//go:fix inline`. Keep the old API for one minor. The runtime logs one startup warning for a deprecated path.
- Wrap River, OTel, and pgx types. Keep OTel imports in `telemetry` only.
- Keep kit `go.mod` requirements at minimum versions, so the app controls `go get -u`.
- Each release ships `kit/MIGRATION.json` that `upgrade --plan` shows.
- Support the last two minors and Go 1.26 and 1.27. On every kit change, CI builds the previous frozen app against the new kit.
- Add golden tests for the API error format (JSON v2 in Go 1.27 changes error text).

## 8. Manifest and lock file

`spinup.toml` is user config. `spinup.lock` is JSON and the CLI writes it.

```json
{
  "schema": 1,
  "cli_version": "0.4.0",
  "template_version": "0.4.0",
  "min_cli": "0.4.0",
  "kit": {"module": "github.com/stopyellingatme/spinup/kit", "version": "v0.4.0"},
  "behavior_version": 3,
  "answers": {"app_name":"blog","module":"github.com/acme/blog","db_version":"18",
              "frontend_mode":"node","client":"openapi-fetch","pm":"pnpm",
              "monitoring":"full","modules":["backups","ci"]},
  "stack_versions": {"postgres":"18.6","caddy":"2.11.7@sha256:...","grafana":"13.2.4"},
  "plugins": [{"name":"stripe","version":"0.3.1","sha256":"...","api":1}],
  "files": {
    "deploy/compose.yml":   {"class":"managed","template":"deploy/compose.yml.tmpl","hash":"sha256:...","mode":"0644"},
    "api/cmd/api/main.go":  {"class":"scaffold","template":"api/cmd/api/main.go.tmpl","hash":"sha256:...","mode":"0644"},
    "api/db/migrations/00001_init.sql": {"class":"seed"}
  },
  "tombstones": ["web/src/lib/components/ui/badge.svelte"],
  "orphans": [],
  "applied_migrations": ["0001-rename-x","0003-bump-alloy-config"]
}
```

- `hash` is the sha256 of the file as the generator wrote it. A disk hash equal to the lock hash means "unchanged".
- Each template pack has a `pack.toml` that lists every file with its class and optional `since` and `renamed_from`. This is the decision point for every future file. If unsure, choose `managed` with an extension point.
- `renames.toml` holds `{from, to, since}`.

## 9. Upgrade algorithm (`spinup upgrade`)

1. **Preconditions.** Need a git repository, a clean tree (unless `--allow-dirty`), a valid lock, and `git` and `go` on `PATH`. Check `min_cli`. If the CLI is older than the lock, stop and say `Try: spinup self-update`.
2. **Branch.** Create `spinup/upgrade-<to>`. If the project is more than one minor behind, chain the steps (0.3 to 0.4 to 0.5). Run steps 3 to 11 for each step. Make one commit per step.
3. **Plan.** Print the changelog, `kit/MIGRATION.json` steps, pending codemods, files grouped by outcome (overwrite, merge, rename, delete, orphan), and new `behavior_version` options. `--plan` writes `.spinup/plan.json` and stops. It changes no project file.
4. **Before codemods.** Run `Before`-stage migrations. They change answers or move files, so the OLD render stays valid.
5. **Render.** Render OLD (old pack, old answers) and NEW (new pack, migrated answers) into the `vfs`. The packs come from the embedded tarballs or from the cache after a checksum-checked download.
6. **Decide each file:**
   - Hash on disk equals lock hash: overwrite with NEW. No merge.
   - `managed` with a hash mismatch: warn and show the diff. Overwrite only with `--force-managed`.
   - `scaffold` with a hash mismatch: run `git merge-file -p --diff3 --diff-algorithm=histogram -L ours -L base -L theirs ours base theirs`. Exit code 0 is clean. A positive code is the conflict count. A negative code is an error. Write inline markers on conflict. Never write `.rej` files.
   - `seed`: skip.
   - Tombstone: skip. `spinup restore <path>` removes the tombstone.
   - Renamed: BASE is the OLD render at `from`, OURS is the file at `from`, THEIRS is the NEW render at `to`. Write at `to`, remove `from`.
   - Deleted in the template: delete if the hash matches. Otherwise keep the file and add it to `orphans`.
   - New in the template but the path exists: BASE is empty, OURS is the existing file, and the result is a conflict.
   - Binary files, and text files over 1 MiB: overwrite or skip only.
   - Mode bits: compare and apply them apart from content. Normalize line endings before the merge.
7. **Apply.** Show the summary. Ask for confirmation in a TTY, or accept `--yes`. Write the `vfs` to the tree.
8. **Dependencies.** Run `go get github.com/stopyellingatme/spinup/kit@vX`. Run `go get -u` for the tool directives. Run `go mod tidy` and `go fix ./...`. Run `pnpm install`.
9. **After codemods.** Run `After`-stage migrations. Run `sqlc generate`, `oapi-codegen`, and `spinup g client`. On a SvelteKit major bump, run `sv migrate`. Judgment-only changes go to `.spinup/UPGRADE_NOTES.md`. This is never on the critical path.
10. **Lock.** Write the lock (versions, hashes, applied migrations, stack versions) only if there are no errors.
11. **Verify** (skip with `--no-verify`): `go build ./...`, `go vet ./...`, `go test ./...`, `pnpm check`, `govulncheck`, and `spinup doctor`. Print each failure with the migration ID that last touched the file.
12. **Finish.** If `<<<<<<<` markers remain, exit 3 and do not commit (unless `--commit-conflicts`). The user fixes the files and runs `spinup upgrade --continue`. This re-checks the markers, writes the lock, and commits. `--abort` runs `git reset --hard` on the upgrade branch after confirmation.

**Codemod interface:**

```go
type Migration struct {
    ID, Since, Desc string
    Stage Stage                          // Before | After
    Plan(*Ctx) ([]Change, error)         // pure, listable
    Apply(*Ctx) error                    // idempotent
}
```

Run a migration when `old < Since <= new` and its ID is not in `applied_migrations`. Use `go/ast` or `x/tools/go/analysis` for Go. Use `//go:fix inline` in the kit whenever it can express the change. Use comment-preserving structured editors for YAML, TOML, and JSON. Never use regular expressions on code.

**Related commands:**
- `diff`: render the current version with the lock answers and diff against disk.
- `check` (CI): fail (exit 4) on managed drift, leftover markers, an invalid lock, a stale generated client, or a template version that is behind.
- `eject <path>`: change managed to scaffold in the lock.

## 10. Generators

A generator is a Go function `func(ctx, in) (vfs.Plan, error)`. It can compose other generators. Every generator supports `--dry-run`, `--diff`, and `--force`. It records its files in the lock with a class. After it runs, the CLI runs `sqlc generate`, `oapi-codegen`, the client generator, and `gofmt`. Then it prints two `Next:` commands.

| Generator | Output |
|---|---|
| `new` | The whole project. It composes the backend, frontend, deploy, monitoring, and ci packs. |
| `resource name field:type...` | Goose migration, sqlc queries, OpenAPI paths and schemas, handler stub, route registration at the marker, SvelteKit list, show, and form pages, API and web tests. Flags: `--api-only`, `--no-pages`, `--auth required\|none`, `--soft-delete`. |
| `model` | Migration, queries, and OpenAPI schema only. |
| `handler`, `page`, `component` | One piece each. `component` wraps `shadcn-svelte add`. |
| `migration add_x` | Empty goose SQL file. |
| `job` | River worker, args struct, test. |
| `mailer` | Template, typed `Mailer` method, Mailpit preview route in dev. |
| `client` | Regenerates `schema.d.ts`. |
| `openapi` | Starter spec. |
| `dashboard`, `alert` | Files in the `custom/` folders. |
| `plugin` | Plugin skeleton with a conformance test. |

Field types: string, text, int, bigint, bool, float, decimal, time, date, uuid, json, `ref:<model>`, `enum:a|b|c`. One table maps each type to SQL, Go, and OpenAPI and TS. A marker in a scaffold file (`// spinup:routes`) is the insert point. If the marker is missing, the generator prints the snippet and the exact place to paste it. It never fails silently. Keep the generated surface small. Auth is an opt-in module.

## 11. Dev loop (`spinup dev`)

1. Run `doctor --quick`: Go, Node 22.17 or later, Docker, ports, and `.env`.
2. Run `docker compose -f deploy/compose.dev.yml up -d` for Postgres 18 and Mailpit. With `--lgtm`, add `grafana/otel-lgtm` v0.36.0. Wait for the health checks.
3. Run migrations through the kit provider, and River migrations if the jobs module is on.
4. Start `go tool air`. On a change in `queries/*.sql` run `sqlc`. On a change in `openapi.yaml` run `oapi-codegen` and the client generator.
5. Start `pnpm --dir web dev`. The Vite proxy sends `/api` to `localhost:8080`, so cookies stay on one origin.
6. Print a URL table: app, Mailpit, Grafana. Prefix each log line with the process name. On a port conflict, pick a free port and write it to `.spinup/dev.json`.
7. On exit or crash, stop all children. Leave the infra running unless `--down`.

## 12. Database

- Migrations are plain SQL in `api/db/migrations`. Goose runs them as a library, so the app and `spinup db migrate` use the same code. sqlc reads the migration directory as its schema. River migrations run in the same step, app first.
- `db migrate` takes a Postgres advisory lock and prints the plan first.
- `db lint` (CI and before `deploy`) fails on `DROP COLUMN`, `DROP TABLE`, `ALTER ... TYPE`, `SET NOT NULL` without a default, and a non-concurrent index. A `-- spinup:allow-destructive <reason>` comment overrides it. This supports expand/contract, so an app rollback stays safe.
- `doctor` warns on Postgres 14 (EOL 2026-11-12). PG 19 is beta and is not allowed.
- Tests use `testkit` with a template database cloned for each test.

## 13. Frontend integration

- The CLI renders the SvelteKit scaffold from templates, not from `sv create`. The `sveltekit()` plugin options in `vite.config.js` set `paths.origin` (from the public URL in `spinup.toml`, or form actions fail with 403), `tracing: { server: true }` (controlled by an env var and a sampling ratio), and `csrf.trustedOrigins`.
- `Dockerfile.web` sets `BODY_SIZE_LIMIT` and `SHUTDOWN_TIMEOUT`. It uses `node --env-file`, Node 24, and the synchronous `import-in-the-middle` hook. OTel packages stay in `dependencies`.
- The contract is `api/openapi.yaml` (OpenAPI 3.0 or 3.1, tested in CI). `oapi-codegen` builds the Go strict server. `openapi-typescript` and `openapi-fetch` build `schema.d.ts` (managed) and `client.ts` (scaffold). CI regenerates and fails on any diff. `--client=heyapi` is opt-in.
- The Go API owns the session: an opaque ID in an `HttpOnly; Secure; SameSite=Lax` cookie, stored in Postgres. `hooks.server.ts` calls `GET /api/session` and sets `locals.user`. `handleFetch` rewrites the public URL to `http://api:8080` and copies the `cookie` header only for that host. The template has a tested helper that passes `Set-Cookie` through form actions.
- Caddy routes `/api/*` to Go and everything else to Node. `/healthz` is in `routes/healthz/+server.ts`.
- On a Kit major bump, `upgrade` runs `sv migrate`.
- The nightly canary job tests shadcn-svelte, `#lib`, bits-ui, and vitest with Kit 3.
- `--frontend-mode=embed` uses `adapter-static` as an SPA with `go:embed` behind the build tag `embedweb`. It is an M10 opt-in.

## 14. Deploy flow (`spinup deploy --env production`)

Each step is idempotent and safe to re-run. `--plan` prints the diff of Compose, Caddy, images, and pending migrations, and changes nothing. Each environment in `spinup.toml` has its own host, age key, secrets file, VM lock, release history, and `protected` flag. A protected environment needs typed confirmation unless `--yes` is used in CI.

1. Preflight: clean git tree, `spinup check`, `db lint`, valid lock. Optional gate: CI is green for this SHA.
2. Build with `docker buildx` and push to GHCR. Tag with the git SHA. Attach provenance and SBOM. Deploy by digest. The deployer never uses `latest`. Skip the build if CI already built the tag.
3. Open SSH through `ssh` with ControlMaster, using the user's SSH config. Take the lock `/opt/<app>/.deploy.lock` (owner, time, TTL). Exit 5 if held.
4. Pre-deploy backup: `backup now --type=diff`. It is required when a migration has no `safe` marker. Skip it with `--skip-backup`.
5. Decrypt secrets locally with sops. Upload `compose.yml`, `Caddyfile`, and the secret files (mode 0600) to `/opt/<app>/releases/<ts>-<sha>/`. Secrets never enter an image, a Compose file, or a log. The kit reads `*_FILE`. The CLI redacts secrets in its output.
6. Run `docker compose pull`.
7. Run migrations as a one-shot: `docker compose run --rm migrate`. It uses the advisory lock. If it fails, the deploy fails.
8. Roll `api` and `web`: `up -d --scale <svc>=2N --no-recreate`. Wait until the new containers pass `/readyz` and Caddy shows healthy upstreams. Then stop and remove the old container IDs. On timeout, roll back automatically (disable with `--no-auto-rollback`).
9. Smoke test: `/healthz`, `/version` (it must return the new SHA), and the URLs listed in the environment file.
10. Record the release: compose file, env snapshot (no secrets), image digests, migration version, user, time. Move the `current` symlink. Keep the last 5.
11. Release the lock. Post a Grafana deploy annotation.

**Rollback** (`spinup rollback`): take the same lock and redeploy the previous release (images by digest, config snapshot). It does not touch the database. If the release has a migration without the `safe` marker, `--with-db-plan` prints the point-in-time restore steps (target = the pre-deploy backup time). It never restores the database automatically.

**Server** (`server bootstrap`): render cloud-init, or run the same steps over SSH on an existing VM. Steps: deploy user with SSH key, root and password login off, UFW 22/80/443, fail2ban, unattended-upgrades, Docker from the official repo, log rotation (`max-size: 10m`, `max-file: 3`), swapfile, sysctl, and `/opt/<app>/{releases,secrets,backups,data}`.

`server audit` checks: SSH keys only, Postgres not published, published ports bound to `127.0.0.1` (Docker bypasses UFW), Grafana behind auth, containers use `read_only` and `cap_drop: [ALL]` and `no-new-privileges` and a non-root user, disk space, and unattended-upgrades status. The optional OpenTofu pack creates the VM, firewall, volume, and DNS record. The CLI only shells out to `tofu`.

## 15. Secrets

- `secrets init --env x` creates an age key pair. The private key goes to the OS keychain or a 0600 file under `~/.config/spinup/keys/`. The public key goes into `.sops.yaml`.
- `edit`, `get`, `set`, `diff`, `rotate` (data key), `rekey` (recipients), and `push` (write files on the VM) wrap the `sops` binary.
- CI holds the age private key as a GitHub environment secret.
- A `SecretsProvider` interface exists. Only sops is in v1.
- `doctor` flags plaintext secrets and a missing `.gitignore` entry.

## 16. Backup and restore

- The Postgres image comes from `Dockerfile.postgres`: `postgres:18` plus pgBackRest. `archive_command` runs `pgbackrest archive-push`. The target is S3-compatible storage with repository encryption. WAL archiving gives point-in-time recovery.
- A `Backend` interface (`Backup`, `List`, `Verify`, `Restore`, `Expire`, `Info`) has the implementations `pgbackrest` (default) and `pgdump` (nightly logical copy, second layer). WAL-G is the third implementation after v1. The interface is the insurance against the pgBackRest maintenance history (archived April 2026, then revived with sponsors).
- Schedule: a systemd timer on the VM. Full weekly, differential daily, WAL continuous. Retention is set in `spinup.toml`.
- `backup verify` steps:
  1. Start a throwaway Postgres container.
  2. Restore the latest backup, or a point-in-time target.
  3. Start it and run sanity queries: the latest goose version equals the lock, and row counts of configured tables.
  4. Report the measured RTO.
  5. Remove the container.

  This runs weekly. A failure raises an alert.
- `backup restore --to <time> --into new` starts a new container. `--in-place` needs typed confirmation. It takes the deploy lock, stops the app, restores, and starts the app again.
- The kit exposes last-backup age as a metric. Alerts use it.

## 17. Monitoring stack

- **App:** `kit/telemetry` sends traces, metrics, and logs over OTLP/HTTP to Alloy on port 4318. Every `slog` line has the trace ID. SvelteKit uses its built-in server tracing with the OTLP exporter. A CI smoke test checks Node-to-Go `traceparent` propagation, because the research did not verify it.
- **Compose services, profile `full`** (about 1.5 to 3 GB RAM): Alloy, Loki, Tempo, Prometheus, Grafana, node-exporter, cAdvisor, postgres-exporter, blackbox exporter, Caddy metrics. Profile `lite` is Prometheus, Grafana, and Loki. `bootstrap` and `doctor` recommend `lite` below 4 GB RAM. Profile `none` turns monitoring off.
- **Storage:** filesystem, with Loki retention 30 days and the compactor on. Prometheus and Tempo retention are set by flags. S3 is optional.
- **Security:** Grafana binds to `127.0.0.1` and is reached through Caddy with auth. Collector ports are not published. `monitor open` uses an SSH tunnel.
- **Provisioning (managed):** datasources, dashboards (Go runtime, HTTP RED, Postgres, Caddy, host, containers, backups, deploys), alert rules, contact points. User dashboards and rules go in `custom/`, and the CLI never touches them. `monitor dashboards sync` exports UI changes into `custom/`.
- **Default alerts:** error rate, p95 latency, disk space, backup failed or stale, restore-verify failed, certificate expires in under 14 days, container restart loop, Postgres connections, WAL archive lag, reboot required. Contact points (email, Slack webhook, ntfy) are set in `spinup.toml` and the secrets are in sops.
- **Heartbeat:** a dead-man's-switch ping to an external URL, because a down VM cannot alert on itself. The blackbox exporter also probes the public URL.
- **Dev:** `grafana/otel-lgtm` v0.36.0. Upstream says it is not for production.

## 18. Plugin protocol

- Plugins are declared in `spinup.toml` (`[[plugins]] name, source, version`). The lock stores the version, `api`, and sha256. The host refuses to run a binary not in the lock. There is no `PATH` scan.
- **Tier 1, template pack** (declarative, no code): `plugin.toml` lists generators, template fragments, file classes, kit dependencies (`go get` lines), Compose fragments, and migrations. It covers most needs. Built-in modules use this same format.
- **Tier 2, executable:** a subprocess that speaks JSON-RPC 2.0 over stdin and stdout.
  1. Handshake: the plugin returns `{name, version, api, capabilities}`. The host supports `api` N and N-1.
  2. Methods: `describe`, `plan`, `apply`, `check`. Capabilities: `generator`, `fragment`, `runtime-package`, `check`, `command`.
  3. A plugin never writes to disk. It returns a `vfs.Plan`. The host applies it and records the files in the lock.
  4. The plugin gets a scrubbed environment and the project directory as working directory. It gets no secrets unless the user grants a named secret.
- Plugin files use the same classes, 3-way merge, renames, and migrations as core. `plugin add` downloads from a release, checks the checksum (and a cosign signature if present), and asks the user to trust it on first use. `plugin update` runs `upgrade --plan` first.
- The `plugin-sdk` conformance kit tests the protocol, including a plugin that writes outside the root or has a bad checksum.

## 19. CI files generated (all managed, GitHub Actions)

- `ci.yml`: `go vet`, `go test`, `govulncheck`, migration check on a Postgres service, `sqlc diff`, OpenAPI and client drift, `db lint`, `svelte-check`, lint, vitest, `docker build`, `spinup check`, a conflict-marker scan.
- `e2e.yml`: Compose stack and Playwright, including cookies through SSR and trace propagation.
- `deploy.yml`: on tag or manual dispatch. Build, push to GHCR with provenance, run `spinup deploy`. Production uses a protected environment with required approval, and OIDC where possible.
- `backup-verify.yml`: weekly `spinup backup verify`.
- `upgrade-check.yml`: weekly `spinup upgrade --plan`. It opens an issue if the project is behind.
- `renovate.json` (scaffold): groups for Go modules, npm, Dockerfiles, Compose image digests, and Actions SHAs. A custom manager covers the kit version.
- Rules in every template: pin actions by full commit SHA (the SHA table is in `internal/ci` and a CLI job refreshes it), least-privilege `permissions:`, never `pull_request_target` with untrusted code, and run Trivy as a pinned container, not the action.
- `ci verify` compares the workflows with a fresh render.

## 20. CLI testing strategy

1. **Unit tests** with fakes for `Runner`, `FS`, `Prompter`, `Merger`, and SSH. Table tests for `vfs`, `render`, `manifest`, planners.
2. **Golden trees** per answer set (default, lite, embed, bun, no-auth, all modules, plugin pack). Update with `-update`. A reviewer must approve golden diffs.
3. **Determinism:** generate at version N, re-render at N, expect zero diff. Run on Linux and macOS.
4. **Merge table:** BASE/OURS/THEIRS cases for Go braces, YAML, JSON, Svelte, rename, delete, mode change, CRLF, and binary.
5. **Upgrade matrix (required from M2):** `testdata/corpus/vX.Y.Z/` holds the real, frozen output of every release. For each of the last N versions, apply scripted user edits (add a route, edit a Compose override, edit a shadcn file, delete a file), upgrade to `main`, and assert: zero unexpected conflicts, expected conflicts where the edits overlap, then `go build`, `go vet`, `go test`, `pnpm check`, `doctor`. Add a chained test from the oldest supported version.
6. **Codemod fixtures:** tree N to tree N+1, run twice for idempotence. CI fails if a release changes a template with no migration entry or explicit "none" marker.
7. **`testscript` CLI scripts** in a temporary git repository for `new`, `generate`, `upgrade --plan`, `destroy`, error text snapshots (with `Try:`), and `--json` schema.
8. **Generated-app gate on each PR:** `go build/vet/test` (testcontainers), `sqlc diff`, `pnpm install --frozen-lockfile`, `pnpm check`, `pnpm build`, vitest.
9. **Nightly E2E:** generate from `main`, `compose up`, Playwright (login, session across SSR, trace in Tempo, log in Loki), `backup now` and `backup verify` against MinIO, then deploy to a Docker-in-Docker VM with sshd: bootstrap, deploy, forced failing health check (expect auto rollback), `rollback`, lock contention, `secrets push`.
10. **Plugin conformance** including malicious cases.
11. **Canary (weekly):** bump every pin in `stackversions.yaml` and the npm and Go pins to latest, run tests 8 and 9, and open an issue on failure.
12. **Docs test:** run every install and quickstart command on a clean runner. Generate the compatibility matrix from `compat/` and `stackversions.yaml`.
13. Fuzz the lock parser, template parser, and field parser. Run the race detector on `devloop`. Run `golangci-lint` and `govulncheck`. Enforce a coverage gate on `merge`, `upgrade`, `manifest`, and `migrate` only. CLI unit tests also run on Windows.

## 21. Release process

- GoReleaser: `CGO_ENABLED=0`, linux and darwin (amd64, arm64), windows best effort. Archives, checksums, SBOM (syft), cosign keyless signing, GitHub provenance attestation. Inject version, commit, date, and kit version with ldflags.
- One tag run publishes `vX.Y.Z` and `kit/vX.Y.Z`. It also attaches `templates-vX.Y.Z.tar.zst`, `compat.json`, and `stack-versions.json`.
- Install channels: Homebrew tap, `install.sh` that checks the checksum and signature, `go install ...@vX.Y.Z` (documented with an explicit version, never `@latest`), and a `ghcr.io/stopyellingatme/spinup` image for CI.
- `self-update` downloads the asset, verifies the checksum and cosign signature, and replaces the binary by atomic rename. If Homebrew owns the binary, it prints `brew upgrade spinup`. A daily update notice is off in CI and with `SPINUP_NO_UPDATE_CHECK=1`.
- Cadence: a monthly minor, patches as needed, security fixes to the last two minors. `CHANGELOG.md` needs a "Breaking" section and an "Upgrade steps" section.
- Governance: two named owners, a release rota, `MAINTAINERS.md`, and `SECURITY.md` from M0. Evolve the CLI in place. Never run a parallel rewrite.
- v1.0 gate: two minors with no breaking kit change, all doc commands tested, a published support policy, and a dogfood app that upgraded across at least two minors.

## 22. Milestones (each ends in something runnable)

| M | Weeks | Scope | Runnable result |
|---|---|---|---|
| M0 Foundations | 1-2 | Repo, two modules, Cobra skeleton, `ui` error renderer, `vfs`, `render`, manifest schema, file classes, `stackversions.yaml`, `compat`, golden and determinism harness, CI, GoReleaser snapshot, owners | `spinup version` runs. Golden harness is green. |
| M1 Tracer bullet | 3-5 | `new` (API only), kit `app/config/httpx/logx/db`, `db migrate`, `build`, Dockerfile, Compose dev file | `new`, then `go run`, serves `/healthz`. Release v0.1.0 and freeze it in the corpus. |
| M2 Upgrade spine | 5-9 | Lock writer, hashes, `diff`, `check`, `doctor` basics, `Merger` (git), renames, orphans, tombstones, codemod framework, `go fix` step, `upgrade --plan/--apply/--continue/--abort`, upgrade matrix CI | A v0.1 app with user edits upgrades to v0.2. Matrix is green. |
| M3 Data and generators | 9-12 | sqlc, oapi-codegen wiring, full `db` group, `db lint`, `generate` (resource, model, handler, migration, job), `destroy`, markers, `test` | `g resource` plus `db migrate` gives a working CRUD API with passing tests. |
| M4 Dev loop and frontend | 12-17 | `dev` supervisor with air, `/_dev` page, SvelteKit 3 scaffold, typed client and drift check, session flow, Vite proxy, shadcn tracking, `g page/component/client`, Playwright smoke, canary job | `spinup dev` gives a browser login flow end to end. |
| M5 Deploy and server | 17-22 | Dockerfiles, Caddy, Compose render, `server bootstrap/audit`, `ssh` wrapper, deployer (lock, rolling, health, releases, rollback), `deployx`, `secrets` group, `tools install`, DinD test suite | Zero-downtime deploy and rollback to a test VM. |
| M6 Monitoring | 22-26 | Kit `telemetry`, Alloy/Loki/Tempo/Prometheus/Grafana, `lite` and `full` profiles, dashboards, alerts, heartbeat, `monitor *`, deploy annotations, stack boot test | Web and API spans in Tempo, logs in Loki, a test alert fires. |
| M7 Backups and maintenance | 26-29 | pgBackRest image, `Backend` interface, schedule, `backup *`, `backup verify`, pre-deploy backup, `db doctor`, `maintenance *`, `doctor --remote`, Renovate config | A verified restore runs on schedule in the test VM. |
| M8 CI generation | 29-31 | Workflows, SHA table, `ci generate/verify`, `upgrade-check.yml`, stack-version PR automation | A generated repo goes green on GitHub. |
| M9 Modules and plugins | 31-36 | Move built-in modules to the pack format, `jobs`, `mail`, `auth` (OIDC), executable plugin protocol, `plugin-sdk`, trust flow, one example plugin | A third-party plugin adds a generator and upgrades cleanly. |
| M10 Extras and hardening | 36-40 | `frontend-mode=embed`, OpenTofu pack, `self-update`, Homebrew tap, signing, docs site from `compat/`, full multi-version matrix, security review, 4-week dogfood | Public 0.x release. |
| M11 Stabilize | 40+ | Deprecation tests, behavior-version policy, WAL-G backend, v1.0 freeze | v1.0 once the gate in section 21 is met. |

Staffing: about 40 weeks for 2 to 3 engineers. Assign at least two people to M2 and M5. If the team is smaller, cut M9 extras, the OpenTofu pack, and the `embed` mode first. Keep M0 to M2 intact.

## 23. Open items to verify during implementation

- Caddy health-check syntax for 2.11, and the `compose up --scale` rolling flags.
- Node-to-Go trace propagation, and shadcn-svelte with Kit 3 `#lib`.
- `openapi-typescript` support for OpenAPI 3.1.
- Versions for `go-toml`, the comment-preserving YAML library, `testscript`, and the `go-selfupdate` choice (use a maintained library, or write about 150 lines).
- The current `govulncheck` version.
- The binary size budget with 3 embedded template versions (target 40 MB or less).
- Whether a formatter for `web/` files is needed. The default is none.