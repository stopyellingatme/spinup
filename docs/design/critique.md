**Critique of the spinup plan, 2026-10-13. Fixes follow each item. Items marked VERIFY need a recheck at the first commit.**

## Verification notes (from web search)
- **pgBackRest:** David Steele archived it in late April 2026. On 18 May a sponsor coalition revived it, and Percona said it would keep supporting it. I found nothing newer than May 2026, so this is not confirmed healthy in October. The plan's `Backend` interface idea is correct. Add a gate: before M7, check the commit cadence and the release date of pgBackRest 2.59.x. If there has been no release for 90 days, make `pgdump` + WAL-G the primary pair.
- **SvelteKit 3:** My search found only 3.0.0-next.x pre-releases, up to next.15 in early August. I could not confirm a stable 3.0.1. VERIFY on npm and pin only what the registry shows. The confirmed breaking changes match the plan:
  - Config moves into the `sveltekit()` Vite plugin.
  - adapter-node 6 drops `ORIGIN` for `kit.paths.origin`.
  - Vite 8, TypeScript 6 and Node 22.17 or later are required.
  - `$app/stores` is removed.
  - `cookie` v2 allows ASCII cookie names only.
  Add these removals to the nightly canary.
- **docker-otel-lgtm:** pkg.go.dev shows v0.30.0 on 2026-07-31. The plan pins 0.36.0. That is possible by October, but VERIFY it. The tag scheme matters because the plan pins by digest.
- **Not checked:** the other ~50 version pins in section 3. Treat all of them as unverified. Several look implausible and need a registry lookup: Grafana 13.2.4, pnpm 12.11.2 and Go 1.27.2.

## (a) Missing items

1. **Merge gap: `git merge-file` needs a git repo.** The plan's preconditions require one, but `merge-file` itself works on temp files, so that is fine. The real gap is `go fix`, `go mod tidy` and `pnpm install` running in step 8 after files are applied. A failure there leaves the tree half-upgraded.
   - Fix: run the whole upgrade in a `git worktree` on the upgrade branch. Copy back only on success.
   - Fix: make `--abort` delete the worktree. `git reset --hard` on the user's branch is dangerous.
2. **User-deleted files.** Tombstones exist, but the rule for creating them is missing.
   - Fix: at upgrade time, if the lock lists a file and the disk lacks it, record a tombstone automatically. Also scan for files missing from `managed` classes. Print them in the plan.
   - Fix: add a `check` warning for a missing `managed` file.
3. **User-edited `managed` file with no merge.** The plan only warns, then overwrites with `--force-managed`. Users will edit these files, for example `compose.yml` to add a service. Every upgrade then gives a permanent warning, and `--force-managed` destroys their work.
   - Fix: default to the 3-way merge for `managed` files that drifted, the same as `scaffold`. Keep `--force-managed` for overwrite.
   - Fix: on drift, offer `eject` or "move to the extension point".
4. **Two-writer problem on the lock hash.** Hash is "as the generator wrote it". After `go/format`, a user running `gofmt` or a newer Go version formats differently, so a clean file looks edited.
   - Fix: hash the normalized content (LF endings, trailing newline). Record the Go version used by `go/format`. Re-render OLD with the same formatter version as the original render. Pin it in the lock.
5. **Render purity is overstated.** OLD render depends on `stackversions.yaml`, the plugin set, and the CLI binary. Principle 4 says it is a pure function of (template version, answers, plugin set).
   - Fix: add `stack_versions` to the purity inputs, and make the embedded pack carry its own copy.
   - Fix: when a plugin version is missing offline, fail with a `Try:` line. Do not render OLD without that plugin.
6. **Offline and air-gapped use.** Older template versions (beyond 3) download from the release. Plugins, `tools install`, and `self-update` all need network.
   - Fix: add `spinup cache warm` and an `--offline` flag. Fail fast with a named missing asset.
   - Fix: keep the `.spinup/` cache keyed by checksum. Document the offline path in the docs test.
