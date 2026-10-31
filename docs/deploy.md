# Production deploy files

The `deploy` pack gives a new app the files that a VM needs to run it in production.
`spinup new` adds the pack by default. The `--modules` default is `ci,deploy`.
The pack requires the `api` pack. It also works for an API-only app.
To create the server too, use `spinup deploy digital-ocean`. See `docs/deploy-digitalocean.md`.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ ls deploy
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
$ ls deploy/environments deploy/cloud-init
deploy/cloud-init:
user-data.yaml

deploy/environments:
production.toml
staging.toml
```

`compose.dev.yml` and `Dockerfile.api` come from the `api` pack.
`Dockerfile.web` comes from the `web` pack.

## Files

| File | Class | Purpose |
| --- | --- | --- |
| `deploy/compose.yml` | managed | The color project: `api`, `web`, `worker` when jobs are enabled, and the one-shot `migrate`. |
| `deploy/compose.override.yml` | seed | Your own services. `compose.yml` includes this file. |
| `compose.services.yml` | generated | The sidecar services. `spinup deploy` writes it into each release from `spinup.toml`. It is not a template file. See [Sidecar services](services.md). |
| `deploy/compose.edge.yml` | managed | The base project: `caddy` and `postgres`. |
| `deploy/Caddyfile` | managed | The site, the security headers, compression, and the routes. |
| `deploy/Caddyfile.d/` | seed | Your own site directives in `*.caddy` files. |
| `Caddyfile.d/spinup-services.caddy` | generated | The public routes of the sidecar services. `spinup deploy` writes it into each release when a service has `public`. It is not a template file. |
| `deploy/Dockerfile.postgres` | managed | The pinned Postgres image plus the pinned pgBackRest package. |
| `deploy/postgres/postgresql.conf` | managed | The Postgres settings. |
| `deploy/postgres/conf.d/` | seed | Your own Postgres settings in `*.conf` files. |
| `deploy/environments/staging.toml` | scaffold | Smoke URLs, replicas, and resource limits of staging. |
| `deploy/environments/production.toml` | scaffold | Smoke URLs, replicas, and resource limits of production. |
| `deploy/cloud-init/user-data.yaml` | managed | The first boot of a new Ubuntu VM. |

## Blue/green projects

The deploy uses blue/green Compose projects (`docs/adr/0003-deploy-strategy.md`).
Each release runs as the Compose project `<app>-blue` or `<app>-green`.

- `compose.yml` holds the services that change with each release: `api`, `web`, `worker`, and `migrate`.
  Its project name is `<app>-${SPINUP_COLOR}`.
- `compose.edge.yml` holds the services that do not change color: `caddy` and `postgres`.
  Its project name is `<app>`. A color switch never restarts it.
- The base project creates two networks. The color projects join them as external networks.
  - `<app>-app` connects Caddy, `api`, and `web`.
  - `<app>-db` connects Postgres, `api`, and `migrate`. It is internal and has no outbound access.
- On `<app>-app`, the services of a color have the aliases `api-<color>` and `web-<color>`.
  Compose also adds the plain service name `api` on each network, and that name resolves to both colors during a switch.
  For this reason, `web` uses `API_URL=http://api-<color>:8080`, never `http://api:8080`.
- Only Caddy publishes ports (80, 443, and 443/udp). Its `spinup.public: "true"` label tells the Compose security lint that the ports are public on purpose.
  Postgres publishes no port.

`spinup deploy` sets these variables:

