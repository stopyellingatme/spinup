---
title: maintenance pg-upgrade — minor and major
parent: Command guides
---

# maintenance pg-minor-upgrade and pg-major-upgrade: upgrade PostgreSQL

These two commands upgrade the production database through SSH.
`pg-minor-upgrade` moves to the newest minor version that spinup pins. `pg-major-upgrade` moves to a new major by dump and restore.
Both take a backup first and save a recovery script before they change the server.

```sh
spinup maintenance pg-minor-upgrade --env production --plan
```

Print the steps of an upgrade with `--plan`. It needs no VM access and changes nothing:

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd todo
$ printf '\n[env.production]\nhost = "app.example.com"\nuser = "deploy"\n' >> spinup.toml
$ spinup maintenance pg-minor-upgrade --env production --plan
Plan: Check the maintenance window and protected confirmation
Plan: Take the deploy lock
Plan: Read SHOW server_version and the pinned image
Plan: Exit successfully if the pinned version is already running
Plan: Take a backup with the configured backend
Plan: Save the old image and recovery command
Plan: Build postgres from the release Dockerfile.postgres
Plan: Recreate only postgres (restart downtime)
Plan: Wait for health and print SHOW server_version
```

## Which upgrade do I need?

| I want ... | Use | Downtime | Undo |
| --- | --- | --- | --- |
| The newest minor of my major (17.4 to 17.6) | `pg-minor-upgrade` | A database restart | The recovery script |
| A new major (17 to 18). The safest path. | `pg-major-upgrade` | The app stops during the dump, restore, and checks | The recovery script. The old volume stays. |
| A new major (17 to 18) with the shortest downtime | [`pg-upgrade-in-place`](maintenance-pgupgrade.md) | Short | Restore the safety backup into a new database |

Every upgrade needs these items:

- `--env`, and the deploy lock. If a deploy holds the lock, the command exits with code 5.
- The maintenance window to be open. Add `--now` to override it. See [maintenance](maintenance.md#the-maintenance-window).
- A confirmation for a protected environment. Type the name when asked, or pass `--confirm-env production`. In CI, set `CI` and add `--yes`.

## Minor upgrade

A minor upgrade replaces the PostgreSQL image with the version that spinup pins for your major.
Nothing changes in your data files, and the downtime is one restart.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-minor-upgrade --env production
...
```

The command works in this order:

1. It compares `SHOW server_version` with the pin. If the versions are equal, it stops with no change. If the server is newer than the pin, it refuses.
2. It takes a full backup with your configured backend.
3. It updates the Dockerfile on the VM and rebuilds the `postgres` image.
4. It recreates only `postgres`, waits for it to be healthy, and checks the new version.

Before each attempt, the command removes a helper container that an earlier attempt left. A failed attempt is therefore safe to run again.
Recovery files are in `/opt/<app>/pg-minor-upgrade`.
Before the next deploy, update the pins in the repository with `spinup upgrade`.

## Major upgrade (dump and restore)

A major upgrade dumps the database, restores it into a new volume on the target version, compares the two, and then swaps them.
The old volume stays until you ask to remove it.

{: .warning }
The app is down from the moment the command stops it until the smoke checks pass. Pass `--i-know` to accept this downtime.