7. **Multi-environment data safety.**
   - `spinup.toml` has staging and production, but nothing stops a production `--env` mistake on `db reset`, `db seed`, `restore` and `secrets rotate`.
   - Fix: add a `protected` check to every destructive command, not only `deploy`. Show the environment name and host in the confirmation prompt. Require retyping the environment name.
8. **Expand/contract is only linted, never enforced across a deploy.** The rolling deploy runs the new migration while old containers still serve. `db lint` blocks the obvious breaking DDL, but it cannot catch:
   - a new code path that needs a column the old code does not write
   - backfills that take a long lock
   - `ADD COLUMN ... DEFAULT` rules
   - Fix: add a migration `lock_timeout` and `statement_timeout` default in the runner. Add a two-release rule for contract steps, enforced by `db lint` comparing against the last deployed release's migration version.
9. **Secret leakage paths not covered.**
   - Decrypted secrets on the VM sit in `releases/<ts>/`. Five old releases mean five copies of secrets and old keys. Fix: keep secrets in one `/opt/<app>/secrets` dir with symlinked versions. Record secret file hashes in the release snapshot, not the secrets.
   - `docker inspect` and Compose `environment:` leak values. Fix: `server audit` must scan for secrets in `docker inspect`.
   - Grafana and Loki can ingest secrets from logs. Fix: add a Loki/Alloy redaction stage and a `logx` denylist.
   - The age private key in the "OS keychain or 0600 file" has no backup or loss story. Fix: require a second recipient (a recovery key) at `secrets init`.
   - CI holds the age key as a GitHub secret. Fix: use a separate CI-only recipient, rotatable with `rekey`.
10. **Supply chain for the CLI's own downloads.** `tools install`, plugins and old template packs rely on checksums. A checksum from the same server proves nothing.
    - Fix: embed the expected sha256 for `sops` and `age` in the CLI binary, or verify cosign signatures against a key embedded in the CLI.
    - Fix: `plugin add` trust-on-first-use is weak. Add an allowlist file in the repo that CI checks.
11. **Plugin sandbox claim is too strong.** A subprocess with a scrubbed environment can still read the user's home dir, SSH keys and the project secrets files.
    - Fix: say so in the docs. Tier 2 plugins run with the user's privileges. Require a typed trust prompt. Do not claim isolation.
12. **Windows.** The plan calls it "best effort" but `dev` (process groups, signals), `air`, `ssh` ControlMaster (not supported on Windows OpenSSH), file modes and symlinks (`current`) all break.
    - Fix: state in the support policy that Windows is supported through WSL2 only for `dev`, `deploy`, `server`. Native Windows covers `new`, `generate`, `build`, `upgrade --plan`. Test those in CI on a Windows runner. Normalize CRLF with `.gitattributes` in the template (`* text=auto eol=lf`).
13. **macOS specifics.** Apple Silicon Docker builds for a linux/amd64 VM are slow and need `--platform`. `build` should default the platform to the environment's target.
14. **Rollback gaps.**
    - Rollback "never touches the database", but the previous release's app may not run on the new schema if a contract step was applied. The expand/contract rule (item 8) is the guard.
    - `current` symlink move plus Compose `up` is not atomic. If the CLI loses SSH mid-deploy, the VM state is unknown. Fix: write a `deploy.state` journal on the VM and add `deploy --resume` and `deploy status`. A stale lock needs `deploy unlock --force` with owner and TTL display.
15. **Single-VM blast radius.** No story for VM loss. Backups exist, but there is no `server restore-from-backup` rebuild runbook.
    - Fix: add a DR drill to the nightly E2E: destroy the DinD VM, bootstrap a new one, and restore from MinIO. Measure RTO.
16. **Postgres major upgrade.** `maintenance pg-major-upgrade` is a single bullet. It is the highest-risk command in the tool.
    - Fix: v1 scope is a documented dump/restore into a new container with a swap. Do not promise in-place `pg_upgrade` with pgBackRest repo changes until M11.
