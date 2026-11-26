# ADR 0037: The operator console

Status: accepted for implementation (2026-11-22)  
Extends: ADR 0009 (deploy), 0010 (monitoring), 0011 (backups), 0013 (modules), and 0035 (agent guidance)  
Source: GitHub issue #54 and jostar findings 27–43, especially 40–43.

## Context

At the first production launch, the operator needed six commands and an SSH tunnel to check one VM.
Grafana was healthy even when its tunnel failed. The alert command hid the names of two firing rules.
Missing worker telemetry looked like an idle worker. A failed metrics write made a successful backup look overdue.

The base project owns Caddy and PostgreSQL. The blue and green projects own the app replicas.
The monitoring project starts after spinup records the release. Its private network carries Prometheus and Loki traffic.
Only Grafana publishes a port on VM loopback. Lite has neither cAdvisor nor the host and PostgreSQL exporters.
The deploy user belongs to the Docker group. That account can effectively administer the host.

## Decision

### 1. Module and lifecycle

The built-in module is `console`. Its pack is `internal/templates/packs/console/`. Its description is `Operator overview of one deployed app.`
Declare `requires = ["deploy"]`. Monitoring enables alerts, log search, and Grafana links. The console also works without monitoring.
API-only, embed, and node apps use the same console. The app login, web build, and jobs module are optional.

Change the base question default from `ci,deploy` to `ci,deploy,console`. Honor an explicit module list, including `--modules ci,deploy`.
The only switch in `spinup.toml` is membership in the top-level `modules` array.
Read `app.name`, the resolved environment and its `public_url`, `monitoring.profile`, and `backups.backend` from existing configuration.
Add no console table, port setting, public URL, or secret name.

| Monitoring selection | Console behavior |
| --- | --- |
| Module absent | Report profile `absent`. Collect host facts. Disable telemetry queries and Grafana links. |
| `none` | Report profile `none`. Apply the same rules as for an absent module. |
| `lite` | Enable alerts, Loki, and five stack dashboards. Obtain host and database facts without exporters. |
| `full` | Add three vendor dashboards. Use the same host sources as lite. |

Existing apps keep their locked answers during upgrade. A project on older packs first upgrades to a template version containing `console`.
Run `spinup module add console --yes --non-interactive`. Review and merge `spinup/module-console`. Deploy once.
The normal selection adds the deploy dependency where necessary. Manifest discovery supplies the registry entry.
The pack adds managed files. The API Dockerfile changes through its existing file class and normal merge.
If the owner ejected that Dockerfile, doctor names the missing build instructions before deploy.
An ejected production `deploy/compose.yml` poses no obstacle. No codemod or generator rewrites existing answers, secrets, or seed files.

### 2. Placement, image, and limits

Generate `api/cmd/console/main.go`. Build the static executable as `/console` in the API image only when the module is selected.
Use the same Go toolchain, kit context, platform, and transport as the API. Obtain the exact API image reference from the recorded release.
The console runs as a separate process. It never starts the app, connects its pool, or calls telemetry setup.
Use `html/template` and standard library HTTP and JSON. A Svelte bundle would add a build and runtime unrelated to operating the VM.

Generate `console/compose.console.yml`. Deploy it to `/opt/<app>/console/compose.console.yml`.
Use Compose project `<app>-console`, service `console`, and default container name `<app>-console-console-1`.
Use entrypoint `["/console","serve"]` and publish `127.0.0.1:7410:7410/tcp`.
Inside the container, listen on `:7410`. Attach only to the dedicated bridge `<app>-console`.
The bridge permits Docker loopback publication. Never attach an app, database, or monitoring service to this bridge except the query relay below.
Caddy at the public edge has no console route.

Run as `65532:65532`. Use a read-only root, `cap_drop: [ALL]`, `no-new-privileges:true`, 32 PIDs, 0.25 CPU, and 64 MiB.
Set `GOMEMLIMIT=40MiB`. Use `restart: unless-stopped` and the existing 10 MiB × 3 log rotation.
Run `/console healthcheck` every 15 seconds with a 3-second timeout and three retries.
It checks only the local HTTP process. Source failures must never restart the service.

