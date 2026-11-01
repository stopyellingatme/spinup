---
title: doctor — check your setup
parent: Command guides
---

# doctor — check your setup

`spinup doctor` checks your tools, your app files, and (with `--remote`) your server.
Run it after you install spinup, before `spinup dev`, and when something does not work.

<!-- doctest: skip -->
```console
$ cd todo
$ spinup doctor
skip	audit-wired	no audit module
skip	client-drift	no web pack
ok	compose-security	Compose security lint passed.
ok	docker	Docker daemon is available
ok	env-file	.env exists
ok	git-version	2.56.0
ok	go-version	go version go1.27.2 darwin/arm64
ok	lock-valid	spinup.lock is valid
ok	managed-missing	managed files are present
ok	plaintext-secrets	tracked secrets are encrypted and .env files are ignored
ok	ports	development ports are available to this app
...
```

Each line has a status, a check name, and a message. This list is cut short.
When a check fails, the next line starts with `Try:` and gives the fix, and the command exits with 1.
This is what you see in a folder that is not a Git repository:

```text
fail	plaintext-secrets	tracked files could not be inspected
Try: encrypt secrets with sops, remove private files from Git with git rm --cached, and add .env, .env.*, and *.agekey to .gitignore
Error: Doctor checks failed
  a local health check failed
  Try: correct the reported failures and run spinup doctor
```

| Status | Meaning | Exit code |
| --- | --- | --- |
| `ok` | The check passed. | 0 |
| `warn` | There is a problem, but it does not block you. | 0 |
| `fail` | You must fix this. | 1 |
| `skip` | The check does not apply, for example an app with no audit module. | 0 |

[Doctor checks](../reference/doctor-checks.md) explains every check and its fix.

## Run only the fast checks

`--quick` runs the checks for your tools and local setup: Git, Go, Node, the package manager, Docker, the ports, and `.env`.
`spinup dev` runs the same set before it starts.

<!-- doctest: skip -->
```console
$ spinup doctor --quick
ok	docker	Docker daemon is available
ok	env-file	.env exists
ok	git-version	2.56.0
ok	go-version	go version go1.27.2 darwin/arm64
ok	ports	development ports are available to this app
...
```

## Check the app files

Inside an app, the full run also checks `spinup.lock`, the managed files, the kit version, the generated web client, plain-text secrets, and `[[services]]`.
Outside an app, these checks show `skip`.

To find out whether the files drifted from the templates, use [`spinup check`](check.md). It is built for CI.

## Check a deployed server

`--remote` connects to the server of one environment over SSH. It reads the state and changes nothing.
It checks Docker, disk space (`/` and `/var/lib/docker`), the age of the newest backup, the secrets folder, the TLS certificate, the clock, the deploy lock, monitoring, and automatic security updates.
The certificate check also fails when the chain is not trusted or the host name does not match.

<!-- doctest: skip -->
```console
$ spinup doctor --remote --env production
ok	remote-backup-age	The newest backup is less than 26 hours old.
ok	remote-certs	The TLS certificate has at least 30 days remaining.
warn	remote-disk	Disk use on /var/lib/docker is 84%.
Try: Free disk space before deployment.
...
```

The statuses and exit codes are the same as for the local checks. `--json` works too.

## Use it in a script

`--json` prints one array. Each item has `id`, `status`, `message`, and, for a problem, `try`.

<!-- doctest: skip -->
```console
$ spinup doctor --quick --json
[
  {
    "id": "docker",
    "status": "ok",
    "message": "Docker daemon is available"
  },
  ...
]
```

The command exits with 1 when any check fails. Warnings do not change the exit code.

## Other options

| Flag | What it does | Example |
| --- | --- | --- |
| `--quick` | Run only the fast local checks. | `spinup doctor --quick` |
| `--remote` | Check the server of the environment that `--env` names. | `spinup doctor --remote --env production` |
| `--json` | Print the results as JSON. | `spinup doctor --json` |
| `--dir <directory>`, `-C` | Check the app in this folder. | `spinup doctor -C ~/code/todo` |
| `--offline` | Never download template versions. | `spinup doctor --offline` |

If a check needs a template that you do not have, run `spinup cache warm` once with network access, and then use `--offline`.

The help text always works, also outside an app:

```console
$ spinup doctor --help
Check local application health
...
```

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `docker` fails. | The Docker daemon is not running. | Start Docker. |
| `ports` warns. | Another program uses port 8080 or 5173. | Stop it. `spinup dev` also picks free ports on its own. |
| `plaintext-secrets` fails. | A private file is tracked by Git, or the folder is not a Git repository. | Run `git init`, and follow the `Try:` line. |
| `kit-version` warns. | The kit in `api/go.mod` does not match the CLI. | Run `spinup upgrade`. See [Kit compatibility](../policy/kit-compat.md). |

## See also

- [Doctor checks](../reference/doctor-checks.md) lists every check and its fix.
- [`check`](check.md) finds drift between your files and the templates.
- [`dev`](dev.md) runs the quick checks first.
- Flag reference: [`doctor`](../site/commands/doctor.md)
