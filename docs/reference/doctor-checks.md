---
title: Doctor checks
parent: Reference
nav_order: 3
---

# Doctor checks

`spinup doctor` runs a list of checks on your machine and your app. Each check has an ID, a status, and a short message.
When a check fails, the line after it starts with `Try:` and gives the fix. This page explains every check.

<!-- doctest: skip -->
```console
$ spinup doctor
skip	audit-wired	no audit module
ok	compose-security	Compose security lint passed.
ok	docker	Docker daemon is available
ok	env-file	.env exists
ok	git-version	2.56.0
ok	go-version	go version go1.27.2 darwin/arm64
ok	lock-valid	spinup.lock is valid
fail	plaintext-secrets	tracked private file: .env
Try: encrypt secrets with sops, remove private files from Git with git rm --cached, and add .env, .env.*, and *.agekey to .gitignore
...
Error: Doctor checks failed
  a local health check failed
  Try: correct the reported failures and run spinup doctor
```

The first column is the status. The second is the check ID. The third is the message.

## Statuses

| Status | Meaning | Exit code |
| --- | --- | --- |
| `ok` | The check passed. | 0 |
| `warn` | The check found a problem that does not block you. | 0 |
| `fail` | The check found a problem that you must fix. | 1 |
| `skip` | The check does not apply. For example, the app has no audit module. | 0 |

## Run the checks

| Command | What runs |
| --- | --- |
| `spinup doctor` | Every local check. |
| `spinup doctor --quick` | Only the fast checks, marked "quick" below. `spinup dev` runs these first. |
| `spinup doctor --remote --env production` | The remote checks on the VM of one environment. |
| `spinup doctor --json` | The same results as a JSON list. Each item has `id`, `status`, `message`, and `try`. |

```console
$ spinup doctor --help
Check local application health
...
```

Outside an app folder, the checks that need an app show `skip`.
If your app uses an [executable plugin](../plugins-exec.md), the plugin can add its own checks to the list.

## Local checks

