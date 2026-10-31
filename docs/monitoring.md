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
At deploy time, the profile must match `monitoring_profile` in `spinup.lock`.
If they differ, preflight fails before the deploy changes the VM.
Restore the locked profile, or re-render the monitoring pack to change its services.
The rendered files determine which services run.
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
| `monitoring/grafana/entrypoint.sh` | managed | Reads the secrets, writes the contact points, and starts Grafana. Resets the admin password after startup. |
| `monitoring/grafana/provisioning/` | managed | The datasources, the dashboard folders, and the alert rule. |
| `monitoring/grafana/dashboards/stack/` | managed | spinup's dashboards and the vendored community dashboards. |
| `monitoring/grafana/dashboards/custom/` | seed | Your own dashboards. |

spinup never changes a seed file after `spinup new`.

## How the data flows

- The app sends OTLP to `http://alloy:4318` on the `<app>-app` network. `spinup deploy` configures this endpoint for the API and web services.
- Alloy removes secrets, batches the data, and sends it on.
  Metrics go to Prometheus with remote write. Logs go to Loki over OTLP. Traces go to Tempo (profile `full`).
- Alloy scrapes Loki, Grafana, Caddy (`caddy:2020`), and itself with `job="alloy"`.
  In the profile `full`, it also scrapes Tempo and the exporters, and it probes `PUBLIC_URL`.
- Prometheus evaluates the alert rules. Grafana sends the notifications. The stack has no Alertmanager.

