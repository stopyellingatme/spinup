# Deploy

`spinup deploy` ships the current commit to the VM of an environment with zero downtime.
It uses blue/green Compose projects and a Caddy reload (`docs/adr/0003-deploy-strategy.md`).
Select the environment with `--env`.
Each environment in `spinup.toml` has its own host, lock, journal, and release history.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging
Building release 20261015T080000Z-0123456789ab
...
Deployed release 20261015T080000Z-0123456789ab to staging (green color)
```

Run the command inside a spinup project, or pass `--dir`:

```console
$ spinup deploy --env staging
Error: Deploy preflight failed
  spinup.toml was not found
  Try: run spinup deploy inside a spinup project, or pass --dir
[exit 1]
```

## Before the first deploy

Do these steps once for each environment:

1. Add the environment to `spinup.toml`: `host`, `user`, `public_url`, and `protected` under `[env.<name>]`.
2. Prepare the VM with `spinup server bootstrap --env <name>`. The deploy needs `/opt/<app>`.
3. Create the secrets with `spinup secrets init --env <name>`.
4. Set `DATABASE_URL` and `POSTGRES_PASSWORD` with `spinup secrets set`. The deploy fails without them.
5. Check `deploy/environments/<name>.toml`: the smoke URLs, the replicas, and the resource limits.

## The steps

The deploy does these steps in this order.
It writes each step to the journal `/opt/<app>/deploy.state` on the VM.

| Step | What it does |
| --- | --- |
| `preflight` | Checks a clean git tree, `spinup check`, `db lint`, the environment, and the secrets. A protected environment needs confirmation. |
| `build` | Builds the `api` and `web` images. The tag is the release ID. |
| (lock) | Opens SSH and takes the lock `/opt/<app>/deploy.lock`. A held lock gives exit code 5. |
| `hooks` | Runs the pre-deploy hooks, for example the backup. |
| `transport` | Moves the images to the VM. |
| `upload` | Writes `releases/<id>/`: the files of `deploy/`, `.env`, and `release.json`. Pushes the secrets to `/opt/<app>/secrets/`. |
| `edge` | Copies the Caddy and Postgres settings to `/opt/<app>/caddy` and `/opt/<app>/postgres`. Starts Caddy and Postgres. |
| `migrate` | Runs the migrations as a one-shot container. The old release is still live. |
| `switch` | Starts the new color, checks each replica, switches Caddy, drains the old color, and removes it. |
| `smoke` | Requests `/healthz` and the smoke URLs through `public_url`. Checks that `/version` reports the new commit. |
| `record` | Moves `current`, adds the release to `releases.json`, and keeps 5 releases. |
| `post` | Runs the post-release hooks. A failure is a warning, because the release is live. |

The release ID is `<UTC time YYYYMMDDTHHMMSSZ>-<12-char git SHA>`.
The deployer never uses `latest`.

An API-only app serves `/version` on `public_url`.
In a full app, Caddy routes `/` to `web`, so the deploy asks the API of the new color through the Caddy container.

## Zero downtime and automatic rollback

The new color must pass its health checks and `/readyz` before Caddy switches to it.
`--health-timeout` sets the limit (default 120s).
If the new color is not healthy, the deploy stops the new color and keeps the old color live.
Caddy does not change.
The deploy fails and writes the failure to the journal.

Use `--no-auto-rollback` to keep the unhealthy color running for debugging.
The old color stays live in both cases.

If the Caddy reload fails, the deploy restores the previous `upstream.caddy`.
Caddy keeps its old configuration after a failed reload.

The deploy never stops the color that serves traffic.

## Plan

`--plan` prints what a deploy would change.
It reads the VM, but it does not take the lock and does not change the VM.
It does not build images.
The global `--dry-run` flag has the same effect.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --plan
Plan: deploy release 20261015T080000Z-0123456789ab to staging (vm.example.com)
Color: blue -> green
Images:
  api: todo-api:20261014T080000Z-ffffffffffff -> todo-api:20261015T080000Z-0123456789ab
Migrations: 1 pending
  00002_add_tags.sql
Secrets:
  changed DATABASE_URL
Files (against current):
  changed compose.yml
...
Nothing changed on the VM (--plan).
```

The plan shows the names of changed secrets. It never shows a value.

## Resume, status, and unlock