With monitoring active, run service `console-query` in the same console project, using the same image and `/console query`.
Its container is `<app>-console-console-query-1`. It joins the console bridge and the existing external network `<app>-monitoring`.
Publish no port. Use port 7411, the same hardening, 0.25 CPU, 48 MiB, and `GOMEMLIMIT=28MiB`.
The relay exposes the two typed reads in section 6 and process health. It has no volume, credential, Docker client, or general proxy.
A separate relay keeps the page and host facts unreachable from the monitoring network.
Disable the relay with no monitoring. A failed relay must leave the overview accessible.
The relay has its own process check, `/console healthcheck query`, which connects to its local port 7411.
Use `GET /healthz` for loopback process checks on both services. The relay health check never contacts a store.

Use `--log-driver none` for the temporary projector.
The long-running containers have a combined limit of 64 MiB without monitoring and 112 MiB with monitoring.
On a 4 GiB VM, 112 MiB is about 2.7% of RAM. The VM gate measures actual idle use.
Sampling adds one temporary container with a 48 MiB limit and a 24 MiB Go heap target.
Measure the 160 MiB combined ceiling and the actual peak during the VM gate.

### 3. Trust and host collection

SSH authenticates the operator. Trust local users of this single-operator VM and workstation.
A public Caddy route would bypass that boundary. SSH also avoids distributing a shared console password. Version 1 has no publication option, second password, cookie, or application session.
Grafana retains its own admin login. The console neither receives nor forwards `GRAFANA_ADMIN_PASSWORD`.
Accept browser Host values only for `127.0.0.1` or `localhost`, with an optional numeric port.
Reject foreign Origin values, including `null`. Ignore forwarding headers. Send no CORS headers.
All public routes are reads. Send `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, and `X-Content-Type-Options: nosniff`.
Send CSP `default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`.
Escape all text with the template engine. Do not use trusted HTML for logs, users, labels, or alerts.

A Docker socket mounted with `:ro` still accepts mutating API requests. Neither service gets that socket.
Neither gets the host root, secrets directory, journal, SSH keys, database credentials, backup repository, or a host namespace.
The web process reads one directory at `/data:ro`. The relay can reach the private monitoring stores.
Treat the relay as trusted query code. Its fixed upstreams and methods constrain callers. It never forwards a write to either store. The underlying stores still trust their network peers.

Embed the fixed POSIX script `internal/consoleinstall/collect.sh` in the CLI. Install it at `/opt/<app>/console/collect.sh`, mode 0700.
It runs as the existing deploy user. Neither templates nor requests supply host code.
The web process cannot start the script, change its inputs, or write a request for it.
Use positional arguments and validated identifiers. Never evaluate file contents as shell code.
Run the image's `/console project` decoder in a disposable container. Give it no network, capability, Docker socket, or writable host mount.
Run as the deploy UID:GID to read the private input directory. Require a nonzero UID.
Bind only that temporary directory at `/input:ro`. Use a read-only root, no new privileges, 32 PIDs, and 0.25 CPU.
Write its JSON output through host stdout redirection. Never execute app image bytes as the deploy user on the host.

Keep temporary probe output in a private directory, mode 0700. Remove it on exit. Disable shell tracing.
Use a 3-second limit per local source and 8 seconds for repository info. Collect independent probes concurrently, at most four.
Kill overdue children. Bound each input to 2 MiB before parsing. An overflow invalidates only that source.
Write only projected fields to `/opt/<app>/console/data/facts.json`. Use a sibling temporary file and rename.
Keep the directory inode stable for the bind mount. Use 0755 for this directory and 0644 for the sanitized file under the 0750 app root.
The host script uses a nonblocking `flock` on `/opt/<app>/console/sample.lock`. Concurrent samplers reuse the current sample.
Never hold the deploy lock for collection.

Sample during deployment. Install user units `spinup-<app>-console-sample.service` and `spinup-<app>-console-sample.timer` in `~/.config/systemd/user/` where the user manager runs.
Use a oneshot service with the absolute script path. Set `OnActiveSec=5s`, `OnUnitActiveSec=30s`, `AccuracySec=1s`, and `WantedBy=timers.target`.
Install atomically, reload the user manager and enable the timer with `--now`. Request neither sudo nor lingering.
Some older VMs and the DinD fixture have no persistent user manager.
Both CLI entry points therefore request one sample over authenticated SSH when the sample exceeds 30 seconds or is missing.
While open, repeat that check every 30 seconds. No HTTP request triggers host commands.
A plain tunnel can show a stale sample while the user timer sleeps. The response always identifies its observation time.

Install the console after `record` and before `monitoring`. Include it in the existing journaled `monitoring` step, even when monitoring itself is disabled.
Keep the step list compatible with older journals. Start the web service alone, then start monitoring, then reconcile the relay.
Use `--no-deps` for independent startup. Remove a previous relay when the profile becomes inactive.
If monitoring fails on first deploy, the console still runs. A failure after record leaves the release live.
Report the failed component and `Try: spinup deploy --resume --env <env>`. Repeated installation replaces the same files, timers, and containers.
After the successful `post` journal entry, request a final sample. This publishes the completed attempt before the first operator visit.
If this last sample fails, keep the previous sample and print `Console sample delayed. Try: spinup status --env <env>`.
In plan mode, print these operations without running them. Rollback switches only the app. The installed console continues reading current VM state.

### 4. Overview v1

`GET /api/v1/overview` returns one UTF-8 JSON object. The page and terminal renderer consume this same representation.
For example, an observed empty queue gives `{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[]}`.
A disabled queue gives `{"state":"disabled","reason":"no_jobs","at":null,"data":null}`.
Use JSON v1 for output. Include every key below. Use UTC RFC3339 timestamps, integral bytes and seconds, and JSON null for unknown scalars.
The following type notation defines the wire format. Brackets mean an array. A question mark permits null.

```text
Overview = {version:1, app:string, env:string, profile:string, observed_at:Time?,
  release:Section<Release?>, containers:Section<[Container]>, alerts:Section<[Alert]>,
  backup:Section<Backup?>, wal:Section<WAL?>, jobs:Section<[Queue]>,
  disk:Section<Space>, memory:Section<Space>, history:Section<[Attempt]>}
