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

Backups need the `backups` module and monitoring needs the `monitoring` module. A new app has neither by default. Add one with `spinup module add <name>`.

| I want to ... | Read | Command |
| --- | --- | --- |
| See my logs | [Monitoring](../monitoring.md#see-logs-and-traces) | `spinup monitor logs --env production` |
| See a trace | [Monitoring](../monitoring.md#see-logs-and-traces) | `spinup monitor trace --env production <trace-id>` |
| Open Grafana | [Monitoring](../monitoring.md#open-grafana) | `spinup monitor open --env production` |
| Get alerts in Slack, email, or ntfy | [Monitoring](../monitoring.md#send-alerts) | `spinup secrets set --env production SLACK_WEBHOOK_URL < webhook.txt` |
| Take a backup | [Backups with pgBackRest](../commands/backup.md#set-up-pgbackrest-backups), [pg_dump](../backups-pgdump.md), or [WAL-G](../backups-walg.md) | `spinup backup now --env production` |
| Restore a backup | [backup restore](../commands/backup-restore.md), [pg_dump](../backups-pgdump.md#restore), or [WAL-G](../backups-walg.md#restore-to-a-point-in-time) | `spinup backup restore --env production --into new` |
| Recover after I lose the VM | [Recover after you lose the VM](../commands/backup-restore.md#recover-after-you-lose-the-vm) | `spinup backup list --env production` |
| Prove a backup works | [backup restore](../commands/backup-restore.md#verify-a-backup), [WAL-G](../backups-walg.md#verify-backups), or [pg_dump](../backups-pgdump.md#verify-a-dump) | `spinup backup verify --env production` |
| Run backups on a schedule | [Backup schedule](../commands/backup-schedule.md) | `spinup backup schedule --env production` |
| Go back to the last release | [rollback](../commands/rollback.md) | `spinup rollback --env production` |
| See the live release and the deploy lock | [deploy](../commands/deploy.md#if-a-deploy-stops) | `spinup deploy status --env production` |
| Check the VM for unsafe settings | [server](../commands/server.md#audit-the-vm) | `spinup server audit --env production` |
| Check the VM health | [Doctor checks](../reference/doctor-checks.md#remote-checks) | `spinup doctor --remote --env production` |
| Check the certificate | [maintenance](../commands/maintenance.md#check-the-certificate) | `spinup maintenance certs --env production` |
| Upgrade PostgreSQL | [Upgrade PostgreSQL](../upgrading.md#upgrade-postgresql) | `spinup maintenance pg-minor-upgrade --env production` |
| Scan for vulnerabilities | [Maintenance](../commands/maintenance.md) | `spinup maintenance vuln-scan --env production` |
| Patch the server | [Maintenance](../commands/maintenance.md) | `spinup maintenance patch --env production --plan` |
| Know what data leaves my app | [Telemetry and data handling](../data-handling.md) | `grep -rn OTEL_EXPORTER_OTLP_ENDPOINT deploy/compose.yml` |
| Move my app to a new spinup version | [Upgrade](../upgrading.md) | `spinup upgrade --plan` |

## Pages in this section

| Page | What it covers |
| --- | --- |
| [Monitoring](../monitoring.md) | Grafana, logs, traces, alerts, and dashboards. |
| [backup](../commands/backup.md#set-up-pgbackrest-backups) | The default backend, pgBackRest. Set up, back up, and check. |
| [backup restore](../commands/backup-restore.md) | Verify, restore, and recover after you lose the VM. |
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
