//! The setup: options from the environment, the OTLP providers, and the global state (Go
//! `telemetry.Setup`).

use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use opentelemetry::propagation::TextMapCompositePropagator;
use opentelemetry::trace::TracerProvider as _;
use opentelemetry::{KeyValue, global};
use opentelemetry_otlp::{
    LogExporter, MetricExporter, SpanExporter, WithExportConfig, WithHttpConfig,
};
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::logs::SdkLoggerProvider;
use opentelemetry_sdk::metrics::SdkMeterProvider;
use opentelemetry_sdk::propagation::{BaggagePropagator, TraceContextPropagator};
use opentelemetry_sdk::runtime;
use opentelemetry_sdk::trace::{Sampler, SdkTracerProvider};
use tracing::{Level, Metadata, Subscriber};
use tracing_subscriber::Layer;
use tracing_subscriber::filter::filter_fn;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::registry::LookupSpan;
use tracing_subscriber::util::SubscriberInitExt;

use super::{LOG_TARGET, SCOPE, SPAN_TARGET, db_layer, log_bridge, quiet_target};
use crate::config::Base;
use crate::logx;

/// The environment variables that [`Options::from_lookup`] reads.
const ENV_KEYS: [&str; 8] = [
    "OTEL_EXPORTER_OTLP_ENDPOINT",
    "OTEL_EXPORTER_OTLP_HEADERS",
    "OTEL_SERVICE_NAME",
    "OTEL_RESOURCE_ATTRIBUTES",
    "OTEL_TRACES_SAMPLER",
    "OTEL_TRACES_SAMPLER_ARG",
    "OTEL_METRIC_EXPORT_INTERVAL",
    "HOSTNAME",
];

/// The resource defaults and the OTel environment values (Go `telemetry.Options`). The
/// environment values override the defaults.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// The service name, normally the app name plus `-api`. It becomes the Prometheus `job`
    /// label.
    pub service_name: String,
    /// The app release (`service.version`).
    pub service_version: String,
    /// The deployment environment (`deployment.environment.name`).
    pub environment: String,
    /// The OTLP HTTP base URL. `None` uses `OTEL_EXPORTER_OTLP_ENDPOINT` of [`Options::env`].
    pub endpoint: Option<String>,
    /// The environment values that the setup reads (see the module documentation). Empty
    /// values are not kept.
    pub env: BTreeMap<String, String>,
}

impl Options {
    /// Returns options for `service_name` with the values of the process environment.
    pub fn new(service_name: impl Into<String>) -> Self {
        Self::from_lookup(service_name, |key| std::env::var(key).ok())
    }

    /// Returns options for `service_name` with the environment values that `lookup` gives
    /// (tests pass a map).
    pub fn from_lookup(
        service_name: impl Into<String>,
        lookup: impl Fn(&str) -> Option<String>,
    ) -> Self {
        let env = ENV_KEYS
            .iter()
            .filter_map(|key| {
                lookup(key)
                    .filter(|v| !v.trim().is_empty())
                    .map(|v| ((*key).to_string(), v))
            })
            .collect();
        Self {
            service_name: service_name.into(),
            env,
            ..Self::default()
        }
    }

    /// The OTLP base URL, or `None` when the export is off.
    pub fn endpoint(&self) -> Option<String> {
        self.endpoint
            .clone()
            .or_else(|| {
                self.env_value("OTEL_EXPORTER_OTLP_ENDPOINT")
                    .map(str::to_owned)
            })
            .map(|e| e.trim().to_owned())
            .filter(|e| !e.is_empty())
    }

    fn env_value(&self, key: &str) -> Option<&str> {
        self.env.get(key).map(String::as_str)
    }

    /// The metric export interval: `OTEL_METRIC_EXPORT_INTERVAL` in milliseconds, or 30
    /// seconds as in Go.
    fn metric_interval(&self) -> Duration {
        self.env_value("OTEL_METRIC_EXPORT_INTERVAL")
            .and_then(|v| v.trim().parse::<u64>().ok())
            .filter(|ms| *ms > 0)
            .map_or(Duration::from_secs(30), Duration::from_millis)
    }

