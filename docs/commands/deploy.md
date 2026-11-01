---
title: deploy — ship a release
parent: Command guides
---

# deploy — ship a release

`spinup deploy` builds your app, sends it to the VM of one environment, and switches traffic to the new release with no downtime.
Use it for every release after the VM is ready.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging
Building release 20261015T080000Z-0123456789ab
...
Deployed release 20261015T080000Z-0123456789ab to staging (green color)
```

The command works inside a spinup project.
Outside a project, pass `--dir` or the command stops at the first check:

```console
$ spinup deploy --env staging
Error: Deploy preflight failed
  spinup.toml was not found
  Try: run spinup deploy inside a spinup project, or pass --dir
[exit 1]
```

{: .note }
Which page do you need?
To create a server and deploy in one command, read [Deploy to DigitalOcean](../deploy-digitalocean.md).
To prepare your own server first, read [Deploy to your own server](../deploy.md).
This page covers the `deploy` command itself.

## How a deploy works

A release runs as one of two Compose projects, called colors: `blue` and `green`.
The old color keeps serving while the new color starts.
Caddy, the web server in front of your app, switches to the new color only after it passes its health checks.
Then the deploy removes the old color.
If the new color is not healthy, Caddy never switches and your users see no change.

Each environment in `spinup.toml` has its own host, lock, journal, and release history.
The journal is a file on the VM that records each step, so a stopped deploy can continue.

## Before the first deploy

Do these steps once for each environment:

1. Add the environment to `spinup.toml` with `host`, `user`, `public_url`, and `protected` under `[env.<name>]`.
2. Prepare the VM with [`spinup server bootstrap --env <name>`](server.md).
3. Create the secrets with [`spinup secrets init --env <name>`](secrets.md).
4. Set `DATABASE_URL` and `POSTGRES_PASSWORD` with `spinup secrets set`. The deploy fails without them.
5. Check `deploy/environments/<name>.toml` for the smoke URLs, the replicas, and the resource limits.

```toml
[env.staging]
host = "203.0.113.10"
user = "deploy"
public_url = "https://staging.example.com"
protected = false
```

## Deploy a release

Run `spinup deploy --env <name>`.
The release ID is the UTC time and the first 12 characters of the git commit, for example `20261015T080000Z-0123456789ab`.
The deploy never uses a `latest` tag.

If the git tree has uncommitted changes, the deploy stops.
Add `--allow-dirty` to deploy anyway.
The release still records the commit at `HEAD`, not your edits.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --allow-dirty
```

If `db lint` reports an unsafe migration, the deploy stops.
Add `--allow-destructive` after you read the report.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --allow-destructive
```

## Preview a deploy

Add `--plan` to see what would change.
The command reads the VM but takes no lock, builds nothing, and changes nothing.
The global `--dry-run` flag does the same.

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

The plan lists the names of changed secrets and never a value.

## If a deploy stops

Each step is safe to repeat.
Run `spinup deploy status` to see where the deploy stopped.
Then run `spinup deploy --resume` to continue from the first step that is not done.

<!-- doctest: skip -->
```console
$ spinup deploy status --env staging
Environment: staging (vm.example.com)
Current release: 20261015T080000Z-0123456789ab (green color, commit 0123456789ab, by alice at 2026-10-15T08:00:00Z)
Live color: green
Lock: free
Last deploy: 20261015T080000Z-0123456789ab
...
$ spinup deploy --env staging --resume
Resuming release 20261015T080000Z-0123456789ab at step migrate
...
```

Add `--json` to `deploy status` for a report that a script can read.
`--resume` needs the commit of the release at `HEAD`.
A new deploy without `--resume` replaces the stopped deploy and prints a warning.
The deploy keeps the old journal as `deploy.state.prev` on the VM.

You cannot combine `--plan` and `--resume`:

```console
$ spinup deploy --plan --resume
Error: Invalid flags
  --plan and --resume cannot be used together
  Try: run spinup deploy --plan, or spinup deploy --resume
[exit 2]
```

### Remove a stuck lock

A deploy takes a lock on the VM so that two deploys cannot run together.
The lock expires after 30 minutes, and a running deploy renews it every 10 minutes.
If a deploy stopped and you are sure that nothing runs, remove its lock with `spinup deploy unlock --force`.
The command without `--force` refuses, because it could remove the lock of a deploy that still runs.

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

## Check health and recover

The new color must pass its health checks and `/readyz` before Caddy switches.
`--health-timeout` sets the wait (default `2m`).

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --health-timeout 5m
```

