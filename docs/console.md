---
title: Operator console
parent: Operate
nav_order: 2
---

# Operator console

The `console` module gives you an overview of one deployed app on one VM.
It works with API-only, embed, and node apps. It continues to serve when the app fails or rolls back.

```console
$ spinup console --help
...
$ spinup status --help
...
```

## Select and deploy

A new app selects `ci,deploy,console` by default. An explicit list replaces that selection.
Use `--modules ci,deploy` to omit the console. The console requires `deploy`. When you add the module, spinup adds `deploy` when necessary.
The top-level `modules` array in `spinup.toml` is the only console switch.
There is no console password, secret, public URL, or port setting.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ ls todo/console
compose.console.yml
compose.query.yml
```

Existing apps keep their locked answers during an upgrade. An upgrade does not enable the console automatically.
If your locked packs predate the console, first upgrade to packs that contain it.
Then add the module, review the branch, merge it, and deploy once:

<!-- doctest: skip -->
```console
$ spinup upgrade --yes
...
$ spinup module add console --yes --non-interactive
...
$ git diff HEAD...spinup/module-console
...
$ git merge spinup/module-console
$ spinup doctor --quick
...
$ spinup deploy --env production
...
```

Adoption uses your locked template version. It adds managed files under `api/cmd/console/` and `console/`.
The normal merge adds the build and copy instructions to `deploy/Dockerfile.api`.
If you ejected that Dockerfile, review those instructions yourself. `console-wired` names the file when they are missing.
An ejected `deploy/compose.yml` does not prevent console adoption. The console uses separate Compose files.

During the first deploy, spinup starts the console before monitoring. It starts the query relay after monitoring becomes available.
If monitoring fails, the app remains live and the console remains available.
After repairs, run `spinup deploy --resume --env production`.

## Open the overview

<!-- doctest: skip -->
```console
$ spinup console open --env production
Console: http://127.0.0.1:49152/
Press Ctrl-C to close the tunnels.
$ spinup status --env production
...
$ spinup status --env production --json
...
```

The workstation port varies. Keep the command running while you use the page. Press Ctrl-C to close its tunnels.
SSH authenticates the operator. Trust local users of the VM and workstation.
The VM publishes the console only at `127.0.0.1:7410`. Never add a public Caddy route for it.
The console has no app session or console account. Grafana retains its separate admin login.
With active monitoring, the command also verifies a Grafana tunnel and adds its local port to the console URL.
If Grafana fails, the console still opens without Grafana links.

The overview shows Live release, Containers, Firing alerts, Last backup, WAL archive, Job queues, Disk, Memory, and Deploy history.
It reads repository metadata for backups, PostgreSQL for WAL and queue counts, and host counters for disk and memory.
An empty successful list means that the source answered with no rows. An unavailable source never becomes an empty success.
History retains at most 20 attempts. Early failures can have unknown user, commit, or color fields.
Rollback does not invent a separate history event.

## Observation times and disabled tiles

The host sampler runs during deployment and every 30 seconds through a systemd user timer.
The timer needs an available user manager. Installation requests neither sudo nor lingering.
On older VMs without that manager, both CLI commands request a sample through SSH when it exceeds 30 seconds.
While the page is open, the CLI repeats that check every 30 seconds.
HTTP requests never start host commands. A plain tunnel can show an old sample while the timer sleeps.

Each tile shows its observation time. After 90 seconds, successful host data becomes `stale` with reason `sample_expired`.
Stale tiles retain their values and times. Failed probes replace prior values with `unavailable` and a reason.
Reasons include `missing`, `timeout`, `invalid`, and `source_down`. A missing sample has unknown times.
A valid partial overview makes `spinup status` exit successfully. Read the states before you treat the values as current.

| Selection | Disabled sections and features |
| --- | --- |
| No monitoring module | Alerts use `no_monitoring`. Log search and Grafana links are disabled. Host facts remain available. |
| Monitoring profile `none` | Alerts use `profile_none`. Log search and Grafana links are disabled. |
| Monitoring profile `lite` | Alerts, logs, and five stack dashboards are available. Host facts need no exporters. |
| Monitoring profile `full` | Three vendor dashboards are available in addition to the lite features. |
| No backups module | Backup and WAL use `no_backups`. |
| Backup backend `pgdump` | WAL uses `no_wal`. |
| No jobs module | Job queues use `no_jobs`. |

A disabled tile is healthy. It has no observation time or data.

## Limits and access

The console runs separately from the app, as user `65532:65532`, with a read-only root and dropped capabilities.
Its only mount contains projected facts at `/data:ro`. Neither console service receives secrets, database credentials, or the Docker socket.
The page joins only its dedicated bridge. The query relay also joins the monitoring network and publishes no port.

The page has a 64 MiB limit and a 40 MiB Go heap target.
The optional relay has a 48 MiB limit and a 28 MiB Go heap target.
Each service has 0.25 CPU and 32 PIDs. Their combined memory limit is 112 MiB with monitoring, or 64 MiB without it.
Sampling adds one temporary container with a 48 MiB limit and a 24 MiB Go heap target.
The combined ceiling during sampling is 160 MiB with monitoring.
Process health checks run every 15 seconds, with a three-second timeout and three retries.
Source failures do not fail those health checks or restart the services.

The overview has a 512 KiB limit. Container and queue lists permit 512 rows. Alerts permit 256 instances.
An overflow invalidates only the affected section.
Log search permits the fixed filters on the Logs page. It returns at most 100 entries and permits windows of 15 minutes, one hour, six hours, or 24 hours.
The default window is one hour. Arbitrary LogQL and store writes are unavailable.
The page escapes text and projects fields, but raw app messages can still contain sensitive prose.

The page displays commands for backups and rollback when applicable. Run those commands on your workstation.
The page cannot start a backup, roll back a release, or change configuration.

## Diagnose the console

<!-- doctest: skip -->
```console
$ spinup doctor --quick
...
$ spinup doctor --remote --env production
...
```

`console-wired` checks the main program, Dockerfile, isolation, limits, probes, and relay selection.
`console-ready` reads process health, response identity, runtime bindings, and networks through SSH.
It does not start a tunnel or refresh the sample. Stale or unavailable sections produce a warning.
The warning names the first affected section in overview order.
If readiness fails, run `spinup deploy --resume --env production` after you repair the reported cause.
If a source is unavailable, run `spinup status --env production` to request a fresh sample.

See [console](commands/console.md), [status](commands/status.md), [Monitoring](monitoring.md), and [Deploy](deploy.md).