17. **Observability of the CLI and update safety.** No telemetry policy is stated. Fix: say explicitly "no telemetry" or opt-in only. The daily update check is a network call, so document it.
18. **Security of the generated app is thin.** Missing: account lockout and brute-force limits, session rotation on login, CSRF between SvelteKit form actions and the Go API, password reset flow, dependency review for npm install scripts (`pnpm` `onlyBuiltDependencies` allowlist), and a Content-Security-Policy default.
19. **Data protection.** There is no migration-time test against production-sized data, and no PII handling in logs, traces or backups. Add a `docs/data-handling.md` and a logs redaction test.

## (b) Wrong, outdated or risky choices

1. **Hash the *normalized* file, not "as written"**: see (a)4.
2. **Docker Compose `--scale 2N --no-recreate` rolling deploy.** This is a hack with real failure modes:
   - Compose names containers by index. Scaling up, then removing "old IDs", leaves Compose state mismatched with the file.
   - Services with `container_name` or fixed host ports cannot scale.
   - Caddy needs dynamic upstreams via Docker DNS (`dynamic a` resolvers) to see new containers.
   - A later `up -d` may recreate the containers you just kept.
   - Fix: make this an M5 spike with a pass/fail test before the deploy design is final. Fallback: blue/green by Compose project name (`app-blue`, `app-green`) with Caddy upstream switching by a reload. That is simpler to reason about. Alternative: use a small existing tool (for example `docker-rollout`) as a pinned dependency instead of owning it.
3. **"Exit code positive = conflict count" for `git merge-file`.** Correct, but the count is capped at 127. Do not use the count for anything but zero versus non-zero. Also `-p` output plus `--diff-algorithm=histogram`: VERIFY that your minimum git version supports the flag for `merge-file`. It was added in git 2.x, but test it in `doctor` with a minimum git version check.
4. **Embedding 3 template versions as zstd tarballs plus kit module in one tag.** The "kit/vX.Y.Z" tag in a repo with a CLI at the root works for Go, but lockstep versioning means a CLI-only bugfix forces a kit tag. The plan says "a patch may ship alone", which contradicts "lockstep". Fix: state one rule. Patch versions are independent, minor versions are lockstep. Enforce it in the release script.
5. **`go tool air` as the dev reload.** `air` is a single-maintainer tool and, being a Go tool directive, becomes a full dependency in the app's `go.mod`, which affects `go get -u` and `govulncheck` results. Fix: keep `air` in a separate `tools` module (`api/tools/go.mod`) or just let `spinup dev` use `go run` with a rebuild-on-change using `fsnotify`. A ~150-line watcher is cheaper than the coupling. The plan rejected this, but the `go.mod` pollution cost was not weighed.
6. **`go get -u` for tool directives in `upgrade`.** This upgrades indirect dependencies beyond what was tested. Fix: only bump the pins listed in the new `stackversions.yaml`, not `-u`.
7. **`go fix ./...` in the upgrade.** It rewrites user code. It is fine on a branch, but it must run in the worktree and appear as its own commit so it can be reverted separately.
8. **Version bundle risk: JSON v2 and Go 1.27.** Go 1.27 timing and the claim "JSON v2 changes error text" need a VERIFY. If `GOEXPERIMENT=jsonv2` is not default, the golden-test cost is wasted. Check the Go 1.27 release notes.
9. **Prometheus without Mimir, Tempo, Loki, Alloy on one VM at 1.5 to 3 GB.** The plan's own `full` profile is heavy for the "single VM" default. Tempo plus Loki plus Grafana at idle is often over 2 GB. Fix: make `lite` the default, and measure RAM in the nightly E2E with a budget assertion.
10. **Grafana on 127.0.0.1 behind Caddy auth.** Reasonable. But Caddy basic auth in front of Grafana plus Grafana's own login is a double login, and a Caddy `basicauth` password hash is another secret. Fix: use Grafana's own auth with a generated admin password in sops, and bind to localhost only with the SSH tunnel as default. Expose through Caddy only as an opt-in.
11. **Dead-man's-switch to an external URL.** This needs a third-party service (healthchecks.io or similar) and an account. State the dependency and make it a `monitoring.heartbeat_url` setting with docs.
12. **Renovate "44.x" and "Trivy 0.75.0 pinned container".** These look unverified. Trivy had a supply-chain incident in 2026 per the plan's own note ("not the action"). Keep the policy, but also pin the Trivy DB source and cache.
13. **Testcontainers in `kit/testkit` and the generated tests.** This forces Docker for every `go test`. Fix: allow `DATABASE_URL` to point at the dev compose Postgres, so unit tests run without testcontainers.
14. **`huh` v1.0.0 and `bubbletea/v2`.** Charm libraries have shifted import paths (`charm.land/...`) recently. VERIFY the module paths and that `huh` works with Bubble Tea v2.
15. **shadcn-svelte tracked "one by one" as scaffold files.** shadcn components are copied code. The upstream registry updates make each upgrade a 3-way merge of dozens of files, which will produce frequent conflicts. Fix: in the lock, track the shadcn registry version and treat `ui/*` as `seed` by default (no auto-merge). Offer `spinup upgrade --ui` as a separate, opt-in step.
16. **`scaffold` hash mismatch merge assumes BASE render equals what the user once got.** That fails if the user ran a *plugin* or *generator* that edited the same scaffold file (for example the `// spinup:routes` marker inserts). The generator edit is a user-style edit relative to BASE, so every route added by `g resource` becomes a merge edit. These are usually clean, but they sit next to template edits in the same file, so conflicts are likely in `routes.go`.
    - Fix: put generator-produced registrations in a separate managed-by-generator file (`routes_gen.go`) that the generators rewrite entirely. Keep `routes.go` stable.
    - The same applies to `openapi.yaml` (paths added by generators). Use `$ref` splitting: one file per resource under `api/openapi/paths/`, and a bundle step.

