# R0.9 Reference app D: web

Issue: #17. Spike code: `spikes-rs/r0-09-ref-web/`.

## Question

Does the existing SvelteKit web pack, as the Go CLI renders it, work against the Rust reference API without a change? The contract is the session cookie through SSR and form actions, the `todo` resource pages, `traceparent` from the web server to the API, and the Playwright suite of `testdata/e2e/web`. Does the ADR 0002 evidence still hold when axum replaces Go `net/http`?

## Method

I joined the merged R0.6, R0.7, and R0.8 code in one workspace. I did not change the three earlier spike directories.

- `kit`: the R0.7 kit (auth, mail) plus `jobs.rs` and `telemetry.rs` of R0.8, with the R0.8 features `jobs` and `telemetry` (default on). `kit::app::router` has the R0.8 HTTP span layer. `App::serve_on` keeps the R0.7 `ConnectInfo` service. The workspace keeps `SQLX_OFFLINE = "true"` and the dev profile `opt-level = 3` for `argon2` and `blake2`.
- `kit/src/apidocs.rs` (new): a port of the Go `kit/apidocs`. It serves `/api/openapi.yaml`, `/api/docs`, `/api/docs/boot.js`, and `/api/docs/scalar.js` with the same CSP and gzip rule. It embeds the vendored Scalar bundle of the Go kit with `include_bytes!`. The suite test `apidocs.spec.ts` needs it.
- `todo-api`: the resource of the e2e app, not the R0.6 golden resource. The e2e app runs `spinup g resource todo title:string done:bool 'due:date?'` with the auth module. So the todo has no `priority`, and each row has a `user_id` (ADR 0015). Every query filters on the signed-in user. Without a user, each operation answers 401 with the Go text `A signed-in user is required.`. The migration `00006_create_todos` replaces `00002_create_todos`, because the table now refers to `users`. `serve` mounts the auth layer and, with `SPINUP_ENV=dev` or `SPINUP_API_DOCS=true`, the API reference. The service name is `demo-api`.
- `web/`: the `web/` directory that the Go CLI renders for the e2e app (`spinup new demo --modules ci,auth`, then the `g resource` command above), without `node_modules` and `.svelte-kit`. I copied it unchanged.
- `run.sh` (the † check):
  1. It builds the Go CLI, renders the app again, and runs `diff -r` against the committed `web/`. A difference fails the run.
  2. It installs with `pnpm install --frozen-lockfile --offline` and builds with `PUBLIC_ORIGIN` and `API_URL`.
  3. It generates `schema.d.ts` from the Rust spec with openapi-typescript 7.13.0 and compares it with the committed client (made from the Go spec).
  4. It builds `todo-api` (debug), empties the database `spinup_r09_e2e` on 127.0.0.1:55433, and runs `todo-api migrate up`.
  5. It starts the local `grafana/otel-lgtm` image (`--pull never`, the R0.8 digest) when it exists. The API and the web server send OTLP to it.
  6. It starts the API and `node build` with the environment of `testdata/e2e/web/run.sh`. It checks the ADR 0002 items with curl. It runs the 14 tests of `testdata/e2e/web/tests`, then the 2 tests of `web/e2e/smoke.spec.ts`.
  7. It searches Tempo through Grafana with TraceQL `{ resource.service.name = "demo-web" } >> { resource.service.name = "demo-api" && kind = server }`. Then it loads each trace and checks that a `demo-api` span has a `demo-web` span as its parent.
- For the baseline, I ran `testdata/e2e/web/run.sh` (the Go API) on the same machine, a few minutes later, against the same database.

Tests (`scripts/rust/spike-check.sh r0-09`, 109 tests, all pass): kit unit 55, `kit/tests` auth 15, jobs 8 (1 ignored: the R0.8 baseline measurement), oidc 6, otlp 1, ratelimit 5, reset 5, telemetry 1, `todo-api` api 10, auth_ratelimit 2, jobs 1. The new `todo-api` tests cover the owner rule (`todos_belong_to_their_owner`) and the 401 answers (`todos_need_a_signed_in_user`). `cargo sqlx prepare --workspace` ran against `spinup_r09` on 55433 (32 queries in `.sqlx/`). `cargo sqlx prepare --check` passes.

Machine: Apple M1 Max, 10 cores, 64 GB. Other heavy sessions ran at the same time. Builds and runs used `nice -n 10` and `CARGO_BUILD_JOBS=4`. Rust 1.99.1, Node 24, pnpm 12.11.2, Playwright 1.64.0 (Chromium), PostgreSQL 18.6, `grafana/otel-lgtm` 0.36.0.

## Numbers

Web pack:

| Item | Result |
|---|---|
| Changes to the web pack | 0. The rendered `web/` equals the committed `web/` (`diff -r`), and the suite ran on it. |
| `schema.d.ts` from the Rust spec against the committed client from the Go spec | 0 differing lines |
| Spec differences that the client sees | 0. The Rust spec is OpenAPI 3.1.0, the Go spec 3.0.3, as in R0.6. |
| `pnpm install --frozen-lockfile --offline`, `pnpm build` | pass |

Playwright (Rust API, 3 runs, all pass):

| Run | LGTM | Suite (14 tests) | Smoke (2 tests) | Load (1 min), start to end |
|---|---|---|---|---|
| 1 | on | 14 passed, 18.7 s | 2 passed, 1.5 s | 18.4 to 14.3 |
| 2 | on | 14 passed, 17.3 s | 2 passed, 1.6 s | 9.4 to 8.1 |
| 3 | off | 14 passed, 18.6 s | 2 passed, 1.6 s | 5.6 to 8.9 |
| Go baseline (`testdata/e2e/web/run.sh`) | off | 14 passed, 13.7 s | 2 passed, 1.3 s | 7.1 to 6.2 |

