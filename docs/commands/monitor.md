---
title: monitor — open Grafana, read logs, manage alerts
parent: Command guides
---

# monitor: open Grafana, read logs, manage alerts

The `monitor` commands let you look at a deployed app from your terminal.
Open Grafana, read logs, follow a trace, and manage alerts, all through SSH.

```sh
spinup monitor open --env production
```

```console
$ spinup monitor --help
...
Available Commands:
  alerts      Show firing and pending alerts
  dashboards  Manage custom Grafana dashboards
  logs        Query Loki logs
  open        Open Grafana through a local SSH tunnel
  silence     Silence alerts matching label=value
  status      Show monitoring health and disk use
  test-alert  Send a test alert to configured contact points
  trace       Show a Tempo span tree (full profile)
...
```

## Before you start

- The app has the `monitoring` module, and `[monitoring] profile` in `spinup.toml` is `lite` or `full`, not `none`. See [Monitoring](../monitoring.md#choose-a-profile).
- You can reach the VM with SSH, and the deploy user can run Docker.
- Every command takes `--env`. You can leave it out when `spinup.toml` has one environment or a `default_env`.
- If SSH asks about a host key it has not seen, add `--accept-new-host-key`.

| Flag | Meaning | Example |
| --- | --- | --- |
| `--env` | The environment in `spinup.toml` | `spinup monitor status --env production` |
| `--accept-new-host-key` | Trust a new SSH host key | `spinup monitor status --env production --accept-new-host-key` |

## Open Grafana

`open` starts an SSH tunnel, prints a local URL, and opens your browser.
Keep the command running while you use Grafana. Press Ctrl-C to close the tunnel.
The tunnel listens only on your loopback address.

<!-- doctest: skip -->
```console
$ spinup monitor open --env production --no-browser
...
```

Add `--no-browser` to print the URL and open it yourself.
Grafana has its own login. The user is `admin`, and the password is the `GRAFANA_ADMIN_PASSWORD` secret.

## Check health

`status` prints the profile, the state and health of each container, and the size of each volume.
Use it first when something looks wrong.

<!-- doctest: skip -->
```console
$ spinup monitor status --env production
Profile: lite
grafana running healthy
loki running healthy
prometheus running healthy
alloy running
todo_monitoring_loki_data 412.3MB
```

## Read logs

`logs` prints the newest 1000 lines of the last hour, oldest first, with timestamps.

<!-- doctest: skip -->
```console
$ spinup monitor logs --env production --service todo-api --since 15m
2026-10-15T08:23:52.114Z {"level":"INFO","msg":"request","path":"/api/todos","status":200,"trace_id":"0123456789abcdef0123456789abcdef"}
...
$ spinup monitor logs --env production --query '{service_name="todo-api"} |= "error"'
...
```

| Flag | Meaning | Example |
| --- | --- | --- |
| `--service` | Show one service (the `service_name` label) | `--service todo-api` |
| `--since` | How far back to look. The default is `1h`. | `--since 15m` |
| `--query` | A LogQL query. It replaces `--service`. | `--query '{service_name="todo-api"} \|= "error"'` |

## Follow a trace

`trace` prints a tree of spans, with the service, the span name, and the duration of each.
Find a trace ID in a log line: each line that belongs to a trace carries a `trace_id` of 32 hexadecimal characters.
Traces need the `full` profile.

<!-- doctest: skip -->
```console
$ spinup monitor trace --env production 0123456789abcdef0123456789abcdef
todo-api GET /api/todos 12.4ms
  todo-api SELECT todos 3.1ms
```

## Work with alerts

`alerts` lists the alerts that fire now and the rules that are pending.
`silence` mutes one alert for a time. It takes one exact matcher, `label=value`, and prints the silence ID.
`test-alert` sends a test alert called `SpinupTestAlert`.

<!-- doctest: skip -->
```console
$ spinup monitor alerts --env production
...
$ spinup monitor silence 'prometheus_alertname=HighErrorRate' --for 2h --env production
5f1c2a1e-0d6b-4b8e-9d3a-7a9c1e2b3c4d
$ spinup monitor test-alert --env production
spinup-test-1792052632114000000
```

The built-in alert rules reach Grafana through one rule. Grafana names that alert `Prometheus alert` and keeps the real name in the label `prometheus_alertname`.
So match `prometheus_alertname=HighErrorRate` to silence the built-in `HighErrorRate` rule.

| Flag | Meaning | Example |
| --- | --- | --- |
| `--for` (silence) | How long to mute. The default is `2h`. | `spinup monitor silence 'prometheus_alertname=HighErrorRate' --for 30m --env production` |

The test alert stays active for five minutes, and Grafana sends it to your contact points.
Check the contact points to confirm that the message arrived.
The commands read the Grafana password from `/opt/<app>/secrets/` on the VM. It never reaches your computer.

## Save custom dashboards

Build a dashboard in the Grafana UI, then run `dashboards sync` to save it in your repository.
It writes `monitoring/grafana/dashboards/custom/<uid>.json` with sorted keys and no database ID, so diffs stay small.
It never touches the dashboards that spinup provisions.

<!-- doctest: skip -->
```console
$ spinup monitor dashboards sync --env production
...
```

Review the files and commit them. They deploy with the next release.

## Release marks

After each release, the deploy hook adds a Grafana annotation with the release ID, git SHA, and user.
If Grafana does not run, the hook skips the annotation. If the annotation fails, the deploy shows a warning and continues.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `trace` finds nothing | The profile is `lite`. | Use the `full` profile. See [Monitoring](../monitoring.md#choose-a-profile). |
| SSH rejects an unknown host key | The VM key is new to your computer. | Check the key, then add `--accept-new-host-key`. |
| The tunnel closes | The command ended. | Keep `monitor open` running. |
| Grafana rejects the password | The secret and Grafana differ. | See [Change the Grafana password](../monitoring.md#change-the-grafana-password). |

## See also

- [Monitoring](../monitoring.md): profiles, alert rules, and dashboards
- [backup](backup.md): backup age and alerts
- [Telemetry and data handling](../data-handling.md)
- Flag reference: [monitor](../site/commands/monitor.md), [open](../site/commands/monitor-open.md), [logs](../site/commands/monitor-logs.md), [trace](../site/commands/monitor-trace.md), [alerts](../site/commands/monitor-alerts.md), [silence](../site/commands/monitor-silence.md), [test-alert](../site/commands/monitor-test-alert.md), [dashboards sync](../site/commands/monitor-dashboards-sync.md)
