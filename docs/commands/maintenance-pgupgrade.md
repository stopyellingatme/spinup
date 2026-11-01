---
title: maintenance pg-upgrade-in-place — upgrade PostgreSQL fast
parent: Command guides
---

# maintenance pg-upgrade-in-place: upgrade PostgreSQL fast

`pg-upgrade-in-place` upgrades the PostgreSQL major version with `pg_upgrade --link`.
It links the data files instead of copying them, so the downtime stays short for a large database.
The old data directory stays on the same volume until you remove it.

```sh
spinup maintenance pg-upgrade-in-place --env production --to 18 --i-know
```

Print the steps first. `--plan` needs no VM access and changes nothing:

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd todo
$ printf '\n[env.production]\nhost = "app.example.com"\nuser = "deploy"\n' >> spinup.toml
$ spinup maintenance pg-upgrade-in-place --env production --to 18 --plan
Plan: Check the maintenance window and protected confirmation
Plan: Take the deploy lock
Plan: Build the helper image on the VM from both pinned PostgreSQL images
Plan: Take a full backup with the configured backend
Plan: Save recovery files and the exact backup restore command
Plan: Stop api and web of both colors, then stop postgres
Plan: Initialize the new data directory on the same volume
Plan: Run pg_upgrade --check. Stop on any problem
Plan: Run pg_upgrade --link. Never start the old cluster again
Plan: Switch the PostgreSQL image and PGDATA
Plan: Start postgres and upgrade the pgBackRest stanza, then take a full backup
Plan: Run vacuumdb --all --analyze-in-stages
Plan: Migrate, start the live color, and run the deploy smoke checks
Plan: Commit the printed repository changes before the next deploy
Plan: Retain the old data directory until --cleanup
```

## Which major upgrade do I use?

| | `pg-major-upgrade` | `pg-upgrade-in-place` |
| --- | --- | --- |
| How | Dump, restore into a new volume, compare, swap | `pg_upgrade` in link mode on the same volume |
| Downtime | Until the restore and checks end | Short |
| Can you go back to the old cluster? | Yes. The old volume is whole. | No. Restore the safety backup into a new database. |
| Best for | Small and medium databases, and the safest path | Large databases |

See [PostgreSQL upgrades](maintenance-pg.md) for the dump and restore command.

{: .warning }
Never start the old cluster after the link upgrade. The two clusters share files, and the old one can damage the upgraded database. spinup replaces `rollback.sh` with a refusal script to stop you.

## Before you start

- The target must be higher than the current major. `--to` accepts 17 or 18, so the running major must be 16 or 17.
- The `postgres` container must run as a numeric, non-root `UID:GID`. The generated app does this.
- `PGDATA` must be inside a named PostgreSQL volume.
- The maintenance window must be open, or you pass `--now`. A protected environment asks for confirmation. Type the name when asked, or pass `--confirm-env production`. In CI, set `CI` and add `--yes`.
- `--i-know` accepts the downtime and the permanent changes to the old cluster.

| Flag | Meaning | Example |
| --- | --- | --- |
| `--to` | The target major: 17 or 18 | `--to 18` |
| `--i-know` | Accept the downtime | `--to 18 --i-know` |
| `--plan` | Print the steps | `--to 18 --plan` |
| `--now` | Run outside the window | `--to 18 --i-know --now` |
| `--cleanup` | Remove the old data directory | `--cleanup --i-know` |

## Run the upgrade

<!-- doctest: skip -->
```console
$ spinup maintenance pg-upgrade-in-place --env production --to 18 --i-know
...
```

The command works in five stages:

1. **Prepare.** It builds a helper image on the VM from both PostgreSQL images, takes a full backup with your backend, and saves the recovery files.
2. **Stop.** It stops `api` and `web` of both colors, then PostgreSQL. The app is down until the migrations and smoke checks pass.
3. **Check.** It creates the new data directory `<major>/docker` on the same volume, then runs `pg_upgrade --check --link`. Any problem stops the command here.
4. **Upgrade.** It runs `pg_upgrade --link`, switches the image and `PGDATA`, and starts PostgreSQL. With pgBackRest, it also runs `stanza-upgrade` and takes a full backup.
5. **Finish.** It runs `vacuumdb --all --analyze-in-stages`, migrates, starts the live color, and runs the smoke checks.

If a step fails before the link upgrade, the command removes the new directory it made, restarts the old cluster and the live color, and releases the recovery directory.
You can fix the cause and run it again.
If that recovery fails, the command keeps the recovery files and prints instructions.

## If it fails after the link upgrade

The old cluster is no longer safe to start.
The error prints the command that restores the safety backup into a new database, with its label and environment.
`--into new` keeps both directories for inspection.

<!-- doctest: skip -->
```console
$ spinup backup restore --env production --label 20261015T062420ZF --into new
...
```

Find the label in the error text or with `spinup backup list --env production`.
For a physical backup, keep the original major available for the restore, and use the saved original configuration.

## After the upgrade: update the repository

The VM runs the new major, and the repository does not yet.
The command prints the changes. Commit them before the next deploy:

1. Set `answers.db_version` in `spinup.lock` to the target major as a string, for example `"18"`.
2. Render the deploy files with the printed `spinup restore` commands.
3. In `deploy/compose.edge.yml`, set `volumes.postgres_data.external` to `true` and keep the original volume name.
4. Set `services.postgres.environment.PGDATA` to the printed path. For PostgreSQL 18 it is `/var/lib/postgresql/18/docker`. For 17 it is `/var/lib/postgresql/data/17/docker`.
5. With pgBackRest, also render its configuration and keep the printed `pg1-path`.

See [PostgreSQL upgrades](maintenance-pg.md#after-the-upgrade-update-the-repository) for the `spinup restore` commands.

## Clean up

When the upgraded app works, remove the old data directory.
Cleanup needs a finished upgrade, and it checks that the repository major, the external volume name, and `PGDATA` match the active cluster.
It removes only the old data directory. It never removes the shared volume.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-upgrade-in-place --env production --cleanup --plan
...
$ spinup maintenance pg-upgrade-in-place --env production --cleanup --i-know
...
```

Cleanup moves the recovery files to `/opt/<app>/pg-upgrade.<from>-<to>`, for example `/opt/todo/pg-upgrade.17-18`.
This frees `/opt/<app>/pg-upgrade` for the next upgrade.
Cleanup refuses if that archive already exists.
Do not use the saved old Compose files to start the old cluster.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `pg_upgrade --check` fails | The old cluster cannot upgrade as it is. | Read the check output, fix the cause, and run again. Nothing was changed. |
| The command refuses a second upgrade | `/opt/<app>/pg-upgrade` exists. | Run `--cleanup --i-know` for the last upgrade. |
| The target directory exists | An earlier attempt left it. | Inspect it on the VM before you run again. |
| Cleanup refuses | The repository does not match the active cluster. | Make the repository changes above. |

## See also

- [PostgreSQL upgrades](maintenance-pg.md): dump and restore, and the minor upgrade
- [backup restore](backup-restore.md): restore the safety backup
- [maintenance](maintenance.md): the maintenance window
- Flag reference: [pg-upgrade-in-place](../site/commands/maintenance-pg-upgrade-in-place.md)