    /// The resource: the defaults, `host.name`, `OTEL_RESOURCE_ATTRIBUTES`, then
    /// `OTEL_SERVICE_NAME` (Go `resource.WithFromEnv`).
    pub(crate) fn resource(&self) -> anyhow::Result<Resource> {
        let mut attrs: BTreeMap<String, String> = BTreeMap::new();
        for (key, value) in [
            ("service.name", &self.service_name),
            ("service.version", &self.service_version),
            ("deployment.environment.name", &self.environment),
        ] {
            if !value.is_empty() {
                attrs.insert(key.into(), value.clone());
            }
        }
        if let Some(host) = self.env_value("HOSTNAME") {
            attrs.insert("host.name".into(), host.trim().to_owned());
        }
        if let Some(text) = self.env_value("OTEL_RESOURCE_ATTRIBUTES") {
            let pairs = parse_pairs(text).map_err(|entry| {
                setup_error(
                    "resource",
                    &format!(
                        "OTEL_RESOURCE_ATTRIBUTES has the entry {entry:?} without a key and \"=\""
                    ),
                )
            })?;
            attrs.extend(pairs);
        }
        if let Some(name) = self.env_value("OTEL_SERVICE_NAME") {
            attrs.insert("service.name".into(), name.trim().to_owned());
        }
        let service = attrs.remove("service.name").unwrap_or_default();
        Ok(Resource::builder_empty()
            .with_service_name(service)
            .with_attributes(attrs.into_iter().map(|(k, v)| KeyValue::new(k, v)))
            .build())
    }

    /// The headers of `OTEL_EXPORTER_OTLP_HEADERS`.
    fn headers(&self) -> anyhow::Result<HashMap<String, String>> {
        match self.env_value("OTEL_EXPORTER_OTLP_HEADERS") {
            None => Ok(HashMap::new()),
            Some(text) => parse_pairs(text).map(|p| p.into_iter().collect()).map_err(|entry| {
                setup_error(
                    "exporter headers",
                    &format!("OTEL_EXPORTER_OTLP_HEADERS has the entry {entry:?} without a key and \"=\""),
                )
            }),
        }
    }

    /// The sampler of `OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG`. The default is
    /// `parentbased_always_on`, as in Go.
    pub(crate) fn sampler(&self) -> anyhow::Result<Sampler> {
        let name = self
            .env_value("OTEL_TRACES_SAMPLER")
            .map_or("parentbased_always_on".to_string(), |s| {
                s.trim().to_ascii_lowercase()
            });
        let ratio = || -> anyhow::Result<f64> {
            match self.env_value("OTEL_TRACES_SAMPLER_ARG") {
                None => Ok(1.0),
                Some(arg) => arg
                    .trim()
                    .parse::<f64>()
                    .ok()
                    .filter(|r| (0.0..=1.0).contains(r))
                    .ok_or_else(|| {
                        setup_error(
                            "sampler",
                            &format!("OTEL_TRACES_SAMPLER_ARG {arg:?} is not a ratio from 0 to 1"),
                        )
                    }),
            }
        };
        Ok(match name.as_str() {
            "always_on" => Sampler::AlwaysOn,
            "always_off" => Sampler::AlwaysOff,
            "traceidratio" => Sampler::TraceIdRatioBased(ratio()?),
            "parentbased_always_on" => Sampler::ParentBased(Box::new(Sampler::AlwaysOn)),
            "parentbased_always_off" => Sampler::ParentBased(Box::new(Sampler::AlwaysOff)),
            "parentbased_traceidratio" => {
                Sampler::ParentBased(Box::new(Sampler::TraceIdRatioBased(ratio()?)))
            }
            other => {
                return Err(setup_error(
                    "sampler",
                    &format!(
                        "OTEL_TRACES_SAMPLER {other:?} is not always_on, always_off, traceidratio, or a parentbased_ form of them"
                    ),
                ));
            }
        })
    }
}

/// Parses `key=value,key=value` with percent-decoded values. An entry without a key and `=`
/// is the error. Empty entries are skipped.
fn parse_pairs(text: &str) -> Result<Vec<(String, String)>, String> {
    let mut out = Vec::new();
    for entry in text.split(',') {
        if entry.trim().is_empty() {
            continue;
        }
        match entry.split_once('=') {
            Some((key, value)) if !key.trim().is_empty() => {
                out.push((percent_decode(key.trim()), percent_decode(value.trim())));
            }
            _ => return Err(entry.trim().to_string()),
        }
    }
    Ok(out)
}

