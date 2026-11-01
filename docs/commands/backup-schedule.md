---
title: backup schedule — back up on a timer
parent: Command guides
---

# backup schedule: back up on a timer

`spinup backup schedule` installs three systemd timers on the VM: a full backup, a differential backup, and a verification.
Run it once after your first deploy, and the VM backs itself up without your help.

```sh
spinup backup schedule --env production
```

Check what the command installs before you connect to the VM.
`--plan` prints the rendered unit files and changes nothing:

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy,backups --no-git --non-interactive
...
$ cd todo
$ printf '\n[env.production]\nhost = "app.example.com"\nuser = "deploy"\n' >> spinup.toml
$ spinup backup schedule --env production --plan
# deploy/systemd/spinup-todo-backup-full.service
# Managed by spinup.
...
OnCalendar=Sun *-*-* 03:00:00
...
```

## Before you start

The VM needs these items. The spinup binary is not one of them.

- Docker Compose and Python 3
- `flock`
- systemd as PID 1
- The app deployed once, because the timers use the Compose files of the current release
- For pgBackRest and WAL-G, the storage secrets pushed (see [backup](backup.md#set-up-pgbackrest-backups))
- A VM set up by `spinup server bootstrap`, which grants the sudo rights that the timers need

If the VM has no systemd, the command fails with a `Try:` line.
Run `/opt/<app>/backups/run.sh` from another scheduler instead.

## Install the schedule

```console
$ spinup backup schedule --env production --plan
...
```

<!-- doctest: skip -->
```console
$ spinup backup schedule --env production
...
```

The command checks each calendar on the VM with `systemd-analyze calendar`, then uploads the script and the units, enables the timers, and prints their schedule.
Run it again after you change a calendar, the backend, the retention, or the deploy user.

Check the timers on the VM:

```sh
spinup server ssh --env production   # opens a shell on the VM
systemctl list-timers 'spinup-*'     # run this on the VM
```

## Change the times

Set systemd calendar values in `spinup.toml`. These are the defaults:

```toml
[backups]
backend = "pgbackrest"
retention = "4"
schedule_full = "Sun *-*-* 03:00:00"
schedule_diff = "Mon..Sat *-*-* 03:00:00"
schedule_verify = "Sat *-*-* 04:00:00"
```

| Key | Default | Example |
| --- | --- | --- |
| `schedule_full` | `Sun *-*-* 03:00:00` | `schedule_full = "Sun *-*-* 01:30:00"` |
| `schedule_diff` | `Mon..Sat *-*-* 03:00:00` | `schedule_diff = "*-*-* 12,23:00:00"` |
| `schedule_verify` | `Sat *-*-* 04:00:00` | `schedule_verify = "Sat *-*-* 05:00:00 UTC"` |

systemd uses the VM timezone unless the calendar names one.
The default differential schedule skips Sunday, when the full backup runs.
After you edit a value, run `spinup backup schedule --env production --plan`, then run the command again to install it.

Each timer runs a missed backup when the VM starts again, and it adds a random delay of up to five minutes.
Each service runs with `Nice=10` and idle I/O, so a backup does not slow the app.

## Remove the schedule

`--remove` disables the timers and deletes the services and units.
It keeps your backups, the metrics, and the script. You can run it again with no harm.
`--remove --plan` shows the steps first.

```console
$ spinup backup schedule --env production --remove --plan
sudo -n systemctl disable --now spinup-todo-backup-full.timer spinup-todo-backup-diff.timer spinup-todo-backup-verify.timer
remove /etc/systemd/system/spinup-todo-backup-{full,diff,verify}.{service,timer}
sudo -n systemctl daemon-reload
```

<!-- doctest: skip -->
```console
$ spinup backup schedule --env production --remove
```

## Where the files go

`spinup backup schedule` renders the unit files from `deploy/systemd/` and the script `deploy/backups/run.sh` in your project. The VM gets these files:

| File on the VM | Purpose |
| --- | --- |
| `/etc/systemd/system/spinup-<app>-backup-{full,diff,verify}.{service,timer}` | The three timers and their services. They run as the `user` of the environment (default `deploy`). |
| `/opt/<app>/backups/run.sh` | The script that each timer runs. It does not need the spinup binary. |
| `/opt/<app>/backups/backend` | The backend name that the script reads. |

## What the timers do

| Timer | Does | Default time |
| --- | --- | --- |
| `full` | A full backup | Sunday 03:00 |
| `diff` | A differential backup (pgBackRest and WAL-G). With `pgdump`, every run is a full dump. | Monday to Saturday 03:00 |
| `verify` | pgBackRest: `pgbackrest verify` of the repository. `pgdump`: lists the contents of the newest dump with `pg_restore`. WAL-G: restores the newest backup into a throwaway container. | Saturday 04:00 |

With pgBackRest and `pgdump`, the scheduled verify finds repository and dump errors but does not restore a database.
For a full restore test with a measured restore time, run [`backup verify`](backup-restore.md#verify-a-backup).

Two runs never overlap. A second run waits until the first one ends.
The default retention is four full backups for pgBackRest and seven dumps for `pgdump`.
The `pgdump` backend keeps its dumps in `/opt/<app>/backups/pgdump`.
The script writes backup metrics for each run. See [Monitoring](../monitoring.md#backup-metrics).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `--env is missing` | The command needs an environment. | Pass `--env production`. |
| A sudo check fails before the upload | The deploy user lacks the sudo rights for the backup units. | Run `spinup server bootstrap --env production` again. |
| A calendar is rejected | `systemd-analyze calendar` cannot parse it. | Test the value on the VM with `systemd-analyze calendar '<value>'`. |
| The first scheduled run fails | The app is not deployed, or a storage secret is missing. | Run `spinup deploy`, and push the secrets. |

## See also

- [backup](backup.md): take and list backups
- [backup restore](backup-restore.md): verify and restore
- [Monitoring](../monitoring.md#backup-metrics): metrics and alerts for backups
- Flag reference: [backup schedule](../site/commands/backup-schedule.md)
