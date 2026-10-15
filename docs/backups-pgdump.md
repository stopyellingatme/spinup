# Logical backups with pgdump

The `pgdump` backend stores PostgreSQL dumps on the VM.
Each dump uses the custom format from `pg_dump -Fc`.
Every dump is full, including requests for `diff` or `incr`.
Dumps contain no WAL and support no point-in-time recovery.

Set these values in `spinup.toml`:

```toml
[backups]
backend = "pgdump"
retention = "7"
```

The default retention is seven dumps for the CLI and the scheduled jobs.
Pruning keeps the newest dumps and never removes the newest dump.
The `pgbackrest` backend remains the default when `backend` is empty.

This example creates the deployment files without a VM:

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd todo
$ test -f deploy/compose.edge.yml
$ spinup backup --help
...
```

The backups pack generates `deploy/compose.backups.yml` for pgBackRest.
When `[backups] backend = "pgdump"`, the deployer excludes this file from the release.
This keeps WAL archiving off because pgdump has no archive repository.
The scheduled script also excludes this file when it selects pgdump.
After changing the backend, deploy the app again to apply the PostgreSQL configuration.

## Storage

Dumps reside in `/opt/<app>/backups/pgdump/`.
A label has the UTC format `YYYYMMDDTHHMMSSZF`.
For example, `20261015T120000ZF.dump` identifies a full dump.
The backend writes `<label>.dump.tmp`, then renames it to `<label>.dump`.
Each dump has mode `0600`.
The directory has mode `0700`.
The listing excludes temporary files.

The backend uses the base Compose project `<app>`.
Its Compose file is `/opt/<app>/current/compose.edge.yml`.
The backend runs `pg_dump` as the `postgres` user inside the `postgres` container.
It reads `POSTGRES_DB` and `POSTGRES_USER` with `printenv` inside that container.
It gets the dump size and modification time with `stat` on the VM.

These dumps remain on the VM.
Copy them to separate storage for recovery after the loss of the VM.

## Verification

A zero target selects the newest dump.
A label selects that dump.
A time target returns an error with a `Try:` line.

Verification creates `<app>-verify-<label>` from the image of the running `postgres` service.
The container publishes no port and uses `--network none`.
The backend waits for PostgreSQL, then restores the dump with `pg_restore`.
It runs `SELECT count(*) FROM goose_db_version` after the restore.
The RTO measures the interval from restore start to the successful query.
Verification removes the container and its anonymous volume after success, failure, or cancellation.
Cleanup uses a fresh context with a timeout of 30 seconds.

## Restore destinations

A new restore leaves `<app>-restore-<label>` running on the VM.
Its port binds to `127.0.0.1:5433`.
The container joins the Docker bridge network.
It joins no app network.
The backend prints an SSH tunnel command with the configured host, user, and port.

For a host named `vm.example`, use this tunnel:

<!-- doctest: skip -->
```console
$ ssh -N -L 5433:127.0.0.1:5433 -- deploy@vm.example
```

Connect the database client to `127.0.0.1:5433` on your computer.
The restored container uses trust authentication.
Its published port is restricted to the VM loopback address.
The current `spinup server ssh` command accepts no forwarding flags or additional arguments.
Use the printed native SSH command for forwarding.

An in-place restore creates `<db>_restore` in the live PostgreSQL container.
It restores the dump before changing the live database.
It prevents new connections and terminates existing connections to the live database.
It renames `<db>` to `<db>_old_<label>`.
It then renames `<db>_restore` to `<db>`.
Each statement runs without a surrounding transaction.
The backend prints the command to drop the old database after you check the app.

The caller controls the deploy lock, confirmation, safety backup, and app shutdown.
The backend changes only the database.
An existing staging database or old database stops the restore before the live database changes.
If a rename fails, inspect the database names in the error before restarting the app.
