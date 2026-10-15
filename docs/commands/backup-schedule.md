# Backup schedule

`spinup backup schedule` installs three systemd timers on the VM.
The services run as the deploy user from the selected environment.
The VM needs Docker Compose, Python 3, `flock`, and systemd as PID 1.
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
schedule_diff = "Mon..Sat *-*-* 03:00:00"
schedule_verify = "Sat *-*-* 04:00:00"
```

These values are the defaults. Systemd uses the VM timezone unless the calendar includes a timezone.
The default differential schedule excludes Sunday, when the full backup runs.
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
Removal tolerates missing timers and repeated requests.
It preserves the backups, the metrics, and the executable script.

Removal also needs permission to delete exactly these six unit files.
The sudoers file of `spinup server bootstrap` and of the cloud-init template grants this permission and nothing wider.

Before installation, the command validates each calendar with `systemd-analyze calendar` on the VM.
It checks access for each required sudo command before the first upload.
If access fails, repeat `spinup server bootstrap` for this environment.
Bootstrap grants access only to the backup units of this app.

Installation uploads `/opt/<app>/backups/run.sh` with mode `0755`.
It writes the backend selection to `/opt/<app>/backups/backend`.
For pgBackRest, it also uploads the repository configuration with the selected retention.
It installs the units in `/etc/systemd/system` with passwordless sudo.
It reloads systemd, enables the timers, and prints their schedule.
Repeat the command after changing the calendars, backend, retention, or deploy user.

Scheduled runs share `/opt/<app>/backups/run.lock` through `flock`.
A concurrent scheduled run waits until the active run finishes.

The script uses the Compose files of `/opt/<app>/current`.
Deploy the app before the first scheduled run.
For pgBackRest, configure the repository secrets before installation.
The script loads the secrets inside the Postgres container.
It does not print their values.

The `pgdump` backend stores custom-format dumps in `/opt/<app>/backups/pgdump` with mode `0600`.
Every scheduled dump is a full backup. The script keeps the selected number of dumps.
The default retention is seven dumps for pgdump and four full backups for pgBackRest.
A VM without systemd gets an error with a `Try:` line.
Use another scheduler to execute the script on such a VM.

Scheduled verification runs `pgbackrest verify` or `pg_restore --list` on the newest dump.
This check detects repository or dump errors. It does not restore a running database.
Use `spinup backup verify` for a full restore test and a measured restore time.

The script and CLI share `/opt/<app>/backups/metrics/.lock` through `flock`.
Each writer holds this lock while it reads, merges, and replaces the metrics.
The script and CLI write `/opt/<app>/backups/metrics/backup.prom` with an atomic rename.
Successful backups update the success timestamp and size for their kind.
Verification updates its timestamp, result, and duration, including when verification fails.
A failed metrics update prints a warning. It does not fail a successful backup.
See [Backup metrics](../monitoring.md#backup-metrics) for the metrics and alerts.
