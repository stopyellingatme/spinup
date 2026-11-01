---
title: Telemetry and data handling
parent: Operate
nav_order: 4
---

# Telemetry and data handling

Your generated API sends traces, metrics, and logs to your own collector, and only when you set an endpoint.
The spinup CLI sends no telemetry. This page shows what leaves your app, what is hidden, and how to turn it off.

```console
$ spinup new todo --api-only --module example.com/todo --no-git --non-interactive
...
$ cd todo
$ grep 'telemetry.Setup' api/cmd/api/main.go
...
```

## What leaves your app

| Signal | What it contains | Goes to | Kept for |
| --- | --- | --- | --- |
| Traces | HTTP method, route pattern, status, and timing. Database operations and timing, with no SQL text and no parameters. | Alloy, then Tempo (`full` profile only) | 336 hours (`tempo_retention`) |
| Metrics | Request count, request duration, active requests, and Go runtime numbers. | Alloy, then Prometheus | 30 days (`prometheus_retention`) |
| Logs | Application records and HTTP access records. A record inside a span has `trace_id` and `span_id`. | Alloy, then Loki | 30 days |

Every signal also carries the service name, service version, environment, and host name.
In production the endpoint is `http://alloy:4318` on the private container network. Nothing goes to the internet.
The `lite` profile drops traces at Alloy. See [Monitoring](monitoring.md#choose-a-profile).

HTTP access logs contain the request path. Do not put secrets or personal data in URL paths.

## Turn telemetry on or off

Telemetry is on only when `OTEL_EXPORTER_OTLP_ENDPOINT` has a value.
In production, Compose sets it for the API, the web service, and the jobs worker when the app has the `monitoring` module with the profile `lite` or `full`.
In development, `spinup dev --lgtm` sets it to `http://localhost:4318`. Without that flag, development sends nothing.

To turn it off, remove the variable from the API and restart. With no endpoint, the app installs no exporter and no background worker.
Local logs and the default redaction stay active. A signal-specific endpoint alone does not turn kit telemetry on.

Use a Compose override to remove it in production. Edit `deploy/compose.override.yml`, which is a seed file and stays yours. Repeat the setting for `web` and `worker` if you want them silent too:

```yaml
services:
  api:
    environment:
      OTEL_EXPORTER_OTLP_ENDPOINT: ""
```

Without the `monitoring` module, or with the profile `none`, production Compose sets no endpoint. Remove any endpoint that you added yourself.
Turning export off does not delete data the collector already has. Retention removes it.

To send telemetry to another collector, set the endpoint to its address:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=https://otel.example.com:4318
```

Use HTTPS when the endpoint needs encryption. The exporters read the standard OpenTelemetry variables for headers, certificates, and timeouts.

## Name your service and sample traces

| Variable | Effect | Example |
| --- | --- | --- |
| `OTEL_SERVICE_NAME` | Sets the service name. The default is the app name plus `-api`. | `todo-api` |
| `OTEL_RESOURCE_ATTRIBUTES` | Adds resource attributes. It can override other defaults. Production Compose sets `service.instance.id` to the deploy color. | `deployment.environment=staging` |
| `OTEL_TRACES_SAMPLER` | Chooses the sampler. The default keeps the parent decision and samples new root spans. | `parentbased_traceidratio` |
| `OTEL_TRACES_SAMPLER_ARG` | The sampler argument. | `0.1` |

To keep one trace in ten, set both sampler variables in the API environment:

```sh
OTEL_TRACES_SAMPLER=parentbased_traceidratio
OTEL_TRACES_SAMPLER_ARG=0.1
```

The API accepts the W3C `traceparent`, `tracestate`, and `baggage` headers. Metrics export every 30 seconds.
The API flushes all three signals when it shuts down.

## What is redacted

Two layers hide secrets.

1. The kit `logx` redactor runs in your app, before a record is printed or exported.
2. Alloy runs in the monitoring stack and acts as a second line. See [Monitoring](monitoring.md#redaction).

The lists differ, because each layer runs in a different place. Both are active when you use the monitoring module.

The app layer masks a value when its key contains `password`, `secret`, `token`, `authorization`, `cookie`, `api_key`, or `dsn`, in any letter case.
The value becomes `****`. It also masks passwords in URLs:

```text
before: connecting to postgres://app:s3cret@db/app
after:  connecting to postgres://app:****@db/app
```

The check also reaches nested groups, attributes added with `With`, and the values of a map, `http.Header`, or `url.Values` one level deep.

### Add your own rules

Replace the redactor when your app has its own secret keys. A `nil` argument brings back the default.

```go
import (
	"log/slog"
	"strings"

	"github.com/stopyellingatme/spinup/kit/logx"
)

type myRedactor struct{ logx.Redactor }

func (r myRedactor) Redact(key string, v slog.Value) slog.Value {
	if strings.Contains(strings.ToLower(key), "ssn") {
		return slog.StringValue("****")
	}
	return v
}

func init() { logx.SetRedactor(myRedactor{}) }
```

A replacement redactor does not include the default rules. Add them yourself when you still want them.

### What you must not log

Redaction cannot find every secret in free text or in your own objects. So do not log request bodies, personal data, or secrets under keys that look harmless.
Do not put secrets in resource attributes, span attributes, metric labels, baggage, or URL paths.
Review your own instrumentation before you send data to an outside collector.

## Where data is stored and who sees it

With the `lite` or `full` profile, all telemetry stays on your monitoring host in Loki, Prometheus, and Tempo.
Grafana binds to the loopback address of the VM. Open it with `spinup monitor open --env production`.
Grafana has its own sign-in. The admin password is the `GRAFANA_ADMIN_PASSWORD` secret.
Change the retention with the `[monitoring]` keys in [Monitoring](monitoring.md#retention-and-limits).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| No telemetry arrives. | `OTEL_EXPORTER_OTLP_ENDPOINT` is empty, you set a signal-specific endpoint only, or the app lacks the `monitoring` module. | Set the main endpoint, or add the module. |
| A secret shows in a log line. | Its key is not in the redaction list. | Rename the key, or add a rule with `logx.SetRedactor`. |
| No traces in Grafana. | The profile is `lite`. | Use `full`. |

## See also

- [Monitoring](monitoring.md) for profiles, alerts, and dashboards.
- [Sidecar services](services.md) for traces that cross services.
- [Secrets](deploy/secrets.md) for `GRAFANA_ADMIN_PASSWORD` and other secrets.
