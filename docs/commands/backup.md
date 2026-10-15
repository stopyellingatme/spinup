# Backups

`spinup backup` backs up the production database with pgBackRest.
pgBackRest is the default backup backend (`docs/adr/0004-backup-backend.md`).
It writes full, differential, and WAL backups to S3-compatible storage, and it encrypts the repository.
Select the environment with `--env`.
Select another project with `--dir`.

The `backups` module adds the files.
It requires the `deploy` module.

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd todo
$ ls deploy/pgbackrest
pgbackrest.conf
$ spinup backup now
Error: select backup environment: --env is missing
  Try: pass --env <environment>
[exit 1]
```

## Files

| File | Class | Purpose |
| --- | --- | --- |
| `deploy/pgbackrest/pgbackrest.conf` | managed | The repository settings, the encryption type, the retention, and the stanza. |
| `deploy/compose.backups.yml` | managed | The override of `compose.edge.yml` for the `postgres` service. |

`compose.backups.yml` changes only the `postgres` service:

- It sets `archive_mode=on` and `archive_command='pgbackrest --stanza=<app> archive-push %p'`.
- It sets `archive_timeout=300`. A quiet database loses at most 5 minutes of changes.
- It mounts `/opt/<app>/backups` at `/var/lib/pgbackrest`. This directory holds `pgbackrest.conf` and the WAL spool.
- It adds the network `<app>-backup`. pgBackRest uses it to reach the S3 endpoint. The `<app>-db` network stays internal.
- Before the server starts, it exports each `/run/secrets/PGBACKREST_*` file as an environment variable.

spinup deploy loads the override after `compose.edge.yml`:

<!-- doctest: skip -->
```console
$ docker compose -p todo -f compose.edge.yml -f compose.backups.yml up -d
```

## Secrets

The repository location and the keys are secrets.
They are never in `pgbackrest.conf`, in an image, or in `docker inspect`.
Add them to `secrets/<env>.enc.yaml`:

| Name | Value |
| --- | --- |
| `PGBACKREST_REPO1_S3_ENDPOINT` | The S3 host, for example `s3.eu-central-1.amazonaws.com` |
| `PGBACKREST_REPO1_S3_BUCKET` | The bucket |
| `PGBACKREST_REPO1_S3_REGION` | The region, for example `eu-central-1` |
| `PGBACKREST_REPO1_S3_KEY` | The access key ID |
| `PGBACKREST_REPO1_S3_KEY_SECRET` | The secret access key |
| `PGBACKREST_REPO1_CIPHER_PASS` | The passphrase of the `aes-256-cbc` repository encryption |

Keep a copy of `PGBACKREST_REPO1_CIPHER_PASS` outside the VM and outside the repository.
Without it, nobody can restore a backup.
Each other `PGBACKREST_*` secret also becomes a pgBackRest option.
For example, `PGBACKREST_REPO1_S3_URI_STYLE=path` selects path-style requests for storage without virtual-host buckets.

<!-- doctest: skip -->
```console
$ printf '%s' 'backups-todo' | spinup secrets set --env production PGBACKREST_REPO1_S3_BUCKET
$ spinup secrets push --env production
```

## Back up now

`backup now` takes one backup and prints its label, size, stored size, and duration.
The default type is `diff`.
pgBackRest takes a full backup when the repository has no full backup.

<!-- doctest: skip -->
```console
$ spinup backup now --env production --type full
backup 20261015-082351F (full) of production: size 29.9 MiB, stored 3.6 MiB, took 1s
```

Each run uploads `deploy/pgbackrest/pgbackrest.conf` to `/opt/<app>/backups/pgbackrest.conf`.
The first run creates the stanza with `pgbackrest stanza-create`.
spinup runs pgBackRest in the `postgres` container through SSH:

<!-- doctest: skip -->
```console
$ docker compose -p todo exec -T postgres sh -c '<load PGBACKREST_* secrets>; exec pgbackrest "$@"' pgbackrest --stanza=todo --type=diff backup
```

The progress of pgBackRest goes to stderr.
`--json` prints the result as JSON on stdout.

## List and status

`backup list` prints each backup, oldest first: the label, the type, the finish time, the size, and the stored size.
`backup status` prints the stanza status, the encryption type, the last backup, and the WAL range of the archive.
Both commands read `pgbackrest info --output=json`. They change nothing.

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

## Retention

pgBackRest keeps 4 full backups by default, with their differential backups and their WAL.
Set another number in `spinup.toml`:

```toml
[backups]
retention = "8"
```

spinup writes the value into `repo1-retention-full` when it uploads `pgbackrest.conf`.
Each backup removes the backups that the retention no longer keeps.

## Backup before each deploy

`spinup deploy` runs the pre-deploy hook `backup`.
The hook takes a `diff` backup before each deploy.

- A project without the `backups` module skips the hook.
- The first deploy skips the backup, because the database does not run yet.
- If the backup fails, the deploy stops before it changes the VM.

`spinup deploy --skip-backup` skips the backup only when each pending migration is safe.
A pending migration is a file in `api/db/migrations` with a version above the newest applied goose version on the VM.
A safe migration has a line that starts with `-- spinup:safe`, followed by an optional reason:

```sql
-- +goose Up
-- spinup:safe adds a nullable column
ALTER TABLE todos ADD COLUMN note text;
```

If a pending migration has no marker, the deploy fails with a `Try:` line.
Deploy without `--skip-backup`, or add the marker to a migration that keeps all data.

## Last backup age in the app

The kit package `backup` gives the age of the newest backup.
`backup.Handler` serves it as JSON at `/_deploy/backup`, for example `{"last_backup_age_seconds":3600}`.
The value is `null` when no backup exists.
The monitoring stack maps the value to a gauge, and alerts use it.

## Not in this version

`backup verify`, `backup restore`, and `backup expire` arrive in milestone M7.
The `Backend` interface already has these operations.
