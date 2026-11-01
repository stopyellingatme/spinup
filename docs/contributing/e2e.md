---
title: Browser end-to-end suite
parent: Contributing
nav_order: 8
---

# Browser end-to-end suite

The suite checks that a user of a generated app can register, log in, keep the session across server-side rendering, use a resource, and log out.
It runs in a real browser against a generated app.
It also checks the shell of the web app, the API reference page, and the sign-in rate limit.

```sh
DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/e2e?sslmode=disable' \
  testdata/e2e/web/run.sh
```

This page also covers the [service trace check](#service-traces) and the nightly workflows.

## What the suite does

The script `testdata/e2e/web/run.sh` does these steps:

1. Build the `spinup` binary.
2. Generate the app `demo` with `--modules ci,auth`. Use the local kit through `SPINUP_KIT_REPLACE`.
3. Write `.env` with your `DATABASE_URL` and two free ports.
4. Empty the `public` schema of the database. Generate the resource `todo title:string done:bool due:date?`, and run the migrations.
5. Build the API binary and the web app.
6. Start the API and `node build`, and wait for `/healthz` on both.
7. Run the Playwright tests in Chromium, then run the smoke test that the generated app ships.
8. Stop both servers, also when a step fails.

The tests are in `testdata/e2e/web/tests/` (`auth.spec.ts`, `todos.spec.ts`, `shell.spec.ts`, `apidocs.spec.ts`, and `ratelimit.spec.ts`). They use role and label locators.

## Run the suite

{: .warning }
Use a database that you can lose. When `psql` is on `PATH`, the script runs `DROP SCHEMA public CASCADE` on `DATABASE_URL` before the migrations.

Set `DATABASE_URL` as in the command at the top of this page.
Without `DATABASE_URL`, the script stops and tells you what to set.
The script also needs Go, Node 24, pnpm, and `python3`.
`make e2e` runs the same script and needs `DATABASE_URL` too.

| Option | Effect |
| --- | --- |
| `--list` | List the tests. Do not start the servers. |
| `E2E_ONLINE=1` | Install npm packages from the network. The default is the offline pnpm store. |
| `E2E_INSTALL_BROWSER=1` | Install the Chromium browser for Playwright. |
| `E2E_KEEP=1` | Keep the work directory and its logs. |

Other arguments go to `playwright test`. For example, list the tests, or keep the work directory of one run:

```sh
testdata/e2e/web/run.sh --list
E2E_KEEP=1 DATABASE_URL=... testdata/e2e/web/run.sh
```

If a run fails, read the logs in the work directory that the script prints.

## Service traces

Run the script to check trace propagation in the Rust and Python templates.

```sh
bash testdata/e2e/services-trace/run.sh
```

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
See [Sidecar services](../services.md#limits-gpus-and-monitoring) for the telemetry of the templates.

## Workflows

- `.github/workflows/nightly.yml` runs `make e2e` every night, with a PostgreSQL service.
- Its `services-trace` job runs the service trace script and uploads the logs when it fails.
- `.github/workflows/canary-sveltekit.yml` runs every week. It bumps `@sveltejs/kit`, `svelte`, `vite`, `bits-ui`, and the shadcn-svelte components to the latest versions. It runs `pnpm check` and `pnpm build`. If a step fails, it opens an issue.

## See also

- [Docker-in-Docker deploy suite](dind.md)
- [Monitoring stack boot test](monitoring-boot.md)
- [Sidecar services](../services.md)
- [Contributing](index.md)