Section<T> = {state:string, reason:string?, at:Time?, data:T?}
Release = {id:string, git_sha:string, color:string, created_at:Time, age_seconds:int, previous_id:string?}
Container = {project:string, service:string, name:string, state:string, health:string?}
Alert = {name:string, severity:string?, since:Time?}
Backup = {backend:string, label:string, kind:string, finished_at:Time, size_bytes:int?}
WAL = {state:string, archived_at:Time?, failed_at:Time?, min_segment:string?, max_segment:string?}
Queue = {queue:string, state:string, count:int}
Space = {total_bytes:int, used_bytes:int, available_bytes:int}
Attempt = {id:string, at:Time, user:string?, git_sha:string?, color:string?, result:string}
```

`app`, `env`, and `profile` come from the installed deployment configuration. `observed_at` is the last completed host sample.
Each section carries its own observation time. Host sections share the host sample time. Alerts use the time of the Prometheus read.
A successful empty list is `[]`. A known absence of a current release or a completed backup is `ok` with null data.
Successful sections use `state:"ok", reason:null`. A disabled section uses `disabled`, null data and null time.
Use `no_monitoring` or `profile_none` for alerts according to the selection. Use `no_jobs` for an absent jobs module.
An absent backups module disables both backup and WAL with `no_backups`. With pgdump, only WAL uses `no_wal`.
A failed observation uses `unavailable`, null data, the attempt time, and one reason from `missing`, `timeout`, `invalid`, or `source_down`.
A missing snapshot has null times. Clock skew over five seconds into the future gives `invalid`.
After 90 seconds, mark formerly successful host sections `stale` with reason `sample_expired`. Keep their data and observation time.
A fresh failed probe replaces its old value. Never convert an outage to zero, empty, or healthy.

### 5. Sources and projection

| Section | Input and field rules |
| --- | --- |
| Release | Read `current/release.json` under the app root. Project `release,git_sha,color,created_at`. Check the active color in `caddy/upstream.caddy` with the deployer's blue/green semantics. On disagreement, report `unavailable/invalid` during the switch. Compute age at sample time as nonnegative whole seconds since creation. After rollback this is the release age. |
| Containers | Select Docker containers with exact Compose project labels for `<app>`, `<app>-blue`, `<app>-green`, `<app>-monitoring`, and `<app>-console`. Include exited containers and all replicas. Use `docker ps -a` and formatted inspect of validated IDs. Read only project, service, container name, `State.Status`, and optional `State.Health.Status`. Strip the leading slash. Absent health checks give null. Sort by project, service, name. Removed containers have no row. |
| Alerts | Query Prometheus `GET /api/v1/alerts` through the relay. Keep `state=firing`. Map `labels.alertname`, `labels.severity`, and `activeAt`. Require a name. Retain each firing instance, including Heartbeat. Sort by name, severity, and time. Exclude annotations and other labels. This reports Prometheus rule state before Grafana silences. |
| Backup | Use the backend selected by deploy, default `pgbackrest`. Run only `pgbackrest --stanza=<app> --output=json info` with the existing secret loader inside the base PostgreSQL container. Read the matching stanza. Select the newest successful backup by stop timestamp. Map label, type, stop and logical size. Missing stanza gives a known empty result. Other nonzero repository status means unavailable. |
| Other backends | For `pgdump`, use the validated completed dump filenames, times and sizes used by `internal/backup/pgdump`. For `walg`, use `wal-g backup-list --json --detail` and the existing WAL-G mapping. Missing size gives null. Never start, expire, verify, or prepare a backup. |
| WAL | For physical backends, read `archive_mode`, `last_archived_time`, and `last_failed_time` from PostgreSQL settings and `pg_catalog.pg_stat_archiver`. State precedence is `off` for disabled archive mode, `failing` for a failure newer than the last success, `waiting` before first success, otherwise `archiving`. A first failure counts as failing. For pgBackRest, include the current database's repository archive min/max when available. Missing optional bounds give null. For pgdump use `disabled/no_wal`. |
| Jobs | Execute the fixed query below inside the base PostgreSQL container. Use local socket authentication with `psql -X -w -U app -d app`, read-only transactions and a two-second statement timeout. Use the same restrictions for the archiver query. Export only queue, River state, and count. Missing table with jobs enabled gives `unavailable/missing`. |
| Disk | Use `LC_ALL=C df -Pk /opt/<app>` on the VM. Multiply total, used, and available KiB by 1024. Reserved blocks can make used plus available smaller than total. |
| Memory | Use host `/proc/meminfo`. Multiply `MemTotal` and `MemAvailable` by 1024. Used equals total minus available. Reject negative or inconsistent counters. |
| History | Start with retained entries from `releases.json`. Combine with the last 1 MiB of complete entries in `deploy.state`. Discard a truncated first line and an incomplete last line. Group by valid release ID. Read user, full SHA, color and creation time from the release entry or matching release snapshot. A journal-only attempt uses its first available event time and null missing metadata. |

```sql
SELECT queue, state::text, count(*) FROM public.river_job
GROUP BY queue, state ORDER BY queue COLLATE "C", state::text COLLATE "C";
```

Use bytewise order for all public lists. Return only nonzero queue/state pairs.
Bound the queue result to 512 rows. Fetch one extra row to detect overflow and report `unavailable/invalid`.
Never read job arguments, errors, or payloads. The console gets no PostgreSQL role or connection string.
The existing `backup.prom` records completed metric writes. It cannot establish archive health or replace repository metadata.

Set `release.previous_id` to the entry immediately following the current release in the stored order of `releases.json`.
Use null when either entry is missing or the history cannot be read. Only a fresh, valid previous ID enables the rollback command.

For history, use the latest journal event in file order for each ID.
A failed step gives `failed`. A completed terminal `post` gives `succeeded`. Any other event gives `incomplete`.
A retained release with no matching event gives `recorded`. A later resume can replace a prior failure.
Ignore journal entries with the synthetic ID `rollback`. They cannot identify the target or actor reliably.
Include the restored release in history only once. Never invent a rollback event, user, success, or full SHA.
Sort by descending time, then ascending ID. Return at most 20 rows. Local failures before the deploy lock have no journal entry.
Show neither journal detail nor release secret hashes. Validate release IDs before constructing paths.

### 6. HTTP, logs, and navigation

| Public GET route | Successful response |
| --- | --- |
| `/healthz` | 200, text `ok\n`. The process can serve HTTP. |
| `/` | HTML overview, recent deploy history, tool links, log form, and copyable actions. |
| `/logs` | HTML search and results. Use the same filter decoder as the JSON route. |
| `/assets/console.css` | Embedded stylesheet. No script, CDN, font download, or app resource. |
| `/api/v1/overview` | 200, section 4, including partial results. |
| `/api/v1/logs` | 200, the log document below. |

The relay listens internally at `http://console-query:7411`.
Its only data routes are `GET /v1/alerts` and `GET /v1/logs`. Alerts return the overview's alert section. Logs return the public log document.
The fixed upstreams are `http://prometheus:9090/api/v1/alerts` and `http://loki:3100/loki/api/v1/query_range`.
Validate filters again at the relay. Disable environment proxies and redirects. Never forward headers, cookies, raw URLs, or arbitrary LogQL.
Apply a 4-second deadline, two concurrent queries per process, 2 MiB for any upstream response, and an 8 KiB request target limit. Return 414 for an oversized target.
Return 429 when busy. The page keeps its other tiles when the relay is unavailable.