| ID | Quick | What it checks | Fails or warns when |
| --- | --- | --- | --- |
| [`git-version`](#git-version) | yes | Your Git is version 2.44 or newer. | Git is missing or too old. |
| [`go-version`](#go-version) | yes | Go is version 1.26 or newer. | Go is missing or too old. |
| [`node-version`](#node-version) | yes | Node is version 24 or newer. | Node is missing or too old. |
| [`pnpm`](#pnpm) | yes | The web package manager runs. | `pnpm` or `bun` is not on `PATH`. |
| [`docker`](#docker) | yes | The Docker daemon answers. | Docker is not running. |
| [`ports`](#ports) | yes | The development ports are free. | Another program uses port 8080, or port 5173 for an app with a web app. |
| [`env-file`](#env-file) | yes | The file `.env` exists. | `.env` is missing. |
| [`compose-security`](#compose-security) | yes | The Compose files follow safe settings. | A service has an unsafe setting. |
| [`roles-drift`](#roles-drift) | yes | The roles file matches `[auth]`. | The two differ. |
| [`audit-wired`](#audit-wired) | yes | The audit module is wired into the API. | `main.go` or a migration lacks the wiring. |
| [`lock-valid`](#lock-valid) | no | `spinup.lock` is valid. | The file is damaged. |
| [`managed-missing`](#managed-missing) | no | Every managed file exists. | You deleted a managed file. |
| [`kit-version`](#kit-version) | no | The kit version matches the CLI version. | The major or minor versions differ. |
| [`client-drift`](#client-drift) | no | The TypeScript client matches the API. | The client is stale. |
| [`plaintext-secrets`](#plaintext-secrets) | no | No secret is in Git as plain text. | A key file or a plain secrets file is tracked. |
| [`services`](#services) | no | The `[[services]]` tables and files are complete. | A service file or secret is missing. |

### git-version

spinup uses Git for upgrades. It needs Git 2.44 or newer.
**Fix:** install a newer Git. On macOS, run `brew install git`.

### go-version

The API builds with Go 1.27 or newer.
**Fix:** install Go from [go.dev](https://go.dev/dl/), then run `spinup doctor` again.

### node-version

The web app needs Node 24 or newer. The check skips an app without a web app.
**Fix:** install Node 24, for example with `nvm install 24`.

### pnpm

The check runs `pnpm --version`, or `bun --version` if your app uses Bun. It skips an app without a web app.
**Fix for pnpm:** run `corepack enable`. **Fix for Bun:** install Bun and add it to `PATH`.

### docker

The check runs `docker info`. `spinup dev` needs Docker for PostgreSQL.
**Fix:** start Docker Desktop or the Docker service.

### ports

The check opens port 8080 (API) and, for an app with a web app, port 5173 (web) to see if they are free.
The ports can come from `.spinup/dev.json`. A port that your own app already uses is not a problem.
**Fix:** stop the other program, or run `spinup dev`. It chooses free ports.

### env-file

`spinup dev` reads `.env`. A new app has `.env.example`.
**Fix:** run `cp .env.example .env`.

### compose-security

The check reads `deploy/compose*.yml` and reports unsafe service settings. Each finding has a rule name:

| Rule | It reports | Fix |
| --- | --- | --- |
| `privileged` | A service runs in privileged mode. | Remove `privileged`. |
| `docker-socket` | A service mounts the Docker socket. | Remove the mount. |
| `host-namespace` | A service shares the namespace of the host. | Use an isolated namespace. |
| `dangerous-cap` | A service adds a risky Linux capability. | Remove the capability. |
| `unconfined` | A service turns off a security profile. | Use the default profile. |
| `public-port` | A service publishes a port to the world. | Bind the port to `127.0.0.1`. |
| `unpinned-image` | An image has no `@sha256` digest. | Pin the image by digest. |
| `literal-secret` | A service sets a secret as plain text. | Use a variable or a `_FILE` variable. |

If a setting is safe on purpose, add a comment above it. The comment must give a reason:

```yaml
image: ${API_IMAGE} # spinup:allow unpinned-image spinup deploy sets the release tag
```

A comment with no reason is itself a finding (`bad-suppression`).

### roles-drift

The file `api/internal/roles/roles.gen.go` comes from `[auth]` in `spinup.toml`. The check also reads `api/cmd/api/main.go` for `auth.WithRoles`.
It skips an app without the auth module.
**Fix:** run `spinup g roles`. If `main.go` lacks the calls, see [Roles](../build/roles.md).

### audit-wired

The audit trail needs `audit.Command` and `authsink` in `api/cmd/api/main.go`. It also needs a trigger on `user_identities` when that table exists.
It skips an app without the audit module.
**Fix:** add the wiring that [Audit trail](../audit.md) shows. For the trigger, run `spinup g audit user_identities` and then `spinup db migrate up`.

### apikeys-drift

The files `api/internal/apikeys/apikeys.gen.go` and `web/src/lib/apikeys.gen.ts` come from the `[apikeys]` tables in `spinup.toml`.
It skips an app without the apikeys module. It warns when:

- `spinup.toml` has an invalid `[apikeys]` table.
- A generated file is missing or differs from `spinup.toml`.

**Fix:** run `spinup g apikeys`. See [API keys and plans](../build/api-keys.md).

### apikeys-wired

The apikeys module needs wiring in `api/cmd/api/main.go`: `apikeys.NewStore`, `auth.WithBearer` with the store, `Start`, the middleware after the auth middleware, `Routes`, the `apikeys` command, and `Close`.
It skips an app without the apikeys module. It reports the first missing call.
If the app also has the audit module, it warns when the audit functions did not exist when the apikeys migration ran.

**Fix:** add the line that `Try:` shows. For the audit triggers, see [API keys and plans](../build/api-keys.md#the-audit-trail).

### flags-drift

The files `api/internal/flags/flags.gen.go` and `web/src/lib/flags.gen.ts` come from the `[flags.KEY]` tables in `spinup.toml`.
It skips an app without the flags module. It warns when:

- `spinup.toml` has an invalid flag table.
- A generated file is missing or differs from `spinup.toml`.
- A flag has an `expires` date before today (UTC).

**Fix:** run `spinup g flags`. For an expired flag, remove its table and its uses, run `spinup g flags`, and then run `flags prune --yes`. See [Feature flags](../build/flags.md).

### flags-wired

The flags module needs wiring in `api/cmd/api/main.go`: `flags.New` with `flags.WithDeclared`, `Start`, the middleware after the auth middleware, `Routes`, the `flags` command, and `Close`.
It skips an app without the flags module. It reports the first missing call.
If the app also has the audit module, it warns when the audit functions did not exist when the flags migration ran.

**Fix:** add the line that `Try:` shows. For the audit triggers, see [Feature flags](../build/flags.md#the-audit-trail).

### lock-valid

`spinup.lock` records what spinup rendered. The check parses it.
**Fix:** restore the file from Git. Do not edit it by hand.

### managed-missing

A managed file is one that spinup owns. If you delete one, the upgrade cannot merge it.
This check only warns.
**Fix:** run `spinup restore <path>`.

### kit-version

The CLI and the kit must share the major and minor version. Patch versions can differ.
The check skips a development build of the CLI and an app that points the kit at a local folder.
**Fix:** run `spinup upgrade`. See [Support policy](../policy/support.md).

### client-drift

The web app calls the API through a generated TypeScript client. The check builds a fresh client from `api/openapi/bundle.gen.yaml` and compares.
It skips an app without a web app, and an app where `web/node_modules` does not exist.
**Fix:** run `spinup g client`.

### plaintext-secrets

The check lists the files in Git (`git ls-files`). It fails for each of these:

- A tracked `.env` or `.env.*` file, except `.env.example`.
- A tracked `*.agekey` private key.
- A tracked `secrets/*.yaml` file that is not encrypted with sops.
- A `.gitignore` that does not exclude `.env` files.

The check also fails when the folder is not a Git repository. The message is `tracked files could not be inspected`.
**Fix:** run `git rm --cached <file>` for a tracked file. Encrypt secrets with `spinup secrets`. Add `.env`, `.env.*`, and `*.agekey` to `.gitignore`.

### services

The check validates each `[[services]]` table, the `Dockerfile`, the OpenAPI file, the generated Go client, and the secret names in `secrets/<env>.enc.yaml`.
A missing file is a failure. A missing client or secret is a warning.
**Fix:** follow the `Try:` line under each finding. See [Sidecar services](../services.md).

## Remote checks

`spinup doctor --remote --env production` connects to the VM over SSH and runs these checks.
It does not change anything on the VM.

<!-- doctest: skip -->
```console
$ spinup doctor --remote --env production
ok	remote-backup-age	The newest backup is less than 26 hours old.
ok	remote-certs	The TLS certificate has at least 30 days remaining.
ok	remote-clock	The VM clock is synchronized.
ok	remote-deploy-lock	No deploy lock is held.
warn	remote-disk	Disk use on /var/lib/docker is 84%.
Try: Free disk space before deployment.
ok	remote-docker	Docker and the application stack are running.
ok	remote-monitoring	The monitoring stack is running.
ok	remote-secrets-dir	The secrets directory has safe permissions.
ok	remote-unattended-upgrades	Unattended upgrades are enabled.
```

| ID | Warns when | Fails when | Fix |
| --- | --- | --- | --- |
| `remote-backup-age` | The newest backup is older than 26 hours, or the VM has no readable backup metric. | The newest backup is older than 48 hours. | Run `spinup backup now --env <env>`. |
| `remote-certs` | The certificate expires in less than 30 days. | The certificate expires in less than 7 days, or the chain or host name is wrong. | Check the public URL. Caddy renews certificates on its own. |
| `remote-clock` | The VM clock is not synchronized. | | Turn on NTP, for example `timedatectl set-ntp true`. |
| `remote-deploy-lock` | A lock has expired or is damaged. | | Run `spinup deploy unlock --force --env <env>` after you check that no deploy runs. |
| `remote-disk` | `/` or `/var/lib/docker` is more than 80% full. | A path is more than 90% full. | Remove old releases with `spinup releases prune`. Remove unused Docker images. |
| `remote-docker` | | Docker is not running, or the app stack is not up. | Run `spinup deploy status --env <env>`. |
| `remote-monitoring` | | The monitoring stack is not running. | Run `spinup deploy --env <env>`. The deploy starts the stack. |
| `remote-secrets-dir` | | The secrets folder is not mode 0700, or a secret file is not mode 0600. | Run `chmod 700` and `chmod 600` on the VM, with the deploy user as owner. |
| `remote-unattended-upgrades` | Automatic security updates are off. | | Enable the `unattended-upgrades` service. |

`remote-backup-age` shows `ok` when the app has no backups module. `remote-certs` skips an environment with no `public_url`. `remote-monitoring` skips an app without monitoring.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `fail plaintext-secrets ... could not be inspected` | The folder is not a Git repository. | Run `git init`, or ignore the check in a scratch folder. |
| `fail docker` right after you start Docker | The daemon is still starting. | Wait a few seconds, then run the command again. |
| `fail ports ... busy outside this app` | A different program uses the port. | Stop it, or run `spinup dev` so it picks free ports. |
| `warn kit-version` after a spinup update | The CLI is newer than the kit in `api/go.mod`. | Run `spinup upgrade`. |
| Every remote check fails | SSH cannot reach the VM. | Check `host`, `user`, and `port` in the [environment](../deploy/environments.md). |

## See also

- [The doctor command guide](../commands/doctor.md): the command and its flags
- [Global flags](global-flags.md): exit codes and `--json`
- [Monitoring](../monitoring.md): what runs on the VM
- [Support policy](../policy/support.md): the tool versions that spinup supports
- [Flag reference of doctor](../site/commands/doctor.md)
