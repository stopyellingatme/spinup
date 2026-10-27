//! The telemetry of the queue: the job duration histogram, and the trace link from the
//! insert to the job. Without the feature `telemetry`, every function here does nothing
//! (ADR 0021 §2: `jobs` does not imply `telemetry`).

use std::time::Duration;

use serde_json::Value;

/// The OTel name of the job duration histogram. Prometheus shows it as
/// `kit_job_duration_seconds`.
pub const JOB_DURATION: &str = "kit.job.duration";

/// The bucket bounds of the job duration histogram, in seconds: the HTTP bounds, then
/// longer bounds up to one hour for slow jobs.
pub const JOB_DURATION_BOUNDS: [f64; 19] = [
    0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0, 30.0, 60.0,
    300.0, 900.0, 3600.0,
];

/// The metadata key of the W3C trace context of the insert.
#[cfg(feature = "telemetry")]
const TRACEPARENT: &str = "traceparent";

/// The job instruments. The worker builds them once, when it starts.
#[derive(Debug, Clone)]
pub(super) struct JobMetrics {
    #[cfg(feature = "telemetry")]
    duration: opentelemetry::metrics::Histogram<f64>,
}

impl JobMetrics {
    /// Returns the instruments on the global meter provider. Before the telemetry starts,
    /// or without an endpoint, they record nothing.
    pub(super) fn new() -> Self {
        #[cfg(feature = "telemetry")]
        {
            let meter = crate::telemetry::meter(crate::telemetry::SCOPE);
            Self {
                duration: meter
                    .f64_histogram(JOB_DURATION)
                    .with_unit("s")
                    .with_description("Duration of job attempts.")
                    .with_boundaries(JOB_DURATION_BOUNDS.to_vec())
                    .build(),
            }
        }
        #[cfg(not(feature = "telemetry"))]
        Self {}
    }

    /// Records one attempt of a job of `kind` on `queue`, with its `result` (`completed`,
    /// `retryable`, `discarded`, `cancelled`, or `snoozed`).
    pub(super) fn record(&self, kind: &str, queue: &str, result: &'static str, took: Duration) {
        #[cfg(feature = "telemetry")]
        {
            use opentelemetry::KeyValue;
            self.duration.record(
                took.as_secs_f64(),
                &[
                    KeyValue::new("job.kind", kind.to_owned()),
                    KeyValue::new("job.queue", queue.to_owned()),
                    KeyValue::new("job.result", result),
                ],
            );
        }
        #[cfg(not(feature = "telemetry"))]
        let _ = (kind, queue, result, took);
    }
}

/// The metadata of a new job: the W3C trace context of the current span, so the job span
/// can link to the trace of the insert. Without a trace (or without the feature
/// `telemetry`), it is `{}`.
pub(super) fn trace_metadata() -> Value {
    #[cfg(feature = "telemetry")]
    {
        use std::collections::HashMap;

        use opentelemetry::global;
        use tracing_opentelemetry::OpenTelemetrySpanExt as _;

        let cx = tracing::Span::current().context();
        let mut carrier: HashMap<String, String> = HashMap::new();
        global::get_text_map_propagator(|p| p.inject_context(&cx, &mut carrier));
        let mut out = serde_json::Map::new();
        for key in [TRACEPARENT, "tracestate"] {
            if let Some(v) = carrier.remove(key)
                && !v.is_empty()
            {
                out.insert(key.to_owned(), Value::String(v));
            }
        }
        Value::Object(out)
    }
    #[cfg(not(feature = "telemetry"))]
    Value::Object(serde_json::Map::new())
}

/// Links `span` (the span of a job) to the trace of the insert in `metadata`, and returns
/// the correlation span that gives the local log lines `trace_id` and `span_id`. Without a
/// trace context (or without the feature `telemetry`), it returns a disabled span.
pub(super) fn link(span: &tracing::Span, metadata: &Value) -> tracing::Span {
    #[cfg(feature = "telemetry")]
    {
        use std::collections::HashMap;

        use opentelemetry::global;
        use opentelemetry::trace::TraceContextExt as _;
        use tracing_opentelemetry::OpenTelemetrySpanExt as _;

        let carrier: HashMap<String, String> = metadata
            .as_object()
            .into_iter()
            .flatten()
            .filter_map(|(k, v)| Some((k.clone(), v.as_str()?.to_owned())))
            .collect();
        if carrier.contains_key(TRACEPARENT) {
            let cx = global::get_text_map_propagator(|p| p.extract(&carrier));
            span.add_link(cx.span().span_context().clone());
        }
        crate::telemetry::correlation_span(span)
    }
    #[cfg(not(feature = "telemetry"))]
    {
        let _ = (span, metadata);
        tracing::Span::none()
    }
}
