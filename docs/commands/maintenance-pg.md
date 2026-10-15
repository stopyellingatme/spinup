# PostgreSQL upgrades

These commands upgrade the production database through SSH.
Both commands require `--env` and the deploy lock.
A held lock returns exit status 5.

```console
$ spinup maintenance pg-minor-upgrade --help
...
$ spinup maintenance pg-major-upgrade --help
...
```

Use `--plan` to print the steps without VM access or changes.
The plan lists the confirmation and window checks.
Execution requires the configured UTC maintenance window.
Use `--now` to override the window.
Protected environments require the shared confirmation.
In CI, supply `--yes` to confirm a protected environment.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-minor-upgrade --env production --plan
...
$ spinup maintenance pg-minor-upgrade --env production
...
$ spinup maintenance pg-major-upgrade --env production --to 18 --plan
...
$ spinup maintenance pg-major-upgrade --env production --to 18 --i-know
...
$ spinup maintenance pg-major-upgrade --env production --cleanup --i-know
...
```

The minor upgrade compares `SHOW server_version` with the pin for the running major.
An equal version needs no upgrade.
A newer running minor causes a refusal.
The command takes a full backup through the configured backend.
It updates the VM Dockerfile to the pin and rebuilds the postgres image.
It recreates only `postgres`, waits for health, and checks the new version.
Downtime is the restart only.

The major upgrade accepts `--to 16`, `--to 17`, or `--to 18`.
The target must exceed the running major and pass the checks from `db doctor`.
The command announces downtime before confirmation.
Supply `--i-know` to accept downtime.

The command stops `api` and `web` of both colors.
The app remains unavailable during the dump, restore, comparison, and swap.
The command always takes a pgdump backup, including when the configured backend is pgBackRest.
It creates `<app>_pgdata_<major>` and an isolated container from the target pin.
An existing target volume causes a refusal.

The command restores the dump and runs `ANALYZE`.
It compares all records in `goose_db_version` and exact counts for every user table.
A mismatch stops the command before the swap.
The command then stops the old server and changes the VM Compose image and volume.
It builds the postgres image with pgBackRest and starts the new server.
It runs the release migrations and starts the live color.

The smoke check uses the deploy paths: `/healthz`, the configured smoke URLs, and `/version`.
The version must contain the release commit.
For a web app, the command reads the API version through the Caddy container.
The command keeps the old volume and image after success.

Each upgrade saves a recovery script before it changes the server.
Each failure after this save prints the exact SSH command to run that script.
The script takes the deploy lock and restores the saved Compose files, environment file, Dockerfile, and old image.
For a major upgrade, the script also starts the previous live color.
A major recovery discards database writes made after the swap.
The dump and the new volume remain available for inspection.

The major command stores recovery files in `/opt/<app>/pg-upgrade`.
It refuses another major upgrade while that directory exists.
Use `--cleanup --i-know` after you verify the upgraded app.
Cleanup requires the new volume to be active and the upgrade to be complete.
It removes the anchor container and the old volume.
It then removes the recovery files.
Cleanup permanently removes this recovery path.
The minor command stores its recovery files in `/opt/<app>/pg-minor-upgrade`.

Commit the repository changes before the next deploy.
The VM swap survives the next `spinup deploy` only after these changes.
The command prints these manual steps with the selected major and volume name:

1. Set `answers.db_version` in `spinup.lock` to the target major as a JSON string, such as `"18"`.
2. Review local changes before you render the files with the commands below.
3. Set `volumes.postgres_data.external` to `true` in `deploy/compose.edge.yml`.
4. Set `volumes.postgres_data.name` to `<app>_pgdata_<major>` in that file.
5. Commit `spinup.lock` and the rendered deploy files.

<!-- doctest: skip -->
```console
$ spinup restore deploy/Dockerfile.postgres --force
...
$ spinup restore deploy/compose.edge.yml --force
...
$ spinup restore deploy/compose.dev.yml --force
...
```

`spinup.toml` has no `db_version` setting.
No command directly changes this lock answer.
The restore commands render from the changed answer and update the hashes in the lock.
Keep the named volume mapping when you review future template merges.
For a minor upgrade, update the repository pins with `spinup upgrade` before the next deploy.

If you use pgBackRest, review its stanza after a major upgrade.
Use the pgBackRest procedure for a new PostgreSQL major and take a new full backup.
The upgrade retains the original physical backups and takes a logical dump for the upgrade.
