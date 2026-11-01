---
title: Deploy to your own server
parent: Deploy
nav_order: 2
---

# Deploy to your own server

This page takes you from a bare Debian or Ubuntu VM to a running app, and then explains the files that the deploy uses.
If you want spinup to create the server too, read [Deploy to DigitalOcean](deploy-digitalocean.md) or [Hetzner Cloud with OpenTofu](infra.md).

<!-- doctest: skip -->
```console
$ spinup server bootstrap --env staging
$ spinup secrets init --env staging --recovery-recipient age1...
$ spinup deploy --env staging
...
Deployed release 20261015T080000Z-0123456789ab to staging (green color)
```

## Deploy for the first time

Do these steps once for each environment.

1. **Describe the server.** Add the environment to `spinup.toml`. For the first step, `user` is an account that has SSH access and passwordless sudo, such as `root` or `ubuntu`:

   ```toml
   [env.staging]
   host = "203.0.113.10"
   user = "root"
   public_url = "https://staging.example.com"
   ```

2. **Point DNS at the server.** Create an A record for the host name of `public_url`. Caddy needs it to get a certificate.
3. **Prepare the VM.** The command installs Docker, a firewall, and a `deploy` user with your SSH key (default `~/.ssh/id_ed25519.pub`).
   The first connection to a new VM stops on an unknown SSH host key, so pass `--accept-new-host-key`:

   ```sh
   spinup server bootstrap --env staging --ssh-key ~/.ssh/id_ed25519.pub --accept-new-host-key
   ```

   Before you trust the key, compare its fingerprint with the one in the console of your provider.
   To read it, run `ssh-keyscan -t ed25519 203.0.113.10 | ssh-keygen -lf -`.
   Then change `user` to `"deploy"` in `spinup.toml`. Every later command, including the deploy, connects as `deploy`.

4. **Create the secrets.** Make a recovery key with `age-keygen`, then:

   ```sh
   spinup secrets init --env staging --recovery-recipient age1...
   printf '%s' 'S3cret!' | spinup secrets set --env staging POSTGRES_PASSWORD
   printf '%s' 'postgres://app:S3cret!@postgres:5432/app?sslmode=disable' | spinup secrets set --env staging DATABASE_URL
   ```

5. **Check the settings** in `deploy/environments/staging.toml`. See [Environments](deploy/environments.md).
6. **Deploy:**

   ```sh
   spinup deploy --env staging
   ```

On the first connection to a new VM, spinup refuses an unknown SSH host key.
Step 3 trusts the key once. A changed key always fails.

## Deploy again

Commit your work and run the same command. Each deploy builds a new release.

| I want to ... | Run |
| --- | --- |
| See what would change | `spinup deploy --env staging --plan` |
| Continue a deploy that stopped | `spinup deploy --env staging --resume` |
| Go back to the previous release | `spinup rollback --env staging` |
| Go back to a stored release | `spinup rollback --env staging --to <release-id>` |
| See the live release and the lock | `spinup deploy status --env staging` |
| List stored releases | `spinup releases list --env staging` |
| Remove a stale deploy lock | `spinup deploy unlock --force --env staging` |

