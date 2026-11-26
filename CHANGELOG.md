# Changelog

All notable changes to spinup appear in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- The monitoring module (`full` profile) has a new dashboard `Containers`, which spinup owns. It replaces the vendored cAdvisor dashboard (grafana.com 14282), which had no license. `spinup upgrade` adds the new file and removes the old file if you did not edit it. The `Containers` link of the console opens the new dashboard.
- `SECURITY.md` names the private advisory form, the supported versions before the first release, and the scope.
- The install docs lead with the build from source, because no release exists yet. The install script, the Homebrew cask, and `spinup self-update` work from the first release.
- The release archive also holds `THIRD_PARTY_NOTICES.md`.
- CI: each checkout sets `persist-credentials: false`, except the checkout of `release.yml`. `docs.yml` has top-level read permissions. The tree gate ignores runs from forks.
- Embed mode: the Content-Security-Policy of the shell now has the directives of node mode. `app.WithSPA` adds `form-action 'self'`, `img-src 'self' data:`, `font-src 'self' data:`, and `connect-src 'self'` (ADR 0039, stage 0). Before, a form in the page could post to another origin. A form that posts to another origin now needs a policy of its own.
- Embed mode: the managed `web/vite.config.js` sets the version name of the build to a hash of the build inputs. Two builds of the same inputs now give the same `index.html`, the same script hash, and the same Go binary content. Before, the build time changed them with each build.
- The web server (Node mode) sends each `console.error` and `console.warn` line to the log store as an OTLP log record, and a new `handleError` hook in `web/src/hooks.server.ts` prints each unexpected error with its request. Read the lines with `spinup monitor logs --service web`. The web app gets two npm packages: `@opentelemetry/api-logs` and `@opentelemetry/exporter-logs-otlp-http`.
- `spinup monitor logs`: `--service api` selects `<app>-api`, the text output shows the service, the level, and the attributes of each line, and `--json` prints one JSON object for each line.
- `spinup backup verify` and `spinup backup restore --into new` (pgBackRest) no longer stop with error 61 on a server that has archived WAL. The restore container now has its own spool path.
- `spinup deploy --plan` no longer fails with a tar error when a secret value is a short common string, such as the app name.
- The kit sets `Cache-Control: private, no-cache` on a response when the handler sets no `Cache-Control` (ADR 0034). `httpx.PublicCache` is the opt-in for a shareable answer.
- The SvelteKit node server sends a Content-Security-Policy with a nonce (ADR 0036). A page with its own inline script needs `nonce="%sveltekit.nonce%"`. A form that redirects to another origin must use `use:enhance`, because the policy has `form-action 'self'`. The billing pages do this.
- The doctor check `client-drift` now fails (it warned before). The generated CI workflow fails on a stale API client.
- `air` watches `api/internal/store`, so a query edit always reaches a build in `spinup dev`.
- `make perf` with no `SUITES` value runs every default suite (`cli`, `app`, `micro`, `web`). It ran only `cli` before.
- The base pack writes four more files (see "Agent guidance" below). The first upgrade moves an own `AGENTS.md` to `AGENTS.project.md` (codemod `0005_agent_guidance`).
- API keys: a new `[apikeys] prefix` no longer invalidates existing keys (ADR 0030 §4.4, amended 2026-11-07). `AuthenticateBearer` accepts any well-formed `<prefix>_<secret>` token, and the configured prefix only mints new keys.
- `docs/commands/generate-openapi.md` gives the handler and the wiring (strict handler, problem error handlers, `auth.Required`) for a hand-written path in an app with no resource, because `spinup g openapi` does not write `routes_gen.go`. The page also names the fix for `missing go.sum entry`. A test compiles the documented code.
- A development build of the CLI now warns on `spinup new` when no kit replace is set, because the pinned kit can lack packages that the templates import. The warning names `SPINUP_KIT_REPLACE=<checkout>/kit`. The doctor check `kit-version` also warns for a development CLI without a local replace.

