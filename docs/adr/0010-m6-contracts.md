# ADR 0010: M6 contracts (monitoring)

Status: accepted (2026-10-15)

The M6 units run in parallel. These decisions fix the contracts between them.

## 1. Telemetry from the app (kit `telemetry`, unit M6.1)

- All OpenTelemetry imports live in kit `telemetry` (architecture §7). Apps call `telemetry.Setup(ctx, telemetry.Options{...})` once in `main.go`, and kit `app` wraps the handler with `otelhttp` and the pool with `otelpgx` when telemetry is on.
- Configuration is the standard OpenTelemetry environment: `OTEL_EXPORTER_OTLP_ENDPOINT` (default off: no endpoint, no export), `OTEL_SERVICE_NAME` (default the app name plus `-api`), `OTEL_RESOURCE_ATTRIBUTES`, and `OTEL_TRACES_SAMPLER`/`OTEL_TRACES_SAMPLER_ARG`. OTLP over HTTP (port 4318).
- Logs: the `otelslog` bridge, and every `slog` record carries `trace_id` and `span_id` when a span is active. The kit `logx` redactor (registered by M6.1 in `kit/logx/redact.go`) masks values whose keys look secret before any exporter sees them.

## 2. The monitoring pack (unit M6.2)

- A module pack `monitoring` (`requires = ["deploy"]`). The `monitoring` answer in `spinup.toml` (`[monitoring] profile = "lite" | "full" | "none"`) picks the services.
- Files under `monitoring/`: `compose.monitoring.yml` (managed, Compose project `<app>-monitoring`, joined to the `<app>-app` network so it reaches `api`, `web`, and Caddy), `alloy/config.alloy`, `loki/`, `tempo/`, `prometheus/` (scrape config and `rules/*.yml`), `blackbox/`, and `grafana/provisioning/{datasources,dashboards,alerting}/` (all managed); `grafana/dashboards/stack/` (managed: the vendored community dashboards in `stack/vendor/`, from a main-loop fragment, plus spinup's RED, Go runtime, Caddy, backups, and deploys dashboards); `grafana/dashboards/custom/` and `alerts/custom.yml` (seed: the CLI never touches them).
- Profiles: `lite` is Alloy, Prometheus, Loki, and Grafana (no Tempo, so traces are dropped at Alloy). `full` adds Tempo, node-exporter, cAdvisor, postgres-exporter, and the blackbox exporter. `none` renders nothing.
- The app sends OTLP to `http://alloy:4318`. No collector port is published. Grafana binds `127.0.0.1:3000` on the VM; `spinup monitor open` reaches it through an SSH tunnel (critic resolution 13). Grafana's admin password is a secret (`GRAFANA_ADMIN_PASSWORD`).
- Datasource UIDs are fixed: `prometheus`, `loki`, `tempo` (the vendored dashboards use `prometheus`).
- The heartbeat: `[monitoring] heartbeat_url` in `spinup.toml`; Alloy or a Prometheus rule plus Alertmanager pings it, so a dead VM is noticed from outside.

## 3. Commands (unit M6.3)

`spinup monitor open|status|logs|trace|alerts|test-alert|silence|dashboards sync` run over the M5 SSH layer (`internal/remote`), never by publishing ports. `monitor` registers a `PostRelease` deploy hook that posts a Grafana annotation for each release.

## 4. Web tracing (unit M6.4)

SvelteKit server tracing with `@opentelemetry/sdk-node`, the OTLP HTTP trace exporter, and `@opentelemetry/instrumentation-undici` (ADR 0002 rule 8), in `web/src/instrumentation.server.ts`, on when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. The packages are in the web `package.json` (main loop).
