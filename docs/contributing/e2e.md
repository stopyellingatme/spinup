# Browser end-to-end suite

The suite proves the promise of milestone M4. A user of a generated app can register, log in, keep the session across server-side rendering, use a resource, and log out.

## What the suite does

The script `testdata/e2e/web/run.sh` does these steps:

1. Build the `spinup` binary.
2. Generate the app `demo` with `--modules ci,auth`. Use the local kit through `SPINUP_KIT_REPLACE`.
3. Write `.env` with your `DATABASE_URL` and two free ports.
4. Generate the resource `todo title:string done:bool`, and run the migrations.
5. Build the API binary and the web app.
6. Start the API and `node build`, and wait for `/healthz` on both.
7. Run the Playwright tests in Chromium.
8. Stop both servers, also when a step fails.

The tests are in `testdata/e2e/web/tests/smoke.spec.ts`. They use role and label locators.

## Run the suite

Use an empty database that you can delete. The script does not clean the database.

```
DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/e2e?sslmode=disable' \
  testdata/e2e/web/run.sh
```

Without `DATABASE_URL`, the script stops and tells you what to set.

| Option | Effect |
| --- | --- |
| `--list` | List the tests. Do not start the servers. |
| `E2E_ONLINE=1` | Install npm packages from the network. The default is the offline pnpm store. |
| `E2E_INSTALL_BROWSER=1` | Install the Chromium browser for Playwright. |
| `E2E_KEEP=1` | Keep the work directory and its logs. |

Other arguments go to `playwright test`.

If a run fails, read the logs in the work directory that the script prints.

## Service traces

Run `bash testdata/e2e/services-trace/run.sh` to check trace propagation in the Rust and Python templates.
The script requires Docker, Go, curl, and Python 3.
The image builds require network access for the pinned images and packages.
The script does not require `DATABASE_URL`.

The script generates an app and both services, then builds their images.
It starts the pinned `grafana/otel-lgtm` image and both services on a private Docker network.
It sends `POST /v1/sum` to each service with the same sampled `traceparent` header.
It reads the trace from Tempo and checks both service spans against the supplied parent ID.
The output shows the trace ID and each parent link.

The script removes its containers, network, and service images when it exits.
Set `E2E_KEEP=1` to keep the work directory and logs after a successful run.
Failed runs always keep the work directory and logs.
Set `SERVICES_TRACE_LOG_DIR` to choose a separate log directory.
The logs include the Tempo response and the expected trace and parent IDs.

The Node template has no trace code, so this script does not cover it.
See [Sidecar services](../services.md#monitoring) for the telemetry of the templates.

## Workflows

- `.github/workflows/nightly.yml` runs `make e2e` every night, with a PostgreSQL service.
- Its `services-trace` job runs the service trace script and uploads the logs when it fails.
- `.github/workflows/canary-sveltekit.yml` runs every week. It bumps `@sveltejs/kit`, `svelte`, `vite`, `bits-ui`, and the shadcn-svelte components to the latest versions. It runs `pnpm check` and `pnpm build`. If a step fails, it opens an issue.
