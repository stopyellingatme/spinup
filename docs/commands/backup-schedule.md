# Backup schedule

`spinup backup schedule` installs three systemd timers on the VM.
The services run as the deploy user from the selected environment.
The VM needs Docker Compose, Python 3, and systemd as PID 1.
It does not need the spinup binary.

```console
$ spinup new scheduled --api-only --module example.com/scheduled --modules ci,deploy,backups --no-git --non-interactive
...
$ cd scheduled
$ ls deploy/backups
run.sh
$ ls deploy/systemd
spinup-scheduled-backup-diff.service
spinup-scheduled-backup-diff.timer
spinup-scheduled-backup-full.service
spinup-scheduled-backup-full.timer
spinup-scheduled-backup-verify.service
spinup-scheduled-backup-verify.timer
$ spinup backup schedule
Error: select backup environment: --env is missing
  Try: pass --env <environment>
[exit 1]
```

Set the calendars in `spinup.toml`:

```toml
[backups]
backend = "pgbackrest"
retention = "4"
schedule_full = "Sun *-*-* 03:00:00"
schedule_diff = "*-*-* 03:00:00"
schedule_verify = "Sat *-*-* 04:00:00"
```

These values are the defaults. Systemd uses the VM timezone unless the calendar includes a timezone.
Each timer persists missed runs and adds a random delay of up to five minutes.
Each service uses `Nice=10` and idle I/O scheduling.

Inspect the rendered schedule before installation:

<!-- doctest: skip -->
```console
$ spinup backup schedule --env production --plan
...
$ spinup backup schedule --env production
...
$ spinup backup schedule --env production --remove
```

`--plan` renders the files without connecting to the VM.
`--remove --plan` prints the removal plan.
`--remove` disables the timers and deletes their services and units.
It preserves the backups, the metrics, and the executable script.

Installation uploads `/opt/<app>/backups/run.sh` with mode `0755`.
It writes the backend selection to `/opt/<app>/backups/backend`.
For pgBackRest, it also uploads the repository configuration with the selected retention.
It installs the units in `/etc/systemd/system` with passwordless sudo.
It reloads systemd, enables the timers, and prints their schedule.
Repeat the command after changing the calendars, backend, retention, or deploy user.

The script uses the Compose files of `/opt/<app>/current`.
Deploy the app before the first scheduled run.
For pgBackRest, configure the repository secrets before installation.
The script loads the secrets inside the Postgres container.
It does not print their values.

The `pgdump` backend stores custom-format dumps in `/opt/<app>/backups/pgdump` with mode `0600`.
Every scheduled dump is a full backup. The script keeps the selected number of dumps.
A VM without systemd gets an error with a `Try:` line.
Use another scheduler to execute the script on such a VM.

Scheduled verification runs `pgbackrest verify` or `pg_restore --list` on the newest dump.
This check detects repository or dump errors. It does not restore a running database.
Use `spinup backup verify` for a full restore test and a measured restore time.

The script and CLI write `/opt/<app>/backups/metrics/backup.prom` with an atomic rename.
Successful backups update the success timestamp and size for their kind.
Verification updates its timestamp, result, and duration, including when verification fails.
A failed metrics update prints a warning. It does not fail a successful backup.
See [Backup metrics](../monitoring.md#backup-metrics) for the metrics and alerts.
