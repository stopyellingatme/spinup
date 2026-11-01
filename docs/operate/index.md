---
title: Operate
nav_order: 5
has_children: true
has_toc: false
---

# Operate

Day-2 work starts after the first deploy. You read logs, get alerts, take backups, and patch the server.
Every task here is one `spinup` command. Add `--env production` to run it against your VM.

```sh
spinup monitor logs --env production --service todo-api --since 1h
spinup backup now --env production
spinup maintenance vuln-scan --env production
```

## I want to ...

| I want to ... | Read | Command |
| --- | --- | --- |
| See my logs | [Monitoring](../monitoring.md#see-logs-and-traces) | `spinup monitor logs --env production` |
| See a trace | [Monitoring](../monitoring.md#see-logs-and-traces) | `spinup monitor trace --env production <trace-id>` |
| Open Grafana | [Monitoring](../monitoring.md#open-grafana) | `spinup monitor open --env production` |
| Get alerts in Slack, email, or ntfy | [Monitoring](../monitoring.md#send-alerts) | `spinup secrets set --env production SLACK_WEBHOOK_URL` |
| Take a backup | [Backups with pg_dump](../backups-pgdump.md) or [Backups with WAL-G](../backups-walg.md) | `spinup backup now --env production` |
| Restore a backup | [Backups with pg_dump](../backups-pgdump.md#restore) or [Backups with WAL-G](../backups-walg.md#restore-to-a-point-in-time) | `spinup backup restore --env production --into new` |
| Prove a backup works | [Backups with WAL-G](../backups-walg.md#verify-backups) or [Backups with pg_dump](../backups-pgdump.md#verify-a-dump) | `spinup backup verify --env production` |
| Run backups on a schedule | [Backup schedule](../commands/backup-schedule.md) | `spinup backup schedule --env production` |
| Upgrade PostgreSQL | [Upgrade PostgreSQL](../upgrading.md#upgrade-postgresql) | `spinup maintenance pg-minor-upgrade --env production` |
| Scan for vulnerabilities | [Maintenance](../commands/maintenance.md) | `spinup maintenance vuln-scan --env production` |
| Patch the server | [Maintenance](../commands/maintenance.md) | `spinup maintenance patch --env production --plan` |
| Know what data leaves my app | [Telemetry and data handling](../data-handling.md) | `grep -rn OTEL_EXPORTER_OTLP_ENDPOINT deploy/compose.yml` |
| Move my app to a new spinup version | [Upgrade](../upgrading.md) | `spinup upgrade --plan` |

## Pages in this section

| Page | What it covers |
| --- | --- |
| [Monitoring](../monitoring.md) | Grafana, logs, traces, alerts, and dashboards. |
| [Backups with pg_dump](../backups-pgdump.md) | The simple backend. Dumps stay on the VM. |
| [Backups with WAL-G](../backups-walg.md) | Point-in-time recovery to S3-compatible storage. |
| [Telemetry and data handling](../data-handling.md) | What the app sends, what is redacted, and how to turn it off. |

## Which backup backend?

The default backend is `pgbackrest`. If you set no `backend` in `spinup.toml`, you get pgBackRest.
You choose another backend with the `backend` key in `[backups]`.

| Backend | Point-in-time recovery | Where backups live | Pick it when |
| --- | --- | --- | --- |
| `pgbackrest` (default) | Yes | S3-compatible storage | You want the default. See [backup](../commands/backup.md). |
| `walg` | Yes | S3-compatible storage | You prefer WAL-G. See [Backups with WAL-G](../backups-walg.md). |
| `pgdump` | No | The VM disk | You want the simplest setup and accept the limits. See [Backups with pg_dump](../backups-pgdump.md). |

## See also

- [Deploy](../deploy/index.md) for the first deploy and its environments.
- [Command guides](../commands/index.md) for every command in detail.
- [Troubleshooting](../troubleshooting.md) for common errors.
