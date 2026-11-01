---
title: Backups with WAL-G
parent: Operate
nav_order: 3
---

# Backups with WAL-G

WAL-G is a backup backend that archives PostgreSQL continuously to S3-compatible storage.
It lets you restore to any moment, such as 04:00 yesterday.

```toml
[backups]
backend = "walg"
retention = "4"
```

<!-- doctest: skip -->
```console
$ spinup backup now --env production --type full
...
$ spinup backup restore --env production --into new --time 2026-10-15T04:00:00Z
...
```

## Which backend is the default?

`pgbackrest` is the default. If `[backups] backend` is empty, spinup uses pgBackRest.
You choose WAL-G, or `pgdump`, with the `backend` key. The values are `pgbackrest`, `walg`, and `pgdump`.
The same `spinup backup` commands work with all three.

| | `walg` | `pgbackrest` (default) | `pgdump` |
| --- | --- | --- | --- |
| Point-in-time recovery | Yes | Yes | No |
| Storage | S3-compatible | S3-compatible | The VM disk |
| Encryption | `WALG_LIBSODIUM_KEY` | `PGBACKREST_REPO1_CIPHER_PASS` | None |
| Read more | This page | [backup](commands/backup.md) | [Backups with pg_dump](backups-pgdump.md) |

## Turn it on

1. Set `backend = "walg"` in `[backups]`. Add the `backups` module if the app lacks it.
2. Set the storage secrets (next section).
3. Deploy: `spinup deploy --env production`.

The `backups` module generates one Compose override for each archive backend. A deploy loads only the one for your backend.

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ ls todo/deploy/compose.backups*.yml
todo/deploy/compose.backups-walg.yml
todo/deploy/compose.backups.yml
```

The PostgreSQL image already contains WAL-G. The version comes from the pinned stack, which `spinup version` and the [pinned stack](site/stack.md) page show.

## Set the storage secrets

Each secret is a name. You pipe the value in on standard input, so it stays out of your shell history:

<!-- doctest: skip -->
```console
$ printf '%s' 's3://backups/todo' | spinup secrets set --env production WALG_S3_PREFIX
$ printf '%s' 'AKIA...' | spinup secrets set --env production AWS_ACCESS_KEY_ID
$ printf '%s' '...' | spinup secrets set --env production AWS_SECRET_ACCESS_KEY
$ openssl rand -base64 32 | spinup secrets set --env production WALG_LIBSODIUM_KEY
$ spinup secrets push --env production
```

| Secret | Meaning | Example |
| --- | --- | --- |
| `WALG_S3_PREFIX` | The bucket and prefix. | `s3://backups/todo` |
| `AWS_ACCESS_KEY_ID` | The access key. | `AKIA...` |
| `AWS_SECRET_ACCESS_KEY` | The secret key. | |
| `WALG_LIBSODIUM_KEY` | The key for client-side encryption (required). | A 32-byte key |
| `AWS_ENDPOINT` | The endpoint, for compatible storage only. | `https://fsn1.your-objectstorage.com` |
| `AWS_REGION` | The region. | `eu-central-1` |
| `AWS_S3_FORCE_PATH_STYLE` | Path-style requests, for storage without virtual-host buckets. | `true` |

{: .warning }
Keep a copy of `WALG_LIBSODIUM_KEY` away from the VM. A restore needs this key and the storage credentials. Without the key, nobody can read the backups.

The secrets reach the container at run time. They never appear in command arguments, the image, or the generated Compose files.

## Back up, list, and prune

<!-- doctest: skip -->
```console
$ spinup backup now --env production --type full
...
$ spinup backup list --env production
...
$ spinup backup prune --env production --plan
...
```

| Type | What WAL-G does |
| --- | --- |
| `full` | A complete base backup. |
| `diff` or `incr` | A delta backup, only when `WALG_DELTA_MAX_STEPS` allows it. Otherwise a full backup. |

`backup now` makes a `diff` backup unless you pass `--type`. Set `WALG_DELTA_MAX_STEPS` like any other secret to allow delta backups:

<!-- doctest: skip -->
```console
$ printf '%s' '6' | spinup secrets set --env production WALG_DELTA_MAX_STEPS
$ spinup secrets push --env production
```

WAL-G lists a delta backup as `incr`.

Retention keeps the newest `retention` full backups (default `"4"`) and the delta backups that depend on them.
WAL-G continues to archive WAL between backups, so you can restore to any time after the oldest kept backup.

## Restore to a point in time

To recover to 04:00 UTC yesterday, restore into a new database first and check the data:

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into new --time 2026-10-15T04:00:00Z
Restored database at 127.0.0.1:5433 on the VM
...
```

The time uses RFC 3339 format. spinup picks the newest backup finished before that time and replays WAL up to it.
The storage must hold every WAL segment from that backup to the time.
Without `--time`, the restore stops when the chosen backup is consistent. It does not replay later WAL.
Select a backup by name with `--label` from `backup list`. Do not use `--label` and `--time` together.

Connect through the SSH tunnel that the command prints. When the data is right, replace the live database:

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into in-place --time 2026-10-15T04:00:00Z --plan
...
$ spinup backup restore --env production --into in-place --time 2026-10-15T04:00:00Z --i-know
...
```

{: .warning }
`--into in-place` stops the app and replaces the data directory. Read the `--plan` output first.

An in-place restore uses the deploy lock, the maintenance window, and a safety backup.
It keeps the old data at `$PGDATA.spinup-before-restore`. Check that directory, then remove it by hand.
A second in-place restore refuses to run while that directory exists. It also refuses symbolic links at either path.
See [backup restore](commands/backup-restore.md) for the flags and the failure messages.

## Verify backups

`backup verify` restores a backup into a throwaway container with no published port. It waits for recovery and queries the table `goose_db_version`.
It removes the container and its volume when it ends.

<!-- doctest: skip -->
```console
$ spinup backup verify --env production
...
$ spinup backup verify --env production --time 2026-10-15T04:00:00Z
...
```

Run a restore drill like this each month. A backup you have never restored is a guess.

## Scheduled backups and metrics

`spinup backup schedule --env production` installs systemd timers. They take full and diff backups and verify them.
See [backup schedule](commands/backup-schedule.md) for the calendars.

Every backend writes the same metrics file after a backup or a verification. The Backups dashboard and the alerts read it.
`BackupStale` fires when the newest success is older than 26 hours. `BackupVerifyFailed` fires when the last verification failed.
See [Backup metrics](monitoring.md#backup-metrics) for the metric names.
A scheduled verify checks the repository. It does not time a full restore, so use `backup verify` for that.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| Restore cannot find WAL. | The storage lacks a WAL segment. | Pick an earlier `--time`, or a `--label`. |
| Restore cannot decrypt. | `WALG_LIBSODIUM_KEY` differs from the key used for the backup. | Set the original key. |
| In-place restore refuses to run. | `$PGDATA.spinup-before-restore` exists. | Inspect it, then remove it. |
| `backup now` fails with an S3 error. | A storage secret is wrong or missing. | Reset it with `spinup secrets set`, then `secrets push`. |

## See also

- [Backups with pg_dump](backups-pgdump.md) for the simple backend.
- [backup](commands/backup.md), [backup restore](commands/backup-restore.md), [backup schedule](commands/backup-schedule.md).
- Flag reference: [backup now](site/commands/backup-now.md), [backup restore](site/commands/backup-restore.md), [backup verify](site/commands/backup-verify.md), [secrets set](site/commands/secrets-set.md).
- [Monitoring](monitoring.md) for the backup alerts.
