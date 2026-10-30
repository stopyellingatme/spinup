# dev

Run `spinup dev` inside a generated application. The command checks the local tools and the `.env` file first.
It starts PostgreSQL, Mailpit, and the sidecar services, then applies migrations.
Next, it starts the API watcher, the jobs worker, and the Vite server.

```console
$ spinup dev --help
Run the local development stack
...
$ spinup dev --watch invalid
Error: unknown dev watcher "invalid"
  Try: use --watch air or --watch wgo
[exit 1]
```

Copy `.env.example` to `.env` before the first start. Install the dependencies before you start the application.
Use Go 1.26 or later, Node 24 or later, pnpm through corepack, and Docker.
On Windows, run the command in WSL2.

The command selects free ports when the preferred ports are busy. It records the ports in `.spinup/dev.json`.
When the app's own Mailpit or Grafana container still runs from an earlier start, the command keeps its port.
The URL table shows the application, API, and Mailpit addresses. With `--lgtm`, the table also shows Grafana.
With `--no-infra`, the command does not record or show a Mailpit address.

| Flag | Effect |
| --- | --- |
| `--no-web` | Skip Vite and its tool checks. |
| `--no-infra` | Use existing infrastructure and skip the Docker check. This flag also skips the sidecar services. |
| `--no-services` | Skip the sidecar services that `spinup.toml` declares. |
| `--no-worker` | Skip the jobs worker. |
| `--lgtm` | Start the pinned Grafana LGTM image with a generated Compose override. |
| `--port 5173` | Select the preferred web port. |
| `--api-port 8080` | Select the preferred API port. |
| `--watch air` | Use Air with the managed `api/.air.toml` configuration. |
| `--watch wgo` | Use wgo when `api/tools.mod` declares that tool. |
| `--down` | Stop the Compose services when the command exits. |
| `--no-color` | Disable color in the process prefixes. |

Air rebuilds the API after Go or migration changes. The command polls SQL and OpenAPI inputs every 500 milliseconds.
After a 300 millisecond debounce, query changes run sqlc. OpenAPI changes run the registered OpenAPI and client steps.
The watcher ignores `bundle.gen.yaml`. If a generation step fails, the command reports the failure and continues.

## Sidecar services

When `spinup.toml` declares `[[services]]`, the command writes `.spinup/compose.services.yml`.
That file builds each `services/<name>/` directory and publishes the service on `127.0.0.1:<dev_port>`.
The command adds the file to `docker compose up` with a second `-f`, and adds `--build`.
Docker uses its build cache, so a service with no changes builds fast.

The API and the worker get `SERVICE_<NAME>_URL=http://127.0.0.1:<dev_port>` for each declared service.
They get these variables also with `--no-services` or `--no-infra`. Then you can start a service yourself on its dev port.

The command also polls the `openapi` file of each service.
After a change, it runs the `service-clients` step, which writes the Go client again in `api/internal/services/<name>/`.

## Jobs worker

When the `jobs` module is on, the command runs `go run ./cmd/worker` in `api/`.
The worker gets the same environment as the API. Its output lines have the `worker` prefix.
After a change to a Go or SQL file in `api/`, Air rebuilds the API, and the command starts the worker again.
The command ignores the same directories as the managed `api/.air.toml` and ignores test files.
Use `--no-worker` to run the worker yourself.

An app with no services and no `jobs` module starts the same processes as before.

## Processes

Each process has a prefix on its output lines. Failed processes restart with an increasing delay, up to five seconds.
Press Ctrl-C to stop all child groups. Children receive SIGTERM, then SIGKILL after five seconds if necessary.
Closing the terminal also stops all child groups.
Docker services continue to run unless you specify `--down`. The `NO_COLOR` environment variable also disables prefix colors.
