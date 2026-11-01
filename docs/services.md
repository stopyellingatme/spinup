---
title: Sidecar services
parent: Build your app
nav_order: 9
---

# Sidecar services

A sidecar service is a small program in its own container, next to your Go API.
Write it in Rust, Python, or Node when Go is not the right tool.
The API and the jobs worker call it over HTTP on the private `app` network.
Browsers cannot reach it, unless you give it a [public route](#let-browsers-call-a-service).

Add a service, then generate the Go client that calls it:

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g service ocr --lang rust
...
$ spinup g client
...
$ test -s api/internal/services/ocr/client.gen.go
$ test -s services/ocr/Dockerfile
$ test -s services/ocr/openapi.yaml
$ test -s services/ocr/Cargo.lock
$ grep '^name = "ocr"$' spinup.toml
name = "ocr"
$ grep '^health = "/healthz"$' spinup.toml
health = "/healthz"
```

The command writes `services/ocr/` and adds a `[[services]]` table to `spinup.toml`.
Every template starts with a health route, `GET /healthz`, and one sample route, `POST /v1/sum`.
Replace the sample route with your own work.

Run the new service and call the sample route:

<!-- doctest: skip -->
```console
$ spinup dev
$ curl -s -X POST http://127.0.0.1:8101/v1/sum -H 'Content-Type: application/json' -d '{"values": [1, 2.5]}'
{"sum":3.5}
```

## When to use a service

Write the code in Go first. Profile it before you move it.
If one function uses most of the time, try to fix that function in Go.

Use a service in these cases:

- The work is heavy on the CPU, and Go is not fast enough.
- The work needs a GPU.
- You need a library that exists only in another language.

Do not use a service for a normal handler or a small job.
A service adds a container to build, a network call, and one more thing to deploy.

## Add a service

Run `spinup g service <name> --lang rust|python|node` from the application root.
The name starts with a lowercase letter and has up to 20 lowercase letters or digits, such as `ocr`.

| Language | Example | Built on |
| --- | --- | --- |
| Rust | `spinup g service ocr --lang rust` | axum on tokio |
| Python | `spinup g service stats --lang python` | FastAPI on uvicorn, with uv |
| Node | `spinup g service pages --lang node` | Hono on Node 24, with pnpm |
| Python, PDF reports | `spinup g service reports --lang python --kind pdf` | WeasyPrint. See [PDF reports](pdf-reports.md). |

Each template has a Dockerfile, locked dependencies, a test, a README, and `openapi.yaml`.
The `openapi.yaml` file is the contract.
The Go client and the TypeScript client come from it.
To preview the files without writing them, add `--dry-run`:

```console
$ spinup g service stats --lang python --dry-run
create services/stats/.dockerignore (scaffold)
...
update spinup.toml (scaffold)
```

All files are `scaffold` files, so you own them after the first run.
Only `rust`, `python`, and `node` are built in. For another language, use a [plugin template](#plugin-templates).

The other flags of `spinup g service` set the ports and the client:

| Flag | Effect | Example |
| --- | --- | --- |
| `--port` | The container port. The default is 8080. | `--port 9000` |
| `--dev-port` | The host port in `spinup dev`. The default is the first unused port from 8101. | `--dev-port 8105` |
| `--no-openapi` | Write no `openapi.yaml` and no Go client. | `--no-openapi` |
| `--public`, `--auth` | Give the service a [public route](#let-browsers-call-a-service). | `--public /svc/ocr --auth session` |

See [Generate a service](commands/generate-service.md) for every flag.

## Call it from Go

Run `spinup g client` after you add a service or change its `openapi.yaml`.
`spinup dev` does this for you when the file changes.
The command writes a typed Go client to `api/internal/services/<name>/`.

| File | Class | Purpose |
| --- | --- | --- |
| `client.gen.go` | generated | The client and the models. Commit it. |
| `<name>.go` | scaffold | The `Config` struct and the `New` function. You own this file. |
| `oapi-codegen.yaml` | managed | The generator settings. |

`Config` has one field, `URL`. The variable `SERVICE_<NAME>_URL` sets it, for example `SERVICE_OCR_URL`.
The client has a 30-second timeout and passes the trace along, so one trace crosses both processes.

Create the client once, then pass it to the handlers that need it:

```go
// In api/cmd/api/main.go, for a service named "ocr".
var ocrCfg ocr.Config
if err := config.Load(&ocrCfg); err != nil {
	return err
}
ocrClient, err := ocr.New(ocrCfg)
if err != nil {
	return err
}

// In a handler. Always pass the request context.
resp, err := ocrClient.SumWithResponse(ctx, ocr.SumJSONRequestBody{Values: []float64{1, 2.5}})
if err != nil {
	return err
}
if resp.JSON200 == nil {
	return fmt.Errorf("ocr: unexpected status %d", resp.StatusCode())
}
total := resp.JSON200.Sum // 3.5
```

The method names come from the `operationId` values in `openapi.yaml`.
When the request context ends, the call to the service ends too.

## Run it in development

`spinup dev` builds each service and publishes it on `127.0.0.1:<dev_port>`.
The API and the worker get `SERVICE_<NAME>_URL` for each service.

<!-- doctest: skip -->
```console
$ spinup dev
```

To work on a service outside Docker, skip the containers and run your own copy on its `dev_port`:

<!-- doctest: skip -->
```console
$ spinup dev --no-services
$ cd services/ocr && cargo run
```

The API and the worker still get the service URLs with `--no-services`.
If a service has `db = true`, `spinup dev` does not set `DATABASE_URL` for it.
Put the address in the `env` table of the service declaration instead.
See [dev](commands/dev.md).

## Hand slow work to a job

A request handler must answer fast. For slow work, let a [background job](build/jobs.md) call the service.
The handler inserts the job and returns at once.

1. Add the client to `Deps` in `api/internal/jobs/deps.go`:

   ```go
   type Deps struct {
   	DB  *pgxpool.Pool
   	OCR *ocr.ClientWithResponses
   }
   ```

2. Create the client in `api/cmd/worker/main.go`, and set the field where `main.go` builds `jobsapp.Deps{...}`.
3. Run `spinup g job ocr_page`. In the new function, call the service with `d.OCR`.
   Return an error to retry the job.

To limit the calls that run at the same time, give the service its own queue.
In `newWorker` of `api/cmd/worker/main.go`, add `jobs.WithQueue("ocr", 2)`.
Insert the jobs with `jobs.Queue("ocr")`. At most two `ocr` jobs then run at once in each worker process.

<a id="public-routes"></a>

## Let browsers call a service

A service is private by default. To let a browser or another system call it, give it a public prefix.
Caddy then serves the service under that prefix and removes the prefix from the path.
A request to `/svc/ocr/healthz` reaches the service as `/healthz`.

```toml
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
public = "/svc/ocr"
auth = "session"
```

The generator flags set the same keys:

<!-- doctest: skip -->
```console
$ spinup g service ocr --lang rust --public /svc/ocr --auth session
```

| `auth` | What Caddy does |
| --- | --- |
| `none` (default) | Sends every request to the service. |
| `session` | First asks the API (`GET /api/session`) with the request cookies. A 2xx answer lets the request through. Any other answer goes back to the client, so no session gives 401. |

The `session` mode needs the `auth` module.
The service gets the cookies, but it gets no header with the user identity.
For `session`, the route also answers 403 to a request that is not GET, HEAD, or OPTIONS when the `Origin` header does not match the site.
This is the same CSRF protection that the API has.

The prefix must follow these rules:

- It starts with `/`, does not end with `/`, and is not `/`.
- Each segment uses lowercase letters, digits, dots, hyphens, or underscores.
- It is not `/api`, `/_app`, `/healthz`, `/readyz`, `/version`, or `/.well-known`, and it is not below them.
- It does not equal or nest with the prefix of another service.

Deploy writes the managed file `Caddyfile.d/spinup-services.caddy` only when a service is public.
If you have your own file with this name in `deploy/Caddyfile.d/`, the preflight fails. Rename your file.
A request to the bare prefix gets a 308 redirect to the prefix with a slash.

The rate limits of the [auth routes](build/auth-security.md) do not cover public routes.
Each `session` request costs one session lookup in the API, so add your own limits in the service code.

## Secrets

A service reads only the secrets that its `secrets` list names.
Deploy binds each named secret as a read-only file at `/run/secrets/<NAME>`.
Store the value with `spinup secrets set`. The command reads the value from standard input:

<!-- doctest: skip -->
```console
$ printf 'sk-live-123' | spinup secrets set --env production OCR_API_KEY
```

Then list the name in the declaration:

```toml
[[services]]
name = "ocr"
secrets = ["OCR_API_KEY"]
```

If a named secret is missing, `spinup deploy` stops in the preflight, before it changes the VM.
`spinup doctor` also reports a name that `secrets/<env>.enc.yaml` does not hold.
See [Secrets](deploy/secrets.md).

<a id="monitoring"></a>

## Limits, GPUs, and monitoring

A service has no CPU or memory limit unless you set one.
Set limits and the replica count in `deploy/environments/<env>.toml`:

```toml
[replicas]
ocr = 2

[resources.ocr]
cpus = "1.5"
memory = "1g"
```

A deploy runs the old and the new set of containers together for a short time (the blue and the green color).
So the VM needs memory for two copies of each service.
The `worker` key in `[replicas]` sets the replica count of the jobs worker. The default is 1.

To reserve a GPU, set `gpus = "all"` or a count such as `gpus = "1"` in the declaration.
The VM needs an NVIDIA driver and the NVIDIA container toolkit. spinup does not install them.

If the monitoring module is on, a deployed service gets the OpenTelemetry variables `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` (`<app>-<name>`), and `OTEL_RESOURCE_ATTRIBUTES`.
The Rust and Python templates send traces. A service span is a child of the span of the Go client call.
The Node template sends no traces. Add an OpenTelemetry SDK to use the variables.
With the `lite` monitoring profile, traces are dropped. Use the `full` profile to see them in Tempo.
See [Monitoring](monitoring.md).

## Deploy and roll back

`spinup deploy` builds, ships, and checks each service like the API.
The switch waits until every replica answers its `health` path.
If a check fails, the old release stays live.

<!-- doctest: skip -->
```console
$ spinup deploy --env production
$ spinup rollback --env production
```

`spinup rollback` restores the services of the previous release together with the API, and the public route of that release.
`spinup backup restore`, `spinup maintenance pg-major-upgrade`, and `spinup maintenance pg-upgrade-in-place` stop and start the worker and the services of the deployed release together with the API. `pg-minor-upgrade` only restarts PostgreSQL.
To build the images without a deploy, run `spinup build --image`. Each service builds from `services/<name>/`.
Each service runs as a non-root user with a read-only root file system, a `/tmp` tmpfs, no capabilities, and `no-new-privileges`.
Your Dockerfile must work with these rules.
See [Deploy to your own server](deploy.md).

The generated `ci.yml` has a `services` job that builds each image and runs its tests.
A new service needs no workflow edit. See [Generated CI](ci.md).

## Reference: the `[[services]]` keys

| Key | Required | Meaning | Example |
| --- | --- | --- | --- |
| `name` | yes | Lowercase letters and digits, up to 20, starting with a letter. Not a Go keyword and not a reserved name. | `"ocr"` |
| `port` | yes | The container port. | `8080` |
| `dev_port` | yes | The host port in `spinup dev`. Unique for each service. | `8101` |
| `health` | yes | An HTTP path that answers 200 when the service is ready. | `"/healthz"` |
| `openapi` | no | The OpenAPI file, relative to `services/<name>/`. Empty means no Go client. | `"openapi.yaml"` |
| `healthcheck` | no | The Compose health check, in exec form. | `["/service", "healthcheck"]` |
| `secrets` | no | The secrets the service can read. | `["OCR_API_KEY"]` |
| `env` | no | Literal variables. A key cannot start with `SPINUP_` or `OTEL_`. | `{ LANG = "en" }` |
| `db` | no | If `true`, the service joins the `db` network. | `true` |
| `gpus` | no | Empty (none), `all`, or a count. | `"all"` |
| `public` | no | A public URL prefix. | `"/svc/ocr"` |
| `auth` | no | `none` (default) or `session`. Needs `public`. | `"session"` |
| `memory` | no | The default memory limit in deploy. `[resources.<name>]` wins. | `"3g"` |
| `tmp_size` | no | The size of the `/tmp` tmpfs. | `"256m"` |

The name is also the package name of the Go client, so a Go keyword such as `type` or `go` is not valid.
The reserved names are `api`, `web`, `migrate`, `worker`, `caddy`, `postgres`, `alloy`, `mailpit`, `grafana`, `loki`, `tempo`, `mimir`, `pyroscope`, `backup`, `backups`, `edge`, and `monitoring`.
If a key is wrong, spinup fails with an error and a `Try:` line.
`spinup doctor` checks the declaration, the files, the generated client, and the secret names.

## Template details

The built-in templates ship a healthcheck command in the image.
It requests `/healthz` on `127.0.0.1` and ignores the `health` key.
If you change `health`, change the healthcheck code in the same way.

| Template | Healthcheck command | Code to change |
| --- | --- | --- |
| Rust | `/service healthcheck` | `src/main.rs` |
| Python | `/app/.venv/bin/python -m service.healthcheck` | `service/healthcheck.py` |
| Node | `/usr/local/bin/node /app/src/healthcheck.ts` | `src/healthcheck.ts` |

The Node template is TypeScript on Hono with no build step.
Node 24 removes the types when it runs `node src/main.ts`.
Run the type check with `pnpm run typecheck`, and the tests with `node --test`.
The Rust telemetry code is in `src/telemetry.rs`, and the Python code is in `service/telemetry.py`.
An existing service keeps its files when you upgrade, because `services/<name>/` is scaffold output.

## Plugin templates

A [plugin](plugins.md) can add templates for other languages.
Run `spinup g service <name> --lang <plugin>/<template>` to use one.
Plugin templates are data only. spinup runs no plugin code.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spinup doctor` warns `generated Go client is missing` | You added the service but did not generate the client. | Run `spinup g client`. |
| `--auth session needs the auth module` | The app has no `auth` module. | Add the module, or use `--auth none`. |
| The deploy stops in the preflight with a secret name | A name in `secrets` is not in `secrets/<env>.enc.yaml`. | Run `spinup secrets set` for that name. |
| The preflight fails about `spinup-services.caddy` | Your `deploy/Caddyfile.d/` has a file with that name. | Rename your file. |

## See also

- [Generate a service](commands/generate-service.md) and the [flag reference](site/commands/generate-service.md)
- [Background jobs](build/jobs.md)
- [PDF reports](pdf-reports.md)
- [Plugins](plugins.md)
- Design record: [ADR 0022](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0022-go-and-sidecar-services.md)
