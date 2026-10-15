# WAL-G backups

WAL-G is an optional backend for continuous PostgreSQL archiving and point-in-time recovery.
pgBackRest remains the default backend.

Select WAL-G in `spinup.toml`:

```toml
[backups]
backend = "walg"
retention = "4"
```

The generated PostgreSQL image contains WAL-G 3.0.9.
The Dockerfile selects the pinned asset for amd64 or arm64.
It verifies the SHA-256 checksum before installation.
The deployer loads `compose.backups-walg.yml` with `compose.edge.yml`.
It selects only the override for the configured backend.

Store these values with the existing secrets commands:

| Secret | Purpose |
|---|---|
| `WALG_S3_PREFIX` | The S3 bucket and prefix, such as `s3://backups/todo` |
| `AWS_ACCESS_KEY_ID` | The S3 access key |
| `AWS_SECRET_ACCESS_KEY` | The S3 secret key |
| `AWS_ENDPOINT` | The endpoint for compatible storage |
| `AWS_REGION` | The storage region |
| `AWS_S3_FORCE_PATH_STYLE` | The path style option for compatible storage |
| `WALG_LIBSODIUM_KEY` | The required key for client-side encryption |

WAL-G reads `WALG_*` and `AWS_*` files from `/run/secrets`.
Secret values enter the container environment at runtime.
They never enter command arguments, the image, or the generated Compose configuration.
Keep the encryption key separately from the VM.
A restore requires that key and the storage credentials.

Run the existing backup commands with `--env`.
The following examples require a configured VM:

<!-- doctest: skip -->
```console
$ spinup backup now --env production --type full
...
$ spinup backup list --env production
...
$ spinup backup verify --env production
...
$ spinup backup restore --env production --into new --time 2026-10-15T04:00:00Z
...
$ spinup backup prune --env production
...
```

A full backup runs `wal-g backup-push $PGDATA` with `WALG_DELTA_MAX_STEPS=0`.
Differential and incremental requests use WAL-G delta backups only when `WALG_DELTA_MAX_STEPS` enables them.
Without that setting, both requests create full backups.
WAL-G reports delta backups as `incr` through the common interface.
Retention runs `wal-g delete retain FULL <retention> --confirm`.

A restore accepts a label from `backup list` or a recovery time.
For a recovery time, the backend selects the newest backup completed before that time.
The restored database uses `wal-g wal-fetch` to obtain the required WAL segments.
A restore without a recovery time promotes when the selected backup becomes consistent.
The storage must contain every WAL segment needed for recovery.

Verification restores into a temporary container with an anonymous volume.
It uses the backup network without joining an application network.
It publishes no port.
It waits for recovery to finish and queries `goose_db_version`.
It removes its container and volume after success, failure, or cancellation.

A new restore keeps its container running on the VM at `127.0.0.1:5433`.
Use the tunnel instructions from the restore command to connect.
An in-place restore uses the existing confirmation, maintenance window, safety backup, and deploy lock.
It retains the previous data at `$PGDATA.spinup-before-restore`.
Inspect that directory before removing it.
A later in-place restore refuses to replace an existing retained directory.
The refusal preserves both the live data and the retained data.
The restore also refuses symbolic links at either path.

The scheduled script supports WAL-G backup and restore verification.
It writes the same atomic metrics file as the other backends.
A failed verification records `backup_last_verify_ok 0`.
A failed backup preserves the previous success metrics.

Inspect the available restore flags without a VM:

```console
$ spinup backup restore --help
...
$ spinup backup verify --help
...
```