| Variable | File | Value |
| --- | --- | --- |
| `SPINUP_COLOR` | `compose.yml` | `blue` or `green` |
| `API_IMAGE`, `WEB_IMAGE` | `compose.yml` | `<app>-api:<release-id>` and `<app>-web:<release-id>`, never `latest` |
| `PUBLIC_ORIGIN` | `compose.yml` | `public_url` of the environment in `spinup.toml` |
| `PUBLIC_HOST` | `compose.edge.yml` | The Caddy site address: the host of `public_url`, or `http://<host>` without TLS |
| `DEPLOY_UID`, `DEPLOY_GID` | both | The IDs of the deploy user on the VM (default `1000`) |
| `API_REPLICAS`, `API_CPUS`, `API_MEMORY` | `compose.yml` | From the environment file |
| `WEB_REPLICAS`, `WEB_CPUS`, `WEB_MEMORY` | `compose.yml` | From the environment file |
| `WORKER_REPLICAS` | `compose.yml` | From `[replicas] worker` in the environment file (default `1`) |
| `POSTGRES_CPUS`, `POSTGRES_MEMORY` | `compose.edge.yml` | From the environment file |

The migrations run before the new color starts:

<!-- doctest: skip -->
```console
$ docker compose -p todo-green -f compose.yml --profile migrate run --rm migrate
```

## Jobs worker

When the `jobs` module is enabled, each color starts a `worker` service from the API image.
The worker runs `/worker`, joins the `app` and `db` networks, and restarts after database failures.
Set `worker` under `[replicas]` in `deploy/environments/<env>.toml` to change its replica count.
The worker has no health check because it has no HTTP endpoint.

## Caddy and the switch

Caddy reads `/etc/caddy`. On the VM, this directory is `/opt/<app>/caddy`.
It holds `Caddyfile`, `Caddyfile.d/`, and `upstream.caddy`.
The mount is a directory mount, so the container sees a file that the deployer renames into place.

`upstream.caddy` is a deploy artifact. It is not in the repository.
`spinup deploy` writes it for the live color, and each release directory keeps a copy for rollback.
Each route of the Caddyfile imports it with two arguments, the service and its port:

```
handle /api/* {
	import upstream.caddy api 8080
}
handle {
	import upstream.caddy web 3000
}
```

An API-only app has one route: `handle { import upstream.caddy api 8080 }`.
For the live color `green`, the deployer writes this file:

```
reverse_proxy {
	dynamic a {
		name {args[0]}-green
		port {args[1]}
		refresh 1s
	}
	lb_policy round_robin
	lb_try_duration 5s
	lb_try_interval 50ms
	transport http {
		dial_timeout 250ms
	}
}
```

- `dynamic a` resolves the color alias, so the file does not change with the replica count.
- `transport http { dial_timeout 250ms }` keeps a request to a stopped container short.
- Do not set `fail_duration`. Docker gives the addresses of removed containers to new containers.
- The global option `grace_period 10s` gives old requests 10 s to finish after a reload.

The `upstream.caddy` file can take a block.
A route can add subdirectives to `reverse_proxy` with `import upstream.caddy api 8080 { ... }`.
The import with two arguments and no block expands as shown above.

## Public service routes

If a `[[services]]` table has `public = "/svc/<name>"`, the deploy writes the managed file `Caddyfile.d/spinup-services.caddy` into the release directory.
The deploy copies it to `/opt/<app>/caddy/Caddyfile.d/` with the other Caddy files, and `caddy validate` checks it.
An app with no public service gets no such file, and its release stays as before.
If `deploy/Caddyfile.d/` holds a file with this name, the preflight fails.
Rename your file.

Each public service gets a `handle_path` route that imports `upstream.caddy` with the name and port of the service.
The route follows the color switch and the rollback.
A request to the bare prefix gets a 308 redirect to the prefix with a final `/`.
This route, for `auth = "none"`, is the file content for a service named `ocr`:

```
redir /svc/ocr /svc/ocr/ 308
handle_path /svc/ocr/* {
	import ../upstream.caddy ocr 8081
}
```