The `rust` and `python` sidecar service templates export traces to the same endpoint.
Their spans are children of the API span, so one trace crosses the API and the service.
See [Sidecar services](services.md#monitoring).

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
| `HighErrorRate` | all | More than 5% of the API requests fail with 5xx for 10 minutes. |
| `HighLatencyP95` | all | The p95 duration of API requests is above 1 s for 10 minutes. |
| `BackupStale` | all | The newest backup is older than 26 hours. |
| `MonitoringTargetDown` | all | A scrape fails for 5 minutes. |
| `AlloyMissing` | all | No Alloy self-scrape data arrives for 5 minutes. |
| `APIRequestMetricsMissing` | all | No API request metrics arrive for 10 minutes. |
| `Heartbeat` | all | Always. This is the dead-man's switch. |
| `DiskSpaceLow`, `DiskWillFillIn24Hours` | full | A filesystem has less than 10% free, or it fills up within 24 hours. |
| `ContainerRestartLoop` | full | A container restarts 3 times in 15 minutes. |
| `PostgresDown`, `PostgresConnectionsHigh` | full | Postgres is down, or it uses more than 80% of `max_connections`. |
| `WalArchiveLag`, `WalArchiveFailing` | full | The last WAL archive is older than 15 minutes, or an archive fails. |
| `PublicURLDown`, `CertificateExpiresSoon` | full | The public URL fails, or its certificate expires in less than 14 days. |

HTTP RED and its error and latency alerts cover the API.
The web service exports traces only. Web request metrics are outside the scope of M6.
`APIRequestMetricsMissing` can also fire before the API receives its first request.
Check the API and Alloy when this warning fires.
`AlloyMissing` detects a stopped collector even when no `up == 0` sample arrives.

Put your own rules in `monitoring/alerts/custom.yml`.
Grafana sends a notification for each firing Prometheus alert.

### Notifications and the heartbeat

`spinup deploy` reads these optional strings from `[monitoring]` in `spinup.toml`:

| Key | Compose variable | Purpose or default |
| --- | --- | --- |
| `profile` | none | Selects `lite`, `full`, or `none`. Must match the rendered profile in `spinup.lock`. |
| `heartbeat_url` | `HEARTBEAT_URL` | Grafana calls the URL every 5 minutes. |
| `alert_email` | `ALERT_EMAIL_TO` | Email recipient. Requires the SMTP settings. |
| `smtp_host` | `SMTP_HOST` | SMTP relay address and port. |
| `smtp_user` | `SMTP_USER` | SMTP user. |
| `smtp_from` | `SMTP_FROM` | Email sender. |
| `ntfy_url` | `ALERT_NTFY_URL` | ntfy destination. Uses the ntfy `grafana` template. |
| `prometheus_retention` | `PROMETHEUS_RETENTION` | Metrics retention. Default: `30d`. |
| `prometheus_retention_size` | `PROMETHEUS_RETENTION_SIZE` | Metrics storage limit. Default: `5GB`. |
| `tempo_retention` | `TEMPO_RETENTION` | Trace retention. Default: `336h`. |

The deployer writes these values to `/opt/<app>/monitoring/.env` with mode 0600.
It quotes values and omits unset values, so Compose defaults apply.
It also writes `DEPLOY_UID` and `DEPLOY_GID` from the VM user.
The environment's `public_url` supplies `PUBLIC_URL` for the blackbox exporter.

The journaled `monitoring` step starts the stack after the app release is recorded and before the `post` hooks.
A failure leaves the app running and fails the deploy.
Correct the monitoring problem, then run `spinup deploy --resume --env staging`.

These values are secrets in `/opt/<app>/secrets`:

- `GRAFANA_ADMIN_PASSWORD` (required). Deploy preflight checks the decrypted secrets before the deploy changes the VM.
- `SLACK_WEBHOOK_URL` (optional). It turns on Slack notifications.
- `SMTP_PASSWORD` (optional).

Set the password from standard input with `spinup secrets set --env staging GRAFANA_ADMIN_PASSWORD`.
Deploy the changed secret with `spinup deploy --env staging`.
If Grafana remains running after deployment, restart it on the VM:

```sh
docker compose -p <app>-monitoring -f /opt/<app>/monitoring/compose.monitoring.yml restart grafana
```

Replace `<app>` with the app name.
On each start, the entrypoint waits for Grafana to become healthy.
It then resets the admin password from `GRAFANA_ADMIN_PASSWORD`, including when the database already exists.
The reset uses the server configuration and data directory.
The CLI receives the password through standard input. Password characters cannot become shell commands.

The entrypoint checks whether the CLI supports `--password-from-stdin`.
If this option is absent, Grafana starts with a manual reset message.
The secret then sets the password only when Grafana creates its database.
For a manual reset, sign in with the existing admin password.
Change the admin password in Grafana to match `GRAFANA_ADMIN_PASSWORD`.
If an automatic reset fails, the entrypoint stops Grafana and reports the failure.

If you set no channel, Grafana still shows the alerts, but it sends nothing.
Point the heartbeat at an external service, for example a healthchecks.io check with a 5-minute period.
A down VM cannot send an alert, so the missing heartbeat is the alert.

## Dashboards

The folder Stack holds spinup's dashboards: HTTP RED, Go runtime, Caddy, Backups, and Deploys.
In the profile `full`, it also holds the vendored Node Exporter Full, cAdvisor, and PostgreSQL dashboards.
The folder Custom holds your own dashboards from `monitoring/grafana/dashboards/custom/`.

- HTTP RED reads `http_server_request_duration_seconds`, the kit metric for API requests. It does not cover web requests.
- Backups shows the backup age, size, and verification results from the metrics file on the VM.
  Both profiles include these panels. The profile `full` also includes panels for Postgres archiver metrics.
- Deploys shows the Grafana annotations with the tag `deploy`.
- Go runtime reads the kit runtime metrics (`go_goroutine_count`, `go_memory_used_bytes`, and the others). `telemetry.Setup` starts them.
- Caddy reads the Caddy metrics on `caddy:2020`. The deploy Caddyfile serves them there, and it does not publish the port.

## Backup metrics

Alloy mounts `/opt/<app>/backups/metrics` as a directory with read-only access.
Its Unix exporter enables only the `textfile` collector in both `lite` and `full`.
Alloy scrapes the exporter every 30 seconds with `job="backups"`.
It sends the samples to Prometheus through remote write.

The CLI and the scheduled script update `backup.prom` with an atomic rename.
Both writers hold `/opt/<app>/backups/metrics/.lock` through `flock` during each update.
Each update preserves other metrics and their metadata.
A failed metrics update prints a warning. It does not fail a successful backup.

| Metric | Meaning |
| --- | --- |
| `backup_last_success_timestamp_seconds{kind}` | The completion time of the last successful backup of this kind. |
| `backup_last_size_bytes{kind}` | The size of that backup in bytes. |
| `backup_last_verify_timestamp_seconds` | The completion time of the last verification. |
| `backup_last_verify_ok` | One for success. Zero for failure. |
| `backup_last_verify_rto_seconds` | The measured duration of the verification. |

The recording rule calculates `backup_last_age_seconds` from the newest success timestamp.
`BackupStale` fires when this age exceeds 26 hours for 15 minutes.
`BackupVerifyFailed` fires when the last verification failed.
Before the first backup, the age metric has no sample.
`BackupStale` also fires when no backup success metric exists for 15 minutes.
This includes a missing metrics file and a VM that has never completed a backup.

Scheduled verification checks the repository or the dump contents.
Its duration measures that check. It does not measure a full restore.
Use `spinup backup verify` for a full restore test.
See [Backup schedule](commands/backup-schedule.md) for the systemd timers.
