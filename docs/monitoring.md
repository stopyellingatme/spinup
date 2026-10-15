# Production monitoring files

The `monitoring` pack gives a new app a monitoring stack that runs next to the app on the VM.
Add it with `--modules`. The pack requires the `deploy` pack.
Nothing in the stack is published to the internet.

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
$ ls monitoring/prometheus/rules
app.yml
stack.yml
$ grep -A2 '^\[monitoring\]' spinup.toml
[monitoring]
# Pick the monitoring stack: lite, full, or none.
profile = "lite"
```

## Profiles

The `--monitoring-profile` flag (the question "Monitoring profile") selects the services.

| Profile | Services | Use |
| --- | --- | --- |
| `lite` (default) | Alloy, Prometheus, Loki, Grafana | A VM with less than 4 GB RAM. Alloy drops traces. |
| `full` | `lite` plus Tempo, node-exporter, cAdvisor, postgres-exporter, blackbox exporter | Traces, host, container, Postgres, and public URL metrics. |
| `none` | no service | No monitoring. The pack renders no monitoring file. |

```console
$ spinup new shop --api-only --module example.com/shop --modules ci,deploy,monitoring --monitoring-profile full --no-git --non-interactive
...
$ ls shop/monitoring shop/monitoring/prometheus/rules
shop/monitoring:
alerts
alloy
blackbox
compose.monitoring.yml
grafana
loki
prometheus
tempo

