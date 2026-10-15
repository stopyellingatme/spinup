# Verify, restore, and prune backups

Select the environment with `--env`.
The `[backups] backend` value selects the backend.
The default backend is `pgbackrest`.
The `backups` module is required.

Each pgBackRest backend uploads `pgbackrest.conf` before its first pgBackRest command.
The upload applies the configured retention and uses mode 0600.
Restore, verification, listing, status, and pruning work on a replacement VM without a prior backup.
These operations do not create a stanza.
Only backup preparation can create a missing stanza.

If a command fails, the error includes the pgBackRest `ERROR` and `WARN` lines from stdout and stderr.
The remote runner redacts secrets before the command includes these lines.

`backup verify` restores a backup into a temporary container.
It waits for recovery and checks `goose_db_version`.
It prints the label, the recovery time, and the result.
It removes the container and its volume after success or failure.
A verification failure returns exit code 1.
Use `--json` for the result as JSON.
Verification records backup metrics after a restore attempt starts.
Invalid targets and failures before the restore attempt preserve the previous verification metrics.

Select a backup with `--label`.
For pgBackRest, select a recovery time with `--time` in RFC 3339 format.
The pgdump backend does not support point-in-time recovery.
With pgdump, use a label or select the latest dump.
Without these flags, the command selects the latest backup.
The flags are mutually exclusive.

<!-- doctest: skip -->
```console
$ spinup backup verify --env production
backup 20261015-082351F: RTO 3s, OK true
$ spinup backup verify --env production --time 2026-10-15T08:24:00Z --json
...
```

## Restore into a separate database

`--into new` preserves the live database.
The restored container uses the app's PostgreSQL image and the backup network.
It publishes port 5433 only on the VM's loopback address.
The command prints SSH commands for access.
Use the OpenSSH command until `server ssh` supports `-L`.

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into new --label 20261015-082351F
Restored database at 127.0.0.1:5433 on the VM
Connect: spinup server ssh --env production -L 5433:127.0.0.1:5433
OpenSSH tunnel: ssh -N -L 5433:127.0.0.1:5433 'deploy@app.example.com'
```

## Replace the live database

`--into in-place` requires `--i-know`.
For a protected environment, type the environment name when prompted.
For CI, use `--yes` with `CI` set.
Without a terminal, the command does not prompt.

The command refuses outside the configured maintenance window.
The window uses UTC.
Use `--now` to override the window.
Use `--plan` to inspect the steps without confirmation or changes.

```console
$ spinup new restore-demo --api-only --module example.com/restore-demo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd restore-demo
$ printf '\n[env.staging]\nhost = "127.0.0.1"\nuser = "deploy"\n' >> spinup.toml
$ spinup backup restore --env staging --into in-place --plan
Plan: Check --i-know, protected confirmation, and maintenance window
Plan: Take the deploy lock
Plan: Resolve and check the restore target
Plan: Take a safety backup
Plan: Re-check the restore target
Plan: Stop api and web of both colors
Plan: Stop postgres
Plan: Restore the live database
Plan: Start postgres
Plan: Run migrate with the live release
Plan: Start the live color
Plan: Run the deploy smoke checks
Plan: Release the deploy lock
$ spinup backup restore --env staging --into in-place
Error: restore the live database: --i-know is required
  Try: inspect --plan, then pass --i-know
[exit 1]
```

The command takes the deploy lock before any changes.
A held lock returns exit code 5 with the deploy lock message.
The command checks that the restore target exists before it takes the safety backup.
For a recovery time, a backup must finish before that time.
The command takes a full safety backup before it stops either color.
For pgBackRest, this backup uses `--no-expire-auto` to preserve the restore target.
The command checks the target again before it stops any service.
Use `--no-safety-backup` to skip the safety backup.

A replacement VM usually has a new database cluster with a different system identifier.
The repository does not know this cluster, so pgBackRest rejects its safety backup with error 051.
The command reports this failure before it stops any service.
It never skips the safety backup automatically.
After you inspect the live database, pass `--no-safety-backup` if you accept restoration without a safety backup.

The command stops the applications and restores the database.
For pgBackRest, it stops PostgreSQL, restores the live volume, and starts PostgreSQL.
It runs the migrations from the live release.
It starts the live color and checks its health and version.
It releases the lock after success or failure.

With pgdump, PostgreSQL continues to run during the restore.
The backend restores the dump into `<db>_restore` and swaps the database names.
It retains the previous database as `<db>_old_<label>`.
This database continues to use disk space.
After you check the application, run the printed `dropdb` command on the VM.
Before you restore the same label again, inspect and remove its retained database.

If a failure occurs after a stop, the command checks whether the live database could have changed.
If the database remains untouched, the command attempts to restart PostgreSQL first, then the live color.
If the database could have changed, the command keeps the applications stopped.
If the live color already started, the command attempts to stop it again.
The error states `api and web are STOPPED`, or reports an unconfirmed state if the stop fails.
The error also reports the PostgreSQL state.

A failed pgBackRest restore can leave PostgreSQL stopped with a partially restored database.
Inspect the database before you start the services.
The error includes the exact commands to start PostgreSQL and the live color on the VM.
With a safety backup, the command prints its label and the exact command to restore it.
With `--no-safety-backup`, the error still includes a `Try:` line and the commands to start the services.

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into in-place --label 20261015-082351F --i-know --now
...
```

## Apply retention

`backup prune` applies the configured retention and lists the remaining backups.
For pgBackRest, `--plan` runs `expire --dry-run` and lists the current backups.
For other backends, `--plan` lists the current backups without removing them.

<!-- doctest: skip -->
```console
$ spinup backup prune --env production --plan
...
$ spinup backup prune --env production
...
```
