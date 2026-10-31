# ADR 0023: Sidecar services follow-ups

Status: accepted (2026-10-31)

## Context

ADR 0022 added sidecar services and put four items out of scope: public routes through Caddy, OTel tracing inside the templates, more template languages, and CI jobs for each service. Issues #38 to #42 track them, with #42 for the upkeep of the sidecar pins. This ADR is the contract of the orchestrate run `20261031-104322`. ADR 0022 still applies where this ADR says nothing.

## 1. Public routes (#38)

- A `[[services]]` table can set `public = "/svc/<name>"` and `auth = "none" | "session"`. `internal/services` validates both (`Service.Public`, `Service.Auth`, `AuthNone`, `AuthSession`, `ReservedPublicPrefixes`, `IsPublic`, `AuthMode`).
  - A prefix starts with `/`, does not end with `/`, is not `/`, and each segment matches `[a-z0-9][a-z0-9._-]*`.
  - A prefix cannot equal or sit under a reserved path (`/api`, `/_app`, `/healthz`, `/readyz`, `/version`, `/.well-known`), or equal or nest with the prefix of another service.
  - `auth` without `public` is an error. An empty `auth` means `none`.
  - `auth = "session"` needs the `auth` module. `project.Validate` checks this, because only the project layer knows the modules. The error has a `Try:` line.
- Deploy writes the managed file `Caddyfile.d/spinup-services.caddy` into each release directory when at least one service is public. It is a deploy artifact, like `compose.services.yml`. It is not a template file, so no pack and no golden tree changes. An app with no public service gets no such file and a byte-identical release.
  - Deploy refuses a user file with that name in `deploy/Caddyfile.d/`, with a `Try:` line.
  - Each public service gets `handle_path <prefix>/* { ... import upstream.caddy <name> <port> }`. A request to the bare prefix gets a 308 redirect to `<prefix>/`.
  - The route imports `upstream.caddy`, so it follows the color switch and rollback with no extra code.
- `auth = "session"` adds a `forward_auth`-style check before the proxy: a `GET /api/session` subrequest to the API of the live color, with the cookies of the request. A 2xx answer sends the request on to the service. Any other answer goes to the client unchanged (401 without a session). Before the first switch, the subrequest gets the 503 placeholder, so the route fails closed.
  - The API middleware holds the CSRF check, and a public route skips it. So for `auth = "session"` the route answers 403 to a request whose method is not GET, HEAD, or OPTIONS when its `Origin` header is not `{scheme}://{host}` of the request.
  - The service gets the cookies, but no user identity header in this version.
  - `upstream.caddy` may get a Caddy `{block}` placeholder so a route can add subdirectives. A two-argument import without a block must expand exactly as before. `make dind` proves that Caddy accepts the result.
- The rate limits of ADR 0016 do not cover public routes. Each `session` request costs one session lookup in the API. The service owns its own limits. The docs say so.
- `spinup g service` gets `--public <prefix>` and `--auth none|session`.

## 2. OpenTelemetry in the templates (#39)

- The Rust and Python templates export traces with OTLP over HTTP to `OTEL_EXPORTER_OTLP_ENDPOINT`. The deploy and dev Compose files already set it, with `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES`. When the variable is not set (tests, offline), telemetry is a no-op.
- Each template extracts the W3C `traceparent` header, so a server span is a child of the API span. The Go client already injects `traceparent` (`otelhttp` transport, the global TraceContext propagator of the kit).
- `/healthz` makes no span.
- Rust uses `opentelemetry`, `opentelemetry_sdk`, `opentelemetry-otlp` (`http-proto`, `reqwest-blocking-client`, no TLS, no gRPC), `opentelemetry-http` (`HeaderExtractor`), and `tracing-opentelemetry`. The OTel crates share one minor version. It uses a small axum middleware, not `axum-tracing-opentelemetry`.
  - The 0.33 API names: `opentelemetry_otlp::SpanExporter::builder().with_http().build()`, `opentelemetry_sdk::trace::SdkTracerProvider`, `InMemorySpanExporter` (tests), `opentelemetry_http::HeaderExtractor`.