A rollback switches traffic back. It does not undo database migrations.
The [deploy guide](commands/deploy.md#reference) has the table of all flags, such as `--allow-dirty`, `--skip-build`, and `--skip-backup`.
The [rollback guide](commands/rollback.md) covers the rollback.

## What the deploy pack generates

`spinup new` adds the `deploy` module by default. It needs the `api` module and works for an API-only app.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ ls todo/deploy
Caddyfile
Caddyfile.d
Dockerfile.api
Dockerfile.postgres
cloud-init
compose.dev.yml
compose.edge.yml
compose.override.yml
compose.yml
environments
postgres
```

`compose.dev.yml` and `Dockerfile.api` come from the `api` module. `Dockerfile.web` comes from the `web` module.

| File | Class | Purpose |
| --- | --- | --- |
| `deploy/compose.yml` | managed | One version of the app: `api`, `web`, `worker` when jobs are on, and the one-shot `migrate`. |
| `deploy/compose.edge.yml` | managed | The parts that never restart on a switch: `caddy` and `postgres`. |
| `deploy/compose.override.yml` | seed | Your own services. `compose.yml` includes it. |
| `deploy/Caddyfile` | managed | The site, the security headers, compression, and the routes. |
| `deploy/Caddyfile.d/` | seed | Your own site directives in `*.caddy` files. |
| `deploy/Dockerfile.postgres` | managed | The pinned Postgres image plus the pinned pgBackRest package. |
| `deploy/compose.backups.yml` | managed | With the `backups` module: the archive settings of Postgres (`compose.backups-walg.yml` for WAL-G). The deploy loads it after `compose.edge.yml`. |
| `deploy/postgres/postgresql.conf` | managed | The Postgres settings. |
| `deploy/postgres/conf.d/` | seed | Your own Postgres settings in `*.conf` files. |
| `deploy/environments/<env>.toml` | scaffold | Smoke URLs, replicas, and limits of one environment. |
| `deploy/cloud-init/user-data.yaml` | managed | The first boot of a new Ubuntu VM. |

A managed file follows spinup upgrades. A seed file is yours after the first write.
`spinup deploy` also writes two files into each release. You do not edit them:
`compose.services.yml` for [sidecar services](services.md), and `Caddyfile.d/spinup-services.caddy` for their public routes.

## Add your own service

Put it in `deploy/compose.override.yml`. The file runs in each color project, so use it for stateless helpers.
Copy the hardening of `compose.yml`. `spinup doctor` lints this file and rejects an image that is not pinned by digest:

```yaml
services:
  helper:
    image: example/helper:1.2.3@sha256:<digest>
    user: "1000:1000"
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    networks: [app]
    restart: unless-stopped
```

For a service that has its own build and public route, use a [sidecar service](services.md) instead.

## Add a Caddy rule

Put a directive in a `*.caddy` file in `deploy/Caddyfile.d/`.
Caddy imports it inside the site block:

```
redir /old /new 301
header /static/* Cache-Control "public, max-age=3600"
```

You cannot name a file `spinup-services.caddy`. The deploy preflight fails if you do.

## Change a Postgres setting

Put it in a `*.conf` file in `deploy/postgres/conf.d/`:

```
shared_buffers = 512MB
work_mem = 16MB
```

### Connection limits

Postgres allows 200 connections (`max_connections`), because both versions connect during a switch.
Each API process keeps a pool of at most 20 connections.
During a switch, the pools use at most 2 x `replicas.api` x 20 connections. With 2 replicas, that is 80.
If you raise the replicas or the pool size, keep that number below 200.

To change the pool size in production, add `pool_max_conns` to the `DATABASE_URL` secret:

```
postgres://app:pw@postgres:5432/app?sslmode=disable&pool_max_conns=10
```

The production Compose file does not pass `DATABASE_MAX_CONNS` to containers.
That variable is for `spinup dev` and binaries that you start yourself.

## Secrets

The deploy needs two secrets in `secrets/<env>.enc.yaml`:

| Name | Use |
| --- | --- |
| `DATABASE_URL` | The API reads it. Use the host `postgres`, for example `postgres://app:<password>@postgres:5432/app?sslmode=disable`. |
| `POSTGRES_PASSWORD` | Postgres reads it when it creates the database. |

The deploy mounts `/opt/<app>/secrets` read-only at `/run/secrets` in `api`, `migrate`, and `postgres`.
The files have mode 0600, so those services and `caddy` run as the deploy user.
`web` runs as the `node` user of its image.
See [Secrets](deploy/secrets.md).

## Jobs worker

When the `jobs` module is on, each version also starts a `worker` from the API image.
It restarts after database failures and has no health check, because it has no HTTP endpoint.
Set `worker` under `[replicas]` in the environment file to change how many run.
See [Background jobs](build/jobs.md).

## Monitoring on deploy

If the app has the `monitoring` module, `spinup deploy` starts the monitoring stack after the release is recorded.
The deploy needs `GRAFANA_ADMIN_PASSWORD` in the secrets of the environment:

```sh
printf '%s' 'a-long-password' | spinup secrets set --env staging GRAFANA_ADMIN_PASSWORD
```

The `profile` in `spinup.toml` must match `monitoring_profile` in `spinup.lock`.
If they differ, the preflight fails before the deploy changes the VM.
If monitoring fails, the deploy fails after the app is live. The app is not rolled back.
Fix the problem and run `spinup deploy --resume`.
See [Monitoring](monitoring.md) for profiles, alerts, and dashboards.

## Behind a CDN

The API limits sign-in attempts for each client address, so it must see the real address of the client.
By default, Caddy writes the `X-Forwarded-For` header from its own peer, and the API trusts private ranges only.
This works with no CDN.

A CDN has public addresses. Without these three steps, all users share the address of the CDN, and one flood can lock them all out:

1. In the global options of `deploy/Caddyfile`, trust the CDN ranges:

   ```
   {
   	servers {
   		trusted_proxies static <CDN ranges>
   	}
   }
   ```

   `Caddyfile.d/*.caddy` cannot hold global options.
   `spinup upgrade` shows a conflict in this managed file when the template changes. Keep this block when you resolve it.
2. In `deploy/compose.yml`, trust the CDN. Add one line to the `environment` of the `api` service, and change `XFF_DEPTH` of the `web` service:

   ```yaml
   services:
     api:
       environment:
         AUTH_TRUSTED_PROXIES: private,<CDN ranges>
     web:
       environment:
         XFF_DEPTH: "2"
   ```

   Do not put these lines in `compose.override.yml`. `compose.yml` includes that file, and Compose rejects a service that both files define.
   `compose.yml` is a managed file, so `spinup upgrade` shows a conflict when the template changes. Keep your lines when you resolve it.
3. Check it. Send a request with a forged `X-Forwarded-For` header. The API must use the address that the CDN wrote.

See [Sign-in security](build/auth-security.md) for the rate limits.

## Hardening

Every service has these settings:

- `read_only: true`, with a `tmpfs` for `/tmp`
- `cap_drop: [ALL]` and `no-new-privileges`
- a non-root user
- a health check (not `migrate`, which exits when the migrations finish)
- log rotation: `json-file`, 10 MB, 3 files

Only Caddy publishes ports (80, 443, and 443/udp). Postgres publishes none.
`spinup doctor` runs a Compose security lint on every `deploy/compose*.yml` file.
To check the rendered files by hand:

<!-- doctest: skip -->
```console
$ cd deploy
$ SPINUP_COLOR=blue API_IMAGE=todo-api:test WEB_IMAGE=todo-web:test PUBLIC_ORIGIN=https://todo.example.com docker compose -f compose.yml config -q
$ PUBLIC_HOST=todo.example.com docker compose -f compose.edge.yml config -q
```

## The first boot of a VM

`deploy/cloud-init/user-data.yaml` prepares a new Ubuntu VM. `spinup server bootstrap` does the same work on an existing VM.

- the `deploy` user (UID 1000) with your SSH key, in the `docker` group
- no root login and no password login
- Docker from the official Docker repository
- UFW that permits only 22/tcp, 80/tcp, 443/tcp, and 443/udp
- fail2ban, unattended upgrades, log rotation, and a 2 GB swap file
- the folders `/opt/<app>/{releases,secrets,backups,data,caddy,postgres}`

To use the file by hand, replace `SPINUP_DEPLOY_SSH_KEY` with the public key of the deploy user.
The `docker` group is equal to root access on the VM.

## How the blue/green switch works

You do not need this to deploy. Read it when you debug a switch.
The [deploy guide](commands/deploy.md#the-steps-of-a-deploy) lists every step of a deploy. This section covers only the switch.

The app runs as the Compose project `<app>-blue` or `<app>-green`. These are the two colors.
A third project, `<app>`, holds `caddy` and `postgres`, which never restart on a switch.
The color projects join two networks of that base project:
`<app>-app` connects Caddy, `api`, and `web`.
`<app>-db` connects Postgres, `api`, and `migrate`. It is internal and has no outbound access.

A switch does these things:

1. Run the migrations with the new image while the old color serves traffic.
2. Start the new color, and wait until every replica is healthy and `/readyz` answers.
3. Rename a new `upstream.caddy` into place and reload Caddy.
4. Drain the old color and remove it.

Caddy reads `upstream.caddy` for the live color. Each route of the Caddyfile imports it with a service and a port:

```
handle /api/* {
	import upstream.caddy api 8080
}
```

The deploy writes `upstream.caddy` and keeps a copy in each release for rollback.
To add options to the proxy for one route, add a block: `import upstream.caddy api 8080 { ... }`.

`web` uses `API_URL=http://api-<color>:8080` and never `http://api:8080`, because the plain name `api` resolves to both colors during a switch.

`spinup deploy` sets the variables of the Compose files. You never set them by hand:
`SPINUP_COLOR`, `API_IMAGE`, `WEB_IMAGE`, `PUBLIC_ORIGIN`, `PUBLIC_HOST`, `DEPLOY_UID`, `DEPLOY_GID`, the `*_REPLICAS`, `*_CPUS`, and `*_MEMORY` values from the environment file.

For a public route of a sidecar service, the deploy writes a `handle_path` route in `Caddyfile.d/spinup-services.caddy`.
See [Sidecar services](services.md).

## The API health check

The `api` health check runs `/api healthcheck`.
The API image has no shell and no `curl`, so the API binary must supply the `healthcheck` command.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `spinup.toml was not found` | You run the command outside a project. | Run it in the project, or pass `--dir`. |
| The deploy fails for `DATABASE_URL` | The secret is missing. | Set `DATABASE_URL` and `POSTGRES_PASSWORD`. |
| The new color is not healthy | The app crashes on start. | Read `spinup server logs`. The old color stays live. Add `--no-auto-rollback` to keep the new color for debugging. |
| A deploy lock is held | Another deploy runs, or one was cut off. | Wait. If it is stale, run `spinup deploy unlock --force`. |
| The monitoring profile does not match | `spinup.toml` differs from `spinup.lock`. | Restore the locked profile. |

## See also

- [Deploy overview](deploy/index.md)
- [Environments](deploy/environments.md) and [Secrets](deploy/secrets.md)
- [The `server` command guide](commands/server.md)
- [Flag reference of `spinup deploy`](site/commands/deploy.md)
- Design record: [Deploy strategy](https://github.com/stopyellingatme/spinup/blob/main/docs/adr/0003-deploy-strategy.md)
