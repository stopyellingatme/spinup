//! The HTTP layer: the server span and the RED metrics (Go `telemetry.HTTPMiddleware`).

use std::time::Instant;

use axum::extract::{MatchedPath, Request, State};
use axum::http::HeaderMap;
use axum::middleware::Next;
use axum::response::Response;
use opentelemetry::metrics::{Counter, Histogram, Meter, UpDownCounter};
use opentelemetry::propagation::Extractor;
use opentelemetry::trace::TraceContextExt as _;
use opentelemetry::{KeyValue, global};
use tracing::Instrument;
use tracing_opentelemetry::OpenTelemetrySpanExt;

use super::{LOG_TARGET, SCOPE, SPAN_TARGET};

/// The OTel name of the HTTP duration histogram. Prometheus shows it as
/// `http_server_request_duration_seconds`.
pub const HTTP_DURATION: &str = "http.server.request.duration";

/// The bucket bounds of the HTTP duration histogram, in seconds: the semantic convention
/// bounds (the Go otelhttp bounds).
pub const HTTP_DURATION_BOUNDS: [f64; 14] = [
    0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1.0, 2.5, 5.0, 7.5, 10.0,
];

/// The HTTP instruments: duration, count, and active requests. Build them once and pass
/// them as the state of [`http_layer`].
#[derive(Debug, Clone)]
pub struct HttpMetrics {
    duration: Histogram<f64>,
    count: Counter<u64>,
    active: UpDownCounter<i64>,
}

impl HttpMetrics {
    /// Returns the instruments on `meter`.
    pub fn new(meter: &Meter) -> Self {
        Self {
            duration: meter
                .f64_histogram(HTTP_DURATION)
                .with_unit("s")
                .with_description("Duration of HTTP server requests.")
                .with_boundaries(HTTP_DURATION_BOUNDS.to_vec())
                .build(),
            count: meter
                .u64_counter("http.server.request.count")
                .with_unit("{request}")
                .with_description("Number of HTTP server requests.")
                .build(),
            active: meter
                .i64_up_down_counter("http.server.active_requests")
                .with_unit("{request}")
                .with_description("Number of active HTTP server requests.")
                .build(),
        }
    }

    /// Returns the instruments on the global meter provider. Before the telemetry starts,
    /// or without an endpoint, they record nothing.
    pub fn global() -> Self {
        Self::new(&global::meter(SCOPE))
    }
}

/// The HTTP layer (Go `telemetry.HTTPMiddleware`): a server span that continues the
/// `traceparent` of the request, and the duration, count, and active-request metrics.
/// Span names and labels use the route pattern, never the concrete path or the host. A
/// request that matches no route has no `http.route`. A 5xx answer marks the span as an
/// error. The local log lines inside the request get `trace_id` and `span_id`.
///
/// Use it with `axum::middleware::from_fn_with_state`, as
/// [`App::with_telemetry`](crate::app::App::with_telemetry) does.
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
    let span = tracing::trace_span!(
        target: SPAN_TARGET,
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
    let log_span = correlation_span(&span);

    let active_attrs = [KeyValue::new("http.request.method", method)];
    m.active.add(1, &active_attrs);
    let res = next
        .run(req)
        .instrument(log_span)
        .instrument(span.clone())
        .await;
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
    m.count.add(1, &attrs);
    m.duration.record(start.elapsed().as_secs_f64(), &attrs);
    res
}

/// Returns a child span of `span` with the fields `trace_id` and `span_id` of its OTel span
/// (Go `correlationHandler`). Run work inside both spans, and each local log line gets the
/// two fields. Without an OTel span it returns a disabled span.
///
/// The span has the target [`LOG_TARGET`](super::LOG_TARGET), so the export skips it. Its
/// level is `ERROR`, so every `LOG_LEVEL` keeps it.
pub fn correlation_span(span: &tracing::Span) -> tracing::Span {
    if span.is_disabled() {
        return tracing::Span::none();
    }
    let cx = span.context();
    let otel = cx.span();
    let sc = otel.span_context();
    if !sc.is_valid() {
        return tracing::Span::none();
    }
    tracing::span!(
        target: LOG_TARGET,
        parent: span,
        tracing::Level::ERROR,
        "trace",
        trace_id = %sc.trace_id(),
        span_id = %sc.span_id(),
    )
}

/// The method label: a known method, or `_OTHER` (semantic conventions). It bounds the
/// series count.
pub(crate) fn metric_method(method: &str) -> &'static str {
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
    fn telemetry_unknown_methods_are_other() {
        assert_eq!(metric_method("GET"), "GET");
        assert_eq!(metric_method("PATCH"), "PATCH");
        assert_eq!(metric_method("CUSTOM_ONE"), "_OTHER");
    }
}
