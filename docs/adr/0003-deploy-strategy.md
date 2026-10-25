# ADR 0003: Deploy strategy (blue/green Compose projects)

Status: accepted (2026-10-15). Unit M5.1.

Evidence: `spikes/bluegreen/run.sh`. The full output of the runs below is in `spikes/bluegreen/results.txt`.

## Decision

**Deploy uses blue/green Compose projects, and a Caddy reload switches the traffic.** This confirms plan critic resolution 9.

- Each release runs as the Compose project `<app>-blue` or `<app>-green`. The deployer starts the idle color, switches Caddy to it, drains the old color, and then removes the old color.
- `spinup rollback` uses the same switch with the colors reversed.
- This ADR replaces `architecture.md` §14 step 8 (`up -d --scale <svc>=2N --no-recreate`). The rolling scale-up is the measured fallback. Use it only if this decision is reversed.

## Pass criterion

Zero failed requests out of 1,000 or more during the switch, in all 3 runs. A failed request is a non-200 answer or a connection error.

Result: **pass.** Run A (the unit check) had zero failures in 3 switch runs and 3 rollback runs. Runs B and C had zero failures in 20 more switches and 20 more rollbacks.

## Method

The images and the health check are superseded by ADR 0019 §4 for Rust apps. The blue/green switch stays.

- Images: `golang:1.27-alpine`, `gcr.io/distroless/static-debian12:nonroot`, and `caddy:2.11.7`, each by the digest in `internal/stackversions/stackversions.yaml`.
- The app (`spikes/bluegreen/app/main.go`) answers `/` with its color and `/healthz` with `ok`. On SIGTERM it calls `http.Server.Shutdown`. The Compose health check runs the app binary with `-health`, because the distroless image has no `curl`.
- Topology: one Docker network `bg-net`. One Caddy container publishes `127.0.0.1:<random port>`. Each color is a Compose project with 2 replicas of service `app` and the network alias `app-<color>`.
- Load: a Go client in a container on `bg-net` (`spikes/bluegreen/app/load/`). It runs 8 workers with no pause, about 5,000 to 9,000 requests each second. It counts every request, including the requests in flight when it stops. By default most requests open a new TCP connection. `BG_REUSE=1` keeps one keep-alive connection for each worker.
- "Requests" counts the requests sent during the switch window. "Failures" counts the whole load run, which also includes 1 s before and 1 s after the window.
- Host: Docker Desktop 27.4.0 (a Linux VM on macOS arm64), Compose v2.31.0.

## Measurements

Run A, the unit check: `bash spikes/bluegreen/run.sh` (3 runs, 20 reloads in each control run, new connections).

| Strategy | Runs | Requests (switch window) | Requests (total) | Failures | Max latency | Switch time (mean / max) |
|---|---|---|---|---|---|---|
| blue/green switch | 3 | 68,898 | 108,208 | 0 | 109 ms | 4,674 / 5,124 ms |
| blue/green rollback | 3 | 75,681 | 125,341 | 0 | 88 ms | 4,324 / 4,711 ms |
| control: 20 Caddy reloads, no switch | 3 | 202,425 | 240,675 | 0 | 150 ms | 9,438 / 9,566 ms |
| rolling (`--scale app=4 --no-recreate`) | 3 | 64,797 | 121,041 | 0 | 654 ms | 3,093 / 3,225 ms |

Run B: `BG_RUNS=10 BG_RELOADS=30` (new connections).

| Strategy | Runs | Requests (switch window) | Failures | Max latency | Switch time (mean / max) |
|---|---|---|---|---|---|
| blue/green switch | 10 | 251,333 | 0 | 117 ms | 4,202 / 4,628 ms |
| blue/green rollback | 10 | 271,950 | 0 | 374 ms | 4,292 / 6,937 ms |
| control: 30 Caddy reloads, no switch | 10 | 856,715 | **8** | 599 ms | 15,534 / 17,057 ms |
| rolling | 10 | 188,033 | 0 | 1,254 ms | 3,510 / 3,783 ms |

Run C: `BG_REUSE=1 BG_RUNS=10 BG_RELOADS=30` (keep-alive connections).

| Strategy | Runs | Requests (switch window) | Failures | Max latency | Switch time (mean / max) |
|---|---|---|---|---|---|
| blue/green switch | 10 | 283,561 | 0 | 197 ms | 4,012 / 5,101 ms |
| blue/green rollback | 10 | 337,778 | 0 | 299 ms | 4,761 / 8,225 ms |
| control: 30 Caddy reloads, no switch | 10 | 1,238,455 | 0 | 139 ms | 13,866 / 14,898 ms |
| rolling | 10 | 245,521 | 0 | 985 ms | 3,085 / 3,263 ms |