/// Decodes `%XX` escapes. A bad escape stays as it is.
fn percent_decode(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%'
            && let Some(hex) = text.get(i + 1..i + 3)
            && let Ok(b) = u8::from_str_radix(hex, 16)
        {
            out.push(b);
            i += 3;
            continue;
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

pub(crate) fn setup_error(component: &str, err: &dyn std::fmt::Display) -> anyhow::Error {
    anyhow::anyhow!(
        "cannot configure telemetry {component}: {err}\nTry: check the OTEL environment configuration"
    )
}

/// The three providers of the export.
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
    /// Builds the OTLP HTTP exporters and the providers for `endpoint` (such as
    /// `http://lgtm:4318`). The exports run on tokio tasks, so call it inside a tokio
    /// runtime.
    pub fn otlp(opts: &Options, endpoint: &str) -> anyhow::Result<Self> {
        let base = endpoint.trim().trim_end_matches('/');
        let resource = opts.resource()?;
        let headers = opts.headers()?;
        let sampler = opts.sampler()?;
        let spans = SpanExporter::builder()
            .with_http()
            .with_endpoint(format!("{base}/v1/traces"))
            .with_headers(headers.clone())
            .build()
            .map_err(|e| setup_error("trace exporter", &e))?;
        let metrics = MetricExporter::builder()
            .with_http()
            .with_endpoint(format!("{base}/v1/metrics"))
            .with_headers(headers.clone())
            .build()
            .map_err(|e| setup_error("metric exporter", &e))?;
        let logs = LogExporter::builder()
            .with_http()
            .with_endpoint(format!("{base}/v1/logs"))
            .with_headers(headers)
            .build()
            .map_err(|e| setup_error("log exporter", &e))?;
        use opentelemetry_sdk::logs::log_processor_with_async_runtime::BatchLogProcessor;
        use opentelemetry_sdk::metrics::periodic_reader_with_async_runtime::PeriodicReader;
        use opentelemetry_sdk::trace::span_processor_with_async_runtime::BatchSpanProcessor;
        let tracer = SdkTracerProvider::builder()
            .with_resource(resource.clone())
            .with_sampler(sampler)
            .with_span_processor(BatchSpanProcessor::builder(spans, runtime::Tokio).build())
            .build();
        let meter = SdkMeterProvider::builder()
            .with_resource(resource.clone())
            .with_reader(
                PeriodicReader::builder(metrics, runtime::Tokio)
                    .with_interval(opts.metric_interval())
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

    /// Flushes and stops the three providers. The SDK calls block, so they run on a blocking
    /// thread: the export tasks need the runtime.
    async fn shutdown(self) -> anyhow::Result<()> {
        let result = tokio::task::spawn_blocking(move || {
            [
                ("traces", self.tracer.shutdown()),
                ("metrics", self.meter.shutdown()),
                ("logs", self.logger.shutdown()),
            ]
            .into_iter()
            .filter_map(|(what, r)| r.err().map(|e| format!("{what}: {e}")))
            .collect::<Vec<String>>()
        })
        .await;
        let failed = match result {
            Ok(failed) => failed,
            Err(e) => vec![format!("the shutdown task failed: {e}")],
        };
        if failed.is_empty() {
            Ok(())
        } else {
            anyhow::bail!(
                "cannot shut down telemetry: {}\nTry: check the collector endpoint and the shutdown timeout",
                failed.join("; ")
            )
        }
    }
}

/// The running telemetry (Go `Setup` and its shutdown function). A clone shares the same
/// providers. Call [`Telemetry::shutdown`] at the end to flush the export:
/// [`App::with_telemetry`](crate::app::App::with_telemetry) does it in an exit hook.
#[derive(Debug, Clone, Default)]
pub struct Telemetry {
    providers: Arc<Mutex<Option<Providers>>>,
}

impl Telemetry {
    /// Returns telemetry that exports nothing.
    pub fn disabled() -> Self {
        Self::default()
    }

    /// Builds the OTLP providers when [`Options::endpoint`] is set, and installs them. It
    /// does not start a tracing subscriber: [`init`] does that. Without an endpoint it
    /// returns disabled telemetry and changes nothing. Call it inside a tokio runtime.
    pub fn start(opts: &Options) -> anyhow::Result<Self> {
        match opts.endpoint() {
            None => Ok(Self::disabled()),
            Some(endpoint) => Ok(Self::install(Providers::otlp(opts, &endpoint)?)),
        }
    }

    /// Installs `providers` as the global tracer and meter providers, sets the W3C trace
    /// context and baggage propagator, and starts the tokio runtime metrics when a tokio
    /// runtime runs. Tests pass providers with in-memory exporters.
    pub fn install(providers: Providers) -> Self {
        global::set_tracer_provider(providers.tracer.clone());
        global::set_meter_provider(providers.meter.clone());
        global::set_text_map_propagator(TextMapCompositePropagator::new(vec![
            Box::new(TraceContextPropagator::new()),
            Box::new(BaggagePropagator::new()),
        ]));
        if let Ok(handle) = tokio::runtime::Handle::try_current() {
            runtime_metrics(&providers.meter, handle);
        }
        Self {
            providers: Arc::new(Mutex::new(Some(providers))),
        }
    }

    /// Reports whether the export runs (Go `telemetry.Enabled`). It is false after
    /// [`Telemetry::shutdown`].
    pub fn enabled(&self) -> bool {
        self.lock().is_some()
    }

    /// The providers, while the export runs.
    pub fn providers(&self) -> Option<Providers> {
        self.lock().clone()
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, Option<Providers>> {
        self.providers
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
    }

    /// The tracing layers of the export: spans to OTel traces, events at `log_level` or
    /// above to OTel logs (redacted), and `sqlx::query` events to database spans. `None`
    /// when the export is off. [`init`] stacks it with the [`logx`] layer.
    pub fn layer<S>(&self, log_level: Level) -> Option<impl Layer<S> + Send + Sync + 'static>
    where
        S: Subscriber + for<'a> LookupSpan<'a> + Send + Sync + 'static,
    {
        let providers = self.providers()?;
        let traces = tracing_opentelemetry::layer()
            .with_tracer(providers.tracer.tracer(SCOPE))
            .with_location(false)
            .with_threads(false)
            .with_target(false)
            .with_filter(filter_fn(trace_enabled));
        let logs = log_bridge(&providers.logger)
            .with_filter(filter_fn(move |meta| log_enabled(meta, log_level)));
        Some(traces.and_then(logs).and_then(db_layer()))
    }

    /// Flushes and stops the export (Go shutdown function). A second call does nothing.
    /// Without export it does nothing.
    pub async fn shutdown(&self) -> anyhow::Result<()> {
        let providers = self.lock().take();
        match providers {
            Some(p) => p.shutdown().await,
            None => Ok(()),
        }
    }
}

/// The spans of the trace export: the kit spans at every level, other spans at `INFO` or
/// above, and never the spans of the log target or the quiet targets. Events stay out: the
/// log bridge exports them.
fn trace_enabled(meta: &Metadata<'_>) -> bool {
    if !meta.is_span() {
        return false;
    }
    let target = meta.target();
    if target.starts_with(LOG_TARGET) || quiet_target(target) {
        return false;
    }
    target.starts_with(SPAN_TARGET) || *meta.level() <= Level::INFO
}

/// The events and spans of the log bridge: `log_level` or above, not the quiet targets and
/// not `sqlx::query` (the database span has it). Spans are kept for their fields.
fn log_enabled(meta: &Metadata<'_>, log_level: Level) -> bool {
    let target = meta.target();
    if quiet_target(target) {
        return false;
    }
    if meta.is_span() {
        return !target.starts_with(LOG_TARGET) && !target.starts_with(SPAN_TARGET);
    }
    target != "sqlx::query" && *meta.level() <= log_level
}

/// The tokio runtime metrics (the Rust twin of the Go runtime metrics). The callbacks read
/// the stable metrics of `handle` at each export.
fn runtime_metrics(provider: &SdkMeterProvider, handle: tokio::runtime::Handle) {
    use opentelemetry::metrics::MeterProvider as _;
    let meter = provider.meter(SCOPE);
    let h = handle.clone();
    let _ = meter
        .u64_observable_gauge("tokio.runtime.workers")
        .with_unit("{worker}")
        .with_description("Number of worker threads of the tokio runtime.")
        .with_callback(move |o| o.observe(h.metrics().num_workers() as u64, &[]))
        .build();
    let h = handle.clone();
    let _ = meter
        .u64_observable_gauge("tokio.runtime.alive_tasks")
        .with_unit("{task}")
        .with_description("Number of alive tasks of the tokio runtime.")
        .with_callback(move |o| o.observe(h.metrics().num_alive_tasks() as u64, &[]))
        .build();
    let h = handle.clone();
    let _ = meter
        .u64_observable_gauge("tokio.runtime.global_queue_depth")
        .with_unit("{task}")
        .with_description("Number of tasks in the global queue of the tokio runtime.")
        .with_callback(move |o| o.observe(h.metrics().global_queue_depth() as u64, &[]))
        .build();
    let h = handle.clone();
    let _ = meter
        .f64_observable_counter("tokio.runtime.worker.busy_duration")
        .with_unit("s")
        .with_description("Total busy time of the tokio worker threads.")
        .with_callback(move |o| {
            let m = h.metrics();
            let total: f64 = (0..m.num_workers())
                .map(|w| m.worker_total_busy_duration(w).as_secs_f64())
                .sum();
            o.observe(total, &[]);
        })
        .build();
    let h = handle;
    let _ = meter
        .u64_observable_counter("tokio.runtime.worker.park_count")
        .with_unit("{park}")
        .with_description("Total number of parks of the tokio worker threads.")
        .with_callback(move |o| {
            let m = h.metrics();
            let total: u64 = (0..m.num_workers()).map(|w| m.worker_park_count(w)).sum();
            o.observe(total, &[]);
        })
        .build();
}

/// Starts the log and the telemetry (Go `telemetry.Setup` plus the logger). Without an
/// OTLP endpoint it calls [`logx::init`] only. With an endpoint it also exports traces,
/// metrics, and logs, and sets the global providers and the propagator. Call it once, in a
/// tokio runtime, before the app starts.
///
/// # Errors
///
/// A bad OTel environment value, an exporter that cannot start, or a second start of the
/// log gives an error with a `Try:` line. A failed start installs no global provider.
pub fn init(base: &Base, opts: &Options) -> anyhow::Result<Telemetry> {
    let Some(endpoint) = opts.endpoint() else {
        logx::init(base)?;
        return Ok(Telemetry::disabled());
    };
    let providers = Providers::otlp(opts, &endpoint)?;
    let telemetry = Telemetry::install(providers);
    let level = logx::parse_level(&base.log_level);
    tracing_subscriber::registry()
        .with(logx::layer(base))
        .with(telemetry.layer(level))
        .try_init()
        .map_err(|e| {
            anyhow::anyhow!(
                "cannot start telemetry: {e}\nTry: call kit::telemetry::init once, and not together with kit::logx::init"
            )
        })?;
    Ok(telemetry)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn opts(pairs: &[(&str, &str)]) -> Options {
        let map: BTreeMap<String, String> = pairs
            .iter()
            .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
            .collect();
        Options::from_lookup("x-api", move |k| map.get(k).cloned())
    }

    #[test]
    fn telemetry_endpoint_blank_is_off() {
        assert_eq!(
            opts(&[("OTEL_EXPORTER_OTLP_ENDPOINT", "  ")]).endpoint(),
            None
        );
        let o = Options {
            endpoint: Some(" http://c:4318 ".into()),
            ..opts(&[])
        };
        assert_eq!(o.endpoint().as_deref(), Some("http://c:4318"));
    }

    #[test]
    fn telemetry_pairs_and_percent_decoding() {
        assert_eq!(
            parse_pairs("a=1, b=x%20y,,c=").unwrap(),
            vec![
                ("a".to_string(), "1".to_string()),
                ("b".to_string(), "x y".to_string()),
                ("c".to_string(), String::new()),
            ]
        );
        assert_eq!(parse_pairs("invalid").unwrap_err(), "invalid");
        assert_eq!(parse_pairs("=v").unwrap_err(), "=v");
        assert_eq!(percent_decode("100%"), "100%");
    }

    #[test]
    fn telemetry_sampler_names() {
        assert!(opts(&[]).sampler().is_ok());
        for name in [
            "always_on",
            "always_off",
            "traceidratio",
            "parentbased_always_off",
            "parentbased_traceidratio",
        ] {
            assert!(
                opts(&[("OTEL_TRACES_SAMPLER", name)]).sampler().is_ok(),
                "{name}"
            );
        }
        let err = opts(&[("OTEL_TRACES_SAMPLER", "sometimes")])
            .sampler()
            .unwrap_err()
            .to_string();
        assert!(err.contains("Try:"), "{err}");
        let err = opts(&[
            ("OTEL_TRACES_SAMPLER", "traceidratio"),
            ("OTEL_TRACES_SAMPLER_ARG", "2"),
        ])
        .sampler()
        .unwrap_err()
        .to_string();
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn telemetry_metric_interval() {
        assert_eq!(opts(&[]).metric_interval(), Duration::from_secs(30));
        assert_eq!(
            opts(&[("OTEL_METRIC_EXPORT_INTERVAL", "250")]).metric_interval(),
            Duration::from_millis(250)
        );
    }
}
