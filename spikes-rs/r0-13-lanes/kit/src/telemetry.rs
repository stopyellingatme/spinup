//! Traces, metrics, and logs over OTLP HTTP, and the HTTP metric layer, as
//! the Go `kit/telemetry` package.
//!
//! Without `OTEL_EXPORTER_OTLP_ENDPOINT`, [`init`] starts only the JSON log
//! and the HTTP layer records into the no-op meter.
//!
//! Metric names: the layer records the OTel semantic convention histogram
//! `http.server.request.duration` (unit `s`) with `http.request.method`,
//! `http.route`, and `http.response.status_code`. The OTLP to Prometheus
//! conversion (Alloy, or the LGTM image) makes it
//! `http_server_request_duration_seconds_{bucket,count,sum}` with the labels
//! `http_request_method`, `http_route`, `http_response_status_code`, and
//! `job=<service.name>`. The monitoring pack queries these names.

use std::time::{Duration, Instant};

use axum::extract::{MatchedPath, Request, State};
use axum::http::HeaderMap;
use axum::middleware::Next;
use axum::response::Response;
use opentelemetry::metrics::{Histogram, Meter, UpDownCounter};
use opentelemetry::propagation::Extractor;
use opentelemetry::trace::TracerProvider as _;
use opentelemetry::{KeyValue, global};
use opentelemetry_appender_tracing::layer::OpenTelemetryTracingBridge;
use opentelemetry_otlp::{LogExporter, MetricExporter, SpanExporter, WithExportConfig};
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::logs::SdkLoggerProvider;
use opentelemetry_sdk::metrics::SdkMeterProvider;
use opentelemetry_sdk::propagation::TraceContextPropagator;
use opentelemetry_sdk::runtime;
use opentelemetry_sdk::trace::SdkTracerProvider;
use tracing::Instrument;
use tracing_opentelemetry::OpenTelemetrySpanExt;
use tracing_subscriber::filter::{LevelFilter, Targets};
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::{EnvFilter, Layer};

use crate::config::Config;

/// The meter and tracer scope of the kit.
pub const SCOPE: &str = "spinup-kit";

/// The OTel name of the HTTP duration histogram.
pub const HTTP_DURATION: &str = "http.server.request.duration";

/// The bucket bounds of the HTTP duration histogram, in seconds. They are
/// the semantic convention bounds (the Go otelhttp bounds).
pub const HTTP_DURATION_BOUNDS: [f64; 14] = [
    0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0,
];

/// The resource defaults. The OTel environment variables
/// (`OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`) override them.
#[derive(Debug, Clone)]
pub struct Options {
    /// The service name, the app name plus `-api`. It becomes the
    /// Prometheus `job` label.
    pub service_name: String,
    /// The app release.
    pub service_version: String,
    /// The deployment environment.
    pub environment: String,
    /// The OTLP HTTP base URL. `None` reads `OTEL_EXPORTER_OTLP_ENDPOINT`.
    pub endpoint: Option<String>,
    /// The metric export interval: `OTEL_METRIC_EXPORT_INTERVAL` (in ms),
    /// default 30 s as in Go.
    pub metric_interval: Duration,
}

impl Options {
    /// Options for `service_name` with the defaults.
    pub fn new(service_name: impl Into<String>) -> Self {
        Self {
            service_name: service_name.into(),
            service_version: String::new(),
            environment: String::new(),
            endpoint: None,
            metric_interval: std::env::var("OTEL_METRIC_EXPORT_INTERVAL")
                .ok()
                .and_then(|v| v.parse().ok())
                .map_or(Duration::from_secs(30), Duration::from_millis),
        }
    }