Each step is safe to repeat.
If a deploy stops, continue it from its first step that is not done:

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --resume
Resuming release 20261015T080000Z-0123456789ab at step migrate
...
```

`--resume` needs the commit of the release at HEAD.
A new deploy without `--resume` replaces an incomplete deploy and prints a warning.
The deploy keeps the previous journal as `deploy.state.prev`.

You cannot use `--plan` and `--resume` together:

```console
$ spinup deploy --plan --resume
Error: Invalid flags
  --plan and --resume cannot be used together
  Try: run spinup deploy --plan, or spinup deploy --resume
[exit 2]
```

`deploy status` prints the current release, the live color, the lock, and the journal of the last deploy:

<!-- doctest: skip -->
```console
$ spinup deploy status --env staging
Environment: staging (vm.example.com)
Current release: 20261015T080000Z-0123456789ab (green color, commit 0123456789ab, by alice at 2026-10-15T08:00:00Z)
Live color: green
Lock: free
Last deploy: 20261015T080000Z-0123456789ab
...
```

Add `--json` for a machine-readable report.

The lock has a TTL of 30 minutes. The deploy refreshes it between steps.
Another deploy can take an expired lock and prints a warning.
If a deploy stopped and you are sure that it does not run, remove its lock:

```console
$ spinup deploy unlock
Error: Cannot unlock
  unlock removes the lock of another deploy that may still run
  Try: check spinup deploy status, then run spinup deploy unlock --force
[exit 1]
```

<!-- doctest: skip -->
```console
$ spinup deploy unlock --force --env staging
Removed the deploy lock of staging (held by alice for release 20261015T080000Z-0123456789ab)
The journal is unchanged. Run spinup deploy --resume to continue the last deploy.
```

## Images

`transport` in `[env.<name>]` of `spinup.toml` selects how the images reach the VM:

- `ssh` (default): `docker save` locally, then `docker load` on the VM. No registry is necessary. The step does nothing when the VM already has the images.
- `registry`: the build pushes the images to GHCR with provenance and an SBOM. The VM pulls each image by its digest. Log in to the registry on the VM first.

The images run as `<app>-api:<release-id>` (ssh) or `ghcr.io/<owner>/<app>-api@sha256:...` (registry).
`--platform` sets the image platform (default `linux/amd64`).

`--skip-build` uses images that exist already.
With `--tag <release-id>`, the deploy uses that release ID, for example images that CI built.
Without `--tag`, the ssh transport uses the newest local image of HEAD.
The registry transport needs `--tag` with `--skip-build`.

## Flags

| Flag | Effect |
| --- | --- |
| `--plan` | Print what would change. Change nothing on the VM. |
| `--resume` | Continue the last deploy from its first step that is not done. |
| `--allow-dirty` | Deploy with uncommitted changes. The release still records the HEAD commit. |
| `--allow-destructive` | Deploy migrations that `db lint` reports as unsafe. |
| `--skip-build` | Use images that exist already. |
| `--tag <release-id>` | Use this release ID. It must end with the HEAD SHA. |
| `--skip-backup` | Skip the pre-deploy backup. The backup hook refuses it for a migration without the `-- spinup:safe` marker. |
| `--no-auto-rollback` | Keep an unhealthy new color running. |
| `--health-timeout <duration>` | The time for the new color to become healthy (default 120s). |
| `--accept-new-host-key` | Accept the SSH host key of a new VM. |
| `--yes` | With `CI` set, confirm a protected environment without a prompt. |

## Protected environments

A protected environment needs confirmation (`protected = true` in `spinup.toml`).
In a terminal, type the environment name.
In CI, pass `--yes`. The `CI` environment variable must be set.
Without a terminal and without CI, the deploy fails before it changes anything.

## Secrets

The deploy decrypts the secrets locally with sops.
It writes each value to `/opt/<app>/secrets/<NAME>` with mode 0600, and removes names that are no longer in the file.
`release.json` holds only the SHA-256 of each value.
Secret values never enter an image, a Compose file, `.env`, the journal, or the output.

## Files on the VM

```
/opt/<app>/
  releases/<release-id>/   compose files, Caddyfile, .env, upstream.caddy, release.json
  current -> releases/<release-id>
  caddy/                   Caddyfile, Caddyfile.d/, upstream.caddy (the live color)
  postgres/                postgresql.conf, conf.d/
  secrets/<NAME>           one file for each secret, mode 0600
  deploy.lock/             the lock
  deploy.state             the journal of the last deploy
  releases.json            the release history (newest first, 5 releases)
```

The deploy checks a new Caddyfile with `caddy validate` before it replaces the live one.
The base project (Caddy and Postgres) uses `compose.edge.yml` and each other `deploy/compose.*.yml` file, for example `compose.backups.yml`.
The deploy removes the images of releases that it removes from the history.
