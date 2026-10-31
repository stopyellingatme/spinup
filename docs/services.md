# Sidecar services

A sidecar service is a part of your app in its own container.
It runs next to the Go API and can use any language.
The API and the jobs worker call it over HTTP on the private `app` network.
By default, the service is not public.
You can give it a public route through Caddy. See [Public routes](#public-routes).

## When to use a service

Write the code in Go first. Profile it before you move it.
If the profile shows that one function uses most of the time, try to fix that function in Go.

Use a service in these cases:

- The work is heavy on the CPU, and a Go version is not fast enough.
- The work needs a GPU.
- You need a library that exists only in another language.

Do not use a service for a normal handler or a small job.
A service adds a container, an image to build, and a network call.

## Add a service

Run `spinup g service <name> --lang rust|python|node` from the application root.
The command writes `services/<name>/` and adds a `[[services]]` table to `spinup.toml`.
See [Generate a service](commands/generate-service.md) for all flags.

```console
$ spinup g service --help | head -1
Add a sidecar service
$ spinup g service --help | grep -c -- '--lang string'
1
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g service ocr --lang rust
...
$ test -s services/ocr/Dockerfile
$ test -s services/ocr/openapi.yaml
$ test -s services/ocr/Cargo.lock
$ grep '^name = "ocr"$' spinup.toml
name = "ocr"
$ grep '^health = "/healthz"$' spinup.toml
health = "/healthz"
```

The `rust` template uses axum on tokio.
The `python` template uses FastAPI on uvicorn and uv.
The `node` template uses Hono on Node 24 and pnpm.
Each template has a Dockerfile, locked dependencies, a test, a README, and `openapi.yaml`.
The `openapi.yaml` file is the contract that the Go client uses.

## The Node template

The `node` template is a TypeScript service on Hono (`hono` and `@hono/node-server`).
It has no build step. Node 24 removes the types when it runs `node src/main.ts`.
`tsconfig.json` sets `erasableSyntaxOnly` and `allowImportingTsExtensions`.
`typescript` runs only as `tsc --noEmit`, through `pnpm run typecheck`.

The template follows the same contract as the other templates:
`GET /healthz`, `POST /v1/sum`, the shared `openapi.yaml`, and a pinned `pnpm-lock.yaml`.
The image is the pinned `node` image.
It runs as the user `node` (1000:1000) with a read-only root file system.
It installs the dependencies with `pnpm install --frozen-lockfile --prod --ignore-scripts` and has no dev tools.
The healthcheck command is `/usr/local/bin/node /app/src/healthcheck.ts`.
Tests use `node --test` and `app.request()` from Hono, so they need no socket.

The directory `services/<name>/` is the Docker build context.
`services/<name>/Dockerfile` builds the image.

## The `[[services]]` fields

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | The service name. It matches `^[a-z][a-z0-9]{0,19}$`. It is not a reserved name and not a Go keyword. |
| `port` | yes | The container port. |
| `dev_port` | yes | The host port in `spinup dev`. It is unique for each service. |
| `health` | yes | An HTTP path that answers 200 when the service is ready. |
| `openapi` | no | The OpenAPI file, relative to `services/<name>/`. An empty value means no Go client. |
| `healthcheck` | no | The Compose health check command, in exec form. |
| `secrets` | no | The names of the secrets that the service can read. |
| `env` | no | Literal variables that are not secret. A key cannot start with `SPINUP_` or `OTEL_`. |
| `db` | no | If `true`, the service joins the `db` network. |
| `gpus` | no | An empty value (none), `all`, or a count such as `1`. |
| `public` | no | A public URL prefix such as `/svc/ocr`. See [Public routes](#public-routes). |
| `auth` | no | The check on a public route: `none` (default) or `session`. It needs `public`. |

The built-in templates have a healthcheck command in the image.
The Rust command is `/service healthcheck`.
The Python command is `/app/.venv/bin/python -m service.healthcheck`.
The Node command is `/usr/local/bin/node /app/src/healthcheck.ts`.
Both commands request `/healthz` on `127.0.0.1` and ignore the `health` field.
If you change `health` in `spinup.toml`, change the healthcheck code of the template in the same way.
The Rust code is in `src/main.rs`, and the Python code is in `service/healthcheck.py`.
The Node code is in `src/healthcheck.ts`.

A Go keyword such as `type` or `go` is not a valid name, because the name is the package name of the Go client.
The reserved names are `api`, `web`, `migrate`, `worker`, `caddy`, `postgres`, `alloy`, `mailpit`, `grafana`, `loki`, `tempo`, `mimir`, `pyroscope`, `backup`, `backups`, `edge`, and `monitoring`.
If a field is wrong, `spinup` fails with an error and a `Try:` line.
Run `spinup doctor` to check the declaration, the files, the generated client, and the secret names.
See [doctor](commands/doctor.md).

## The Go client

For each service with an `openapi` value, spinup generates a typed Go client in `api/internal/services/<name>/`:

| File | Class | Purpose |
| --- | --- | --- |
| `oapi-codegen.yaml` | managed | The oapi-codegen configuration. |
| `client.gen.go` | generated | The client and the models. Commit it. |
| `<name>.go` | scaffold | The `Config` struct and the `New` function. You own this file. |

`Config` has one field, `URL`. The environment variable `SERVICE_<NAME>_URL` sets it.
`New(cfg Config)` returns a `*ClientWithResponses`.
The client uses `otelhttp` and a 30-second timeout, so a trace continues across the call.

Run `spinup g client` after you change `openapi.yaml`.
`spinup dev` does this for you.
See [Generate the TypeScript client](commands/generate-client.md).

## Call the service from a handler

Load `Config` with the other settings of the API. Create the client once, and pass it to the handler.

```go
// In cmd/api, with the OCR service named "ocr".
var ocrCfg ocr.Config
if err := config.Load(&ocrCfg); err != nil {
	return err
}
ocrClient, err := ocr.New(ocrCfg)
if err != nil {
	return err
}

// In a handler.
resp, err := ocrClient.SumWithResponse(ctx, ocr.SumJSONRequestBody{Values: []float64{1, 2.5}})
if err != nil {
	return err
}
if resp.JSON200 == nil {
	return fmt.Errorf("ocr: unexpected status %d", resp.StatusCode())
}
total := resp.JSON200.Sum
```

The generated method names come from the `operationId` values in `openapi.yaml`.
Always pass the request `ctx`. When the caller stops, the call stops.

## Hand work from a job

A request handler must answer fast. For slow work, hand the work to a job.
The worker calls the service, and the handler returns at once.
This pattern has three parts.

1. Add the client to `jobs.Deps` in `api/internal/jobs/deps.go`:

   ```go
   type Deps struct {
   	DB  *pgxpool.Pool
   	OCR *ocr.ClientWithResponses
   }
   ```

2. Create the client in `api/cmd/worker/main.go`, and set the field in `jobsapp.Deps{...}`.
   The worker has the `SERVICE_<NAME>_URL` variables in dev and in deploy.

3. Run `spinup g job ocr_page`. In the worker function, call the service with `d.OCR`.
   Return an error to retry the job. River retries it with a delay.

To limit the calls that run at the same time, use a separate queue.
Add the queue in `newWorker` of `cmd/worker/main.go`:

```go
jobs.WithQueue("ocr", 2),
```

Insert the job with `jobs.Queue("ocr")`. At most two `ocr` jobs run at once in each worker process.
The service has no queue of its own, so this limit protects it.
See [Generate a background job](commands/generate-job.md).

## Secrets

A service reads only the secrets that its `secrets` list names.
Deploy binds each named secret as a read-only file at `/run/secrets/<NAME>`.
The service never sees the other secrets of the app.

Set the value with `spinup secrets set --env <env> <NAME>`.
The doctor reports a name that `secrets/<env>.enc.yaml` does not hold.
If a named secret is missing, `spinup deploy` stops in the preflight, before it changes the VM.
Compose does not create a missing secret file. The start of the color fails instead.

## Monitoring

If the monitoring module is on, a deployed service gets these variables:

| Variable | Value |
| --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://alloy:4318` |
| `OTEL_SERVICE_NAME` | `<app>-<name>` |
| `OTEL_RESOURCE_ATTRIBUTES` | `service.instance.id=${SPINUP_COLOR}` |

The `rust` and `python` templates send traces to `OTEL_EXPORTER_OTLP_ENDPOINT` with OTLP over HTTP.
If the variable is not set, as in tests or offline, the telemetry code does nothing.
Each template reads the W3C `traceparent` header.
A server span of the service is therefore a child of the span of the Go client in the API.
`/healthz` makes no span.

- The Rust code is in `src/telemetry.rs`. It uses a small axum middleware and the `opentelemetry` crates.
- The Python code is in `service/telemetry.py`. It uses `FastAPIInstrumentor` and excludes `/healthz`.
- The `node` template sends no traces. Add an OpenTelemetry SDK to your service to use these variables.

Existing services keep their code, because `services/<name>/` is scaffold output.
Only a new service gets the telemetry code.
In the profile `lite`, Alloy drops traces. Use the profile `full` to see them in Tempo.
The Go client already traces the call, so the API side of the trace is present.

## Public routes

A service is private by default. Only the API and the worker call it.
To let browsers or other systems call a service, give it a public prefix.
Caddy then serves the service below that prefix.

```toml
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
public = "/svc/ocr"
auth = "session"
```

You can also use the flags of the generator:

<!-- doctest: skip -->
```console
$ spinup g service ocr --lang rust --public /svc/ocr --auth session
```

Follow these rules for the prefix:

- It starts with `/`, does not end with `/`, and is not `/`.
- Each segment matches `[a-z0-9][a-z0-9._-]*`.
- It cannot equal or sit below `/api`, `/_app`, `/healthz`, `/readyz`, `/version`, or `/.well-known`.
- It cannot equal or nest with the prefix of another service.

`spinup deploy` writes the managed file `Caddyfile.d/spinup-services.caddy` into the release directory.
It writes this file only when at least one service is public.
An app with no public service gets no such file.
If you have your own file with this name in `deploy/Caddyfile.d/`, the preflight fails.
Rename your file.

Each public service gets a route that strips the prefix.
A request to `/svc/ocr/healthz` reaches the service as `/healthz`.
A request to the bare prefix `/svc/ocr` gets a 308 redirect to `/svc/ocr/`.
The route uses `upstream.caddy`, so it follows the color switch and the rollback.
`spinup rollback` restores the route of the old release together with the service.

The `auth` field selects the check on the route:

| Value | Behavior |
| --- | --- |
| `none` | Caddy sends every request to the service. This is the default. |
| `session` | Caddy first asks the API of the live color with `GET /api/session` and the cookies of the request. |

With `auth = "session"`, a 2xx answer sends the request on to the service.
Any other answer goes to the client unchanged, so a request with no session gets 401.
Before the first switch, the route fails closed.
The service gets the cookies of the request, but it gets no user identity header.
The `session` mode needs the `auth` module.
If the app does not have the module, `spinup` fails with a `Try:` line.

The API middleware holds the CSRF check, and a public route skips it.
So for `auth = "session"`, the route answers 403 to a request that is not GET, HEAD, or OPTIONS.
This happens when the `Origin` header is not `{scheme}://{host}` of the request.

The rate limits of the auth routes (see [Accounts and sessions](auth.md)) do not cover public routes.
Each `session` request costs one session lookup in the API.
The service owns its own limits, so add them in the service code.

## Limits and GPUs

A service with no `[resources.<name>]` entry in `deploy/environments/<env>.toml` has no CPU limit and no memory limit.
Set limits, and set the replica count, in that file:

```toml
[replicas]
ocr = 2

[resources.ocr]
cpus = "1.5"
memory = "1g"
```

With the jobs module on, `worker` in `[replicas]` sets the replica count of the jobs worker.
The default is 1. A value below 1 gives 1.

During a switch, both colors run. The VM needs memory for two sets of each service.

Set `gpus = "all"` or a count in the declaration to reserve a GPU.
The VM needs an NVIDIA driver and the NVIDIA container toolkit.
spinup does not install them.

## Run in development

`spinup dev` writes `.spinup/compose.services.yml`, builds each service, and publishes it on `127.0.0.1:<dev_port>`.
Use `--no-services` to skip the services and run one yourself on its dev port.
The API, the worker, and the migrate step get the `SERVICE_<NAME>_URL` variables also with `--no-services`.
If a service has `db = true`, `spinup dev` does not set `DATABASE_URL` for it.
The service must read the database address from the `env` table of its declaration, or from its own dev environment.
`spinup dev` sets only `PORT`, the `env` pairs, the secrets, and the OpenTelemetry variables.
When `openapi.yaml` changes, `spinup dev` generates the Go client again.
See [dev](commands/dev.md).

## Build, deploy, and roll back

`spinup build --image` builds each service with the context `services/<name>`.
See [build](commands/build.md).

`spinup deploy` builds, ships, and checks each service like the API.
It writes `compose.services.yml` into the release directory.
The switch waits until each replica of each service answers its `health` path through Caddy.
If a check fails, the old color stays live.

`release.json` records the name, the port, the health path, and the image of each service.
`spinup rollback` reads this record, so it restores the services of that release.
`spinup backup restore` and `spinup maintenance pg upgrade` stop and start the whole color projects.
The worker and the services of the deployed release stop and start together with `api` and `web`, whatever the local `spinup.toml` declares.
The migrate step gets the `SERVICE_<NAME>_URL` variables in deploy and in restore.
See [Deploy](deploy.md).

Each service gets the hardening of the API.
It runs as a non-root user with a read-only root file system, a `/tmp` tmpfs, no capabilities, and `no-new-privileges`.
Your Dockerfile must work with these rules.

## Plugin service templates

A plugin can supply service templates for other languages.
The `plugin.toml` file has a `[[service_templates]]` table with `name`, `description`, and `dir`.
Run `spinup g service <name> --lang <plugin>/<template>` to use one.
spinup renders the directory with `text/template`.
The data are `Name`, `App`, and `Port`.
A `.tmpl` file is rendered and loses the suffix. Other files are copied.
A file with an execute bit, such as `entrypoint.sh`, gets mode 0755. Other files get mode 0644.

A template directory can have a `service.toml` file with `healthcheck` and `health`.
spinup uses these values in the new `[[services]]` table.
Plugin templates are data only. spinup runs no plugin code.
See [Plugins](plugins.md).

## Limits of this version

- A public route passes no user identity header to the service.
- The `node` template has no OpenTelemetry code inside.
- Only `rust`, `python`, and `node` are built in. Use a plugin template for other languages.

## CI

The generated `ci.yml` has a `services` job when the app declares services.
It builds each image and runs the tests of each service in Docker.
You need no edit in the workflow when you add a service.
See [Generated CI](ci.md).