### Added
- spinup has a license: MIT (`LICENSE`). The files that spinup writes into your app are yours, with no notice duty (`README.md`, "Generated code"). `THIRD_PARTY_NOTICES.md` lists the third-party files in the repository (ADR 0040).
- Files for contributors: `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1), issue forms, and a pull request template.
- Rules for the repository: three rulesets in `.github/rulesets/` protect `main` and the release tags, and `scripts/github/apply-rulesets.sh` applies them. `main` needs a pull request and the checks `lint`, `actionlint`, `test`, `windows`, `upgrade`, and `test-macos`. Read `docs/contributing/repository-rules.md`.
- New tests guard the repository files. `TestRulesetsMatchWorkflows` and `TestCheckoutsDropCredentials` guard the rulesets and the checkouts. `TestRepositoryWorkflowsPassVerify` guards the workflows of this repository. `TestDocsRootFiles` guards the links and quotes of the root markdown files.
- Web performance (ADR 0034). Embed mode serves precompressed `.br` and `.gz` assets with a strong `ETag` and answers `304`, and its CSP uses script hashes in place of `'unsafe-inline'`. `httpx.AcceptsEncoding`. The doctor check `web-bundle` with an optional `web/bundle-budget.json`. The perf suites `web` and `dev` (opt-in), `make perf-gate` (allocation budgets, in CI), `make perf-baseline`, and a committed baseline in `perf/baseline/`.
- Stricter quality gates (ADR 0036). The doctor checks `openapi-drift` and `sqlc-drift`. A docs lint (`TestDocsLint`, `TestDocsFacts`). Fuzz targets for `httpx.DecodeJSON` and `httpx.CheckJSONBody`. An XSS end-to-end test, `TestNoRawHTML`, and `E2E_BROWSERS=all` for Firefox and WebKit. An all-modules test.
- Live data (ADR 0033). The opt-in `live` module adds server-sent event feeds with a snapshot, numbered patches, and resume by cursor. Postgres is the only broker. `kit/x/live` has `Publish`, `Hub`, `Stream`, `Snapshot`, and `MarkMore`. `spinup g live <resource>` and `spinup g resource <Name> --live` write the feed and the `Publish` calls. The web helper `web/src/lib/live.svelte.ts` has `liveRefresh` and `liveList`. The app command `live`, the doctor check `live-wired`, and `app.WithDrain` in the kit. `app.WithRequestTimeoutExempt` now adds a predicate on each call.
- Agent guidance (ADR 0035). A new app gets `AGENTS.md` (managed), `AGENTS.project.md` (seed, the rules of the app), `CLAUDE.md` (seed), and the skill `.claude/skills/spinup/SKILL.md` (managed). Each pack adds its rows to the decision table of `AGENTS.md`. `spinup new --agent-guidance=false` leaves the files out. `docs/llms.txt` is an index of the docs for agents.
- A pin notice: one line on stderr when the CLI is older than the `cli_version` of `spinup.lock`, or much newer. `SPINUP_NO_PIN_NOTICE=1`, `--json`, and CI make it silent. The quick doctor check `cli-version` reports the same state.
- `Worker.PeriodicOnStart` (ADR 0007 §8) inserts a periodic job when the worker starts, and then every interval. `Worker.Periodic` keeps its signature. River keeps the schedule in memory, so `Periodic` waits one full interval after each start. A worker that restarts more often never runs a long-interval job. See `docs/build/jobs.md`.
- Job time limit and permanent failure (ADR 0007 §7). `jobs.WithJobTimeout` sets the default limit of a job (the default stays one minute), `jobs.Timeouter` sets the limit of one kind, and `jobs.Cancel` ends a job without a retry. See `docs/build/jobs.md`.
- API keys and plan quotas (ADR 0030). The opt-in `apikeys` module gives each user Bearer keys with the scope `read` (GET and HEAD) or `write`. Named plans in `[apikeys.plans.<name>]` set `per_minute` and `per_day`, and all keys of a user share the budget of the user plan. `spinup g apikeys` writes `api/internal/apikeys/apikeys.gen.go` and `web/src/lib/apikeys.gen.ts`. The kit adds `auth.WithBearer` and the package `kit/x/apikeys`. The minute limit is in memory in each process. The day limit is shared through PostgreSQL and flushed every `APIKEYS_FLUSH` (default 5s). Answers carry `RateLimit-*` and `Retry-After` headers. Keys show on `/settings/api-keys`. Plans change with the app command `apikeys` or on `/admin/apikeys`. The doctor checks `apikeys-drift` and `apikeys-wired` find stale generated files and missing wiring. Add it with `spinup new --modules auth,apikeys` or `spinup module add apikeys`. See `docs/build/api-keys.md`.
- Feature flags (ADR 0029). The opt-in `flags` module declares flags in `[flags.<key>]` tables of `spinup.toml`. `spinup g flags` writes typed keys to `api/internal/flags/flags.gen.go` and `web/src/lib/flags.gen.ts`. A flag is on for everyone, for a role, for named users, or for a sticky percentage of signed-in users. The kit package `kit/x/flags` evaluates flags from an in-process snapshot that a poll refreshes (`FLAGS_REFRESH`, default 10s). Change flags with the app command `flags` or on the `/admin/flags` page. Pages get the public flags in `data.flags`. The doctor checks `flags-drift` and `flags-wired` find stale generated files, expired flags, and missing wiring. Add it with `spinup new --modules auth,flags` or `spinup module add flags`. See `docs/build/flags.md`.
- A separate version space for kit migrations (ADR 0028). The migrations of the `auth` and `audit` packs live in `api/db/kit/` and use the table `spinup_kit_version`, so they cannot clash with app migrations. An existing database adopts its applied kit rows once, under the migration lock, after a schema check. `spinup db migrate kit up|down|status|to <version>|version` (or `go run ./cmd/api migrate kit ...` in `api/`) acts on the kit space only. A rollback needs `--i-know`.
- Sidecar services (ADR 0022, ADR 0023). `[[services]]` in `spinup.toml` and `spinup g service <name> --lang rust|python|node` add a component in another language next to the Go API, with a typed Go client, Compose files for dev and deploy, deploy with rollback, public routes through Caddy, OTel tracing, and `spinup ci services`.
- A PDF report sidecar (ADR 0026): `spinup g service <name> --lang python --kind pdf` (WeasyPrint), `httpx.WriteAttachment`, and `pdf.Serve`.
- A roles guard (ADR 0024): roles in the `[auth]` table, `spinup g roles`, `auth.RequireRole`, the operator command `user`, `--role` in the generators, and the doctor check `roles-drift`.
- An audit trail (ADR 0025): the `audit` module with database triggers, `kit/audit`, `spinup g audit`, a History page, the operator command `audit`, and the doctor check `audit-wired`.
- A performance loop (`make perf`, `make perf-compare`, `docs/contributing/performance.md`) with benchmarks for the kit, the render, and a generated app.
- A request deadline (`app.WithRequestTimeout`, `REQUEST_TIMEOUT`) and a job payload limit (`jobs.WithMaxPayloadBytes`).
- A docs site with the just-the-docs theme. Every command has a help example with real flags.

- CI: Add `scripts/ci/install-tools.sh` (pinned and checksum-verified actionlint, shellcheck, and bun), `scripts/ci/warm-cache.sh` (Go modules, pnpm store, and bun cache for the offline tests), and the composite action `.github/actions/prepare-tests`. The `test`, `golden`, and `matrix` jobs use them.
- CI: Narrow the Windows job. It builds and vets every package for Windows and tests only the packages that `scripts/ci/windows-packages.sh` selects. `scripts/ci/windows-excluded.txt` gives the reason for each excluded package. Skip Unix-only tests on Windows. Fix the local kit replace check for rooted paths and the Git root check of `spinup upgrade` for forward-slash paths on Windows.
- M14: Add the global flag `--confirm-env <name>`. It answers the typed confirmation of a protected environment without a terminal. The value must equal the environment name. A different value fails and never starts a prompt. `--yes` with `CI=true` still works (ADR 0018).
- M14: Render the kit version of generated apps from the kit pin in `stackversions.yaml`. The release check fails when the pin differs from the CLI in major or minor version. Add a test for the action pins in workflows. Add `scripts/do-live-test.sh` for the DigitalOcean live test.
- M13: Add `spinup deploy digital-ocean`. Sign in with a browser, create the droplet, firewall, DNS record, host key pin, and secrets, then deploy. Add the `login`, `logout`, `status`, and `destroy` subcommands.
- M0: Establish the Go CLI, kit, manifests, templates, and checks for deterministic output.
- M1: Generate Go APIs and PostgreSQL migrations. Add build commands, database commands, and documentation checks.
- M2: Track generated files with a lock file. Add upgrades with a three-way merge and template caches.
- M3: Generate resources, models, handlers, migrations, jobs, and OpenAPI contracts.
- M4: Generate SvelteKit applications, authentication, pages, components, and clients. Add the development loop and browser checks.
- M5: Deploy through SSH with blue/green releases. Add encrypted secrets, server commands, rollback, and backups.
- M6: Add OpenTelemetry, metrics, logs, traces, dashboards, alerts, and monitoring commands.
- M7: Verify and restore backups. Add PostgreSQL maintenance, remote checks, dependency updates, and recovery drills.
- M8: Generate GitHub workflows and browser checks. Add release archives, signatures, SBOMs, provenance, an installer, Homebrew, and the CI image.
- kit `auth` limits the auth routes for each client address (ADR 0016). The routes are login, registration, password reset, and the OIDC callback. A client that is over its limit gets 429 with `Retry-After`, before any database work. `AUTH_RATE_LIMIT`, `AUTH_RATE_LIMIT_<CLASS>`, `AUTH_TRUSTED_PROXIES`, `auth.WithRateLimits`, and `auth.WithTrustedProxies` change the limits. Loopback clients have no limit.
- The web `handleFetch` sends the client address to the API in `X-Forwarded-For`, and the deploy `compose.yml` sets `ADDRESS_HEADER` on the web service. Run `spinup upgrade` so that web form logins count for each client and not for the web server.

### Changed

- Strict JSON v2 request decoding (ADR 0027). `httpx.DecodeJSON` rejects unknown and duplicate fields. Generated write routes check required and known fields (`httpx.CheckJSONBody`, `BodyRules`) and limit the body to 1 MiB. Responses and persisted JSON keep the v1 format.
- Owned references use a composite foreign key on `(user_id, id)` (ADR 0015 §7). List endpoints accept an offset up to 10000 and count the total in the same query.
- The kit stores sessions with one query and no password hash at startup. Auth migration `00007_users_auth_version.sql` adds `users.auth_version` and `sessions.user_id`.
- `spinup` caches the tool binaries of generators (`SPINUP_NO_TOOL_CACHE=1` turns the cache off) and runs independent post-steps in parallel.
- `migrate status` prints the kit lines first, as `applied  kit <file>`. App migration numbers continue after the highest number in `api/db/migrations` and `api/db/kit`.

- M14: `spinup deploy digital-ocean status` shows the team as `<name> (<uuid>)` with an environment and without one. The JSON output has `account.team_name` as well as `account.team`.
- M14: The SSH pin cache keeps one `known_hosts-<hash>` file for each user, host, and port. A change of the pinned keys replaces the file atomically. Old files from earlier versions stay unused in the cache directory.
- M14: `codemod.SetTOML` writes one blank line before a table header that it appends.
- A local kit replace in `api/go.mod` now works for image builds (ADR 0018). `spinup build --image` and the deploy build pass the kit directory to Docker buildx as the build context `kit`, and `deploy/Dockerfile.api` uses it. Without the context, the build uses the published kit version. A replace to a missing directory fails with a `Try:` line. Run `spinup upgrade` to get the new Dockerfile.
- `spinup upgrade` and `spinup module add` keep a local kit replace of the current `api/go.mod` when `SPINUP_KIT_REPLACE` is not set. The variable still wins when it is set. A relative replace to a directory in the repository keeps its text. A relative replace that points outside the repository becomes an absolute path, and the upgrade prints a warning. A replace to a missing directory stops the upgrade with a `Try:` line.

- Load test fixes: the database pool holds 20 connections by default (`db.DefaultMaxConns`), and `DATABASE_MAX_CONNS` changes the size. Login, registration, and password reset answer 503 with `Retry-After` when the password hash queue is full (`auth.WithHashLimits`). A login takes its queue place before its database work. One hash runs for each 10 CPUs (at least 1), so a login burst cannot starve the other routes.

- The resource list page costs less to render. The page renders one actions menu (`row-menu.svelte`) outside the rows. Each row has a plain button for the menu and inline icons. The row checkbox is a submit button that works without JavaScript. The toggle form keeps the page number. In the load test, a page view of 20 rows used 1.8 ms of Node CPU, not 4.4 ms (60% less).

### Fixed
- `billing`: Checkout now sends the metered prices of a plan (ADR 0032 section 21, amended 2026-11-07). A plan with a meter has a licensed price and a metered price. Checkout sent only the licensed price, so the subscription had no metered item and Stripe never billed the usage. The route now sends the licensed price with quantity 1, and every metered price of the same plan with no quantity. `stripe.CheckoutParams` has a new field `MeteredPrices`. `stripefake` accepts `line_items[N]` for any N and puts every item on the subscription. `billing check` fails a plan with a meter and no metered price, and warns about a metered price with no meter. Checkout adds only the metered prices with the interval, interval count, and currency of the licensed price, and answers 503 with a `Try:` line if none match. `stripefake` rejects mixed intervals and currencies like Stripe.
- `billing`: A metered price alone no longer buys a plan that has a licensed price. `POST /api/me/billing/checkout` answers 400 with a `Try:` line for the metered lookup key of such a plan. A plan whose prices are all metered (a pure usage plan) can still be bought through its metered price, but only when every declared price is active in Stripe. The plan of a subscription comes from its licensed item.
- kit/jobs: the `UniqueByArgs` comment and the jobs guide now say that a completed job also blocks a unique insert for 24 hours (River's completed retention). The old text said only waiting or running jobs count.
- Deploy pack: `deploy/cloud-init/user-data.yaml` holds the Docker release key (fingerprint 9DC858229FC7DD38854AE2D88D81803C0EBFCD88), so cloud-init needs no keyserver. A keyserver lookup failed on a live droplet and stopped the Docker install. Run `spinup upgrade` to get the file.
- `spinup deploy digital-ocean`: only one provisioning or destroy run works on an environment at a time, and `spinup-host-key` serializes `next` and `activate`. Two runs raced in the host key rotation. A failed command on the droplet shows its stdout.

- Upgrade: Never merge `api/go.mod` or `api/go.sum` as text. Keep the app file and let the dependency step raise the kit pin, the stack pins, and the `go` line. A require of the unpublished placeholder kit `v0.1.0` moves to the kit pin. Keep the inner `Try:` line when an upgrade phase fails.
- `spinup tools install` accepts the real age release archives. It keeps `age` and `age-keygen` and skips the other regular files under `age/`. It still refuses absolute paths, `..`, nested paths, links, devices, and duplicates.
- The `--platform` default of `spinup plugin new` is empty and resolves to the current platform at run time. The generated command pages no longer depend on the machine.
- M14: `dofake` follows more rules of the DigitalOcean API: name rules for tags, droplets, and firewalls, firewall rule checks, the pending firewall status, `0` for all ports, a duplicate tag, rate limit headers, and `request_id` in error bodies. `spinup deploy digital-ocean` refuses an environment name that ends with a dash, because DigitalOcean refuses the droplet name.

- The web pack `app.css` defines the shadcn-svelte variants `data-open:`, `data-closed:`, `data-checked:`, `data-unchecked:`, `data-disabled:`, `data-active:`, `data-horizontal:`, and `data-vertical:`. Before this change, the checkbox had no checked fill, and the menus had no open and close animation.

### Breaking

- No release is declared by this section.
- Go 1.27 is the minimum (ADR 0027). Generated apps declare `go 1.27.0`. `GOEXPERIMENT=nojsonv2` is not supported.
- A generated write route answers 422 for a field that the resource does not declare. Clients that send extra fields must stop.
- `testkit.DBWithMigrations` takes options (`...testkit.DBOption`). Every call still compiles. Code that uses the function as a value must change.
- `db.WithMaxConns` takes an `int`, not an `int32`.

### Upgrade steps

- Upgrade to `0.1.0-alpha.419` or later in one step. Codemods `0002_validation_known_fields`, `0003_kit_migrations`, and `0004_kit_testkit` run during `spinup upgrade`. They add the known-field rules, move the pack migrations to `api/db/kit/`, and add `testkit.WithKitMigrations` to the generated tests.
- After the upgrade, run `spinup db migrate up` before you start the new binary. The first run prints one `adopted  kit <file>` line for each kit migration that the database already applied. `spinup deploy` runs the migration for you.
- If the upgrade stops with `is a renamed kit migration`, follow its `Try:` line once for each database.
- Review `spinup upgrade --plan` before each upgrade.
- Commit the generated files and the lock file after a successful upgrade.
