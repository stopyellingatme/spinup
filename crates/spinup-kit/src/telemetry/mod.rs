//! Traces, metrics, and logs over OTLP HTTP, the HTTP metric layer, the database span layer,
//! and the redacted log bridge (Go `kit/telemetry`, `kit/app/telemetry.go`, and
//! `kit/db/telemetry.go`).
//!
//! # Start
//!
//! Call [`init`] once, in the tokio runtime, before the app starts. Then give the result to
//! [`App::with_telemetry`](crate::app::App::with_telemetry):
//!
//! ```no_run
//! use spinup_kit::app::App;
//! use spinup_kit::config::Base;
//! use spinup_kit::telemetry::{self, Options};
//!
//! # async fn start() -> anyhow::Result<App> {
//! let config: Base = spinup_kit::config::load()?;
//! let telemetry = telemetry::init(&config, &Options::new("todo-api"))?;
//! Ok(App::new(&config).name("todo").with_telemetry(&telemetry))
//! # }
//! ```
//!
//! Without `OTEL_EXPORTER_OTLP_ENDPOINT`, [`init`] starts only the JSON log of
//! [`logx`](crate::logx), and [`App::with_telemetry`](crate::app::App::with_telemetry)
//! changes nothing. With an endpoint, [`init`] exports the three signals over OTLP HTTP
//! (protobuf) to `<endpoint>/v1/traces`, `/v1/metrics`, and `/v1/logs`. It sets the global
//! tracer provider, the global meter provider, and the W3C trace context and baggage
//! propagator. The exit hook of the app flushes and stops the export.
//!
//! # Environment
//!
//! | Variable | Effect |
//! |---|---|
//! | `OTEL_EXPORTER_OTLP_ENDPOINT` | The OTLP HTTP base URL. Empty turns the export off. |
//! | `OTEL_EXPORTER_OTLP_HEADERS` | Request headers, as `key=value,key=value` (values are percent-decoded). |
//! | `OTEL_SERVICE_NAME` | Replaces the service name. |
//! | `OTEL_RESOURCE_ATTRIBUTES` | Resource attributes, as `key=value,key=value`. |
//! | `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | The sampler. The default is `parentbased_always_on`. |
//! | `OTEL_METRIC_EXPORT_INTERVAL` | The metric export interval in milliseconds. The default is 30 000. |
//! | `HOSTNAME` | The `host.name` resource attribute. |
//!
//! # Metric names
//!
//! The HTTP layer records the names of the Go kit. The OTLP to Prometheus conversion (Alloy,
//! or the LGTM image) adds the unit and type suffixes, and `job=<service.name>`. The
//! monitoring pack and the `g dashboard` and `g alert` generators query the Prometheus names.
//!
//! | OTel name | Prometheus name | Labels |
//! |---|---|---|
//! | `http.server.request.duration` (s) | `http_server_request_duration_seconds_{bucket,count,sum}` | `http_request_method`, `http_route`, `http_response_status_code` |
//! | `http.server.request.count` | `http_server_request_count_total` | `http_request_method`, `http_route`, `http_response_status_code` |
//! | `http.server.active_requests` | `http_server_active_requests` | `http_request_method` |
//! | `tokio.runtime.workers` | `tokio_runtime_workers` | |
//! | `tokio.runtime.alive_tasks` | `tokio_runtime_alive_tasks` | |
//! | `tokio.runtime.global_queue_depth` | `tokio_runtime_global_queue_depth` | |
//! | `tokio.runtime.worker.busy_duration` (s) | `tokio_runtime_worker_busy_duration_seconds_total` | |
//! | `tokio.runtime.worker.park_count` | `tokio_runtime_worker_park_count_total` | |
//!
//! The labels use the route pattern (`/api/todos/{id}`), never the concrete path. A request
//! that matches no route has no `http_route`. An unknown method is `_OTHER`. The tokio
//! metrics replace the Go runtime metrics.
//!
//! # Logs
//!
//! The log bridge sends each event at `LOG_LEVEL` or above to the OTel log export. It writes
//! the fields of the open spans and of the event through [`logx::redact`](crate::logx::redact),
//! and masks the URL passwords of the message, as the local JSON log does. Inside a request,
//! each line of the local log gets `trace_id` and `span_id`.
//!
//! # Database spans
//!
//! sqlx has no OpenTelemetry hook. [`db_layer`] turns each `sqlx::query` event into a client
//! span (`SELECT`, `INSERT`, ...) under the current span. The span has no SQL text, as Go
//! `otelpgx` with `WithDisableSQLStatementInAttributes`.
//!
//! # Tests
//!
//! Span tests install a global subscriber. They run only under nextest, which runs each
//! test in its own process (`make rs-test`).

mod app;
mod db;
mod http;
mod log;
mod setup;

pub use db::{DbLayer, SQLX_TARGET, db_layer};
pub use http::{HTTP_DURATION, HTTP_DURATION_BOUNDS, HttpMetrics, correlation_span, http_layer};
pub use log::{LogBridge, log_bridge};
pub use setup::{Options, Providers, Telemetry, init};

use opentelemetry::global::{self, BoxedTracer};
use opentelemetry::metrics::Meter;

/// The meter and tracer scope of the kit.
pub const SCOPE: &str = "spinup-kit";

/// The target of the spans that only the trace export sees (the HTTP server span). The
/// span level is `TRACE`, so the local log skips its fields below `LOG_LEVEL=trace`.
pub const SPAN_TARGET: &str = "spinup_kit::telemetry::span";

/// The target of the spans that carry `trace_id` and `span_id` for the local log. The trace
/// export and the log bridge skip them.
pub const LOG_TARGET: &str = "spinup_kit::telemetry::log";

/// Returns the named meter of the global meter provider (Go `telemetry.Meter`). Before
/// [`init`], or without an endpoint, its instruments record nothing.
pub fn meter(name: &'static str) -> Meter {
    global::meter(name)
}

/// Returns the named tracer of the global tracer provider (Go `telemetry.Tracer`). Before
/// [`init`], or without an endpoint, its spans record nothing.
pub fn tracer(name: &'static str) -> BoxedTracer {
    global::tracer(name)
}

/// The targets whose events and spans never reach the export: the HTTP client and the
/// OTel SDK. An export must not log or trace itself.
pub(crate) fn quiet_target(target: &str) -> bool {
    const QUIET: [&str; 8] = [
        "hyper",
        "hyper_util",
        "reqwest",
        "h2",
        "tower",
        "opentelemetry",
        "opentelemetry_sdk",
        "opentelemetry_otlp",
    ];
    QUIET.iter().any(|q| {
        target == *q
            || target
                .strip_prefix(q)
                .is_some_and(|rest| rest.starts_with("::"))
    })
}
