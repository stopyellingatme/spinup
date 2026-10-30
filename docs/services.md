# Sidecar services

A sidecar service is a part of your app in its own container.
It runs next to the Go API and can use any language.
The API and the jobs worker call it over HTTP on the private `app` network.
In this version, the service is not public.

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

Run `spinup g service <name> --lang rust|python` from the application root.
The command writes `services/<name>/` and adds a `[[services]]` table to `spinup.toml`.
See [Generate a service](commands/generate-service.md) for all flags.

```console
$ spinup g service --help | head -1
Add a Rust or Python sidecar service
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
Each template has a Dockerfile, locked dependencies, a test, a README, and `openapi.yaml`.
The `openapi.yaml` file is the contract that the Go client uses.

The directory `services/<name>/` is the Docker build context.
`services/<name>/Dockerfile` builds the image.

## The `[[services]]` fields

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | The service name. It matches `^[a-z][a-z0-9]{0,19}$` and is not a reserved name. |
| `port` | yes | The container port. |
| `dev_port` | yes | The host port in `spinup dev`. It is unique for each service. |
| `health` | yes | An HTTP path that answers 200 when the service is ready. |
| `openapi` | no | The OpenAPI file, relative to `services/<name>/`. An empty value means no Go client. |
| `healthcheck` | no | The Compose health check command, in exec form. |
| `secrets` | no | The names of the secrets that the service can read. |
| `env` | no | Literal variables that are not secret. A key cannot start with `SPINUP_` or `OTEL_`. |
| `db` | no | If `true`, the service joins the `db` network. |
| `gpus` | no | An empty value (none), `all`, or a count such as `1`. |

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

## Monitoring

If the monitoring module is on, a deployed service gets these variables:

| Variable | Value |
| --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://alloy:4318` |
| `OTEL_SERVICE_NAME` | `<app>-<name>` |
| `OTEL_RESOURCE_ATTRIBUTES` | `service.instance.id=${SPINUP_COLOR}` |

The templates do not send traces. Add an OpenTelemetry SDK to your service to use these variables.
The Go client already traces the call, so the API side of the trace is present.

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

During a switch, both colors run. The VM needs memory for two sets of each service.

Set `gpus = "all"` or a count in the declaration to reserve a GPU.
The VM needs an NVIDIA driver and the NVIDIA container toolkit.
spinup does not install them.

## Run in development

`spinup dev` writes `.spinup/compose.services.yml`, builds each service, and publishes it on `127.0.0.1:<dev_port>`.
Use `--no-services` to skip the services and run one yourself on its dev port.
The API, the worker, and the migrate step get the `SERVICE_<NAME>_URL` variables also with `--no-services`.
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
`spinup backup restore` and `spinup maintenance pg upgrade` stop and start the worker and the services together with `api` and `web`.
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

A template directory can have a `service.toml` file with `healthcheck` and `health`.
spinup uses these values in the new `[[services]]` table.
Plugin templates are data only. spinup runs no plugin code.
See [Plugins](plugins.md).

## Limits of this version

- A service has no public route through Caddy. Only the API and the worker call it.
- The `rust` and `python` templates have no OpenTelemetry code inside.
- Only `rust` and `python` are built in. Use a plugin template for other languages.
- spinup adds no CI job for a service.