The public and relay log filters are `service`, `level`, `since`, and `text`.
Default to all services, all levels, `1h`, and an empty literal respectively.
Accept a single service matching `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`.
Accept levels `debug`, `info`, `warn`, and `error`. Accept `15m`, `1h`, `6h`, or `24h`.
Allow at most 256 UTF-8 bytes of text with no control characters. An empty optional field selects its default.
Reject duplicates, unknown keys, raw query expressions, malformed encoding, and unrecognized values.
Only the HTML routes also accept `grafana_port`, an integer from 1024 through 65535, supplied by the CLI.

Build the stream selector with exact equality on `service_name`. For all services use `{service_name=~".+"}`.
Append ` |= <quoted text>` when text is present. Append ` | detected_level=<quoted level>` when level is present.
Use Go `strconv.Quote` for both literals and the service value. Use the pinned Loki's detected severity metadata.
A line with no detected level matches only the unfiltered query. Test this with an actual OTLP log in the gate.
Use `direction=backward`, `limit=101`, and Unix nanoseconds for the absolute inclusive start and end.
Read both ordinary Loki value pairs and entries with structured metadata. Project only the fields below.
Merge streams by descending timestamp, then service, level, and line in byte order. Return 100 lines. Mark `more` when a further match exists.

```text
Logs = {version:1, start:Time, end:Time, query:string, entries:[Line], more:bool, explore_path:string}
Line = {at:Time, service:string, level:string?, line:string}
Error = {error:{code:string, message:string, try:string}}
```

