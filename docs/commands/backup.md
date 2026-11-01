---
title: backup — back up the database
parent: Command guides
---

# backup: back up the database

`spinup backup` takes, lists, checks, and removes backups of the production database.
It runs on the VM through SSH, so use it after your first deploy.

```sh
spinup backup now --env production
```

```console
$ spinup backup --help
...
Available Commands:
  list        List the backups in the repository
  now         Take a backup now and print its label, size, and duration
  prune       Apply backup retention and list the remaining backups
  restore     Restore a backup into a new or live database
  schedule    Install or remove backup timers on the VM
  status      Show the repository status and the last backup
  verify      Restore a backup in isolation and check the database
...
```

Every `backup` command needs `--env`, for example `--env production`.
Use `--dir` (or `-C`) to run the command for a project in another directory:

```sh
spinup backup list --env production -C ~/code/todo
```

## Pick a command

| I want to ... | Run | Read |
| --- | --- | --- |
| Take a backup now | `spinup backup now --env production` | [Take a backup](#take-a-backup) |
| See my backups | `spinup backup list --env production` | [List and check backups](#list-and-check-backups) |
| Check the repository | `spinup backup status --env production` | [List and check backups](#list-and-check-backups) |
| Prove a backup restores | `spinup backup verify --env production` | [backup restore](backup-restore.md) |
| Bring a backup back | `spinup backup restore --env production --into new` | [backup restore](backup-restore.md) |
| Remove old backups | `spinup backup prune --env production` | [backup restore](backup-restore.md#remove-old-backups) |
| Back up on a timer | `spinup backup schedule --env production` | [backup schedule](backup-schedule.md) |

## Choose a backend

The `backend` key in `spinup.toml` selects how spinup stores the backups.
The default is `pgbackrest`. The commands on this page work the same way for every backend.

| Backend | Good for | Point-in-time restore | Guide |
| --- | --- | --- | --- |
| `pgbackrest` (default) | Most production apps. Full, differential, and WAL backups in S3-compatible storage, encrypted. | Yes | This page |
| `walg` | The same storage model, with WAL-G. | Yes | [Backups with WAL-G](../backups-walg.md) |
| `pgdump` | A small app or a staging environment. Full dumps on the VM disk, seven kept. | No | [Backups with pg_dump](../backups-pgdump.md) |

```toml
[backups]
backend = "pgbackrest"
```

## Set up pgBackRest backups

Add the `backups` module to an app that has the `deploy` module.
For an existing app, run `spinup module add backups` and merge the branch it prints. See [module](module.md).
The module adds the files in the table below.

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd todo
$ ls deploy/pgbackrest
pgbackrest.conf
```

| File | Purpose |
| --- | --- |
| `deploy/pgbackrest/pgbackrest.conf` | The repository settings, the encryption type, the retention, and the stanza name. |
| `deploy/compose.backups.yml` | Turns on WAL archiving for the `postgres` service. `spinup deploy` loads it after `compose.edge.yml`. |

WAL archiving ships the write-ahead log to storage at least every five minutes.
A quiet database therefore loses at most five minutes of changes if the VM dies.

Next, put the storage keys in the encrypted secrets file.
They are never in `pgbackrest.conf`, in an image, or in `docker inspect`.

| Secret name | Value |
| --- | --- |
| `PGBACKREST_REPO1_S3_ENDPOINT` | The S3 host, for example `s3.eu-central-1.amazonaws.com` |
| `PGBACKREST_REPO1_S3_BUCKET` | The bucket |
| `PGBACKREST_REPO1_S3_REGION` | The region, for example `eu-central-1` |
| `PGBACKREST_REPO1_S3_KEY` | The access key ID |
| `PGBACKREST_REPO1_S3_KEY_SECRET` | The secret access key |
| `PGBACKREST_REPO1_CIPHER_PASS` | The passphrase for the `aes-256-cbc` repository encryption |

<!-- doctest: skip -->
```console
$ printf '%s' 'backups-todo' | spinup secrets set --env production PGBACKREST_REPO1_S3_BUCKET
$ spinup secrets push --env production
```

{: .warning }
Keep a copy of `PGBACKREST_REPO1_CIPHER_PASS` outside the VM and outside the repository. Without it, nobody can restore a backup.

Any other `PGBACKREST_*` secret becomes a pgBackRest option.
For example, `PGBACKREST_REPO1_S3_URI_STYLE=path` selects path-style requests for storage that has no virtual-host buckets.

## Take a backup

`backup now` takes one backup and prints its label, size, stored size, and duration.
The default type is `diff`. If the repository has no full backup yet, pgBackRest takes a full backup.
The first run also creates the stanza. A stanza is the entry that pgBackRest keeps for your database in the repository.

<!-- doctest: skip -->
```console
$ spinup backup now --env production --type full
backup 20261015-082351F (full) of production: size 29.9 MiB, stored 3.6 MiB, took 1s
```

| Flag | Meaning | Example |
| --- | --- | --- |
| `--type` | `full`, `diff` (default), or `incr` | `spinup backup now --env production --type incr` |
| `--json` | Print the result as JSON on stdout | `spinup backup now --env production --json` |

Each run uploads `deploy/pgbackrest/pgbackrest.conf` to the VM first.
The progress of pgBackRest goes to stderr, so `--json` stays easy to parse.

## List and check backups

`backup list` prints each backup, oldest first, with its label, type, finish time, size, and stored size.
`backup status` prints the stanza status, the encryption type, the last backup, and the range of archived WAL.
If you installed [a schedule](backup-schedule.md), it also lists the timers and the backup metrics.
Both commands only read. They change nothing.

<!-- doctest: skip -->
```console
$ spinup backup list --env production
20261015-082351F  full  2026-10-15T08:23:52Z  size 29.9 MiB  stored 3.6 MiB
20261015-082351F_20261015-082353D  diff  2026-10-15T08:23:54Z  size 29.9 MiB  stored 408 B
$ spinup backup status --env production
stanza todo: ok (code 0)
cipher: aes-256-cbc
last backup: 20261015-082351F_20261015-082353D (diff), finished 2026-10-15T08:23:54Z
backups: 2
WAL archive: 000000010000000000000001 to 000000010000000000000005
```

Use `--json` for a script:

<!-- doctest: skip -->
```console
$ spinup backup list --env production --json
...
```

## Keep more or fewer backups

pgBackRest keeps 4 full backups by default, with their differential backups and their WAL.
Set another whole number from 1 to 999 in `spinup.toml`.
Each backup removes the backups that the retention no longer keeps.

```toml
[backups]
retention = "8"
```

To apply a new retention now, run `spinup backup prune`. See [backup restore](backup-restore.md#remove-old-backups).

## Back up before each deploy

`spinup deploy` takes a `diff` backup before it changes the VM.
If the backup fails, the deploy stops, so a bad deploy never starts without a restore point.

- An app without the `backups` module skips the backup.
- The first deploy skips the backup, because the database does not run yet.

To skip the backup, run `spinup deploy --skip-backup`.
This works only when every pending migration is safe.
A pending migration is a file in `api/db/migrations` that is newer than the newest migration the VM applied.
A safe migration has a line that starts with `-- spinup:safe`, followed by an optional reason:

```sql
-- +goose Up
-- spinup:safe adds a nullable column
ALTER TABLE todos ADD COLUMN note text;
```

If a pending migration has no marker, the deploy fails with a `Try:` line.
Deploy without `--skip-backup`, or add the marker to a migration that keeps all data.

## Check the age of the last backup

The app serves the age of its newest backup at `/_deploy/backup`:

```json
{"last_backup_age_seconds":3600}
```

The value is `null` when no backup exists.
The monitoring stack turns the value into a metric, and an alert fires when it grows too old.
See [Monitoring](../monitoring.md) for the backup metrics and alerts.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `select backup environment: --env is missing` | The command needs an environment. | Pass `--env production`. |
| `backup now` fails with an S3 error | A storage secret is wrong or missing. | Reset it with `spinup secrets set`, then run `spinup secrets push`. |
| `/opt/<app>/backups` does not exist | The VM is not set up. | Run `spinup server bootstrap --env production`. |

## See also

- [backup restore](backup-restore.md): verify, restore, and prune
- [backup schedule](backup-schedule.md): back up on a timer
- [Backups with WAL-G](../backups-walg.md) and [Backups with pg_dump](../backups-pgdump.md)
- [Monitoring](../monitoring.md): backup metrics and alerts
- Flag reference: [backup](../site/commands/backup.md), [backup now](../site/commands/backup-now.md), [backup list](../site/commands/backup-list.md), [backup status](../site/commands/backup-status.md)
