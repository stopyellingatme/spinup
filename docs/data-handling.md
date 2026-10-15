# Application telemetry and data handling

Generated APIs send telemetry only when `OTEL_EXPORTER_OTLP_ENDPOINT` has a value.
The CLI sends no telemetry.
Call `telemetry.Setup` before you create the app or its database pool.
The generated API already does this.

## Signals and destinations

The API sends traces, metrics, and logs through OTLP over HTTP.
The production endpoint is `http://alloy:4318` on the private container network.
Alloy sends logs to Loki and metrics to Prometheus.
The `full` profile also sends traces to Tempo.
The `lite` profile discards traces at Alloy.
Grafana reads these stores through its configured datasources.

HTTP traces include the method, route pattern, response status, and timing.
The API accepts W3C `traceparent`, `tracestate`, and `baggage` headers.
Database spans record operations and timing without SQL text or query parameters.
HTTP metrics include a request count, a duration histogram, and an active request count.
Logs include application records and HTTP access records.
Records with an active span include `trace_id` and `span_id`.

Each signal includes the service name, service version, deployment environment, and host name.
The default service name is the app name plus `-api`.
`OTEL_SERVICE_NAME` overrides this name.
`OTEL_RESOURCE_ATTRIBUTES` adds resource attributes and can override other defaults.
`OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG` control trace sampling.
The default sampler preserves the parent decision and samples new root spans.

The exporters read the standard OTel settings for endpoints, headers, certificates, and timeouts.
Use HTTPS when your endpoint requires transport encryption.
Metrics export every 30 seconds.
Traces and logs export in batches.
The generated API flushes all three signals during shutdown.

## Redaction

The default `logx` redactor masks matching attributes before local output or OTLP export.
A key matches when it contains `password`, `secret`, `token`, `authorization`, `cookie`, `api_key`, or `dsn`.
Matching ignores letter case.
The replacement value is `****`.
The redactor also checks nested slog groups and attributes attached with `With`.

URL passwords are masked in string attributes, error values, and messages.
For example, `postgres://user:password@db/app` becomes `postgres://user:****@db/app`.
The username remains visible.
Applications can replace the redactor with `logx.SetRedactor`.
A nil replacement restores the default redactor.
The monitoring profile also applies redaction in Alloy.

Redaction does not detect every secret in arbitrary text or application objects.
Do not log request bodies, personal data, or secrets under unrelated keys.
Do not place secrets in resource attributes, span attributes, metric labels, baggage, or URL paths.
HTTP access logs contain request paths.
Review application instrumentation before you send data to an external collector.

## Retention and access

The `lite` and `full` profiles store telemetry on the monitoring host by default.
Loki retains logs for 30 days and uses its compactor to remove expired data.
Prometheus retention follows its flags in `monitoring/compose.monitoring.yml`.
Tempo retention follows its configuration under `monitoring/tempo/` in the `full` profile.
The `lite` profile has no trace store.
Changes to storage or retention can change how long exported data remains available.

Grafana binds to the loopback address on the host.
Use `spinup monitor open` to access it through an SSH tunnel.
Grafana uses its own authentication.
Its administrator password is the `GRAFANA_ADMIN_PASSWORD` secret.

## Turn telemetry off

Remove `OTEL_EXPORTER_OTLP_ENDPOINT` from the API environment, then restart the API.
With no endpoint, setup installs no providers, exporters, or background workers.
The app also omits HTTP instrumentation and database instrumentation.
Local application logs and their default redaction remain active.
Setting only a signal-specific endpoint does not enable kit telemetry.

Set the monitoring profile to `none` to omit the monitoring stack.
Remove the endpoint from each application service as well.
Disabling export does not delete data that the collector already received.
Retention continues to govern that data.

This example creates an API with the telemetry call already present:

```console
$ spinup new telemetry-demo --api-only --module example.com/telemetrydemo --no-git --non-interactive
...
$ cd telemetry-demo
$ grep 'telemetry.Setup' api/cmd/api/main.go
...
```