Use RFC3339Nano for log times. The `query` field is the exact LogQL sent.
Read `service_name` and `detected_level` from stream labels or entry metadata. Clip displayed lines to 4096 UTF-8 bytes at a rune boundary.
Apply the Alloy body redaction patterns again before output. Strip terminal controls, including escape sequences.
Search reads the same redacted Loki stream as Grafana. Operators must keep unlabelled credentials out of application messages.
Never log a query string, a log body, a secret value, upstream diagnostics, or a command's raw output.

Construct `explore_path` as `/explore?schemaVersion=1&panes=<encoded JSON>&orgId=1`.
Use pane key `a`, datasource UID `loki`, and one query with `refId:"A"`, datasource `{"type":"loki","uid":"loki"}`, and the exact `expr`.
Put decimal Unix milliseconds in strings `range.from` and `range.to` from the same absolute window.
Encode the JSON as a query parameter once. Widen the end to the next millisecond when necessary.
Use `http://127.0.0.1:<grafana_port>` as the Grafana origin. An absent forward shows `spinup monitor open --env <env>` instead of a dead link.
Grafana authenticates the browser itself.

Use these dashboard paths on that same origin. Lite and full expose `/d/spinup-http-red`, `/d/spinup-go-runtime`, `/d/spinup-caddy`, `/d/spinup-backups`, and `/d/spinup-deploys`.
Full also exposes `/d/rYdddlPWk`, `/d/spinup-containers`, and `/d/000000039`.
Use the resolved app origin for the app link. Accept only absolute HTTP(S) origins without user information, query, or fragment.
Use the fixed documentation prefix `https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/`. This is the value of `render.DocsBase`. The table gives the exact tile captions and commands.
Substitute the validated environment name for `<env>`. Also show the overview command on every overview tile.

