# Maintenance commands

The `maintenance` commands operate a deployed VM through SSH.
Each command needs `--env` and Docker access on the VM.

```console
$ spinup maintenance --help
...
$ spinup maintenance patch --help
...
$ spinup maintenance vuln-scan --help
...
$ spinup maintenance prune --help
...
$ spinup maintenance vacuum --help
...
$ spinup maintenance certs --help
...
$ spinup maintenance window --help
...
```

<!-- doctest: skip -->
```console
$ spinup maintenance patch --env production --plan
...
$ spinup maintenance patch --env production --reboot
...
$ spinup maintenance vuln-scan --env production --fail-on critical
...
$ spinup maintenance prune --env production --plan
...
$ spinup maintenance vacuum --env production
...
$ spinup maintenance vacuum --env production --full events
...
$ spinup maintenance certs --env production
...
$ spinup maintenance window --env production
...
```

`patch` uses `apt-get upgrade`.
It does not use `dist-upgrade`.
Use `--plan` to list available packages.
Use `--reboot` to restart a host that needs a reboot.
The command refuses a reboot outside the maintenance window.
Use `--now` only when you accept that interruption.

`vuln-scan` scans each running app image with the pinned Trivy image.
It exits with status 4 for HIGH or CRITICAL findings.
Use `--fail-on critical` to ignore HIGH findings.
The scan mounts the Docker socket read-only.
This one-shot operator scan can inspect image metadata through that socket.

`prune` removes unused app images and the Docker build cache.
It retains the image references from the last five releases in `releases.json`.
It also retains every image that a container uses, including a stopped container.
It compares image IDs to protect registry digest references and alternative tags.
It never removes volumes.

`vacuum` reads `POSTGRES_USER` and `POSTGRES_DB` inside the PostgreSQL container.
It runs `VACUUM (ANALYZE)` in that database.
It prints ten tables with the most dead tuples.
Use `--full <table>` for one table during the maintenance window.

`certs` reads the public TLS certificate from the operator machine.
It warns when fewer than 30 days remain.
It fails when fewer than seven days remain.
`window` reports the UTC maintenance window, the current status, and the next start.
