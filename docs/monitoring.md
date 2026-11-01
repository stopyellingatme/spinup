---
title: Monitoring
parent: Operate
nav_order: 1
---

# Monitoring

The `monitoring` module runs Grafana, Prometheus, Loki, and an OpenTelemetry collector (Alloy) next to your app.
You see logs, traces, metrics, and alerts in one place. Nothing in the stack is open to the internet.

## See your logs and traces in Grafana

On your laptop, one command starts your app with a local Grafana. It needs Docker.

<!-- doctest: skip -->
```console
$ spinup dev --lgtm
...
Grafana  http://localhost:3000
```

On your VM, add the module, deploy, and open Grafana through an SSH tunnel:

<!-- doctest: skip -->
```console
$ spinup module add monitoring --yes --non-interactive
...
$ git merge spinup/module-monitoring
$ spinup secrets set --env production GRAFANA_ADMIN_PASSWORD
$ spinup deploy --env production
...
$ spinup monitor open --env production
...
```

`module add` works on a branch, so run the `git merge` command that it prints before you deploy. See [Upgrade](upgrading.md#add-or-remove-modules).
The deploy fails early if the `GRAFANA_ADMIN_PASSWORD` secret is missing. See [Secrets](deploy/secrets.md) to set up secrets for an environment.
Keep `monitor open` running while you use Grafana. Press Ctrl-C to close the tunnel.
Sign in as `admin`. See [Open Grafana](#open-grafana).

A new app can start with the module:

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,monitoring --no-git --non-interactive
...
$ cd todo
$ ls monitoring
alerts
alloy
compose.monitoring.yml
grafana
loki
prometheus
$ grep -A2 '^\[monitoring\]' spinup.toml
[monitoring]
# Pick the monitoring stack: lite, full, or none.
profile = "lite"
```

## Monitor your app in development

`spinup dev --lgtm` starts the Grafana LGTM image in Docker. The image holds Loki, Tempo, and Prometheus, so you see logs, traces, and metrics.
The command sets `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` for your API. You change no setting.
The URL table that `spinup dev` prints includes the Grafana address, usually `http://localhost:3000`. Open it and choose **Explore**.
See [dev](commands/dev.md) for the other flags.

## Choose a profile

The profile sets how much of the stack runs. Set it with `--monitoring-profile` or in `spinup.toml`.

| Profile | Runs | Use it when |
| --- | --- | --- |
| `lite` (default) | Alloy, Prometheus, Loki, Grafana | The VM has less than 4 GB of RAM. Alloy drops traces. |
| `full` | `lite` plus Tempo, node-exporter, cAdvisor, postgres-exporter, and a blackbox probe | You want traces, host, container, and database metrics, and a public URL check. |
| `none` | Nothing | You do not want monitoring. The module renders no monitoring file. |

```console
$ spinup new shop --api-only --module example.com/shop --modules ci,deploy,monitoring --monitoring-profile full --no-git --non-interactive
...
$ ls shop/monitoring/prometheus/rules
app.yml
certs.yml
host.yml
postgres.yml
stack.yml
```

To change the profile later, change `[monitoring] profile` and re-render the module.
The profile in `spinup.toml` must match `monitoring_profile` in `spinup.lock`.
If they differ, the deploy stops before it changes the VM.

Plan for memory. An idle `lite` stack uses about 430 to 520 MiB, and Grafana uses most of it.
The `full` profile adds about 70 to 100 MiB for Tempo and the four exporters.

## Open Grafana

Grafana listens on `127.0.0.1:3000` of the VM. You reach it through an SSH tunnel:

<!-- doctest: skip -->
```console
$ spinup monitor open --env production --no-browser
...
```

Use `--no-browser` to print the URL and open it yourself.
The username is `admin`. The password is the `GRAFANA_ADMIN_PASSWORD` secret.
The deploy fails early if this secret is missing.

### Change the Grafana password

1. Set the new password. The command reads it from standard input.

   <!-- doctest: skip -->
   ```console
   $ spinup secrets set --env production GRAFANA_ADMIN_PASSWORD
   ```

2. Deploy: `spinup deploy --env production`.
3. If Grafana still runs the old password, restart it on the VM. Replace `todo` with your app name.

   ```sh
   docker compose -p todo-monitoring -f /opt/todo/monitoring/compose.monitoring.yml restart grafana
   ```

On each start, Grafana resets the admin password from the secret.

## See logs and traces

Query the stores from your terminal. You do not need to open Grafana.

<!-- doctest: skip -->
```console
$ spinup monitor logs --env production --service todo-api --since 1h
...
$ spinup monitor logs --env production --query '{service_name="todo-api"} |= "error"'
...
$ spinup monitor trace --env production 0123456789abcdef0123456789abcdef
...
```

`--service` filters on the `service_name` label. `--query` takes a LogQL query and replaces that filter.
Traces need the `full` profile. Every log line that belongs to a trace carries a `trace_id` that you can paste into `monitor trace`.
In Grafana, a log line links to its trace, and a span links to its log lines.

## How the data flows

1. The API and web service send OpenTelemetry data to `http://alloy:4318` on the private app network. `spinup deploy` sets this endpoint.
2. Alloy removes secrets from the data. Metrics go to Prometheus, logs go to Loki, and traces go to Tempo (`full` only).
3. Prometheus evaluates the alert rules. Grafana sends the notifications. The stack has no Alertmanager.

Alloy also scrapes Loki, Grafana, Caddy, and itself. With `full`, it scrapes Tempo and the exporters, and it probes your `public_url`.
A sidecar service that uses the Rust or Python template sends its traces to the same endpoint. One trace then crosses the API and the service. See [Sidecar services](services.md).

## Send alerts

Grafana sends a notification for each firing alert. With no channel, Grafana shows alerts but sends nothing.
Add a channel in `spinup.toml`, or set a secret:

```toml
[monitoring]
profile = "lite"
heartbeat_url = "https://hc-ping.com/your-uuid"
alert_email = "ops@example.com"
smtp_host = "smtp.example.com:587"
smtp_user = "apikey"
smtp_from = "alerts@example.com"
ntfy_url = "https://ntfy.sh/todo-alerts"
```

Set the secrets for Slack and SMTP, then deploy:

<!-- doctest: skip -->
```console
$ spinup secrets set --env production SLACK_WEBHOOK_URL
$ spinup secrets set --env production SMTP_PASSWORD
$ spinup deploy --env production
```

| Channel | Config | Secret |
| --- | --- | --- |
| Slack | none | `SLACK_WEBHOOK_URL` |
| Email | `alert_email`, `smtp_host`, `smtp_user`, `smtp_from` | `SMTP_PASSWORD` |
| ntfy | `ntfy_url` | none |
| Heartbeat | `heartbeat_url` | none |

`spinup deploy` writes these keys to `/opt/<app>/monitoring/.env` on the VM, with mode `0600`. Do not edit that file by hand.

The heartbeat is a dead-man's switch. Grafana calls the URL every 5 minutes.
Point it at an outside service, such as a healthchecks.io check with a 5-minute period.
A dead VM cannot send an alert, so the missing heartbeat is the alert.

Send a test alert to prove delivery:

<!-- doctest: skip -->
```console
$ spinup monitor test-alert --env production
...
$ spinup monitor alerts --env production
...
$ spinup monitor silence 'alertname=HighErrorRate' --for 2h --env production
...
```

If the monitoring step fails during a deploy, the app keeps running and the deploy fails.
Fix the problem, then run `spinup deploy --resume --env production`.

## Alert rules

| Alert | Profile | Fires when |
| --- | --- | --- |
| `HighErrorRate` | all | More than 5% of API requests fail with 5xx for 10 minutes. The API must receive more than 1 request every 10 seconds. |
| `HighLatencyP95` | all | The p95 API request time is above 1 s for 10 minutes, with the same traffic minimum. |
| `BackupStale` | all | The newest backup is older than 26 hours, or no backup metric exists for 15 minutes. |
| `BackupVerifyFailed` | all | The last backup verification failed. Run `spinup backup verify`. |
| `MonitoringTargetDown` | all | A scrape fails for 5 minutes. |
| `AlloyMissing` | all | No Alloy data arrives for 5 minutes. |
| `APIRequestMetricsMissing` | all | No API request metrics arrive for 10 minutes. It can fire before the first request. |
| `Heartbeat` | all | Always. This is the dead-man's switch. |
| `DiskSpaceLow`, `DiskWillFillIn24Hours` | full | A disk has less than 10% free, or it fills within 24 hours. |
| `ContainerRestartLoop` | full | A container restarts 3 times in 15 minutes. |
| `PostgresDown`, `PostgresConnectionsHigh` | full | Postgres is down, or it uses more than 80% of `max_connections`. |
| `WalArchiveLag`, `WalArchiveFailing` | full | The last WAL archive is older than 15 minutes, or an archive fails. |
| `PublicURLDown`, `CertificateExpiresSoon` | full | The public URL fails, or its certificate expires in under 14 days. |

The alerts cover the API. The web service sends traces only, so no alert watches web requests.

### Add your own alert

Put Prometheus rules in `monitoring/alerts/custom.yml`. spinup never changes this file after `spinup new`.

```yaml
groups:
  - name: custom
    rules:
      - alert: TooManyRequests
        expr: sum(increase(http_server_request_duration_seconds_count[1h])) > 100000
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: More than 100000 API requests in one hour
```

Deploy to load the rule: `spinup deploy --env production`.

## Dashboards

Grafana has two folders.

| Folder | Holds | You edit it? |
| --- | --- | --- |
| Stack | HTTP RED, Go runtime, Caddy, Backups, and Deploys. With `full`, also Node Exporter Full, cAdvisor, and PostgreSQL. | No. spinup replaces these on upgrade. |
| Custom | Your dashboards, from `monitoring/grafana/dashboards/custom/`. | Yes. |

To keep a dashboard you built in the Grafana editor, export it into `custom/`:

<!-- doctest: skip -->
```console
$ spinup monitor dashboards sync --env production
...
```

Review the exported JSON files and commit them. The command never writes into `stack/`.

What the stack dashboards show:

| Dashboard | Shows |
| --- | --- |
| HTTP RED | Request rate, errors, and duration of the API. It does not cover web requests. |
| Go runtime | Goroutines, memory, and garbage collection of the API. |
| Caddy | Requests that the edge proxy handles. |
| Backups | The backup age, size, and verification results. See [Backup metrics](#backup-metrics). |
| Deploys | An annotation for each release, with the release ID, git SHA, and user. |

When you write your own dashboard, use the fixed datasource UIDs `prometheus`, `loki`, and `tempo`.

## Files

| File | Class | You can ... |
| --- | --- | --- |
| `monitoring/compose.monitoring.yml` | managed | Leave it. spinup updates it. |
| `monitoring/alloy/config.alloy` | managed | Leave it. |
| `monitoring/loki/loki.yml`, `tempo/tempo.yml`, `prometheus/` | managed | Leave them. Use the `[monitoring]` keys for metric and trace retention. |
| `monitoring/alerts/custom.yml` | seed | Edit it. Your alert rules. |
| `monitoring/grafana/dashboards/custom/` | seed | Edit it. Your dashboards. |

A managed file changes when you upgrade spinup. A seed file is yours. See [Upgrade](upgrading.md#what-happens-to-your-edits).

## Retention and limits

| Key in `[monitoring]` | Default | Meaning |
| --- | --- | --- |
| `prometheus_retention` | `30d` | How long Prometheus keeps metrics. |
| `prometheus_retention_size` | `5GB` | The disk limit for metrics. |
| `tempo_retention` | `336h` | How long Tempo keeps traces (`full`). |

Loki keeps logs for 30 days.

## Redaction

Two layers hide secrets. The kit `logx` redactor in your app runs first. Alloy runs second and acts as a safety net.
Alloy deletes any log or span attribute whose key contains `password`, `passwd`, `secret`, `token`, `api_key`, `access_key`, `private_key`, `authorization`, `cookie`, or `session`.
It also masks bearer and basic credentials, URL passwords, `password=` pairs, and JSON Web Tokens in values and log bodies.
The app layer has its own key list. See [Telemetry and data handling](data-handling.md#what-is-redacted).

## Security

- Grafana binds to the VM loopback address. No collector port is published, and only the app network reaches Alloy.
- The `monitoring` network is internal. Only Grafana has outbound access, for notifications.
- Every service is read-only, drops all Linux capabilities, and sets `no-new-privileges`.
- cAdvisor reads the Docker socket for container names. node-exporter mounts the host root read-only. The Compose file explains both with a `spinup:allow` comment.

## Backup metrics

Every backup backend writes the same metrics file on the VM. The Backups dashboard and the backup alerts read it.
`spinup backup now` and the scheduled timers update the file after each run.

| Metric | Meaning |
| --- | --- |
| `backup_last_success_timestamp_seconds{kind}` | When the last backup of this kind finished. |
| `backup_last_size_bytes{kind}` | The size of that backup. |
| `backup_last_verify_timestamp_seconds` | When the last verification finished. |
| `backup_last_verify_ok` | `1` for success, `0` for failure. |
| `backup_last_verify_rto_seconds` | How long the verification took. |

`BackupStale` fires when the newest success is older than 26 hours. `BackupVerifyFailed` fires when the last verification failed.
A failed backup keeps the previous success metrics. A failed metrics update prints a warning and does not fail the backup.
A scheduled verify checks the repository or the dump. It does not time a full restore, so use `spinup backup verify` for that.
See [Backups with pg_dump](backups-pgdump.md) and [Backups with WAL-G](backups-walg.md).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The deploy stops with a profile error. | `[monitoring] profile` differs from `monitoring_profile` in `spinup.lock`. | Restore the locked profile, or re-render the module. |
| `monitor trace` finds nothing. | The profile is `lite`, which drops traces. | Use `full`. |
| `APIRequestMetricsMissing` fires on a new app. | The API has had no request yet. | Send one request. |
| The new Grafana password does not work. | Grafana still runs with the old secret. | Restart Grafana as shown in [Change the Grafana password](#change-the-grafana-password). |
| The deploy fails at the `monitoring` step. | The monitoring stack did not start. | Fix it, then run `spinup deploy --resume --env production`. |

## See also

- [Telemetry and data handling](data-handling.md) for what the app sends.
- [monitor](commands/monitor.md) for every monitor command, and the flag reference for [monitor open](site/commands/monitor-open.md) and [monitor logs](site/commands/monitor-logs.md).
- [Sidecar services](services.md) for traces that cross a service.
- [Backups with pg_dump](backups-pgdump.md) and [Backups with WAL-G](backups-walg.md).
