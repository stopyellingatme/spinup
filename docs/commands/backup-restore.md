# Verify, restore, and prune backups

Select the environment with `--env`.
The `[backups] backend` value selects the backend.
The default backend is `pgbackrest`.
The `backups` module is required.

`backup verify` restores a backup into a temporary container.
It waits for recovery and checks `goose_db_version`.
It prints the label, the recovery time, and the result.
It removes the container and its volume after success or failure.
A verification failure returns exit code 1.
Use `--json` for the result as JSON.
Verification also records backup metrics on the VM.

Select a backup with `--label`.
For pgBackRest, select a recovery time with `--time` in RFC 3339 format.
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
Plan: Take a safety backup
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
The command takes a full safety backup before it stops either color.
Use `--no-safety-backup` to skip this backup.

The command stops the applications, restores PostgreSQL, and runs the migrations from the live release.
It starts the live color and checks its health and version.
If a step fails, the command prints the safety label and a recovery command.
It releases the lock after success or failure.

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
