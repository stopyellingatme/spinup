# Check local health

`spinup doctor` reports the git version, lock validity, missing managed files, and kit compatibility.
Use `--quick` to run only the fast checks: the git version and the dev environment (Go, Node, pnpm, Docker, ports, and `.env`). `spinup dev` runs the same set first.
Use `--json` for a JSON array of results.
Checks that require a project skip outside a project.
Failures produce exit 1.
Warnings do not cause a failure.

The kit check skips development builds and a local kit replacement.
The kit check reads `api/go.mod` through `go mod edit -json`.
The command does not change the module.

Use `--dir <directory>` to select a project.
Use `--offline` to prevent template downloads.
If a template is unavailable, use `spinup cache warm`.

## Remote checks

Use `spinup doctor --remote --env <name>` to check a deployed VM.
The command checks Docker, disk space, backups, secrets, certificates, monitoring, and host maintenance.
The command also runs the existing VM memory check.
Use `--json` to write the same result format as local doctor.
Failures produce exit 1.
Warnings do not produce a failure.

This example needs a deployed environment, so the doc test does not run it:

```sh
spinup doctor --remote --env production --json
```

```console
$ spinup doctor --help
...
```