If the new color is not healthy, the deploy stops it, keeps the old color live, and fails.
To look at the broken color, add `--no-auto-rollback`.
The deploy keeps the unhealthy color running, and the old color stays live.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --no-auto-rollback
```

Before the deploy installs a new Caddyfile, it checks the file with `caddy validate`.
If the check fails, the live Caddy and Postgres files stay as they are.
If Caddy refuses the new configuration at reload, the deploy restores the previous one.
If the SSH connection drops during the Caddy reload, the deploy cannot tell which configuration is loaded.
It keeps both colors running.
Run `spinup deploy status` to check, then run `spinup deploy --resume`.

To go back to an older release after a successful deploy, use [`spinup rollback`](rollback.md).

## Deploy the same release again

The deploy refuses a release ID that already exists on the VM.
To run the same images again, add `--redeploy`.
The deploy picks the first unused suffix, such as `-r1`, and starts the other color.
The live release stays untouched.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --skip-build --tag 20261015T080000Z-0123456789ab --redeploy
...
Uploading release 20261015T080000Z-0123456789ab-r1 (green color)
...
Deployed release 20261015T080000Z-0123456789ab-r1 to staging (green color)
```

## Choose how images reach the VM

Set `transport` in `[env.<name>]` of `spinup.toml`:

| Value | What happens |
| --- | --- |
| `ssh` (default) | `docker save` on your machine, then `docker load` on the VM. No registry is needed. |
| `registry` | The build pushes the images to GHCR with provenance and an SBOM. The VM pulls them by digest. Log in to the registry on the VM first. |

The images run as `<app>-api:<release-id>` with `ssh` and as `ghcr.io/<owner>/<app>-api@sha256:...` with `registry`.
The `ssh` transport skips the upload when the VM has the images already.

Use `--skip-build` to deploy images that exist already.
Add `--tag <release-id>` to name them, for example images that CI built.
Without `--tag`, the `ssh` transport uses the newest local image of `HEAD`.
With the `registry` transport, `--skip-build` needs `--tag`.
Use `--platform` for a VM that is not `linux/amd64`:

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --platform linux/arm64
```

## Skip the backup

Before a deploy that has migrations, a hook makes a database backup.
`--skip-backup` skips it.
The hook still refuses when a migration lacks the `-- spinup:safe` marker.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --skip-backup
```

## Deploy to a protected environment

An environment with `protected = true` asks for confirmation.
In a terminal, type the environment name.
In CI, set `CI` and pass `--yes`, or pass `--confirm-env <name>`.
With no terminal and no CI, the deploy fails before it changes anything.

<!-- doctest: skip -->
```console
$ spinup deploy --env production --yes
```

`spinup deploy unlock --force` asks for the same confirmation.

## Accept a new host key

The first connection to a new VM stops when SSH does not know its host key.
Add `--accept-new-host-key` to trust the key once.
A changed key always fails.

<!-- doctest: skip -->
```console
$ spinup deploy --env staging --accept-new-host-key
```

## The steps of a deploy

| Step | What it does |
| --- | --- |
| `preflight` | Checks the git tree, `spinup check`, `db lint`, the environment, and the secrets. |
| `build` | Builds the `api` and `web` images. The tag is the release ID. |
| (lock) | Takes the lock on the VM. A held lock gives exit code 5. |
| `hooks` | Runs the pre-deploy hooks, for example the backup. |
| `transport` | Moves the images to the VM. |
| `upload` | Writes the release directory and pushes the secrets. |
| `edge` | Updates and starts Caddy and PostgreSQL. |
| `migrate` | Runs the migrations while the old release still serves. |
| `switch` | Starts the new color, checks each replica, switches Caddy, drains the old color, and removes it. |
| `smoke` | Requests `/healthz` and your smoke URLs. Checks that `/version` shows the new commit. |
| `record` | Marks the release as current and keeps the last 5 releases. |
| `post` | Runs the post-release hooks. A failure is a warning, because the release is live. |

An API-only app serves `/version` on `public_url`.
In a full app, Caddy sends `/` to the web app, so the `smoke` step asks the API of the new color through the Caddy container.

## Secrets and files on the VM