shop/monitoring/prometheus/rules:
app.yml
certs.yml
host.yml
postgres.yml
stack.yml
```

`spinup.toml` repeats the profile as `[monitoring] profile`.
At deploy time, `spinup.toml` wins.
The profile `none` renders no monitoring file.
The vendored community dashboards in `monitoring/grafana/dashboards/stack/vendor/` need the exporters, so only the profile `full` renders them.
`spinup new` asks the monitoring question only when you select the `monitoring` module.

Measured on an idle stack, the services use about this much memory:

| Service | Memory |
| --- | --- |
| Grafana | 250 to 320 MiB |
| Alloy | 75 to 80 MiB |
| Prometheus | 55 to 60 MiB |
| Loki | 45 to 70 MiB |
| Tempo (full) | 40 MiB |
| cAdvisor, node-exporter, postgres-exporter, blackbox exporter (full) | 8 to 15 MiB each |

## Files

| File | Class | Purpose |
| --- | --- | --- |
| `monitoring/compose.monitoring.yml` | managed | The Compose project `<app>-monitoring`. |
| `monitoring/alloy/config.alloy` | managed | The OTLP receiver, the redaction stage, the exporters, and the scrapes. |
| `monitoring/loki/loki.yml` | managed | Loki with filesystem storage and 30 days of retention. |
| `monitoring/tempo/tempo.yml` | managed | Tempo with local storage (profile `full`). |
| `monitoring/prometheus/prometheus.yml` | managed | The rule files and the remote-write receiver. |
| `monitoring/prometheus/rules/*.yml` | managed | The default alert rules. |
| `monitoring/alerts/custom.yml` | seed | Your own Prometheus rules. |
| `monitoring/blackbox/blackbox.yml` | managed | The probe of the public URL (profile `full`). |
| `monitoring/grafana/entrypoint.sh` | managed | Reads the secrets and writes the contact points. Then it starts Grafana. |
| `monitoring/grafana/provisioning/` | managed | The datasources, the dashboard folders, and the alert rule. |
| `monitoring/grafana/dashboards/stack/` | managed | spinup's dashboards and the vendored community dashboards. |
| `monitoring/grafana/dashboards/custom/` | seed | Your own dashboards. |

spinup never changes a seed file after `spinup new`.

## How the data flows

- The app sends OTLP to `http://alloy:4318` on the `<app>-app` network. Set `OTEL_EXPORTER_OTLP_ENDPOINT=http://alloy:4318` to turn it on.
- Alloy removes secrets, batches the data, and sends it on.
  Metrics go to Prometheus with remote write. Logs go to Loki over OTLP. Traces go to Tempo (profile `full`).
- Alloy scrapes Loki, Grafana, Caddy (`caddy:2020`), and itself.
  In the profile `full`, it also scrapes Tempo and the exporters, and it probes `PUBLIC_URL`.
- Prometheus evaluates the alert rules. Grafana sends the notifications. The stack has no Alertmanager.

The datasource UIDs are fixed: `prometheus`, `loki`, and `tempo`.
In the profile `full`, a log line links to its trace, and a span links to its log lines.

### Secret redaction

The kit `logx` redactor masks secret values before the export.
Alloy is the second line. For logs and spans:

- It removes every attribute whose key contains `password`, `secret`, `token`, `api_key`, `access_key`, `private_key`, `authorization`, `cookie`, or `session`.
- It masks bearer and basic credentials, the password in a URL, `password=` style pairs, and JSON Web Tokens in attribute values and log bodies.

## Security

- Grafana listens on `127.0.0.1:3000` of the VM. `spinup monitor open` reaches it through an SSH tunnel.
- No collector port is published. Only the app network reaches Alloy.
- Every service is read-only, drops every Linux capability, and sets `no-new-privileges`.
- The `monitoring` network is internal. Grafana has its own network for notifications.
- cAdvisor reads the Docker socket for container names. node-exporter mounts the host root read-only for disk metrics.
  The file explains each exception with a `spinup:allow` comment.

## Alerts

| Alert | Profile | Fires when |
| --- | --- | --- |
| `HighErrorRate` | all | More than 5% of the requests of a service fail with 5xx for 10 minutes. |
| `HighLatencyP95` | all | The p95 request duration is above 1 s for 10 minutes. |
| `BackupStale` | all | The newest backup is older than 26 hours. |
| `MonitoringTargetDown` | all | A scrape fails for 5 minutes. |
| `Heartbeat` | all | Always. This is the dead-man's switch. |
| `DiskSpaceLow`, `DiskWillFillIn24Hours` | full | A filesystem has less than 10% free, or it fills up within 24 hours. |
| `ContainerRestartLoop` | full | A container restarts 3 times in 15 minutes. |
| `PostgresDown`, `PostgresConnectionsHigh` | full | Postgres is down, or it uses more than 80% of `max_connections`. |
| `WalArchiveLag`, `WalArchiveFailing` | full | The last WAL archive is older than 15 minutes, or an archive fails. |
| `PublicURLDown`, `CertificateExpiresSoon` | full | The public URL fails, or its certificate expires in less than 14 days. |

Put your own rules in `monitoring/alerts/custom.yml`.
Grafana sends a notification for each firing Prometheus alert.

### Notifications and the heartbeat

`spinup deploy` sets these variables for `compose.monitoring.yml`:

| Variable | From | Purpose |
| --- | --- | --- |
| `ALERT_EMAIL_TO` | `spinup.toml` | Email notifications. They need `SMTP_HOST`, `SMTP_USER`, and `SMTP_FROM`. |
| `ALERT_NTFY_URL` | `spinup.toml` | ntfy notifications. Grafana uses the ntfy `grafana` template. |
| `HEARTBEAT_URL` | `[monitoring] heartbeat_url` | Grafana calls the URL every 5 minutes. |
| `PUBLIC_URL` | `spinup.toml` | The blackbox exporter probes it (profile `full`). |
| `PROMETHEUS_RETENTION`, `PROMETHEUS_RETENTION_SIZE` | environment file | The retention of Prometheus. The defaults are `30d` and `5GB`. |
| `TEMPO_RETENTION` | environment file | The retention of Tempo. The default is `336h`. |

These values are secrets in `/opt/<app>/secrets`:

- `GRAFANA_ADMIN_PASSWORD` (required). Grafana does not start without it.
- `SLACK_WEBHOOK_URL` (optional). It turns on Slack notifications.
- `SMTP_PASSWORD` (optional).

If you set no channel, Grafana still shows the alerts, but it sends nothing.
Point the heartbeat at an external service, for example a healthchecks.io check with a 5-minute period.
A down VM cannot send an alert, so the missing heartbeat is the alert.

## Dashboards

The folder Stack holds spinup's dashboards: HTTP RED, Go runtime, Caddy, Backups, and Deploys.
In the profile `full`, it also holds the vendored Node Exporter Full, cAdvisor, and PostgreSQL dashboards.
The folder Custom holds your own dashboards from `monitoring/grafana/dashboards/custom/`.

- HTTP RED reads `http_server_request_duration_seconds`, the kit request metric.
- Backups reads `backup_last_age_seconds`, the kit gauge `backup.last_age`, and the Postgres archiver metrics.
- Deploys shows the Grafana annotations with the tag `deploy`.
- Go runtime reads the kit runtime metrics (`go_goroutine_count`, `go_memory_used_bytes`, and the others). `telemetry.Setup` starts them.
- Caddy reads the Caddy metrics on `caddy:2020`. The deploy Caddyfile serves them there, and it does not publish the port.