| Caption | Related command | Docs path |
| --- | --- | --- |
| Live release | `spinup deploy status --env <env>` | `commands/deploy.md` |
| Containers | `spinup status --env <env>` | `commands/status.md` |
| Firing alerts | `spinup monitor alerts --env <env>` | `monitoring.md` |
| Last backup | `spinup backup status --env <env>` | `commands/backup.md` |
| WAL archive | `spinup status --env <env>` | `commands/backup.md` |
| Job queues | `spinup status --env <env>` | `build/jobs.md` |
| Disk and Memory (two tiles) | `spinup status --env <env>` | `commands/status.md` |
| Deploy history | `spinup releases list --env <env>` | `commands/releases.md` |
| Logs | `spinup monitor logs --env <env> --since <since> --query <quoted LogQL>` | `commands/monitor.md` |
| Grafana | `spinup monitor open --env <env>` | `monitoring.md` |

Use captions `Disk` and `Memory` for the host tiles. Use the app name for the app link.
Use POSIX single-argument quoting for the whole CLI LogQL argument, including embedded apostrophes.
The CLI log command repeats the filter with a relative window. The Explore link preserves the absolute window.
Render the captions `Back up now` and `Roll back to the previous release` beside selectable plain text:

```text
spinup backup now --type full --env <env>
spinup rollback --env <env>
```

Show the backup command only with backups. Show rollback only with a known previous retained release.
The operator runs each command on the workstation. Rollback selects the target under the existing deploy lock and uses protected-environment confirmation.
Standalone `backup now` uses its backend's concurrency rules. It does not acquire the deploy lock. A predeploy backup already runs under that lock.
A typed phrase in a page cannot grant either privilege safely. Version 1 has no web action, confirmation form, mutation route, or dispatch token.

Use this catalog for public errors. JSON uses the shape above. HTML uses the same message with a `Try:` line.
Reject non-GET methods on known paths with 405 and `Allow: GET`. Reject unknown paths with 404.

| Status | Code | Message | Try |
| --- | --- | --- | --- |
| 400 | `invalid_filter` | Invalid console query. | Use the filters shown on the Logs page. |
| 403 | `forbidden` | Console origin refused. | Open the URL printed by spinup console open. |
| 404 | `not_found` | Console route not found. | Open the console overview. |
| 405 | `method_not_allowed` | This console route accepts GET only. | Open the console overview. |
| 409 | `monitoring_disabled` | Log search requires monitoring. | Enable the lite or full monitoring profile and deploy. |
| 414 | `query_too_long` | Console query exceeds 8 KiB. | Shorten the search text. |
| 429 | `busy` | The console query limit is busy. | Retry the query. |
| 503 | `source_down` | The log source did not answer with valid data. | Check spinup monitor status. |

### 7. CLI and checks

Register `spinup console open --env <env> [--no-browser] [--accept-new-host-key]` and `spinup status --env <env> [--json] [--accept-new-host-key]`.
Both accept the existing global project and output flags. Use normal environment resolution.
Reuse the repaired SSH forward lifecycle from `monitor open`, including pinned host keys, readiness, cancellation, and shared-master ownership.
Bind an ephemeral IPv4 loopback port on the workstation.
Accept host keys only through the existing pin or explicit first-use policy. Ignore the host and port in HTTP headers when building links. Forward to VM loopback port 7410.
Verify the HTTP health response before printing `Console: http://127.0.0.1:<port>/` or opening the browser.
With active monitoring, also forward an ephemeral local port to VM loopback port 3000. Verify a Grafana health response.
Append `?grafana_port=<local-port>` to the console URL only after Grafana answers. Its failure never prevents opening the console.
Keep the forwards alive until interrupt. Close only the forwards this invocation owns. Never publish an SSH destination from a request parameter.