    fn endpoint(&self) -> Option<String> {
        self.endpoint
            .clone()
            .or_else(|| std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT").ok())
            .filter(|e| !e.trim().is_empty())
    }
}

/// The three providers. They export over OTLP HTTP (protobuf) on tokio tasks.
#[derive(Debug, Clone)]
pub struct Providers {
    /// The tracer provider.
    pub tracer: SdkTracerProvider,
    /// The meter provider.
    pub meter: SdkMeterProvider,
    /// The logger provider.
    pub logger: SdkLoggerProvider,
}

impl Providers {
    /// Builds the exporters and providers for `endpoint` (such as
    /// `http://lgtm:4318`). Call it inside a tokio runtime.
    pub fn new(opts: &Options, endpoint: &str) -> anyhow::Result<Self> {
        let base = endpoint.trim_end_matches('/');
        let mut attrs = Vec::new();
        if !opts.service_version.is_empty() {
            attrs.push(KeyValue::new(
                "service.version",
                opts.service_version.clone(),
            ));
        }
        if !opts.environment.is_empty() {
            attrs.push(KeyValue::new(
                "deployment.environment.name",
                opts.environment.clone(),
            ));
        }
        let resource = Resource::builder()
            .with_service_name(opts.service_name.clone())
            .with_attributes(attrs)
            .build();
        let spans = SpanExporter::builder()
            .with_http()
            .with_endpoint(format!("{base}/v1/traces"))
            .build()
            .map_err(|e| setup_error("trace exporter", &e))?;
        let metrics = MetricExporter::builder()
            .with_http()
            .with_endpoint(format!("{base}/v1/metrics"))
            .build()
            .map_err(|e| setup_error("metric exporter", &e))?;
        let logs = LogExporter::builder()
            .with_http()
            .with_endpoint(format!("{base}/v1/logs"))
            .build()
            .map_err(|e| setup_error("log exporter", &e))?;
        use opentelemetry_sdk::logs::log_processor_with_async_runtime::BatchLogProcessor;
        use opentelemetry_sdk::metrics::periodic_reader_with_async_runtime::PeriodicReader;
        use opentelemetry_sdk::trace::span_processor_with_async_runtime::BatchSpanProcessor;
        let tracer = SdkTracerProvider::builder()
            .with_resource(resource.clone())
            .with_span_processor(BatchSpanProcessor::builder(spans, runtime::Tokio).build())
            .build();
        let meter = SdkMeterProvider::builder()
            .with_resource(resource.clone())
            .with_reader(
                PeriodicReader::builder(metrics, runtime::Tokio)
                    .with_interval(opts.metric_interval)
                    .build(),
            )
            .build();
        let logger = SdkLoggerProvider::builder()
            .with_resource(resource)
            .with_log_processor(BatchLogProcessor::builder(logs, runtime::Tokio).build())
            .build();
        Ok(Self {
            tracer,
            meter,
            logger,
        })
    }

    /// The tracing layers: spans to OTel traces, events to OTel logs. The log
    /// bridge drops the events of the HTTP client, so an export does not log
    /// itself.
    pub fn layers<S>(&self) -> impl Layer<S> + Send + Sync + 'static
    where
        S: tracing::Subscriber
            + for<'a> tracing_subscriber::registry::LookupSpan<'a>
            + Send
            + Sync
            + 'static,
    {
        let quiet = Targets::new()
            .with_default(LevelFilter::TRACE)
            .with_target("hyper", LevelFilter::OFF)
            .with_target("hyper_util", LevelFilter::OFF)
            .with_target("reqwest", LevelFilter::OFF)
            .with_target("h2", LevelFilter::OFF)
            .with_target("opentelemetry", LevelFilter::OFF)
            .with_target("opentelemetry_sdk", LevelFilter::OFF)
            .with_target("opentelemetry_otlp", LevelFilter::OFF);
        tracing_opentelemetry::layer()
            .with_tracer(self.tracer.tracer(SCOPE))
            .and_then(OpenTelemetryTracingBridge::new(&self.logger).with_filter(quiet))
    }

    /// Flushes and stops the three providers. It blocks, so it runs on a
    /// blocking thread: the export tasks need the runtime.
    pub async fn shutdown(self) -> anyhow::Result<()> {
        tokio::task::spawn_blocking(move || {
            let results = [
                ("traces", self.tracer.shutdown()),
                ("metrics", self.meter.shutdown()),
                ("logs", self.logger.shutdown()),
            ];
            let failed: Vec<String> = results
                .into_iter()
                .filter_map(|(what, r)| r.err().map(|e| format!("{what}: {e}")))
                .collect();
            if failed.is_empty() {
                Ok(())
            } else {
                anyhow::bail!(
                    "cannot shut down telemetry: {}\nTry: check the collector endpoint and the shutdown timeout",
                    failed.join("; ")
                )
            }
        })
        .await?
    }
}

fn setup_error(component: &str, err: &dyn std::fmt::Display) -> anyhow::Error {
    anyhow::anyhow!(
        "cannot configure telemetry {component}: {err}\nTry: check the OTEL environment configuration"
    )
}

/// The running telemetry. Call [`Telemetry::shutdown`] at the end to flush.
#[derive(Debug)]
pub struct Telemetry {
    providers: Option<Providers>,
}

impl Telemetry {
    /// True when the OTLP export runs.
    pub fn enabled(&self) -> bool {
        self.providers.is_some()
    }

