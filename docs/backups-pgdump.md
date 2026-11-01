---
title: Backups with pg_dump
parent: Operate
nav_order: 2
---

# Backups with pg_dump

The `pgdump` backend is the simplest backup. It saves a full copy of your database to the VM disk with `pg_dump`.
Use it for a small app, a staging environment, or a first deploy. Move to [WAL-G](backups-walg.md) or pgBackRest when you need more.

```toml
[backups]
backend = "pgdump"
retention = "7"
```

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd todo
$ ls deploy
...
```

<!-- doctest: skip -->
```console
$ spinup backup now --env production
...
$ spinup backup list --env production
...
```

## When to use pg_dump

| | `pgdump` | `walg` | `pgbackrest` (default) |
| --- | --- | --- | --- |
| Point-in-time recovery | No | Yes | Yes |
| Backup type | Always full | Full and delta | Full, differential, WAL |
| Where backups live | The VM disk | S3-compatible storage | S3-compatible storage |
| Survives loss of the VM | No, unless you copy dumps off | Yes | Yes |
| Setup | One key in `spinup.toml` | Storage secrets | Storage secrets |

{: .warning }
Dumps stay on the VM. If the VM is lost, the dumps are lost with it. Copy them to other storage on a schedule.

If you set no `backend`, spinup uses `pgbackrest`. `pgdump` is opt-in.

## Turn it on

1. Add the `backups` module if the app does not have it: `spinup module add backups --yes --non-interactive`.
2. Set `backend = "pgdump"` in `[backups]`.
3. Deploy: `spinup deploy --env production`.

The deploy turns off WAL archiving for pgdump, because a dump has no archive repository.
Deploy again after you change the backend, so PostgreSQL picks up the new configuration.

`retention` is a string in `spinup.toml` and counts dumps. The default is `"7"`. Pruning always keeps the newest dump.

## Take, list, and prune dumps

<!-- doctest: skip -->
```console
$ spinup backup now --env production
backup 20261015T120000ZF (full) of production: size 29.9 MiB, ...
$ spinup backup list --env production
...
$ spinup backup prune --env production --plan
...
$ spinup backup prune --env production
...
```

Every dump is full. Asking for `--type diff` or `--type incr` still makes a full dump.
`spinup deploy` takes a backup before each deploy. See [backup](commands/backup.md).

## Restore

You restore into a new database or over the live one.

### Into a new database

This keeps the live database as it is. Use it to inspect old data or to copy rows back.

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into new
Restored database at 127.0.0.1:5433 on the VM
...
OpenSSH tunnel: ssh -N -L 5433:127.0.0.1:5433 'deploy@vm.example'
```

Run the `ssh` command from the `OpenSSH tunnel` line. Then connect your database client to `127.0.0.1:5433` on your laptop.
The restored database listens only on the VM loopback address and uses trust authentication.
`spinup server ssh` cannot forward ports, so do not use the `Connect:` line. Use the `ssh` command.

To restore a chosen dump, pass its label from `backup list`:

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into new --label 20261015T120000ZF
```

`--time` does not work with pgdump. It returns an error that names the label option, because a dump has no point-in-time data.

### Over the live database

{: .warning }
`--into in-place` replaces the live database. spinup stops the app first, and the app is down until the restore ends.

Preview the steps first. The real restore needs `--i-know`.
It runs only inside your maintenance window, unless you add `--now`. A protected environment also asks you to type its name.

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into in-place --plan
...
$ spinup backup restore --env production --into in-place --label 20261015T120000ZF --i-know
...
```

The restore builds `<db>_restore` next to the live database and swaps the names. The old database stays as `<db>_old_<label>`.
After you check the app, drop the old database with the `dropdb` command that spinup prints.
If a rename fails, read the database names in the error before you restart the app.
See [backup restore](commands/backup-restore.md) for the safety backup and maintenance window.

## Verify a dump

`backup verify` restores a dump into a temporary container with no network. Then it reads the table `goose_db_version` to prove the data is there.
It removes the container when it ends. Pass `--label` to check one dump. Without it, spinup checks the newest.

<!-- doctest: skip -->
```console
$ spinup backup verify --env production
backup 20261015T120000ZF: RTO 3s, OK true
```

A failed check exits with code 1 and records `backup_last_verify_ok 0` for the `BackupVerifyFailed` alert.

## Copy dumps off the VM

Dumps live in `/opt/todo/backups/pgdump/`, one `<label>.dump` file each. The files use mode `0600`.
Copy them with `rsync` from a machine you trust:

```sh
rsync -av deploy@vm.example:/opt/todo/backups/pgdump/ ./todo-dumps/
```

Run this from cron or CI to get an off-site copy.

## Reference

| Item | Value |
| --- | --- |
| Backend key | `backend = "pgdump"` in `[backups]` |
| Retention key | `retention = "7"` (a string, counts dumps) |
| Dump format | `pg_dump -Fc` (custom format) |
| Label | UTC time, such as `20261015T120000ZF` (`F` means full) |
| Directory | `/opt/<app>/backups/pgdump/`, mode `0700` |
| Port for `--into new` | `127.0.0.1:5433` on the VM |

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `select pgdump target: logical dumps do not support point-in-time recovery` | You passed `--time`. | Pass `--label` from `backup list`, or omit the flag for the newest dump. |
| `backup restore` refuses an existing `<db>_old_<label>`. | An earlier in-place restore of the same label left it. | Inspect it, then drop it. |
| `BackupStale` fires. | No dump in 26 hours. | Run `spinup backup schedule --env production` for automatic dumps. See [Backup metrics](monitoring.md#backup-metrics). |
| `retention must be a positive integer` | `retention` is not a whole number above zero. | Set `retention = "7"`. |

## See also

- [Backups with WAL-G](backups-walg.md) for point-in-time recovery.
- [backup](commands/backup.md), [backup restore](commands/backup-restore.md), and [backup schedule](commands/backup-schedule.md).
- Flag reference: [backup now](site/commands/backup-now.md), [backup restore](site/commands/backup-restore.md), [backup verify](site/commands/backup-verify.md), [backup prune](site/commands/backup-prune.md).
- [Monitoring](monitoring.md) for the backup alerts.