Status refreshes as described in section 3 and reads the same overview endpoint over SSH.
Validate version, identity, and shape before writing stdout. JSON emits only the received document and one final newline.
Text starts with `<app> (<env>)` and `Observed: <observed_at>`. Use `unknown` for a null time.
Then print the nine overview sections in section 4 order, one caption per line, followed by indented field rows.
Use the exact captions in section 6, separating Disk and Memory. For field rows use `  <key>=<JSON value>` in schema order.
For an array print one compact JSON object per indented line. For a null or empty success print `  none`.
Append ` [<state>: <reason>]` to a non-ok caption. Show stale data. Suppress rows when unavailable or disabled.
A valid partial overview exits 0. Disabled module, tunnel failure, and incompatible response exit nonzero with no partial JSON.
Use these CLI diagnostics, replacing placeholders with resolved values:

```text
Cannot open console: module console is disabled.
Try: spinup module add console
Cannot read console: the service for <env> did not answer.
Try: spinup doctor --remote --env <env>
Cannot read console: the overview has an incompatible version, identity, or shape.
Try: upgrade the CLI and deploy the console for <env>
```

Preserve SSH host-key diagnostics and their repair instructions. On browser-launch failure, keep the verified URL and tunnel available.
A source outage uses the section contract. It never triggers a direct fallback query from the terminal.

Register `console-wired` as a quick local doctor check and `console-ready` as a remote check.
Both skip with `Console module is disabled.` when absent.
The local check validates the generated main, API Dockerfile build and copy, compose isolation, probe settings, and selected relay profile.
Success reads `Console wiring is present.` Failure reads `Console wiring is unsafe or incomplete: <path>.`
Use `Try: review the console pack and restore or merge <path>`. Inspect rendered semantics, including locally modified files.
The remote check uses its injected SSH runner to read health, the overview identity, and the actual port binding.
Fail on a non-loopback bind, missing service, invalid response, or wrong identity. Report `Console is unreachable, exposed, or incompatible.`
Use `Try: spinup deploy --resume --env <env>`. Warn on stale or unavailable data with `Console source unavailable: <section>.`
Choose the first affected section in schema order. Use `Try: spinup status --env <env>`. Success reads `Console is ready.`

Publish `docs/console.md`, `docs/commands/console.md`, and `docs/commands/status.md`. Link the operator page from monitoring and deploy.
Add both read commands to the pack's agent rows. Use `console_new.txtar`, `console_add.txtar`, and `app_gate_console.txtar`.
The app gate exercises the generated binary. A separate DinD gate exercises VM data sources, both tunnels, and app failure.

## Consequences

The web service survives an app outage and a rollback. A stopped database affects its database tiles.
An operator can distinguish a clean empty result from missing instrumentation or an old observation.
A web compromise exposes the projected facts and bounded log searches. It supplies no host command channel or store credential.
The SSH-only design trusts other local users of the VM and workstation. A multi-tenant host needs a different boundary.
Raw app messages can still contain sensitive prose. Field projection and credential-pattern redaction cannot classify every such message.

Version 1 covers one app on one VM. It excludes public access, console accounts, roles, configuration edits, arbitrary queries, and web mutations.
Follow-up decisions cover delegated operator login, audited and locked actions, durable deployment-attempt history, and multiple hosts.
A background sampler with separate OS credentials needs its own access review. The VM gate records resource use before changing the limits.

## Appendix A. Implementation boundaries

The pack owns three generated files and its agent fragment.
Use `files/api/cmd/console/main.go.tmpl`, `files/console/compose.console.yml.tmpl`, and `fragments/agents-rows.md.tmpl`.
Keep the two Dockerfile instructions conditional on `has "console" .modules`.
Use `CONSOLE_IMAGE` for the resolved image. Give both services `CONSOLE_APP`, `CONSOLE_ENV`, and `CONSOLE_PROFILE`.
Give the page `CONSOLE_ORIGIN`, `CONSOLE_BACKEND`, and `CONSOLE_JOBS`. Backend `none` disables backup probes. Jobs is `true` or `false`.
Deploy writes these values into the console project's `.env` through literal Compose escaping. Reject newlines, NUL, invalid names, and unexpected enum values.
Keep this file outside the data mount. Render the relay and its external network only for lite and full.
Always declare the overlay file. Render only `services: {}` in the overlay for absent monitoring and profile none.
Guard the body with module membership and `index . "monitoring_profile"`. Pack predicates evaluate missing keys even in an unused branch.
Give the relay Compose profile `query`. Ordinary startup names only the web service.
Starting the web service must work before the monitoring network exists. The separate overlay below enforces this order.