With `auth = "session"`, the route first sends `GET /api/session` to the API of the live color with the cookies of the request.
A 2xx answer sends the request on to the service. Any other answer goes to the client unchanged.
The route also answers 403 to a request that is not GET, HEAD, or OPTIONS when its `Origin` header is not `{scheme}://{host}`.
Before the first switch, the route fails closed with the 503 placeholder.
See [Sidecar services](services.md#public-routes) for the fields and the limits.

The switch steps are in ADR 0003: start the new color with `up -d --wait`, check each replica, rename the new `upstream.caddy` into place, reload Caddy, drain the old color, and remove the old color with `down -t 15`.

## Client addresses

The Go API limits the auth routes for each client address (`login`, `register`, password reset, and the OIDC callback).
It needs the real address of the client, not the address of a proxy.
Caddy sets the `X-Forwarded-For` header to the address of its own peer.
The generated Caddyfile has no `trusted_proxies` setting, so Caddy drops a forged header value from a client.

- A request from the browser to `/api/*` goes through Caddy to the API. The API reads the address from `X-Forwarded-For`.
- A form action of the web app goes from Node to the API. `compose.yml` sets `ADDRESS_HEADER: X-Forwarded-For` and `XFF_DEPTH: "1"` on the `web` service.
  Node then reads the last entry, which Caddy wrote. `handleFetch` in `hooks.server.ts` sends it to the API.
- The API trusts the header only when the peer is in `AUTH_TRUSTED_PROXIES`. The default is the private address ranges.

Without `ADDRESS_HEADER`, the web form requests have no client address.
The API then puts all of them in one shared bucket for each class.
One flood through the web forms can then make the login fail for every web user until the bucket refills.
An old `compose.yml` has this problem. Run `spinup upgrade` to get the new file.

### Client addresses behind a CDN

A CDN has public addresses, so the API does not trust them by default.
Without the steps below, all users share the address of the CDN and one flood can lock them all out.
Do these three steps:

1. In the global options of `deploy/Caddyfile`, trust the CDN ranges.
   Write the ranges of your CDN in place of the placeholder:

   ```
   {
   	servers {
   		trusted_proxies static <CDN ranges>
   	}
   }
   ```

   Caddy then appends the address of the CDN to `X-Forwarded-For` and keeps the client address that the CDN wrote.
   `deploy/Caddyfile` is a managed file, and `Caddyfile.d/*.caddy` cannot hold global options.
   `spinup upgrade` shows a conflict in this file when the template changes. Keep this block when you resolve the conflict.
2. Set `AUTH_TRUSTED_PROXIES` for the `api` service in `deploy/compose.override.yml` to `private` and the CDN ranges.
   For example: `AUTH_TRUSTED_PROXIES: private,<CDN ranges>`.
3. Set `XFF_DEPTH: "2"` for the `web` service in `deploy/compose.override.yml`.
   The last entry is now the CDN. The entry before it is the client.

Check the result with a request that has a forged `X-Forwarded-For` header.
The API must use the address that the CDN wrote, not the forged one.

## Secrets

Compose mounts `/opt/<app>/secrets` read-only at `/run/secrets` in `api`, `migrate`, and `postgres`.
The deploy needs these secrets in `secrets/<env>.enc.yaml`:

| Name | Use |
| --- | --- |
| `DATABASE_URL` | The API reads it through `DATABASE_URL_FILE`. Use the host `postgres`, for example `postgres://app:<password>@postgres:5432/app?sslmode=disable`. |
| `POSTGRES_PASSWORD` | Postgres reads it through `POSTGRES_PASSWORD_FILE` when it creates the database. |

The secret files have mode 0600 and the deploy user owns them.
For this reason, `api`, `migrate`, `caddy`, and `postgres` run as `DEPLOY_UID:DEPLOY_GID`.
`web` runs as the `node` user of its image.

## Monitoring

When the app selects the `monitoring` module, `spinup deploy` starts its rendered stack after `record` and before `post`.
The `post` hooks can then annotate Grafana.
The step uploads `monitoring/` to `/opt/<app>/monitoring.new/` and preserves file permissions.
It updates `/opt/<app>/monitoring/` in place and preserves the inodes of mounted files and directories.
It removes files absent from the upload.

The deployer hashes each service's mounted configuration locally, including custom rules and dashboards.
Each hash enters `.env` and a Compose label.
A changed hash recreates that service.
An unchanged hash does not cause a recreation.
Docker keeps the data in named volumes.

The step runs Compose as project `<app>-monitoring` with `up -d --wait --remove-orphans` and a five-minute timeout.
The deployed API and web services send OTLP to `http://alloy:4318`.
Their service names are `<app>-api` and `<app>-web`.
Both services set `OTEL_RESOURCE_ATTRIBUTES=service.instance.id=${SPINUP_COLOR}`.
This attribute separates the metric series of blue and green during a switch.

The OTEL settings require the `monitoring` module and an active profile.
`spinup new` discards answers for unselected packs.

Preflight requires `GRAFANA_ADMIN_PASSWORD` in the decrypted secrets for the selected environment.
Set it from standard input with `spinup secrets set --env staging GRAFANA_ADMIN_PASSWORD`.
The upload step writes it to `/opt/<app>/secrets/GRAFANA_ADMIN_PASSWORD` with mode 0600.

The monitoring step creates `/opt/<app>/monitoring-secrets/` with mode 0700.
It copies only `GRAFANA_ADMIN_PASSWORD` and the available `SLACK_WEBHOOK_URL`, `SMTP_PASSWORD`, and `POSTGRES_PASSWORD` files.
The VM copies use `install -m 0600`.
Grafana and postgres-exporter mount this directory read-only at `/run/secrets`.
The postgres-exporter uses `DATA_SOURCE_PASS_FILE=/run/secrets/POSTGRES_PASSWORD`.

Secret values never enter the monitoring `.env` file.

The deployer writes `/opt/<app>/monitoring/.env` with mode 0600:

| Key | Source |
| --- | --- |
| `DEPLOY_UID`, `DEPLOY_GID` | `id -u` and `id -g` on the VM |
| `PUBLIC_URL` | `public_url` of the selected environment |
| `HEARTBEAT_URL` | `[monitoring] heartbeat_url` |
| `ALERT_EMAIL_TO` | `[monitoring] alert_email` |
| `SMTP_HOST`, `SMTP_USER`, `SMTP_FROM` | `[monitoring] smtp_host`, `smtp_user`, `smtp_from` |
| `ALERT_NTFY_URL` | `[monitoring] ntfy_url` |
| `PROMETHEUS_RETENTION` | `[monitoring] prometheus_retention` |
| `PROMETHEUS_RETENTION_SIZE` | `[monitoring] prometheus_retention_size` |
| `TEMPO_RETENTION` | `[monitoring] tempo_retention` |
| `ALLOY_CONFIG_HASH` | The files under `monitoring/alloy/` |
| `PROMETHEUS_CONFIG_HASH` | The files under `monitoring/prometheus/` and `monitoring/alerts/` |
| `LOKI_CONFIG_HASH`, `TEMPO_CONFIG_HASH` | The files under `monitoring/loki/` and `monitoring/tempo/`, respectively |
| `GRAFANA_CONFIG_HASH` | The files under `monitoring/grafana/` |
| `BLACKBOX_CONFIG_HASH` | The files under `monitoring/blackbox/` |

The deployer quotes values and omits unset values, so Compose defaults apply.

The profile in `spinup.toml` must match `monitoring_profile` in `spinup.lock`.
If they differ, preflight fails before the deploy changes the VM.
Restore the locked profile, or re-render the monitoring pack to change its services.
The rendered files determine which services run.

The journal records `skipped` when the module is absent, the profile is `none`, or `monitoring/compose.monitoring.yml` is absent.
A monitoring failure fails the deploy after the app release is recorded.
It does not roll back the app.
Correct the monitoring problem, then run `spinup deploy --resume --env staging` to repeat the failed step.

## Hardening

Every service has these settings:

- `read_only: true`, with a `tmpfs` for `/tmp` (and `/var/run/postgresql` for Postgres)
- `cap_drop: [ALL]` and `security_opt: ["no-new-privileges:true"]`
- a non-root user
- a health check (not `migrate`, which exits when the migrations are done)
- log rotation: `json-file` with `max-size: 10m` and `max-file: "3"`

Caddy binds ports 80 and 443 without a capability.
The sysctl `net.ipv4.ip_unprivileged_port_start: 0` applies only to the network namespace of the container.

`spinup doctor` runs the Compose security lint on every `deploy/compose*.yml` file.
To check the rendered files by hand, set the variables and run `docker compose config`:

<!-- doctest: skip -->
```console
$ cd deploy
$ SPINUP_COLOR=blue API_IMAGE=todo-api:test WEB_IMAGE=todo-web:test PUBLIC_ORIGIN=https://todo.example.com docker compose -f compose.yml config -q
$ PUBLIC_HOST=todo.example.com docker compose -f compose.edge.yml config -q
```

## Postgres

`Dockerfile.postgres` starts from the pinned Postgres image and installs the pinned pgBackRest package from apt.postgresql.org.
The image tag contains both pins, for example `todo-postgres:18.6-trixie-pgbackrest2.59.3`.
A new pin gives a new tag, so Compose builds the new image.

Postgres reads `/etc/postgresql/postgresql.conf`. On the VM, this directory is `/opt/<app>/postgres`.
The file sets `max_connections = 200`, because both colors connect during a switch.
Each API process holds a pool of at most 20 connections (`db.DefaultMaxConns`).
A switch runs both colors, so the API pools use at most 2 × `replicas.api` × 20 connections.
With the default of 2 replicas, that is 80. The migrate step and the monitoring and backup tools add a few more.
To change the pool size in production, add `pool_max_conns=<n>` to the `DATABASE_URL` secret.
The production Compose file does not pass `DATABASE_MAX_CONNS` to the containers. That variable is for `spinup dev` and for binaries that you start yourself.
If you raise `replicas.api` or the pool size, keep 2 × replicas × pool below `max_connections`.
It includes `conf.d/` for your own settings. The backups pack sets its archive settings in `deploy/compose.backups.yml`, which the deployer loads after `compose.edge.yml`.

## Environments

`deploy/environments/<env>.toml` holds the deploy settings that are not in `spinup.toml`:

- `smoke_urls`: the paths that the deploy requests after the switch. It always checks `/healthz` and `/version` too.
- `[replicas]`: the number of containers of each service in a color.
- `[resources.<service>]`: `cpus` and `memory` limits for `api`, `web`, and `postgres`, and for each sidecar service. A service with no entry has no limit. See [Sidecar services](services.md).

During a switch, both colors run. The VM needs memory for two sets of `api` and `web`.

## Cloud-init

`deploy/cloud-init/user-data.yaml` prepares a new Ubuntu VM on its first boot:

- the `deploy` user (UID 1000) with an SSH key, in the `docker` group
- no root login and no password login
- Docker from the official Docker repository
- UFW that permits only 22/tcp, 80/tcp, 443/tcp, and 443/udp
- fail2ban for SSH, unattended-upgrades, log rotation, and a 2 GB swap file
- `/opt/<app>/{releases,secrets,backups,data,caddy,postgres}` and `/opt/<app>/data/caddy/{data,config}`

Replace `SPINUP_DEPLOY_SSH_KEY` with the public key of the deploy user before you use the file.
`spinup server bootstrap` does this for you.
The `docker` group is equal to root access on the VM.

## API health check

The `api` health check runs `/api healthcheck` in the container.
The API image is distroless, so it has no shell and no `curl`.
The API binary must supply the `healthcheck` command.