    /// Flushes and stops the export. Without export it does nothing.
    pub async fn shutdown(self) -> anyhow::Result<()> {
        match self.providers {
            Some(p) => p.shutdown().await,
            None => Ok(()),
        }
    }
}

/// Starts the JSON log at `LOG_LEVEL` and, with an OTLP endpoint, the export
/// of traces, metrics, and logs. It sets the global meter provider, tracer
/// provider, and W3C trace context propagator. Call it once, in a tokio
/// runtime, before the app starts.
pub fn init(config: &Config, opts: &Options) -> anyhow::Result<Telemetry> {
    let Some(endpoint) = opts.endpoint() else {
        crate::app::init_logging(config);
        return Ok(Telemetry { providers: None });
    };
    let providers = Providers::new(opts, &endpoint)?;
    global::set_tracer_provider(providers.tracer.clone());
    global::set_meter_provider(providers.meter.clone());
    global::set_text_map_propagator(TraceContextPropagator::new());
    let fmt = tracing_subscriber::fmt::layer()
        .json()
        .with_writer(std::io::stderr);
    tracing_subscriber::registry()
        .with(EnvFilter::new(&config.log_level))
        .with(fmt)
        .with(providers.layers())
        .try_init()
        .map_err(|e| {
            anyhow::anyhow!("cannot start telemetry: {e}\nTry: call kit::telemetry::init once")
        })?;
    Ok(Telemetry {
        providers: Some(providers),
    })
}

/// The HTTP instruments. Build them once and pass them as the layer state.
#[derive(Debug, Clone)]
pub struct HttpMetrics {
    duration: Histogram<f64>,
    active: UpDownCounter<i64>,
}

impl HttpMetrics {
    /// The instruments on `meter`.
    pub fn new(meter: &Meter) -> Self {
        Self {
            duration: meter
                .f64_histogram(HTTP_DURATION)
                .with_unit("s")
                .with_description("Duration of HTTP server requests.")
                .with_boundaries(HTTP_DURATION_BOUNDS.to_vec())
                .build(),
            active: meter
                .i64_up_down_counter("http.server.active_requests")
                .with_unit("{request}")
                .with_description("Number of active HTTP server requests.")
                .build(),
        }
    }

    /// The instruments on the global meter provider. Before [`init`], or
    /// without an endpoint, they record nothing.
    pub fn global() -> Self {
        Self::new(&global::meter(SCOPE))
    }
}

/// The HTTP layer: a server span (child of `traceparent`) and the duration
/// and active-request metrics. Labels use the route pattern, never the
/// concrete path. A request that matches no route has no `http.route`.
/// Use it with `axum::middleware::from_fn_with_state`.
pub async fn http_layer(State(m): State<HttpMetrics>, req: Request, next: Next) -> Response {
    let start = Instant::now();
    let method = metric_method(req.method().as_str());
    let route = req
        .extensions()
        .get::<MatchedPath>()
        .map(|p| p.as_str().to_owned());
    let name = match &route {
        Some(r) => format!("{method} {r}"),
        None => method.to_owned(),
    };
    let span = tracing::info_span!(
        "HTTP request",
        otel.name = %name,
        otel.kind = "server",
        otel.status_code = tracing::field::Empty,
        http.request.method = %method,
        http.route = route.as_deref().unwrap_or_default(),
        url.path = %req.uri().path(),
        http.response.status_code = tracing::field::Empty,
    );
    let parent = global::get_text_map_propagator(|p| p.extract(&Headers(req.headers())));
    let _ = span.set_parent(parent);

    let active_attrs = [KeyValue::new("http.request.method", method)];
    m.active.add(1, &active_attrs);
    let res = next.run(req).instrument(span.clone()).await;
    m.active.add(-1, &active_attrs);

    let status = res.status().as_u16();
    span.record("http.response.status_code", status);
    if status >= 500 {
        span.record("otel.status_code", "ERROR");
    }
    let mut attrs = vec![
        KeyValue::new("http.request.method", method),
        KeyValue::new("http.response.status_code", i64::from(status)),
    ];
    if let Some(r) = route {
        attrs.push(KeyValue::new("http.route", r));
    }
    m.duration.record(start.elapsed().as_secs_f64(), &attrs);
    res
}

/// The method label: a known method, or `_OTHER` (semantic conventions).
fn metric_method(method: &str) -> &'static str {
    match method {
        "GET" => "GET",
        "HEAD" => "HEAD",
        "POST" => "POST",
        "PUT" => "PUT",
        "DELETE" => "DELETE",
        "CONNECT" => "CONNECT",
        "OPTIONS" => "OPTIONS",
        "TRACE" => "TRACE",
        "PATCH" => "PATCH",
        _ => "_OTHER",
    }
}

struct Headers<'a>(&'a HeaderMap);

impl Extractor for Headers<'_> {
    fn get(&self, key: &str) -> Option<&str> {
        self.0.get(key).and_then(|v| v.to_str().ok())
    }

    fn keys(&self) -> Vec<&str> {
        self.0.keys().map(|k| k.as_str()).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unknown_methods_are_other() {
        assert_eq!(metric_method("GET"), "GET");
        assert_eq!(metric_method("BREW"), "_OTHER");
    }

    #[test]
    fn no_endpoint_means_no_export() {
        let opts = Options {
            endpoint: Some("  ".into()),
            ..Options::new("x-api")
        };
        assert_eq!(opts.endpoint(), None);
    }
}
