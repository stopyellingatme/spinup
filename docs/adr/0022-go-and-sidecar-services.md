# ADR 0022: Stop the Rust rewrite, and add sidecar services

Status: accepted (2026-10-30)

ADRs 0019 to 0021 are on the branch `rust`. They record the Rust rewrite up to R2. This ADR has the next free number after them, so the numbers stay unique.

## Context

On 2026-10-24 we started a rewrite of spinup in Rust (ADR 0019 on `rust`, plan `docs/design/rust-plan.md`, epic #2).
R0, R1, and R2 are done. On 2026-10-29 we measured the Go product (`main`) against the Rust branch on the same machine (branch `bench`, `make bench`).

The results:

| Measure | Go | Rust |
|---|---:|---:|
| `spinup --help` | 20.6 ms | 10.7 ms |
| Generate 20 resources (files only) | 12.1 s | 3.6 s |
| API release binary | 28.1 MB | 13.2 MB |
| API start to `/healthz` | 51 ms | 16 ms |
| API memory after load | 157 MiB | 90 MiB |
| Cold dev build of the app | 11 s | 118 s |
| Release build | 11 s | 162 s |
| Check after an edit | 0.42 s | 1.21 s |
| Handler edit to new response | 1.8 s | 3.5 s |
| Add a resource, end to end | 18.6 s | 37.5 s |
| Signed-in list route | 2,470 req/s | 1,645 req/s |

Rust wins on what the users of an app run. Go wins on what the developers of an app wait for.
spinup is a Buffalo-style tool, so the dev loop is its main promise. A spinup app waits on PostgreSQL, not on the CPU.
The Rust compiler gives better errors, but the same work makes it slow. Tuning cannot remove most of the gap.

Some apps need work that Go does badly: CPU-heavy work, GPU work, or a library that exists only in another language (for example Python machine learning).
A generated app has no supported way to run such a component today:
- `deploy/compose.override.yml` can hold extra services, but `spinup build --image` and `spinup deploy` build, ship, and check only the `api` and `web` images (`internal/build/image.go:45`, `internal/deploy/flow_preflight.go:169`).
- `spinup dev` starts only PostgreSQL and Mailpit (`deploy/compose.dev.yml`, a managed file).
- Plugins make files. They do not run in the deployed app (ADR 0013 §4, ADR 0014 §1).
- `spinup dev` and the deploy templates do not start the jobs worker (`docs/mail.md:50`). So a job cannot hand work to anything in production.

## Decisions

### 1. Stop the Rust rewrite

- Go stays the language of the CLI, the kit, the plugin SDK, and the generated API.
- The Go freeze ends. The tag `go-freeze-r0` stays as a marker. Go gets normal changes again.
- The branches `rust` and `bench` stay as a reference. Nobody deletes them. `rust` is on GitHub. `bench` is local.
- `docs/design/rust-plan.md` stays on `main` with the status "stopped". CLAUDE.md drops the Rust lane rules.
- The epic #2 and the open milestones #6, #7, and #8 close as "not planned", with a link to this ADR.

### 2. Sidecar services

A sidecar service is a component of an app in its own container, next to the API. It can use any language.
The API (and the worker) call it over HTTP on the private `app` network. The service is not public in this version.

**Layout.** A service lives in `services/<name>/`. That directory is the Docker build context, and `services/<name>/Dockerfile` builds the image.

**Declaration.** `spinup.toml` declares each service:

```toml
[[services]]
name = "ocr"                         # required
port = 8080                          # required: the container port
dev_port = 8101                      # required: the host port in `spinup dev`
health = "/healthz"                  # required: an HTTP path that answers 200 when the service is ready
openapi = "openapi.yaml"             # optional: relative to services/<name>/; "" means no Go client
healthcheck = ["/service", "healthcheck"]   # optional: the Compose health check command (exec form)
secrets = ["OCR_LICENSE_KEY"]        # optional: secret names that the service can read
env = { MODEL = "small" }            # optional: literal, non-secret variables
db = false                           # optional: true joins the `db` network
gpus = ""                            # optional: "" (none), "all", or a count such as "1"
```

Rules (`internal/services`, the contract package):
- `name` matches `^[a-z][a-z0-9]{0,19}$`. A name cannot be a reserved name: `api`, `web`, `migrate`, `worker`, `caddy`, `postgres`, `alloy`, `mailpit`, `grafana`, `loki`, `tempo`, `mimir`, `pyroscope`, `backup`, `backups`, `edge`, `monitoring`.
- The derived names are fixed:
  - the environment prefix `SERVICE_<NAME>` (the name in upper case)
  - the image `<app>-<name>:<release-id>`
  - the network alias `<name>-${SPINUP_COLOR}`
  - the URL variable `SERVICE_<NAME>_URL`, with the value `http://<name>-${SPINUP_COLOR}:<port>` in deploy and `http://127.0.0.1:<dev_port>` in dev
  - the OTel service name `<app>-<name>`
- `port` and `dev_port` are from 1 to 65535. `dev_port` is unique across services and differs from the API and web dev ports.
- A secret name follows the `spinup secrets` rule `^[A-Z_][A-Z0-9_]*$`. An `env` key follows the same rule and cannot start with `SPINUP_` or `OTEL_`.
- `project.Config.Services` holds the list. `project.Validate` runs the rules. Each error ends with a `Try:` line.

**Compose files from the declaration.** spinup writes the Compose files of the services from `spinup.toml`. They are not template files, and they are not in the lock.
- Deploy writes `compose.services.yml` into the release directory. The color project uses it with a second `-f`. Rollback uses the file of its release.
- Each service gets the same hardening as the API: a non-root user, `read_only: true`, a `/tmp` tmpfs, `cap_drop: [ALL]`, `no-new-privileges`, log rotation, and a health check when `healthcheck` is set.
  The image ref is a literal. The color stays `${SPINUP_COLOR}`, because rollback changes only the color in `.env`.
- Each named secret is a read-only bind of the one file `/opt/<app>/secrets/<NAME>` to `/run/secrets/<NAME>`. A service never sees the other secrets.
- With the monitoring module on, the service gets `OTEL_EXPORTER_OTLP_ENDPOINT=http://alloy:4318`, `OTEL_SERVICE_NAME=<app>-<name>`, and `OTEL_RESOURCE_ATTRIBUTES=service.instance.id=${SPINUP_COLOR}`.
- `gpus` adds a device reservation (`driver: nvidia`, `capabilities: [gpu]`).
- The same file adds `SERVICE_<NAME>_URL` to the `environment` of `api`, and of `worker` when the jobs module is on.
- Replicas and resources come from `deploy/environments/<env>.toml` (`[replicas] <name>`, `[resources.<name>]`), as for `api`.
- `spinup dev` writes `.spinup/compose.services.yml`. It has `build: services/<name>` and publishes `127.0.0.1:<dev_port>`.
- The renderers are pure functions in `internal/services`. A unit test runs the `compose-security` lint rules on their output.

**Build and deploy.**
- `spinup build --image` builds each service with `-f services/<name>/Dockerfile` and the context `services/<name>`. `--target` selects images by name, also with `--image`.
- Deploy builds, ships (ssh or registry), and checks each service like `api`. The image list maps to services by name, not by index.
- The switch waits until each replica of each service answers `health` through the Caddy container, like `api`.
- `release.json` records the services (name, port, health, image). Rollback reads that record.
- `deploy --skip-build` finds the service images with the same release ID.

**The Go client.** For each service with `openapi`, spinup generates a typed Go client:
- `api/internal/services/<name>/oapi-codegen.yaml` (managed by the generator): package `<name>`, `client: true`, `models: true`.
- `api/internal/services/<name>/client.gen.go`: the output of the pinned oapi-codegen. It is generated, not locked, and committed (like `api.gen.go`, ADR 0007).
- `api/internal/services/<name>/<name>.go` (scaffold): a `Config` struct with `URL string` from `SERVICE_<NAME>_URL`, and `New(cfg Config) (*ClientWithResponses, error)`.
  It uses an `http.Client` with `otelhttp.NewTransport` and a 30-second timeout, so traces join across the call.
- `spinup g client` and the `openapi` post-step regenerate the service clients. `spinup dev` regenerates a client when its `openapi` file changes.
- This needs no new dependency. oapi-codegen v2.8.0, `oapi-codegen/runtime`, and `otelhttp` are already pinned.

**Templates.** `spinup generate service <name> --lang rust|python` writes `services/<name>/` and appends the `[[services]]` table to `spinup.toml`.
- `rust`: axum on tokio, with the package and binary name `service`, a committed `Cargo.lock`, `/healthz`, one example route, and a `service healthcheck` subcommand. The image builds on the pinned `rust` alpine image and runs on `distroless/static-debian13` (the R2 pins).
- `python`: FastAPI on uvicorn, managed with uv, with the project name `service` and a committed `uv.lock`. The image uses the pinned `python` slim image.
- Each template has `openapi.yaml` (the contract that the Go client uses), a README, and a test.
- The fixed package name `service` keeps the lockfile the same for each service, so the generator copies it without changes.
- `stackversions.yaml` pins the crates, the Python packages, and the images. Only the main loop edits it.
- The generator never writes over an existing `services/<name>/`. It fails with a `Try:` line.

**Plugins.** A plugin can supply service templates:
- `plugin.toml` gets `[[service_templates]]` with `name`, `description`, and `dir` (a directory in the plugin).
- `spinup g service <name> --lang <plugin>/<template>` renders that directory with `text/template` and the data `Name`, `App`, `Port`. A `.tmpl` file is rendered and loses the suffix. Other files are copied.
- The output paths stay under `services/<name>/`. `pack.CheckOutputPath` checks each one.
- This is data only (tier 1). spinup runs no plugin code for it.

**The jobs worker.** Dev and deploy start the worker when the jobs module is on:
- `deploy/Dockerfile.api` also builds `/worker`. `compose.yml` gets a `worker` service with the API image, the `db` and `app` networks, and the API hardening.
- The worker gets a `healthcheck` subcommand that checks the database.
- `spinup dev` supervises `go run ./cmd/worker` next to the API.
- A job hands work to a service through the service client in `jobs.Deps`. `docs/services.md` shows the pattern.

**The doctor check.** The doctor check `services` reports these problems:
- a declared service with no `services/<name>/Dockerfile`
- a missing `openapi` file
- a client that is older than its `openapi` file
- a secret that `secrets/<env>.enc.yaml` does not name

**Out of scope for this version:** public routes through Caddy, OTel tracing inside the templates, more template languages, and per-service CI jobs. Each one is a later issue.

### 3. Work plan

The epic is the GitHub issue labeled `epic` and `services`. One `/orchestrate` run builds it. Integration merges into `main`.
The main loop pre-seeds first: this ADR, the contract package `internal/services`, the `project.Config` field, the pins in `stackversions.yaml`, the template lockfiles, and the `wire_gen.go` imports.

## Consequences

- An app can add a component in any language and keep the Go dev loop for the rest.
- Each service adds one image to each deploy, and the image build time of that language.
- The `api` and `web` names stop being the only image names in build and deploy.
- The jobs worker runs in dev and in production for the first time.