## (c) Scope too large for a small team

1. **Cut or delegate from v1:**
   - Executable plugins (tier 2) and `plugin-sdk`: ship template packs only. Defer JSON-RPC until a real third-party need exists.
   - `maintenance pg-minor-upgrade` and `pg-major-upgrade`: document runbooks instead.
   - OpenTofu pack, `embed` frontend mode, `bun` option, `heyapi` client, WAL-G, `--channel next`, `completion` extras: cut for v1.
   - `monitor trace/logs/silence/dashboards sync`: keep `open` and `status`. Use the Grafana UI for the rest.
   - `server ssh/exec/reboot/ps/logs`: these are thin wrappers over `ssh`. Keep `logs` and `ssh` only.
   - `releases show/prune`, `backup prune/schedule/status`, `secrets rekey/diff`: merge into fewer subcommands.
2. **Delegate to existing tools:**
   - `self-update`: use Homebrew and `install.sh` only. Drop the in-binary updater (a security-sensitive code path).
   - Dashboards: import community dashboards for Go runtime, node-exporter, cAdvisor and postgres-exporter, pinned by ID and revision. Write only HTTP RED and backups yourself.
   - Alert rules: use `awesome-prometheus-alerts` rules as the base.
   - Server hardening: consider a pinned cloud-init plus a well-known script instead of owning each step. Keep `server audit` as the check.
   - Merge: already delegated to git, good.
3. **Combinatorial test matrix.** Answer sets (default, lite, embed, bun, no-auth, all modules, plugin pack) times three OSes times two Go versions times N frozen corpus versions grow quickly. Fix: cap the corpus to the last 2 minors plus the oldest supported. Run the full matrix nightly only. Use the default answer set per PR.
4. **40 weeks for 2 to 3 engineers is optimistic.** Estimate from the sizes: M5 (deploy plus server plus secrets) and M2 (upgrade engine) each look like 10+ weeks of real work for the hardest parts. Plan 55 to 65 weeks, or cut as above. The plan's own fallback (cut M9 extras) is the right first move, and add: cut the `full` monitoring profile to M10.

## (d) Milestone ordering problems

1. **M2 (upgrade) before M3 and M4.** The upgrade spine is proven only against an API-only v0.1 app. Real upgrade pain comes from generated code, SvelteKit and Compose files. A green matrix at M2 gives false confidence.
   - Fix: keep M2, but add a gate. No v0.2 release is frozen in the corpus until M4 produces a full-stack app. Freeze corpora from v0.3 or later, and treat v0.1 as throwaway.
   - Fix: freeze each corpus with its *answers variants*, not only default.