- The switch time starts at `compose up` of the new color and ends when `compose down` of the old color returns. Most of it is the start and the health checks of the new color.
- The drain wait in Run A was 1,152 to 1,413 ms.
- In every run, all requests sent before the switch reached the old color, and all requests sent after it reached the new color. The script checks this.
- Blue/green has a lower tail latency than rolling (at most 374 ms against at most 1,254 ms). In rolling, Caddy keeps the addresses of stopped containers until its next DNS refresh.

## The switch that worked

The deployer does these steps in this order:

1. Start the new color and wait for its health checks: `docker compose -p <app>-<new> up -d --wait`.
2. Make sure that each replica passes `/healthz`: `docker exec <id> /app -health`.
3. Write the new `upstream.caddy` to a temporary file. Then rename it over the old file, so that Caddy never reads half a file.
4. Reload Caddy: `docker exec <caddy> caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile`.
5. Drain: poll `GET http://127.0.0.1:2019/reverse_proxy/upstreams` inside the Caddy container. Wait until no address of the old color has `num_requests` above 0. Wait at least 1 s and at most 10 s.
6. Remove the old color: `docker compose -p <app>-<old> down -t 15`. The app finishes its requests in flight on SIGTERM.

The Caddyfile (Caddy admin listens on `localhost:2019` inside the container only):

```
{
	admin localhost:2019
	auto_https off
	grace_period 10s
}

:8080 {
	import /etc/caddy/upstream.caddy
}
```

The `upstream.caddy` file that the deployer writes for the live color:

```
reverse_proxy {
	dynamic a {
		name app-green
		port 8080
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

- `dynamic a` resolves the network alias of the color, so the file does not change when the replica count changes.
- `grace_period 10s` gives the requests that the old config started 10 s to finish after a reload.
- Do not set `fail_duration` (passive health checks). Docker gives the IP addresses of removed containers to new containers. An early spike version with `fail_duration 10s` stalled requests for several seconds. Caddy still marked the reused addresses as failed.
- `dial_timeout` inside `dynamic a` is the timeout for the DNS server. The timeout for the upstream is `transport http { dial_timeout }`. Its default is 3 s. With the default, every request to the address of a stopped container stalled for 3 s.

## Use in the deploy pack (unit M5.4)

The spike did not measure these rules. They follow from the measured pattern.

- Put Caddy and Postgres in a base project (`<app>`) that does not change color. Put `api` and `web` in the color projects. Join them to one shared external network, with the aliases `api-<color>` and `web-<color>`.
- Compose adds the service name (`api`) as an alias on every network that the service joins. Thus on the shared network, `api` resolves to both colors during a switch. Give `web` the color alias of its own API, for example `API_URL=http://api-green:8080`. Do not use `http://api:8080` on the shared network.
- The deployer writes `upstream.caddy` for each switch. It is a deploy artifact, not a seed file. Store a copy in the release directory, so that a rollback can restore it.
- Caddy routes `/api/*` to `api-<color>:8080` and all other paths to `web-<color>:3000`. Each route uses the `upstream.caddy` options above.

## Risks

- **Resources double during the switch.** Both colors run for 3.4 to 8.2 s in the spike. A real switch also includes the image start time. The VM needs memory for two sets of `api` and `web`. `server bootstrap` and `doctor` must take this into account.
- **Postgres is shared by both colors.** Both colors connect during the switch, so the connection pools also double. Set `max_connections` for two pools.
- **Migrations must be backward compatible.** The migrations run before the new color starts (§14 step 7). The old color then runs on the new schema until the switch ends. A rollback also runs the old code on the new schema. Use expand and contract: add in one release, and remove only in a later release. `db lint` enforces the two-release rule for contract steps (critic resolution 11).
- **A Caddy reload can drop a new connection.** In the control runs, Caddy reloads with no switch failed 8 requests in 300 reloads with new connections (Run B). The same test with keep-alive connections failed none in 300 reloads (Run C). An early trial run of the spike (before `results.txt`) had 1 failed request (`EOF`) in 1 blue/green switch. The failures have the same signature: a new connection gets `EOF` or `connection reset by peer` about 1 ms after it opens. The likely cause is that Caddy opens a new listener with `SO_REUSEPORT` on reload and then closes the old listener, which resets the connections in its accept queue. We did not verify this in the Caddy source. With about 5,500 requests each second, most on new connections, the rate was about 1 dropped request in 40 reloads. The risk is lower at real traffic levels. Mitigations: each deploy does one reload only, and browsers retry a request that fails on a reused connection. If this risk becomes unacceptable, rolling is the measured fallback. It had zero failures in 23 runs and does not reload Caddy, but it mixes versions and has higher tail latency.
- **Long requests delay the drain.** Uploads, server-sent events, and WebSockets keep an old upstream busy. The drain waits at most 10 s. Then `compose down -t 15` stops the old color. Caddy `grace_period` and the drain maximum must agree.
- **The host was Docker Desktop.** The target is a Linux VM. The Docker-in-Docker suite (unit M5.8) must run the switch again on Linux.

strategy: bluegreen