- Python uses `opentelemetry-sdk`, `opentelemetry-exporter-otlp-proto-http`, and `opentelemetry-instrumentation-fastapi` (`FastAPIInstrumentor.instrument_app`, with `/healthz` in the excluded URLs). It never uses `opentelemetry-exporter-otlp`, which pulls `grpcio`.
- Unit tests use in-memory exporters and need no socket. A Docker e2e reads one trace from Tempo (`grafana/otel-lgtm`) that crosses both templates.

## 3. The Node template (#40)

- `spinup g service <name> --lang node` adds a Node 24 TypeScript service on Hono (`hono`, `@hono/node-server`). There is no build step: Node 24 strips the types (`node src/main.ts`). `tsconfig.json` sets `erasableSyntaxOnly` and `allowImportingTsExtensions`, and `typescript` runs only as `tsc --noEmit`.
- It follows the template contract of ADR 0022: `GET /healthz`, `POST /v1/sum` with the `SumResult` schema, the shared `openapi.yaml` (byte-identical across languages), a pinned lockfile, and an in-image health check command declared in `spinup.toml`.
  - The image is the pinned `node` image. It runs as user `node` (1000:1000), works with a read-only root file system, installs with `pnpm install --frozen-lockfile --prod --ignore-scripts` through corepack, and has no dev tools.
  - The health check command is `["/usr/local/bin/node", "/app/src/healthcheck.ts"]`. Absolute paths keep it independent of the working directory of the exec.
- Tests use `node --test` and `app.request()` from Hono, so they need no socket and no extra dependency.
- The pins are in `stackversions.yaml` `node_packages` (`Stack.NodePackages`), and `typescript` is in `npm`. `TestTemplatePins` checks that `package.json` matches them exactly. `package.json` and `pnpm-lock.yaml` are shared files.

## 4. CI for the services of an app (#41)

- `spinup ci services [--json]` prints the services of `spinup.toml` as a GitHub matrix: `{"include":[{"name":"ocr","lang":"rust"}]}`. The language comes from the files in `services/<name>/`: `Cargo.toml` is `rust`, `pyproject.toml` with `uv.lock` is `python`, `package.json` is `node`, and any other service is `none` (build only).
- `spinup ci test-service <name> [--dry-run]` renders a throwaway test Dockerfile from the pinned images and runs the tests of the service in it, through `internal/exec`. `--dry-run` prints the Dockerfile. The runner needs only Docker.
  - Rust: `cargo test --locked`.
  - Python: `uv sync --locked`, then `pytest`.
  - Node: `pnpm install --frozen-lockfile`, then `pnpm test`, then `pnpm run typecheck` if the script exists.
- The ci pack workflow gets a `services` matrix job fed by `spinup ci services --json`. It builds each image with `spinup build --image --target <name>` and runs `spinup ci test-service <name>`. It does not run when the app has no services. `spinup g service` therefore needs no CI edit.
  - `ci.yml` is a managed file, so existing apps get the job through the 3-way merge. `packs.digest` records `none`.

## 5. Upkeep of the sidecar pins (#42)

- `scripts/bump-services.sh report` lists newer versions of the crates, Python packages, and Node packages from crates.io, PyPI, and npm. For each image, it prints the pinned tag and `manual check`, because tag schemes differ by registry. It writes nothing.
- `scripts/bump-services.sh lock` regenerates `Cargo.lock`, `uv.lock`, and `pnpm-lock.yaml` inside the pinned images, from the pins of the manifests. `--dry-run` prints the commands.
- Only CI or the main loop runs the script, because it writes shared files. A weekly canary workflow posts the report, regenerates the lockfiles, and runs the services app gate. The direct pins and the image digests do not change, so the canary tests newer transitive dependencies only. A newer direct pin or image needs a manual bump, which the report shows. The canary opens no pull request.
- A tool that resolves newer direct pins and image tags, tests them, and opens bump pull requests for the whole stack is a later issue.

## Consequences

- Generated apps without public services, without the new templates, and without services in CI behave exactly as before.
- Existing services keep their code. The OTel and Node changes reach only new services, because `services/<name>/` is scaffold output.