The difference to Go is mostly the login rate limit test: 6.3 to 6.8 s with Rust, 4.0 s with Go. That test sends 20 or more logins, and each login hashes a password. R0.7 measured one argon2id hash at 98 ms in Rust (one thread) and 54 ms in Go (4 lanes). The other 13 tests take 0 to 0.2 s more each. Run 3 shows that the OTel export is not the cause.

ADR 0002 evidence against axum (curl checks of `run.sh`, run 2):

| ADR 0002 check | Result with the Rust API |
|---|---|
| cookie-ssr: the session cookie set through a form action survives SSR | PASS. `POST /register` (same origin) gives the browser `session=<value>; Max-Age=604799; Path=/; HttpOnly; SameSite=Lax`. `GET /account` with the cookie renders the email. The suite signs in, reloads, and signs out. |
| form-action: same-origin POST accepted, cross-site or missing Origin 403 | PASS. `Origin: http://evil.example` gives 403, no Origin gives 403, same origin succeeds. The Rust API also answers 403 to a cross-site `POST /api/auth/login`. |
| paths-origin: `paths.origin` controls the CSRF origin | PASS. The build bakes `PUBLIC_ORIGIN`, and only that origin passes. |
| traceparent: the SvelteKit server sends `traceparent` to the API | PASS. Tempo has the traces, see below. |
| docker-build | not run: R0.9 does not change the web image. |

Traceparent (Tempo through Grafana, `grafana/otel-lgtm` 0.36.0):

| Run | Traces found | Example |
|---|---|---|
| 1 | 4 of 4 checked have an API server span with a web parent | root `demo-web` `sveltekit.handle.root`. The API span `POST /api/auth/login` is a child of the undici span `POST`. 11 spans, services `demo-api` and `demo-web`. |
| 2 | 2 of 2 checked | `GET /api/session` under the web `GET` span (8 spans), `POST /api/auth/register` (11 spans) |

The Tempo search returned only a few traces in each run (4 and 2), because the search window and the batch export cut it. It found at least one in each run.

Request statuses in the API log of run 1: 116 × 200, 61 × 201, 5 × 204, 23 × 401, 1 × 404, 1 × 429. The API log has no WARN and no ERROR line.

Build and size (debug, `todo-api`):

| Item | Value | Load (1 min) |
|---|---|---|
| Clean debug build of `todo-api` (with `SQLX_OFFLINE=false`) | 99.4 s (user 190 s) | 11.9 to 14.4 |
| Debug binary | 67.6 MB (67 588 472 B), with the embedded Scalar bundle (1.26 MB) | |
| Packages (normal, `todo-api`) | 288 | |
| Packages in `Cargo.lock` | 374 | |

## Verdict

Verdict: go

The web pack runs against the Rust API with 0 changes. The 14 tests of `testdata/e2e/web` and the 2 smoke tests of the app pass in 3 of 3 runs. The client generated from the Rust spec equals the committed client. The session cookie works through form actions and SSR, and the SvelteKit CSRF check and the Rust cross-origin check both refuse a cross-site POST. Tempo shows traces where the API server span is a child of the web span, so `traceparent` reaches the API and both services export to Tempo. The ADR 0002 evidence holds against axum. The Rust API needed three changes, all in the API: the owned todo resource of the e2e app, a port of `kit/apidocs`, and the join of the R0.6 to R0.8 code.

## Recommendation

- Keep the web pack as it is for the Rust API. The hooks, `postAuth`, `forwardSessionCookie`, the undici instrumentation, and the generated client need no change. R3 changes only the comments that name "the Go API" (`hooks.server.ts`, `auth.ts`, `client.ts`, `instrumentation.server.ts`). That is text, not behavior.
- Port `kit/apidocs` to the Rust kit in R1 (`kit/src/apidocs.rs` of this spike is a start). The suite needs `/api/docs` and `/api/openapi.yaml` with the strict CSP. Keep the vendored Scalar bundle in one place, and make the Rust kit crate carry its own copy (a published crate cannot use `include_bytes!` outside its package).
- The resource generator (R2) must write the owner form when the auth module is on: `user_id` in the table, the filter in each query, and the 401 answer before any other check. The Go order is also the Rust order: the owner check runs before the page range check, so an anonymous `?limit=201` answers 401, not 422.
- Finding: the Rust API sends `Max-Age=604799` on the session cookie, and Go sends 604800. tower-sessions truncates the time between the expiry and now. The web forwards the value unchanged. The cookie lives 1 s less, so no user sees it, but a test that compares the header text fails. R1 must round up as scs does, or own the cookie (see the R0.7 note about an own session layer).
- Finding: Rust auth is slower in the browser suite (rate limit test 6.3 to 6.8 s against 4.0 s), because one argon2id hash runs on one thread. This repeats the R0.7 number. R1 must decide the hash parallelism with the load test.
- Run `run.sh` again at the R1 gate: `DATABASE_URL=postgres://postgres:postgres@127.0.0.1:55433/postgres ./run.sh` in `spikes-rs/r0-09-ref-web/`. It needs Docker only for the Tempo step (`LGTM=0` skips it).
- Not done: the docker-build check of ADR 0002 (the web image does not change), password reset and OIDC through the web pages (the web pack has no pages for them), a Mailpit run, and `vite dev` with the proxy against the Rust API. The databases `spinup_r09` (the schema for `cargo sqlx prepare`) and `spinup_r09_e2e` (emptied by each run) stay on 127.0.0.1:55433.
