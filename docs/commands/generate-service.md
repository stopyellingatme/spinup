---
title: generate service — add a sidecar service
parent: Command guides
---

# generate service — add a sidecar service

A sidecar service is a small program in its own container, next to your Go API.
`spinup g service <name> --lang rust|python|node` creates one from a template.
Use it when another language fits the job better, such as Python for data work.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ spinup g service ocr --lang rust --dry-run | head -n 3
create services/ocr/.dockerignore (scaffold)
create services/ocr/Cargo.lock (scaffold)
create services/ocr/Cargo.toml (scaffold)
$ spinup g service ocr --lang rust
Created 8, updated 1 files
Next: spinup g client
Next: spinup dev
$ tail -n 7 spinup.toml
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
openapi = "openapi.yaml"
healthcheck = ["/service", "healthcheck"]
```

The command creates `services/ocr/` and adds a `[[services]]` table to `spinup.toml`.
It keeps the rest of your `spinup.toml` as it was.
Then run `spinup g client` to get a Go client for the API of the service, and `spinup dev` to start everything.
[Sidecar services](../services.md) explains how the API calls the service.

## Choose a language

| Flag value | Template | Example |
| --- | --- | --- |
| `--lang rust` | axum on tokio | `spinup g service ocr --lang rust` |
| `--lang python` | FastAPI with uv | `spinup g service stats --lang python` |
| `--lang node` | Hono on Node 24, in TypeScript with no build step | `spinup g service pages --lang node` |
| `--lang <plugin>/<template>` | A template from a plugin | `spinup g service ocr --lang acme/ocr` |

All three built-in templates have `GET /healthz` and `POST /v1/sum`.
The sum route takes `{"values": [1, 2.5]}` and returns `{"sum": 3.5}`.
Replace it with your own work.
Each template also has a Dockerfile, locked dependencies, tests, and an OpenAPI file.
The generated README shows how to run the tests of the service.

## Name the service

The name starts with a lower-case letter and has up to 20 lower-case letters and digits, such as `ocr` or `pdf2`.
Some names are reserved for spinup itself, such as `api`, `web`, `worker`, `postgres`, and `caddy`.
A Go keyword such as `go` or `type` also fails, because the Go client uses the name as its package name.
A name that is taken or reserved fails, also with `--force`.

```console
$ spinup g service Bad --lang rust
Error: generator "service" failed: service name "Bad" must match ^[a-z][a-z0-9]{0,19}$. Try: use lower-case letters and digits, such as ocr
[exit 1]
$ spinup g service ocr --lang rust
Error: generator "service" failed: cannot generate service "ocr": services/ocr already exists
  Try: choose another service name
[exit 1]
```

## Flags

| Flag | What it does | Default | Example |
| --- | --- | --- | --- |
| `--lang` | The language or plugin template. Required. | none | `--lang python` |
| `--port` | The port inside the container. | `8080` | `--port 9000` |
| `--dev-port` | The port on your machine in `spinup dev`. Each service needs its own. | The first free port from `8101` | `--dev-port 8200` |
| `--public` | Give the service a public URL prefix. | none | `--public /svc/files` |
| `--auth` | The check on the public route: `none` or `session`. | `none` | `--auth session` |
| `--kind` | The template variant: `service` or `pdf`. | `service` | `--kind pdf` |
| `--no-openapi` | Leave out the OpenAPI file and the Go client. | off | `--no-openapi` |

A service without `--no-openapi` declares `openapi = "openapi.yaml"` in `spinup.toml`.
`spinup g client` uses that file to build the Go client.

```console
$ spinup g service classify --lang python --port 9000 --no-openapi | head -n 1
Created 11, updated 1 files
$ tail -n 6 spinup.toml
[[services]]
name = "classify"
port = 9000
dev_port = 8102
health = "/healthz"
healthcheck = ["/app/.venv/bin/python", "-m", "service.healthcheck"]
```

Do not rename the Python package `service` inside `services/classify/`.
The Dockerfile, the health check, and the lockfile refer to it by that name.

## Open a public route

By default, only your API and your worker can call a service.
Use `--public <prefix>` to let browsers call it through the proxy, for example `--public /svc/files`.
The prefix must not be, or sit under, `/api`, `/_app`, `/healthz`, `/readyz`, `/version`, or `/.well-known`.

`--auth none` leaves the route open to anyone.
`--auth session` lets only signed-in users in, and it needs the `auth` module.
`--auth` without `--public` is an error, because a private service has no route to protect.

```console
$ spinup g service files --lang node --public /svc/files | tail -n 2
Next: spinup g client
Next: spinup dev
$ tail -n 8 spinup.toml
[[services]]
name = "files"
port = 8080
dev_port = 8103
health = "/healthz"
public = "/svc/files"
openapi = "openapi.yaml"
healthcheck = ["/usr/local/bin/node", "/app/src/healthcheck.ts"]
```

The rules for public routes are in [Sidecar services](../services.md#let-browsers-call-a-service).

## Make a PDF report service

`--kind pdf` builds on the Python template and adds a WeasyPrint renderer with `POST /v1/render`.
It also sets `memory = "3g"` and `tmp_size = "256m"` in the `[[services]]` table, because rendering needs room.
It works only with `--lang python`.

```console
$ spinup g service reports --lang python --kind pdf | head -n 1
Created 20, updated 1 files
$ grep tmp_size spinup.toml
tmp_size = "256m"
$ spinup g service broken --lang rust --kind pdf
Error: generator "service" failed: cannot generate service: --kind pdf needs --lang python, got "rust"
  Try: spinup g service broken --lang python --kind pdf
[exit 1]
```

See [PDF reports](../pdf-reports.md) for the renderer.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `--lang is required` | Add `--lang rust`, `--lang python`, or `--lang node`. |
| `auth is set, but the service has no public route` | Add `--public`, or drop `--auth`. |
| `--auth session needs the auth module` | Run `spinup module add auth`, or use `--auth none`. |
| `--kind pdf needs --lang python` | Use the Python template for PDF reports. |
| Two services use the same host port | Pass `--dev-port` with a free port. |

## See also

- [Sidecar services](../services.md): call a service from your API, deploy it, and secure it
- [PDF reports](../pdf-reports.md)
- [generate client](generate-client.md): build the Go client
- [Plugins](../plugins.md): service templates from plugins
- Flag reference: [`generate service`](../site/commands/generate-service.md)