`--to` selects the target: 16, 17, or 18.
The target must be higher than the running major, so a target of 16 needs a running major below 16.
The target must also be a major that `spinup db doctor` accepts.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-major-upgrade --env production --to 18 --plan
...
$ spinup maintenance pg-major-upgrade --env production --to 18 --i-know
...
```

The command works in this order:

1. It stops `api` and `web` of both colors.
2. It takes a `pgdump` backup, also when your backend is pgBackRest.
3. It starts the target image on a new volume `<app>_pgdata_<major>`. If that volume exists, the command refuses.
4. It restores the dump and runs `ANALYZE`.
5. It compares the migration table and the exact row count of every user table. A mismatch stops the command before the swap.
6. It swaps the image and the volume, and starts the new server. With pgBackRest, it also runs `stanza-upgrade`, which tells the repository about the new major.
7. It runs the migrations, starts the live color, and runs the deploy smoke checks.

The command keeps the old volume and image after it succeeds.
Recovery files are in `/opt/<app>/pg-upgrade`. The command refuses another major upgrade while this directory exists.

| Flag | Meaning | Example |
| --- | --- | --- |
| `--to` | The target major: 16, 17, or 18 | `--to 18` |
| `--i-know` | Accept the downtime | `--to 18 --i-know` |
| `--plan` | Print the steps and the repository changes | `--to 18 --plan` |
| `--now` | Run outside the window | `--to 18 --i-know --now` |
| `--cleanup` | Remove the old volume after a finished upgrade | `--cleanup --i-know` |

## After the upgrade: update the repository

The VM now runs the new major. Your repository does not know that yet.
Without these changes, the next `spinup deploy` brings back the old volume.
The command prints them. Do these steps in order:

1. Set `answers.db_version` in `spinup.lock` to the target major as a string, for example `"18"`. No command does this for you, and `spinup.toml` has no such key.
2. Review your local changes to the deploy files. The next step overwrites them.
3. Render the deploy files again from the new answer:

<!-- doctest: skip -->
```console
$ spinup restore deploy/Dockerfile.postgres --force
...
$ spinup restore deploy/compose.edge.yml --force
...
$ spinup restore deploy/compose.dev.yml --force
...
```

4. In `deploy/compose.edge.yml`, point the data volume at the new volume:

```yaml
volumes:
  postgres_data:
    external: true
    name: todo_pgdata_18
```

5. Commit `spinup.lock` and the rendered deploy files.

When you merge template changes later, keep the `external: true` volume mapping in `deploy/compose.edge.yml`.

If you use pgBackRest, render its configuration and take a full backup at once.
The old physical backups still exist, but they belong to the old major.

<!-- doctest: skip -->
```console
$ spinup restore deploy/pgbackrest/pgbackrest.conf --force
...
$ spinup backup now --env production --type full
...
```

## If an upgrade fails

Every failure after the recovery script was saved prints the exact SSH command that runs it (`rollback.sh` in the recovery directory, `/opt/<app>/pg-upgrade` or `/opt/<app>/pg-minor-upgrade`).
The script takes the deploy lock and restores the saved Compose files, environment file, Dockerfile, and old image.
After a major upgrade, it also starts the previous live color.

{: .warning }
A major recovery discards every database write made after the swap. The dump and the new volume stay on the VM so you can inspect them.

<!-- doctest: skip -->
```console
$ ssh -- deploy@app.example.com 'sh /opt/todo/pg-upgrade/rollback.sh'
```

## Clean up

When the upgraded app works, remove the old volume.
Cleanup needs a finished upgrade, `answers.db_version` in `spinup.lock` set to the active major, and `external: true` with the new name in `deploy/compose.edge.yml`.
If one is missing, cleanup refuses and lists what to fix.

{: .warning }
Cleanup removes the old volume and the recovery files for good. You cannot undo the upgrade after that.

<!-- doctest: skip -->
```console
$ spinup maintenance pg-major-upgrade --env production --cleanup --i-know
...
```

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The command refuses to run | It is outside the maintenance window. | Wait, or add `--now`. |
| Exit code 5 | The deploy lock is held. | Wait for the other command. |
| The target volume exists | An earlier attempt left `<app>_pgdata_<major>`. | Inspect it. Remove it only when you do not need it. |
| A major upgrade is refused | `/opt/<app>/pg-upgrade` exists. | Run `--cleanup` after you verify the last upgrade. |
| The row counts differ | The restore did not match the dump. | The command stops before the swap. Run the recovery command that the error prints, then try again. |
| The next deploy goes back to the old major | The repository changes are missing. | Do the steps in [After the upgrade](#after-the-upgrade-update-the-repository). |

## See also

- [In-place PostgreSQL upgrades](maintenance-pgupgrade.md)
- [maintenance](maintenance.md): patching, pruning, and the window
- [backup](backup.md) and [backup restore](backup-restore.md)
- [restore](restore.md): render a template file again
- Flag reference: [pg-minor-upgrade](../site/commands/maintenance-pg-minor-upgrade.md), [pg-major-upgrade](../site/commands/maintenance-pg-major-upgrade.md)
