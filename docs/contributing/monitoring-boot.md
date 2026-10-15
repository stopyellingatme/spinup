# Monitoring stack boot test

The boot test keeps the promise of the monitoring pack: the `lite` profile runs on a VM with 2 GiB of RAM.
It boots each profile next to a stand-in app.
It fails when a service is unhealthy, when the data does not arrive, or when the stack uses more memory than its budget.
The contracts are in ADR 0010 §2 and in critic resolution 13 of the plan.

## What the test does

The script `testdata/e2e/monitoring/run.sh` does these steps:

1. Build the `spinup` binary.
2. Generate the API-only app `spinup-boot-<profile>` with `--modules ci,deploy,monitoring` and `--monitoring-profile <profile>`.
3. Write the secrets `GRAFANA_ADMIN_PASSWORD` and `POSTGRES_PASSWORD` to a work directory, with mode 0600.
4. Start the stand-in base project.
   The network `spinup-boot-<profile>-app` holds the pinned Caddy image with the rendered `deploy/Caddyfile`.
   In `full`, the internal network `spinup-boot-<profile>-db` holds the pinned Postgres image.
5. Run `docker compose -p spinup-boot-<profile> -f monitoring/compose.monitoring.yml up -d --wait`.

Then it does these checks:

| Check | How |
| --- | --- |
| Every service is healthy | Each container runs with zero restarts. A service with a health check is `healthy`. Loki has no health check, so it must run. |
| Grafana answers | `GET 127.0.0.1:<port>/api/health` returns `"database": "ok"`. |
| Prometheus has its targets up | Every target in `/api/v1/targets` is `up`. Prometheus scrapes only itself. |
| Alloy scrapes the stack | The query `up` returns 1 for each job that Alloy scrapes (see the table below). |
| Backup metrics reach Prometheus | A sample `backup.prom` says the last full backup finished one hour ago. Alloy reads it with its textfile collector, and the recording rule `backup_last_age_seconds` returns about 3600. |
| Logs reach Loki | A log line with a random marker goes to Alloy over OTLP/HTTP. Loki returns it. |
| Traces reach Tempo (`full`) | A span with a random trace ID goes to Alloy. Tempo returns the trace. |
| Memory | The total memory of the services in `docker stats` stays under the budget. |

The test reaches the internal network through the datasource proxy of Grafana (`/api/datasources/proxy/uid/<uid>/...`).
This also proves that the provisioned datasources work.
The OTLP data goes from the Caddy container on the app network to `http://alloy:4318`, the same path that the app uses.

| Profile | Jobs with `up == 1` |
| --- | --- |
| `lite` | `prometheus`, `alloy`, `caddy`, `loki`, `grafana` |
| `full` | the `lite` jobs, plus `tempo`, `node`, `cadvisor`, `postgres`, `blackbox-exporter`, and `blackbox` |

The component `prometheus.exporter.self` labels its target `job="integrations/self"`.
`config.alloy` relabels it to `job="alloy"` before the scrape.

## The memory budget

| Profile | Budget | Measured (idle, 2026-10-15) |
| --- | --- | --- |
| `lite` | 1024 MiB | 477 to 515 MiB |
| `full` | 2560 MiB | 579 to 601 MiB |

The budgets are the defaults of the plan.
The measurements in `docs/monitoring.md` and in this test are far below them, so the test keeps the defaults.
Grafana uses more than half of the memory.
The `lite` budget leaves 1 GiB of a 2 GiB VM for the app, Postgres, Caddy, and the system.

The test reads `docker stats --no-stream` three times, 5 seconds apart, after the data checks.
It compares the highest total with the budget.
`docker stats` reports the memory of the cgroup without the inactive file cache.
The stand-in Caddy and Postgres containers are not part of the total.

## Run the test

Docker must run, and the pinned images must be on the machine or available to pull.

```
bash testdata/e2e/monitoring/run.sh lite
bash testdata/e2e/monitoring/run.sh full
```

| Option | Effect |
| --- | --- |
| `BOOT_GRAFANA_PORT=<port>` | Publish Grafana on this port of `127.0.0.1`. The default is 3000. |
| `BOOT_BUDGET_MIB=<MiB>` | Use this memory budget. |
| `BOOT_TIMEOUT=<seconds>` | Wait this long for each check. The default is 180. |
| `BOOT_LOG_DIR=<dir>` | Copy the logs of a failed run to this directory. |

If port 3000 is in use and `BOOT_GRAFANA_PORT` is not set, the script picks a free port and prints it.
The script also needs Go, `curl`, and `python3`.
A run takes 1 to 2 minutes on a laptop (lite 73 s, full 104 s).

## Changes to the rendered files

The test changes only these values, with the file `boot.override.yml` in the work directory:

- The secrets come from the work directory, not from `/opt/<app>/secrets`. Grafana and the postgres-exporter run as the current user (`DEPLOY_UID`, `DEPLOY_GID`).
- The backup metrics directory comes from the work directory (`BOOT_METRICS`), not from `/opt/<app>/backups/metrics`, which Docker Desktop cannot share.
- The Grafana port is `BOOT_GRAFANA_PORT`.
- On Docker Desktop, node-exporter mounts `/` without `rslave`. The root mount of the Docker Desktop VM is private, so `rslave` fails there. A Linux host keeps the rendered mount.

The stand-in Caddy answers every route with status 200, because `upstream.caddy` is a deploy artifact.
`PUBLIC_URL` is `http://caddy/`, so the blackbox exporter probes a page that answers.

## Teardown

A trap always removes the Compose project with its volumes, the stand-in containers, the networks, and the work directory.
Every Docker object has the prefix `spinup-boot-`.
The script also removes the objects of an earlier run that stopped before its trap ran.

## CI

The workflow `.github/workflows/monitoring.yml` runs the test every night.
It also runs on a pull request or a push to `main` that changes the monitoring pack, the deploy `Caddyfile`, the stack versions, or the test.
The matrix has the profiles `lite` and `full`.
When a run fails, the workflow uploads the logs as the artifact `monitoring-boot-<profile>`.
