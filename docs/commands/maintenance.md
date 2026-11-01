---
title: maintenance — patch and clean the VM
parent: Command guides
---

# maintenance: patch and clean the VM

The `maintenance` commands keep a deployed VM healthy.
They patch the operating system, scan images for known vulnerabilities, free disk space, vacuum the database, and check the TLS certificate.
Each command runs through SSH and needs `--env`. Add `--json` to print the result as JSON.

```sh
spinup maintenance patch --env production --plan
```

```console
$ spinup new todo --api-only --module example.com/todo --modules ci,deploy --no-git --non-interactive
...
$ cd todo
$ printf '\n[env.production]\nhost = "app.example.com"\nuser = "deploy"\n' >> spinup.toml
$ spinup maintenance --help
...
Available Commands:
  certs               Check the public certificate expiry
  patch               Install available package updates
  pg-major-upgrade    Upgrade the PostgreSQL major with a verified dump and retained old volume
  pg-minor-upgrade    Upgrade PostgreSQL to the pinned minor after a backup
  pg-upgrade-in-place Upgrade PostgreSQL with pg_upgrade --link and retain the old directory
  prune               Remove unused app images and build cache
  vacuum              Vacuum the production database
  vuln-scan           Scan running images with Trivy
  window              Show the maintenance window
...
```

## Pick a command

| I want to ... | Run |
| --- | --- |
| Install OS package updates | `spinup maintenance patch --env production` |
| Find known vulnerabilities in my images | `spinup maintenance vuln-scan --env production` |
| Free disk space | `spinup maintenance prune --env production` |
| Reclaim database space and refresh statistics | `spinup maintenance vacuum --env production` |
| Check when the TLS certificate ends | `spinup maintenance certs --env production` |
| See when I may do risky work | `spinup maintenance window --env production` |
| Upgrade PostgreSQL | [Upgrade PostgreSQL](#upgrade-postgresql) |

## The maintenance window

Risky work only runs inside a time window, so it happens when few people use the app.
Set the window in UTC in `spinup.toml`. An empty window means any time.
The format is `<days> HH:MM-HH:MM`. The days are one day (`Sun`), a range (`Mon-Fri`), or `*` for every day.
A window can cross midnight, such as `Mon-Fri 22:00-02:00`. It belongs to the day on which it starts.

```toml
[maintenance]
window = "Sun 02:00-05:00"
```

`window` prints three lines: the window, `Inside: true` or `Inside: false`, and the `Next start` time.
The PostgreSQL upgrades, `vacuum --full`, and the reboot of `patch --reboot` refuse to run outside the window.
Add `--now` to run anyway, when you accept the interruption.

<!-- doctest: skip -->
```console
$ spinup maintenance window --env production
Window: Sun 02:00-05:00
Inside: false
Next start: 2026-11-08T02:00:00Z
```

## Patch the OS

`patch` runs `apt-get update` and `apt-get upgrade`. It never runs `dist-upgrade`.
Run `--plan` first to list the packages that would change.
The patch itself runs at any time. If the host needs a reboot, add `--reboot`. Only the reboot waits for the window.

<!-- doctest: skip -->
```console
$ spinup maintenance patch --env production --plan
...
$ spinup maintenance patch --env production --reboot
...
```

| Flag | Meaning | Example |
| --- | --- | --- |
| `--plan` | List packages. Install nothing. | `spinup maintenance patch --env production --plan` |
| `--reboot` | Reboot when the host requires it. Only inside the window. | `spinup maintenance patch --env production --reboot` |
| `--now` | Reboot outside the window. | `spinup maintenance patch --env production --reboot --now` |

## Scan images for vulnerabilities

`vuln-scan` scans each running image of the app and of the monitoring stack with the pinned Trivy image.
It prints the number of CRITICAL and HIGH findings for each image.
It exits with code 4 when it finds one, so a CI job can fail on it.

<!-- doctest: skip -->
```console
$ spinup maintenance vuln-scan --env production
IMAGE CRITICAL HIGH
todo-api:20261015-1 0 2
todo-web:20261015-1 0 0
[exit 4]
$ spinup maintenance vuln-scan --env production --fail-on critical
...
```

`--fail-on critical` ignores HIGH findings. The default is `high`.
The scan mounts the Docker socket read-only to read the image data.

## Clean images and the build cache

`prune` removes unused app images and the Docker build cache.
It keeps the images of the last five releases and every image that a container uses, also a stopped one.
It never removes volumes.
`--plan` lists the images and removes nothing.

<!-- doctest: skip -->
```console
$ spinup maintenance prune --env production --plan
todo-api:20260920-3
todo-web:20260920-3
$ spinup maintenance prune --env production
Removed 2 unused images.
```

## Vacuum the database

`vacuum` runs `VACUUM (ANALYZE)` in your database and prints the ten tables with the most dead rows.
It runs at any time. To rewrite one table and give the disk space back, use `--full <table>`.
This locks the table, so it runs only in the window. Add `--now` to run it outside the window.

<!-- doctest: skip -->
```console
$ spinup maintenance vacuum --env production
todos|120
users|4
$ spinup maintenance vacuum --env production --full events
...
$ spinup maintenance vacuum --env production --full events --now
...
```

## Check the certificate

`certs` reads the public TLS certificate from your computer, not from the VM.
It connects to the `public_url` of the environment in `spinup.toml`.
It warns when fewer than 30 days remain. It fails when fewer than seven days remain.

<!-- doctest: skip -->
```console
$ spinup maintenance certs --env production
Issuer: CN=R11,O=Let's Encrypt,C=US
Not after: 2027-01-12T08:41:07Z
Days left: 72
```

## Upgrade PostgreSQL

Three commands upgrade PostgreSQL. Pick one:

| Upgrade | Command | Downtime | Guide |
| --- | --- | --- | --- |
| A newer minor version (17.4 to 17.6) | `maintenance pg-minor-upgrade` | A database restart | [PostgreSQL upgrades](maintenance-pg.md#minor-upgrade) |
| A newer major (17 to 18), by dump and restore | `maintenance pg-major-upgrade` | Until the restore ends | [PostgreSQL upgrades](maintenance-pg.md#major-upgrade-dump-and-restore) |
| A newer major (17 to 18), in place | `maintenance pg-upgrade-in-place` | Short, because files are linked and not copied | [In-place upgrades](maintenance-pgupgrade.md) |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | The command finished. |
| 4 | `vuln-scan` found a HIGH or CRITICAL issue. |
| 5 | A deploy or a PostgreSQL upgrade holds the deploy lock. |

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The command refuses to run | It is outside the maintenance window. | Wait for the window, or add `--now`. |
| `initialize SSH connection` fails | spinup cannot reach the VM. | Check `host` and `user` in `spinup.toml` and your SSH key. |
| Exit code 5 | The deploy lock is held. | Wait for the other command to end. |
| `public_url is invalid` from `certs` | The environment has no `public_url`. | Set `public_url` in `[env.production]`. |

## See also

- [PostgreSQL upgrades](maintenance-pg.md) and [In-place PostgreSQL upgrades](maintenance-pgupgrade.md)
- [backup](backup.md): take a backup before risky work
- [Monitoring](../monitoring.md): watch the app after the work
- Flag reference: [maintenance](../site/commands/maintenance.md), [patch](../site/commands/maintenance-patch.md), [vuln-scan](../site/commands/maintenance-vuln-scan.md), [prune](../site/commands/maintenance-prune.md), [vacuum](../site/commands/maintenance-vacuum.md), [certs](../site/commands/maintenance-certs.md), [window](../site/commands/maintenance-window.md)
