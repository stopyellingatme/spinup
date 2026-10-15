# In-place PostgreSQL upgrades

Use this command for a major upgrade with `pg_upgrade --link`.
It retains the named volume and creates a new data directory on that volume.
The command is separate because the existing CLI has no flag registration hook.
The dump and restore command remains `maintenance pg-major-upgrade`.

```console
$ spinup maintenance pg-upgrade-in-place --help
...
```

Select the target major with `--to 17` or `--to 18`.
The target must exceed the current major.
The source major must have an image pin in the stack table.
The runtime user must use a non-root numeric `UID:GID`.
The command requires `PGDATA` inside a named PostgreSQL volume.

Use `--plan` to inspect the steps without VM access or changes.
Execution uses the existing maintenance window, protected confirmation, deploy lock, and lock heartbeat.
Use `--now` to override the window.
Supply `--i-know` to accept downtime.
For a protected environment in CI, also supply `--yes`.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-upgrade-in-place --env production --to 18 --plan
...
$ spinup maintenance pg-upgrade-in-place --env production --to 18 --i-know
...
```

The command builds a helper image on the VM.
The helper uses the target image pin and copies the source binaries from the source image pin.
It also copies the source libraries and shared PostgreSQL files.
It takes a full backup through the configured backend before downtime.
It saves the backup label, old image, Compose files, Dockerfile, and environment file.

The command stops `api` and `web` of both colors.
It then stops PostgreSQL.
The app remains unavailable until the migrations and smoke checks succeed.
The helper initializes `<major>/docker` on the same volume as the old directory.
It preserves the source encoding, locale, and checksum setting.
An existing target directory causes a refusal.

The helper runs `pg_upgrade --check --link` first.
Any check failure stops the operation before the link upgrade or Compose switch.

Before the link upgrade starts, a failure removes the target directory created by this operation.
The command checks the paths and ownership marker before removal.
It restarts the original PostgreSQL cluster and the live color.
It then releases the recovery directory, so you can correct the failure and retry.
If recovery fails, the command retains the recovery files and prints instructions for inspection.

The command then runs `pg_upgrade --link`.
It disables the old control file and removes the old PostgreSQL container without removing the volume.

The command changes the VM image and `PGDATA`, then starts PostgreSQL.
For PostgreSQL 18, `PGDATA` is `/var/lib/postgresql/18/docker`.
For PostgreSQL 17, it is `/var/lib/postgresql/data/17/docker`.
With pgBackRest, the command updates the VM configuration, runs `stanza-upgrade`, and takes a full backup.
It then runs `vacuumdb --all --analyze-in-stages`, migrates, and starts the live color.
The shared smoke checks verify the health paths and release version.

Never start the old cluster after the link upgrade.
The clusters share files.
Starting the old cluster can damage the upgraded database.
Before the link upgrade starts, the command replaces `rollback.sh` with a refusal script.
This guard also applies when the command fails during the link upgrade or after the new server starts.

Restore the safety backup into a new database instead.
Each failure after the link upgrade starts prints the exact command with its backup label and environment.
The command uses `--into new` to preserve both directories for inspection.
Keep the original major available when you restore a physical backup.
After you change the repository major, use the saved original configuration for that restore.

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --label 20261015T062420ZF --into new
...
```

Commit the printed repository changes before the next deploy.
Set `answers.db_version` in `spinup.lock` to the target major as a JSON string.
Render the deploy files with the printed `spinup restore` commands.
Set `volumes.postgres_data.external` to `true` and retain the original volume name in `deploy/compose.edge.yml`.
Set `services.postgres.environment.PGDATA` to the printed target path.
For pgBackRest, also render its configuration and retain the printed `pg1-path`.

The old directory remains until you request cleanup.
Cleanup requires a completed upgrade and the matching active volume, major, and data directory.
It also checks the repository major, external volume name, and `PGDATA`.
It removes only the old data directory.
It never removes the shared volume or starts the old cluster.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-upgrade-in-place --env production --cleanup --plan
...
$ spinup maintenance pg-upgrade-in-place --env production --cleanup --i-know
...
```

Cleanup archives the recovery files in `/opt/<app>/pg-upgrade.<from>-<to>`, including the guarded script and backup label.
For example, an upgrade from 17 to 18 uses `/opt/<app>/pg-upgrade.17-18`.
The archive preserves the files and releases `/opt/<app>/pg-upgrade` for another major upgrade.
Cleanup refuses an existing archive before removing the old data directory.
A major upgrade refuses to replace an existing recovery directory.
Never use the saved old Compose files to start the old cluster.
