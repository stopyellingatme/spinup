---
title: backup restore — verify, restore, and prune backups
parent: Command guides
---

# backup restore: verify, restore, and prune backups

Three commands protect you after a backup exists.
`backup verify` proves a backup restores. `backup restore` brings it back. `backup prune` removes old backups.

```sh
spinup backup verify --env production                  # prove the newest backup restores
spinup backup restore --env production --into new      # bring it back beside the live database
spinup backup prune --env production                   # apply the retention now
```

The `[backups] backend` key in `spinup.toml` selects the backend (`pgbackrest`, `walg`, or `pgdump`).
All three commands need `--env` and the `backups` module.
They also work on a replacement VM that has no earlier backup of its own.

## Choose the backup

`verify` and `restore` take the newest backup unless you choose one.

| Flag | Meaning | Example |
| --- | --- | --- |
| `--label` | Use one backup. Find labels with `backup list`. | `spinup backup verify --env production --label 20261015-082351F` |
| `--time` | Restore to this moment (RFC 3339, UTC). Needs a backup that finished before it. | `spinup backup verify --env production --time 2026-10-15T08:24:00Z` |

Do not pass both flags.
The `pgdump` backend has no point-in-time data, so `--time` fails there. Use `--label` or the newest dump.

## Verify a backup

`backup verify` restores the backup into a temporary container, waits for recovery, and reads the migration table.
It prints the label, the time the restore took (RTO), and the result.
It removes the container and its volume when it finishes, even after a failure.
Your live database is never touched.

<!-- doctest: skip -->
```console
$ spinup backup verify --env production
backup 20261015-082351F: RTO 3s, OK true
$ spinup backup verify --env production --time 2026-10-15T08:24:00Z --json
...
```

A failed verification exits with code 1, so a script or a CI job can stop on it.
Verification records its result in the backup metrics. See [Monitoring](../monitoring.md).

## Restore into a separate database

`--into new` keeps the live database as it is.
It starts a second PostgreSQL on the VM and listens on port 5433 of the VM loopback address only.
Use it to look at old data or to copy rows back.

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into new --label 20261015-082351F
Restored database at 127.0.0.1:5433 on the VM
OpenSSH tunnel: ssh -N -L 5433:127.0.0.1:5433 'deploy@app.example.com'
```

Copy the printed `ssh -N -L` line to open a tunnel, then connect to `127.0.0.1:5433` on your computer.

## Replace the live database

{: .warning }
`--into in-place` replaces the live database and stops the app while it runs. Writes made after the backup are lost.

Follow these steps:

1. Run with `--plan` and read the steps. This needs no VM access.
2. Run with `--i-know` to accept the downtime. For a protected environment, type the environment name when prompted.
3. Check the app, then remove any retained old database (see the `pgdump` note below).

Step 1 on a new project looks like this:

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
...
Plan: Release the deploy lock
```

Step 2 runs the restore:

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --into in-place --label 20261015-082351F --i-know
...
```

Without `--i-know`, the command stops before it changes anything:

```console
$ spinup backup restore --env staging --into in-place
Error: restore the live database: --i-know is required
  Try: inspect --plan, then pass --i-know
[exit 1]
```

| Flag | Meaning | Example |
| --- | --- | --- |
| `--i-know` | Accept the downtime and the loss of newer writes. Required. | `--into in-place --i-know` |
| `--plan` | Print the steps. Change nothing. It needs no `--i-know`. `--dry-run` does the same. | `--into in-place --plan` |
| `--now` | Run outside the maintenance window. The window is in UTC. | `--into in-place --i-know --now` |
| `--no-safety-backup` | Skip the full safety backup that runs first. | `--into in-place --i-know --no-safety-backup` |
| `--yes` | Confirm a protected environment in CI. Set `CI` too. | `CI=true spinup backup restore --env production --into in-place --i-know --yes` |
| `--confirm-env` | Type the protected environment name in the command, with no terminal. | `--into in-place --i-know --confirm-env production` |

What the command does, in order:

1. It takes the deploy lock. If another deploy holds it, the command exits with code 5.
2. It checks that the restore target exists.
3. It takes a full safety backup, so you can undo the restore. For pgBackRest, this backup keeps the target backup from expiring.
4. It stops `api` and `web`, restores the database, and starts PostgreSQL.
5. It runs the migrations of the live release, starts the live color, and runs the deploy smoke checks.
6. It releases the lock, also after a failure.

With `pgdump`, PostgreSQL keeps running.
spinup restores the dump into `<db>_restore`, then swaps the database names.
The previous database stays as `<db>_old_<label>` and uses disk space.
After you check the app, run the printed `dropdb` command on the VM.
Remove that database before you restore the same label again.

## If a restore fails

The error tells you the state of the app and the exact commands to run.
These rules apply:

- If the live database is untouched, the command restarts PostgreSQL and the live color for you.
- If the database could have changed, the command keeps the app stopped. The error says `api and web are STOPPED`.
- A failed pgBackRest restore can leave PostgreSQL stopped with a partly restored database. Inspect it before you start any service.
- If a safety backup exists, the error prints its label and the command that restores it.

The error also names the PostgreSQL state, and it ends with a `Try:` line.
For pgBackRest, it includes the `ERROR` and `WARN` lines of pgBackRest, with secrets removed.

## Remove old backups

`backup prune` applies the retention from `spinup.toml` and lists the backups that remain.
Every backup applies the retention too, so you need `prune` only after you lower `retention`.
With `--plan`, it removes nothing. For pgBackRest, it runs `expire --dry-run`. For the other backends, it only lists the backups.

<!-- doctest: skip -->
```console
$ spinup backup prune --env production --plan
...
Prune plan: no backups removed
20261015-082351F
20261015-082351F_20261015-082353D
$ spinup backup prune --env production
Backups remaining:
20261015-082351F
20261015-082351F_20261015-082353D
```

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `--i-know is required` | You ran an in-place restore without consent. | Run `--plan`, then add `--i-know`. |
| The command refuses to run | It is outside the maintenance window. | Wait for the window, or add `--now`. |
| Exit code 5 | A deploy holds the deploy lock. | Wait for the deploy, then run again. |
| pgBackRest error 051 on the safety backup | A replacement VM has a new database cluster that the repository does not know. | Inspect the live database. If you accept a restore without an undo point, add `--no-safety-backup`. spinup never skips the safety backup for you. |
| `--time` fails with pgdump | A dump has no point-in-time data. | Use `--label` from `backup list`. |

## See also

- [backup](backup.md): take, list, and check backups
- [backup schedule](backup-schedule.md): verify on a timer
- [restore](restore.md): bring back a template file, not a database
- [Backups with WAL-G](../backups-walg.md) and [Backups with pg_dump](../backups-pgdump.md)
- Flag reference: [backup verify](../site/commands/backup-verify.md), [backup restore](../site/commands/backup-restore.md), [backup prune](../site/commands/backup-prune.md)
