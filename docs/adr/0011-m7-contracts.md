# ADR 0011: M7 contracts (backups and maintenance)

Status: accepted (2026-10-15)

The M7 units run in parallel. These decisions fix the contracts between them. ADR 0004 keeps pgBackRest as the default backend. ADR 0009 fixes the VM layout and the remote runner.

## 1. Configuration (`spinup.toml`, main loop)

The pre-seed adds these keys to `project.Config`. All keys are optional.

```toml
[backups]
backend = "pgbackrest"         # or "pgdump"
retention = "4"                # existing: the number of full backups (pgdump: the number of dumps)
schedule_full = "Sun *-*-* 03:00:00"   # systemd OnCalendar
schedule_diff = "*-*-* 03:00:00"
schedule_verify = "Sat *-*-* 04:00:00"

[maintenance]
window = "Sun 02:00-05:00"     # UTC; empty means "any time"
```

## 2. Backends (`internal/backup`)

- `backup.Open(o Options) (Backend, *Session, error)` selects the backend from `[backups] backend`. `pgbackrest` is the default. Unit M7.2 adds `Open`. Unit M7.1 adds the `pgdump` backend in `internal/backup/pgdump` and registers it with `backup.RegisterBackend("pgdump", factory)` from `init()`.
- **pgdump** keeps custom-format dumps (`pg_dump -Fc`, run in the `postgres` container of the base project) in `/opt/<app>/backups/pgdump/<label>.dump` on the VM, mode 0600. The label is `<UTC YYYYMMDDTHHMMSSZ>F`. It keeps `retention` dumps. It has no WAL and no point-in-time restore: `Target.Time` is an error with a `Try:` line. `Kind` is ignored (every dump is full).
- `Verify` restores into a throwaway container on the VM (`<app>-verify-<label>`, the app's own postgres image, no published port, on no app network), waits for `pg_isready`, runs `SELECT count(*) FROM goose_db_version` and the checks in §4, records the RTO (restore start to the first successful query), and removes the container and its volume in every case.
- `Restore` with `Into.New` leaves a container `<app>-restore-<label>` running, bound to `127.0.0.1:5433` of the VM, and prints the `spinup server ssh -L` line to reach it. `Into.InPlace` (§3) replaces the live database.
- `Expire` applies the retention.

## 3. In-place restore

`backup restore --into in-place` is destructive. It calls `project.ConfirmProtected` and needs `--i-know`. It takes the deploy lock (ADR 0009 §2), so no deploy runs at the same time. Steps, each printed: take a safety backup of the live database (skip with `--no-safety-backup`), stop `api` and `web` of both colors, stop `postgres`, restore (pgBackRest `--delta`, or pgdump `pg_restore --clean --if-exists` into a new database that is then renamed), start `postgres`, run `migrate` (the release's own binary), start the live color, run the smoke check of the deployer. On failure it prints the label of the safety backup and the exact command to restore it.

## 4. Backup metrics

Nothing in the kit measures backups. The VM writes a Prometheus text file instead, and Alloy reads it in every profile.

- File: `/opt/<app>/backups/metrics/backup.prom`, written atomically (write a temp file, then `mv`), mode 0644.
- Metrics (gauges): `backup_last_success_timestamp_seconds{kind}`, `backup_last_size_bytes{kind}`, `backup_last_verify_timestamp_seconds`, `backup_last_verify_rto_seconds`, `backup_last_verify_ok`.
- Writers: every successful `backup now`, the pre-deploy backup hook, the scheduled jobs (§5), and `backup verify`. `backup.RecordMetrics(ctx, remote.Runner, app, Sample)` is the only writer (unit M7.2). It reads the current file and replaces only its own lines.
- The monitoring pack mounts `/opt/<app>/backups/metrics` read-only into Alloy and reads it with `prometheus.exporter.unix` (only the `textfile` collector). A recording rule keeps the existing names: `backup_last_age_seconds = time() - max by (job) (backup_last_success_timestamp_seconds)`.

## 5. Schedule (systemd on the VM)

- `backup schedule` installs systemd units from the backups pack (`deploy/systemd/*.tmpl`, managed): `spinup-<app>-backup-full.{service,timer}`, `spinup-<app>-backup-diff.{service,timer}`, and `spinup-<app>-backup-verify.{service,timer}`, with the `OnCalendar` values of §1 and `Persistent=true`. `backup schedule --remove` removes them.
- The services run `/opt/<app>/backups/run.sh <full|diff|verify>` as the deploy user. `run.sh` (managed, in the backups pack) runs the same backend commands as the CLI and writes the metrics file of §4. The VM never needs the spinup binary.
- `backup status` shows the timers (`systemctl list-timers`) and the last results from the metrics file.
- A VM without systemd (the DinD test VM) gets a clear error with a `Try:` line.

## 6. Maintenance (`internal/maintain`, `spinup maintenance`)

- Commands: `patch` (apt upgrade on the VM, reports `reboot-required`; `--reboot` reboots inside the window), `vuln-scan` (Trivy from its pinned image on the VM against the running images; exit 4 when it finds HIGH or CRITICAL issues), `prune` (removes images of releases outside the last 5 and the build cache), `vacuum` (`VACUUM (ANALYZE)` and a bloat report), `certs` (the expiry of the public certificate; warns under 14 days), `window` (prints the window and whether now is inside it).
- `maintain.InWindow(window string, now time.Time) (bool, error)` (pre-seed, `internal/maintain/window.go`) is the one parser of the window format `<Day[-Day]|*> HH:MM-HH:MM`, in UTC. A window may cross midnight (`Sat 23:00-02:00`). Disruptive commands (`patch --reboot`, `pg-*-upgrade`, `backup restore --into in-place`) refuse outside the window unless `--now`.
- Postgres upgrades (`internal/maintain/pg`): `pg-minor-upgrade` rebuilds the postgres image with the pinned minor and recreates the container after a backup. `pg-major-upgrade --to <major>` uses the pgdump backend: dump, start the new major in a new volume, restore, swap, and keep the old volume until `--cleanup`.

## 7. Remote doctor

`spinup doctor --remote --env <name>` runs the `Remote` checks with a `remote.Runner` in `Deps["remote"]` (the key that `vm-memory` uses). Unit M7.5 adds the flag and the checks: disk use, Docker running, the deploy lock, unattended upgrades, clock sync, the secrets directory mode, backup age (from the metrics file), certificate expiry, and the monitoring stack.

## 8. Test storage

The DR drill (unit M7.6) uses SeaweedFS (`server -s3`, the pinned image) as the S3 repository. MinIO is archived.
