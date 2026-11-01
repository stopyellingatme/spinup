---
title: rollback — return to an earlier release
parent: Command guides
---

# rollback — return to an earlier release

`spinup rollback` switches a deployed environment back to a release that is still on the VM.
Use it when a release is live but wrong and you want the last good release again.

<!-- doctest: skip -->
```console
$ spinup rollback --env staging
...
Rolled back staging to release 20261014T080000Z-ffffffffffff (blue color)
```

Without `--to`, the command selects the release before the current one.
It takes the same deploy lock as [deploy](deploy.md), starts the stored release, tests it, and only then switches Caddy.
It writes each step to the deploy journal on the VM.

## Roll back to the previous release

Run `spinup rollback --env <name>`.
If you are not sure which release is current, run `spinup deploy status --env <name>` first.

## Roll back to a chosen release

List the stored releases, then pass an ID to `--to`.
The VM keeps the last 5 releases.

<!-- doctest: skip -->
```console
$ spinup releases list --env staging
...
$ spinup rollback --to 20261012T080000Z-aaaaaaaaaaaa --env staging
```

A wrong ID stops the command at once:

```console
$ spinup rollback --to invalid --env staging
...
  Try: select an ID from spinup releases list
[exit 1]
```

## What a rollback does not change

Rollback uses the images and the configuration of the stored release.
It never changes the database, and it never restores old secrets.

{: .warning }
If the release you leave ran a migration, the database keeps that change.
Check that the older code works with the newer schema before you roll back.

## Plan the database recovery

When the release you leave changed the migration version, rollback prints a warning.
To print a manual recovery plan at any time, add `--with-db-plan`.
The plan uses the time of the backup that the deploy made before the release.
The command never guesses a recovery time.
If the release has no backup time, the plan tells you to find a verified time first.

<!-- doctest: skip -->
```console
$ spinup rollback --with-db-plan --env staging
Database unchanged. Review the manual recovery plan with --with-db-plan.
Manual recovery plan for todo, target 2026-10-15T07:59:30Z:
Stop application writes and isolate Postgres.
...
```

To restore data, read [Backups with pg_dump](../backups-pgdump.md) or [Backups with WAL-G](../backups-walg.md).

## Roll back a protected environment

A protected environment asks you to type its name.
In CI, set `CI` and pass `--yes`.
Without a terminal, pass `--confirm-env <name>`.

<!-- doctest: skip -->
```console
$ spinup rollback --env production --confirm-env production
```

## Reference

| Flag | Meaning | Example |
| --- | --- | --- |
| `--to <release-id>` | Roll back to this stored release. | `spinup rollback --to 20261012T080000Z-aaaaaaaaaaaa` |
| `--with-db-plan` | Print manual database recovery steps. Restore nothing. | `spinup rollback --with-db-plan` |

## See also

- [deploy](deploy.md): the deploy that created the release, and the `/_deploy/info` endpoint that reports the running release
- [releases](releases.md): list the stored releases
- [Environments](../deploy/environments.md)
- Flag reference: [rollback](../site/commands/rollback.md)
