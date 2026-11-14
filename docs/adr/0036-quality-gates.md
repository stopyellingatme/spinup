# ADR 0036: Stricter quality gates

Status: accepted (owner, 2026-11-13)
Date: 2026-11-13
Extends: ADR 0005 (doc tests), ADR 0008 (client generation), ADR 0012 section 4 (generated workflows), ADR 0014 (embed mode), ADR 0027 (strict JSON)

ADR 0033 (live data) and ADR 0035 (agent guidance) are reserved for the next two milestones.

## Context

The comparison with corten on 2026-11-13 (ADR 0034) found these gaps in the gates of spinup:

- The doctor check `client-drift` only warned, and it skipped when `web/node_modules` did not exist. That is the case in the generated CI job `spinup`. No step proved that `bundle.gen.yaml`, `api.gen.go`, the sqlc output, and the TypeScript client match their sources.
- No test built an app with every module, and no script ran `spinup check` and `spinup doctor` after a chain of `module add`.
- The docs had no lint for links. Two pages said Go 1.26 while the stack says 1.27.
- The SvelteKit node server sent only `frame-ancestors 'none'` as its CSP. The embed `index.html` allowed `'unsafe-inline'` scripts. No test checked that a rendered page escapes user text.
- `httpx.DecodeJSON` and `httpx.CheckJSONBody` had no fuzz test.

The owner wants CI runner minutes kept low. Each gate here runs in an existing job or costs seconds.

## Decision

### 1. Drift checks

| Check ID | Package | What it does | Status when stale |
|---|---|---|---|
| `openapi-drift` | `internal/doctor/gendrift` | Compares `openapi.Bundle(apiDir)` with `api/openapi/bundle.gen.yaml`. When the bundle has paths, it runs oapi-codegen to a temporary file through `run.ToolCommand` and compares the result with `api/internal/gen/api.gen.go`. | `fail`, `Try: spinup g openapi` |
| `sqlc-drift` | `internal/doctor/gendrift` | Runs `sqlc diff` through `run.ToolCommand`. It skips when `api/db/queries` has no `.sql` file. | `fail`, `Try: spinup db sqlc` |
| `client-drift` | `internal/doctor/clientdrift` | As before. | `fail` (was `warn`), `Try: spinup g client`. A tool failure is `fail`. A missing `web/node_modules` stays `skip`. |

1. The two new checks are local and not quick. They skip when the app has no `api` directory.
2. The generated CI job `web` gains one step after its install: it runs `gen:client` and fails when `git status --porcelain -- src/lib/api` prints a path. The job `spinup` proves that the bundle matches its sources. The job `web` proves that the client matches the bundle.
3. The job `spinup` adds `api/tools.sum` to its `cache-dependency-path`, so that the tool builds are cached.
4. A stamp of input hashes in `spinup.lock` is rejected. Any `sqlc generate` outside spinup would fail the gate.

### 2. Content Security Policy

1. Node mode: the managed `web/vite.config.js` sets `csp` in the SvelteKit options, with `mode: 'auto'` and these directives: `default-src 'self'`, `script-src 'self'` (SvelteKit adds the nonce), `style-src 'self' 'unsafe-inline'`, `img-src 'self' data:`, `font-src 'self' data:`, `connect-src 'self'`, `object-src 'none'`, `base-uri 'self'`, `form-action 'self'`, `frame-ancestors 'none'`.
2. A unit may split `style-src` into `style-src-elem 'self'` and `style-src-attr 'unsafe-inline'` only when `make e2e` passes with it.
3. The scaffold `hooks.server.ts` does not change. It sets `frame-ancestors 'none'` only when the response has no CSP.
4. Embed mode: `WithSPA` sends `script-src 'self'` plus one `'sha256-...'` source for each inline script of `index.html`. `'unsafe-inline'` stays only in `style-src`.
5. Trusted Types are deferred. The behaviour of shadcn and bits-ui under them is not verified.

### 3. XSS tests

1. `testdata/e2e/web/tests/xss.spec.ts` creates a todo with the title `<img src=x onerror="window.__xss=1">`. It asserts the literal text on the list, read, and edit views, that `window.__xss` is not set, that no dialog opened, that no `securitypolicyviolation` event fired, and that the CSP header of the page has a nonce.
2. The seed `web/e2e/smoke.spec.ts` asserts that the CSP header has `script-src`.
3. `TestNoRawHTML` in `internal/templates` fails when a pack file or a generator template contains `{@html`.
4. The switch `E2E_BROWSERS=all` adds Firefox and WebKit to `testdata/e2e/web/playwright.config.ts`. The default stays Chromium. Generated apps stay on Chromium.

### 4. All-modules gate

1. `testdata/script/all_modules.txtar` runs `spinup new` with every module, then `go build`, `go vet`, `spinup check`, and `spinup doctor --json`. It then creates a minimal app, runs a chain of `module add`, and runs `spinup check` again. It skips on darwin, where the CI runner costs about ten times the Linux price.
2. `testdata/script/app_gate_all.txtar` (app gate) adds `pnpm check` and a build of the web app.

### 5. Docs lint

1. `TestDocsLint` in `internal/doctest` checks every page under `docs/` for: a relative link to a file that does not exist, a link anchor that the target page does not have, a code fence that does not close, and a table row whose cell count differs from its header.
2. `TestDocsFacts` checks each "Go 1.N or newer" and "Go 1.N or later" statement against `go.min` of `stackversions.yaml`.
3. Both names match the `TestDocs` pattern, so `make doctest` and `make test` run them.
4. A check that each cited commit is reachable is rejected. The docs cite no real commits. `docs/contributing/writing-docs.md` says: cite an ADR or a release, not a hash.

### 6. Fuzz targets

1. `kit/httpx/fuzz_test.go` has `FuzzDecodeJSON` and `FuzzCheckJSONBody`.
2. Properties: no panic. The status of an error is 400, 413, or 422. After `CheckJSONBody` the body is byte-equal and readable. A body that `DecodeJSON` accepts is not a 400 in `CheckJSONBody` with empty rules. A duplicate key is always refused.
3. The seed corpus runs in the normal `test` job. `nightly.yml` fuzzes each target for 60 seconds.

### 7. CI minute budget

| Gate | Where | Cost |
|---|---|---|
| Drift checks | the existing `spinup doctor` step of a generated app | none for a new app, 10 to 20 s warm for an app with routes and queries (estimate) |
| Client diff | the generated `web` job | about 3 s |
| Docs lint, `TestNoRawHTML`, fuzz seeds, `all_modules` | the spinup `test` job | about 1 min in total (estimate) |
| XSS e2e, Firefox and WebKit, 2 fuzz targets | nightly | about 7 min (estimate) |

## Consequences

- An existing app whose client is stale now fails `spinup doctor`. The fix is `spinup g client`.
- A page with an inline script that SvelteKit does not render gets a CSP violation in node mode. The owner adds a nonce (`%sveltekit.nonce%`) or moves the script to a module.

## Unit file sets

See `docs/design/plan.md`, section "N1".