2. **SvelteKit 3 scaffold lands in M4, with a "nightly canary" that does not exist until then.** Given the SvelteKit 3 stability uncertainty above, run a 1-week frontend spike in M0/M1: scaffold, SSR cookie flow, OTel tracing, Docker build. If it fails, fall back to SvelteKit 2 pinned. Record the decision.
3. **Deploy (M5) before monitoring (M6) and backups (M7)**, but the deploy flow already requires `backup now` (step 4), the `deployx` endpoints, and a Grafana annotation (step 11). Fix: stub those steps behind interfaces in M5, or move the pgBackRest image and basic `backup now` into M5. A deploy without a pre-deploy backup should not be the first thing to ship to real VMs.
4. **`doctor --remote` and `server audit` are in M5/M7** while the security rules (Docker bypasses UFW, 127.0.0.1 binds) must be correct in the *first* generated compose file. Fix: put a compose lint (`ci verify`-style static check) in M1 so the rule is tested from the start.
5. **CI generation (M8) is last-but-three**, yet every earlier milestone needs CI for the generated app (the plan's test 8 "generated-app gate"). Fix: move the minimal `ci.yml` into M1 or M3. Keep the full workflow set in M8.
6. **`secrets` (M5) needed by `deploy` and by `.env` in `dev` earlier.** Fine for dev, but `doctor` plaintext checks and the `*_FILE` convention should be in the kit from M1. Add `config` `*_FILE` to the M1 scope, which the plan lists. OK.
7. **Release v0.1.0 at M1 commits you to the lock schema too early.** Fix: label 0.x pre-releases as `-alpha` until M2 finalizes the lock `schema` and the migration mechanism. Include a lock schema migration test from schema 1 to 2 in M2.
8. **Docs test, `compat` and the docs site are in M0 and M10.** Move the docs-test harness to M1 so every command added later brings its doc. A doc written at M10 for 40 weeks of commands is a cost bomb.
9. **Dogfood is only the last 4 weeks of M10.** Fix: start a dogfood app at M4, and upgrade it at every minor. The v1.0 gate already requires two minors of upgrades, so this must start early to be met by M11.

## Concrete top-10 priority fixes
1. Run upgrade in a worktree. Make `--abort` delete the worktree.
2. 3-way merge for drifted `managed` files by default.
3. Normalize hashes (LF, formatter version) and add `stack_versions` to the render inputs.
4. Spike and test the rolling deploy mechanism early. Plan blue/green fallback.
5. Separate generator-owned registrations (`routes_gen.go`, per-resource OpenAPI files) from user-edited files.
6. Cut tier 2 plugins, in-binary `self-update`, WAL-G, OpenTofu, `embed` and `bun` from v1.
7. Move backup and minimal CI earlier (M1 to M5). Add a compose security lint in M1.
8. Verify all version pins against registries on the first-commit day. SvelteKit 3 stable status, otel-lgtm 0.36.0, pgBackRest release health, Go 1.27 and `huh`/Bubble Tea v2 import paths come first.
9. Make `lite` the default monitoring profile. Assert a RAM budget in the nightly E2E.
10. Add a DR drill (destroy the VM, rebuild, restore) and a secrets recovery key to the acceptance tests.

Sources:
- [LWN: pgBackRest is no longer maintained](https://lwn.net/Articles/1069951/)
- [LWN: pgBackRest will continue](https://www.linux.org/threads/lwn-net-pgbackrest-will-continue.66585/)
- [Scaling Postgres 415](https://scaling-postgres-1.castos.com/episodes/pgbackrest-is-dead-scaling-postgres-415)
- [pkg.go.dev: grafana/docker-otel-lgtm](https://pkg.go.dev/github.com/grafana/docker-otel-lgtm)
- [SvelteKit 3.0 pre-release digest](https://releases.sh/collections/frontend-frameworks/digest/2026-08-03)
- [Reading the SvelteKit 3.0 Pre-Release](https://www.youngju.dev/transcribe/2026-07-17-sveltekit-3-pre-release-what-breaks.en)