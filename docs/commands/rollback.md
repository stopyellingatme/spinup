# rollback

Run `spinup rollback --env staging` to switch to the previous release.
Use `--to <release-id>` to select another stored release.
The command takes the deploy lock and uses the same switch as deploy.
It tests the release before it changes `current`.
It records each step in the deploy journal.

```console
$ spinup rollback --help
...
$ spinup rollback --to invalid --env staging
...
  Try: select an ID from spinup releases list
[exit 1]
```

Rollback uses the stored images and configuration.
It never changes the database or restores secrets.
Protected environments require confirmation.
For unattended operation, pass `--yes` in CI.

Use `--with-db-plan` to print a manual recovery plan.
The plan uses `pre_deploy_backup_time` from the release being left.
A snapshot can declare `migrations_safe` when every migration has the `-- spinup:safe` marker.
If safety metadata is absent, a migration version change produces a warning.
If the backup time is absent, obtain a verified time before recovery.
The command never estimates a recovery time.

Configure the API with `app.WithDeployInfo(deployx.Options{})` to mount `GET /_deploy/info`.
The response contains `version`, `git_sha`, `migration_version`, and `started_at`.
Set `MigrationVersion` to a function that reads the current database version.
The handler accepts private or loopback peer addresses.
Other peers must send `Authorization: Bearer <token>` with the token from `DEPLOYX_TOKEN_FILE`.
Forwarded requests require a token, even when the proxy has a private address.