The deploy decrypts the secrets on your machine with sops and writes one file for each secret to `/opt/<app>/secrets/<NAME>` with mode `0600`.
It removes files for names that no longer exist.
Secret values never enter an image, a Compose file, `.env`, the journal, or the output.
The file `release.json` keeps only the SHA-256 of each value.
To change a secret without a deploy, read [`spinup secrets push`](secrets.md#push-secrets-to-the-vm).

```
/opt/<app>/
  releases/<release-id>/   compose files, Caddyfile, Caddyfile.d/, .env, upstream.caddy, release.json
  current -> releases/<release-id>
  caddy/                   Caddyfile, Caddyfile.d/, upstream.caddy (the live color)
  postgres/                postgresql.conf and conf.d/
  secrets/<NAME>           one file for each secret
  deploy.lock/             the lock
  deploy.state             the journal of the last deploy
  releases.json            the release history, newest first
```

If a service in `spinup.toml` has `public`, the deploy also writes `Caddyfile.d/spinup-services.caddy` with its routes and restores them on a rollback.
If `deploy/Caddyfile.d/` holds your own file with that name, the `preflight` step fails with a `Try:` line.
See [Sidecar services](../services.md).

## Let the app report its release

Your API can report which release runs.
Mount the endpoint `GET /_deploy/info` in `api/cmd/api/main.go`:

```go
app.WithDeployInfo(deployx.Options{
    MigrationVersion: currentMigrationVersion,
})
```

The response has `version`, `git_sha`, `migration_version`, and `started_at`.
Requests from a private or loopback address need no token.
Any other request, or any request that a proxy forwarded, must send `Authorization: Bearer <token>`.
The token comes from the file that the environment variable `DEPLOYX_TOKEN_FILE` names.

<!-- doctest: skip -->
```console
$ curl -H "Authorization: Bearer $(cat token)" https://app.example.com/_deploy/info
{"version":"...","git_sha":"0123456789ab...","migration_version":"00002","started_at":"2026-10-15T08:00:00Z"}
```

## Reference

| Flag | Meaning | Example |
| --- | --- | --- |
| `--plan` | Print what would change and change nothing. | `spinup deploy --plan` |
| `--resume` | Continue from the first step that is not done. | `spinup deploy --resume` |
| `--allow-dirty` | Deploy with uncommitted changes. | `spinup deploy --allow-dirty` |
| `--allow-destructive` | Deploy migrations that `db lint` reports as unsafe. | `spinup deploy --allow-destructive` |
| `--skip-build` | Use images that exist already. | `spinup deploy --skip-build --tag <id>` |
| `--tag <release-id>` | Use this release ID. It must end with the `HEAD` commit. | `--tag 20261015T080000Z-0123456789ab` |
| `--redeploy` | Reuse a release ID in a new directory. | `spinup deploy --redeploy --skip-build --tag <id>` |
| `--skip-backup` | Skip the pre-deploy backup. | `spinup deploy --skip-backup` |
| `--no-auto-rollback` | Keep an unhealthy new color running. | `spinup deploy --no-auto-rollback` |
| `--health-timeout` | Wait for the new color (default `2m`). | `--health-timeout 5m` |
| `--platform` | Image platform (default `linux/amd64`). | `--platform linux/arm64` |
| `--accept-new-host-key` | Trust the SSH host key of a new VM. | `spinup deploy --accept-new-host-key` |
| `--yes` | Confirm a protected environment in CI. | `spinup deploy --yes` |
| `--force` | With `deploy unlock`, remove the lock. | `spinup deploy unlock --force` |

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| Exit code 5 | Another deploy holds the lock. Run `spinup deploy status`. If that deploy is gone, run `spinup deploy unlock --force`. |
| `git status failed` | The project is not a git repository, or it has no commit. Commit your work first. |
| The deploy fails on `DATABASE_URL` | The environment has no secrets. Run `spinup secrets init --env <name>`, then set the secrets. |
| `host key` error | The VM is new or changed. For a new VM, add `--accept-new-host-key`. |

## See also

- [Deploy to DigitalOcean](../deploy-digitalocean.md) and [Deploy to your own server](../deploy.md)
- [rollback](rollback.md), [secrets](secrets.md), [server](server.md), and [releases](releases.md)
- [Environments](../deploy/environments.md)
- Flag reference: [deploy](../site/commands/deploy.md), [deploy status](../site/commands/deploy-status.md), [deploy unlock](../site/commands/deploy-unlock.md)
- Design record: [deploy strategy](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0003-deploy-strategy.md)
