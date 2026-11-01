---
title: dev — run the local stack
parent: Command guides
---

# dev — run the local stack

`spinup dev` starts everything your app needs on your machine: PostgreSQL, Mailpit, the API, the jobs worker, and the web server.
Run it inside the app, and edit code while it runs. The API and the web app reload on each change.

<!-- doctest: skip -->
```console
$ cd todo
$ spinup dev
...
App      http://localhost:5173
API      http://localhost:8080
Mailpit  http://localhost:8025
```

| URL | What it is |
| --- | --- |
| App | The web app, with hot reload. |
| API | The Go API. Open `/healthz` to check it. |
| Mailpit | A fake mail inbox. Every mail your app sends in development lands here. |
| Grafana | Logs, metrics, and traces. It appears only with `--lgtm`. |

Press Ctrl-C to stop. Docker services keep running unless you add `--down`.

## Before the first start

You need these tools. `spinup doctor --quick` checks them, and `spinup dev` runs the same check first.

- Go 1.26 or later
- Node 24 or later, with pnpm through corepack (or bun, if you chose it)
- Docker
- On Windows, WSL2. Run the command inside WSL2.

`spinup new` already made your `.env`. If you cloned the app, copy `.env.example` to `.env` first.

On the first start with a web app in `node` mode, the command also installs the web packages.
It skips the install with `--no-web`, in `embed` mode, or when `web/node_modules` exists.

## What starts

1. Docker Compose starts PostgreSQL and Mailpit. It also starts your [sidecar services](../services.md), if you have any.
2. The command applies the pending database migrations.
3. The API starts under a file watcher, so it rebuilds after a change to a Go file.
4. The jobs worker starts, if the `jobs` module is on.
5. The web server starts, if the app has a web app.

Each process writes lines with its own prefix, such as `api` or `worker`. Add `--no-color` (or set `NO_COLOR`) to turn the prefix colors off.
If a process crashes, the command restarts it. The wait grows up to five seconds.

If a port is busy, the command picks a free port and prints it in the URL table.
It saves the ports in `.spinup/dev.json`. If your own Mailpit or Grafana container still runs from an earlier start, the command keeps its port.
With `--no-infra`, the table has no Mailpit line.

## Stop or restart

Press Ctrl-C to stop all processes. Each one gets a stop signal, and a kill after five seconds.
Closing the terminal stops them too.

To stop the Docker services as well, start with `--down`:

<!-- doctest: skip -->
```console
$ spinup dev --down
```

## Choose what runs

Skip the web server when you work on the API only:

<!-- doctest: skip -->
```console
$ spinup dev --no-web
```

Use your own database and mail server. This skips Docker and the sidecar services:

<!-- doctest: skip -->
```console
$ spinup dev --no-infra
```

Skip the sidecar services, or the jobs worker, and run them yourself:

<!-- doctest: skip -->
```console
$ spinup dev --no-services --no-worker
```

Add Grafana for logs, metrics, and traces. The URL table then shows a `Grafana` line:

<!-- doctest: skip -->
```console
$ spinup dev --lgtm
```

Move to other ports, or change the Go watcher:

<!-- doctest: skip -->
```console
$ spinup dev --port 5174 --api-port 8081 --watch wgo
```

| Flag | What it does | Example |
| --- | --- | --- |
| `--no-web` | Skip the web server and its tool checks. | `spinup dev --no-web` |
| `--no-infra` | Skip Docker, Mailpit, and the sidecar services. | `spinup dev --no-infra` |
| `--no-services` | Skip the sidecar services of `spinup.toml`. | `spinup dev --no-services` |
| `--no-worker` | Skip the jobs worker. | `spinup dev --no-worker` |
| `--lgtm` | Start the Grafana LGTM image. | `spinup dev --lgtm` |
| `--port <n>` | Prefer this web port. Default: `5173`. | `spinup dev --port 5174` |
| `--api-port <n>` | Prefer this API port. Default: `8080`. | `spinup dev --api-port 8081` |
| `--watch air\|wgo` | Pick the Go watcher. Default: `air`. `wgo` needs `api/tools.mod` to list it. | `spinup dev --watch wgo` |
| `--down` | Stop the Docker services when the command ends. | `spinup dev --down` |

An unknown watcher stops the command at once:

```console
$ spinup dev --watch invalid
Error: unknown dev watcher "invalid"
  Try: use --watch air or --watch wgo
[exit 1]
```

## Sidecar services in dev

If `spinup.toml` has `[[services]]`, the command builds each `services/<name>/` directory and starts it.
Each service listens on `127.0.0.1:<dev_port>`.

The API and the worker get a variable for each service, such as `SERVICE_PDF_URL=http://127.0.0.1:<dev_port>`.
They get it also with `--no-services` or `--no-infra`, so you can start a service yourself on its dev port.

When you change the `openapi` file of a service, the command writes the Go client in `api/internal/services/<name>/` again.
[Sidecar services](../services.md) explains how to add one.

## Jobs worker in dev

When the `jobs` module is on, the command runs `go run ./cmd/worker` in `api/`.
The worker gets the same environment as the API, and its lines start with `worker`.
After you change a Go or SQL file in `api/`, the command restarts the worker.
Use `--no-worker` to run the worker yourself. See [Background jobs](../build/jobs.md).

## How file watching works

The watcher rebuilds the API after a Go or migration change.
It also checks your SQL and OpenAPI files twice a second.
After a short pause, a query change runs `sqlc`, and an OpenAPI change regenerates the API and the web client.
If a generation step fails, the command shows the error and keeps running.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| A check fails before anything starts. | A tool is missing or too old. | Run `spinup doctor --quick` and follow the `Try:` line. |
| Docker errors at the start. | The Docker daemon is not running. | Start Docker, or use `--no-infra` with your own database. |
| The URL table shows other ports. | Another program uses 5173 or 8080. | Stop that program, or choose the ports with `--port` and `--api-port`. |
| The web app cannot reach the API. | The API crashed after a Go error. | Read the `api` lines, fix the error, and save the file. |

## See also

- [Your first app](../get-started/first-app.md) uses `dev` in a full walk-through.
- [`doctor`](doctor.md) runs the same tool checks.
- [Sidecar services](../services.md) and [Background jobs](../build/jobs.md) start with `dev`.
- Flag reference: [`dev`](../site/commands/dev.md)