Use `console/compose.query.yml` as the separate overlay for the relay and its external network in all active monitoring deployments.
The base file always contains only the web service and the dedicated bridge. The overlay has no standalone project or bridge.
Deploy passes both files only when starting the relay, after monitoring startup. The third generated source is `files/console/compose.query.yml.tmpl`.
This split also permits independent recovery after removal of the monitoring project.

The generated main calls `console.Main(os.Args[1:], os.Stdout, os.Stderr)` from `kit/x/console` and exits with its integer result.
Valid argument lists are `serve`, `query`, `project`, `healthcheck`, and `healthcheck query`. Reject others with exit 2.
The projector reads `/input` and writes an overview with an unqueried alerts section. The page replaces that section on each overview request.
`facts.json` has exactly the overview shape. The page checks version, app and environment before using it.
Reject a snapshot from a different profile. Apply the installed feature selection even when the file is missing or invalid.
Budget 384 KiB for the host document and 128 KiB for the alert section, including JSON overhead.
Limit the final overview to 512 KiB. On section overflow use `unavailable/invalid`.
Limit containers to 512 rows and alerts to 256 instances. Test limits against encoded JSON bytes.
Treat overflow as invalid for that section. Limit user and queue names to 128 UTF-8 bytes. Remove control characters from every displayed string.

The host script takes no arguments for a forced sample, or the single argument `--if-old` for the 30-second check.
Locate its absolute installation directory without using the caller's working directory. Use a deployment-owned file `probe.conf`, mode 0600.
Its seven LF-terminated lines contain app, env, profile, backend, jobs, resolved image, and validated app origin, in that order.
Use `read -r` and quote arguments. Never source this file. Atomically replace it during installation.
Pass the identity and selection to the projector using the same `CONSOLE_*` variables. No secret enters them.
The following basenames define the projector inputs. Each successful probe writes a regular file. A failed probe writes only `<name>.err`.
The error file contains one section reason from section 4. Disabled probes create neither file. An error file takes precedence.
All times refer to the start of the sample, passed as `CONSOLE_OBSERVED_AT` by the sampler.

| Name | Bytes |
| --- | --- |
| `release.json`, `releases.json` | Corresponding deploy JSON. An absent current release gives JSON null. An absent history file gives an empty array. |
| `upstream.caddy`, `deploy.state` | Active upstream text and bounded journal suffix. Missing files give empty input. Use embedded times and retained entries for history, with null fields for unrecorded attempts. |
| `snapshots/<id>.json` | Optional metadata from `releases/<id>/release.json` from the newest 20 valid release directories, ordered by descending ID. Select only IDs matching `deploy.ValidReleaseID`. Reject symlinks and files outside the releases directory. |
| `containers.ndjson` | One JSON object per container in the wire shape. Use Docker's format helpers for individual strings. |
| `backup.json` | Native pgBackRest or WAL-G JSON. For pgdump, supply an array of completed dumps with `label`, `finished_at`, and `size_bytes`. |
| `wal.json` | One object with `archive_mode`, `archived_at`, and `failed_at`. SQL null gives JSON null. |
| `jobs.json` | JSON array of Queue objects from section 5. Order the aggregate explicitly. |
| `disk.txt`, `memory.txt` | POSIX df output and host meminfo text. |

For journal-only rows, use matching release metadata when the upload completed. Keep unknown metadata null for earlier failures.
Never use an identifier from a request to select a host file. Skip the temporary projector container in its own sample.
The JSON response uses one grammar across host, relay, page and CLI. New fields may be additive. A changed meaning needs a new API version.
