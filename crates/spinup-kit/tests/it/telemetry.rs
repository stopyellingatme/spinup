//! The `telemetry` tests of the kit (Go `kit/telemetry/*_test.go`, `kit/app/telemetry_test.go`,
//! and `kit/db/telemetry_test.go`).
//!
//! These tests install the global tracer provider, the global meter provider, and a global
//! tracing subscriber. They run only under nextest, which runs each test in its own process
//! (`make rs-test`). A scoped subscriber (`set_default`) is not safe here: tracing caches
//! the interest of a callsite from the first thread that hits it.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use axum::Router;
use axum::body::{Body, Bytes};
use axum::extract::{self, State};
use axum::http::{HeaderMap, Request, StatusCode};
use axum::routing::{get, post};
use opentelemetry::baggage::BaggageExt as _;
use opentelemetry::trace::{
    Span as _, SpanContext, SpanId, SpanKind, TraceContextExt as _, TraceFlags, TraceId,
    TraceState, Tracer as _,
};
use opentelemetry::{Context, KeyValue};
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::logs::{InMemoryLogExporter, SdkLoggerProvider};
use opentelemetry_sdk::metrics::data::{AggregatedMetrics, MetricData, ResourceMetrics};
use opentelemetry_sdk::metrics::{InMemoryMetricExporter, PeriodicReader, SdkMeterProvider};
use opentelemetry_sdk::trace::{InMemorySpanExporter, SdkTracerProvider, SpanData};
use prost::Message;
use spinup_kit::app::{App, Command};
use spinup_kit::config::Base;
use spinup_kit::logx;
use spinup_kit::telemetry::{self, Options, Providers, Telemetry};
use tower::ServiceExt;
use tracing::Instrument;
use tracing_subscriber::layer::SubscriberExt;

use crate::httpx::Capture;

/// The telemetry with in-memory exporters.
struct Mem {
    telemetry: Telemetry,
    spans: InMemorySpanExporter,
    metrics: InMemoryMetricExporter,
    logs: InMemoryLogExporter,
    meter: SdkMeterProvider,
}

impl Mem {
    /// Installs in-memory providers for `service`.
    fn install(service: &str) -> Self {
        let resource = Resource::builder_empty()
            .with_service_name(service.to_string())
            .build();
        let spans = InMemorySpanExporter::default();
        let metrics = InMemoryMetricExporter::default();
        let logs = InMemoryLogExporter::default();
        let tracer = SdkTracerProvider::builder()
            .with_resource(resource.clone())
            .with_simple_exporter(spans.clone())
            .build();
        let meter = SdkMeterProvider::builder()
            .with_resource(resource.clone())
            .with_reader(PeriodicReader::builder(metrics.clone()).build())
            .build();
        let logger = SdkLoggerProvider::builder()
            .with_resource(resource)
            .with_simple_exporter(logs.clone())
            .build();
        let telemetry = Telemetry::install(Providers {
            tracer,
            meter: meter.clone(),
            logger,
        });
        Self {
            telemetry,
            spans,
            metrics,
            logs,
            meter,
        }
    }

    /// Sets the global subscriber: the JSON log at info to a buffer, plus the telemetry
    /// layers.
    fn subscribe(&self) -> Capture {
        subscribe(&self.telemetry)
    }

    fn spans(&self) -> Vec<SpanData> {
        self.spans.get_finished_spans().unwrap()
    }

    /// The last metric export after a flush.
    fn collect(&self) -> ResourceMetrics {
        self.meter.force_flush().unwrap();
        self.metrics
            .get_finished_metrics()
            .unwrap()
            .pop()
            .expect("one export")
    }
}

/// Sets the global subscriber with the JSON log at info to a buffer and the layers of
/// `telemetry`.
fn subscribe(telemetry: &Telemetry) -> Capture {
    let cap = Capture::default();
    let subscriber = tracing_subscriber::registry()
        .with(logx::layer_with(logx::Options::default(), cap.clone()))
        .with(telemetry.layer(tracing::Level::INFO));
    tracing::subscriber::set_global_default(subscriber)
        .expect("the only global subscriber of this test process");
    cap
}

fn lookup(pairs: &[(&str, &str)]) -> impl Fn(&str) -> Option<String> + use<> {
    let map: BTreeMap<String, String> = pairs
        .iter()
        .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
        .collect();
    move |k| map.get(k).cloned()
}

async fn send(app: &Router, method: &str, path: &str, headers: &[(&str, &str)]) -> StatusCode {
    let mut req = Request::builder().method(method).uri(path);
    for (k, v) in headers {
        req = req.header(*k, *v);
    }
    app.clone()
        .oneshot(req.body(Body::empty()).unwrap())
        .await
        .unwrap()
        .status()
}

/// The todo routes, nested under `/api` as in the reference app.
fn api() -> Router {
    let todos = Router::new()
        .route(
            "/todos",
            post(|| async { StatusCode::INTERNAL_SERVER_ERROR }),
        )
        .route(
            "/todos/{id}",
            get(|| async {
                tracing::info!("todo loaded");
                "{}"
            }),
        );
    Router::new().nest("/api", todos)
}

fn metric<'a>(
    rm: &'a ResourceMetrics,
    name: &str,
) -> Option<&'a opentelemetry_sdk::metrics::data::Metric> {
    rm.scope_metrics()
        .flat_map(|s| s.metrics())
        .find(|m| m.name() == name)
}

fn attr_of(attrs: &[KeyValue], key: &str) -> Option<String> {
    attrs
        .iter()
        .find(|kv| kv.key.as_str() == key)
        .map(|kv| kv.value.as_str().into_owned())
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

// ---------------------------------------------------------------------------------------
// Setup (Go setup_test.go and telemetry_test.go)
// ---------------------------------------------------------------------------------------

type Received = Arc<Mutex<Vec<(String, HeaderMap, Bytes)>>>;

async fn collect(
    State(received): State<Received>,
    extract::Path(signal): extract::Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> StatusCode {
    received.lock().unwrap().push((signal, headers, body));
    StatusCode::OK
}

/// Starts a fake OTLP collector on 127.0.0.1 under `/otlp`. It returns the base URL.
async fn collector() -> (String, Received) {
    let received: Received = Arc::default();
    let app = Router::new()
        .route("/otlp/v1/{signal}", post(collect))
        .with_state(received.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    (format!("http://{addr}/otlp"), received)
}

fn proto_attr(
    attrs: &[opentelemetry_proto::tonic::common::v1::KeyValue],
    key: &str,
) -> Option<String> {
    use opentelemetry_proto::tonic::common::v1::any_value::Value;
    attrs.iter().find(|kv| kv.key == key).and_then(|kv| {
        match kv.value.as_ref()?.value.as_ref()? {
            Value::StringValue(s) => Some(s.clone()),
            Value::IntValue(i) => Some(i.to_string()),
            other => Some(format!("{other:?}")),
        }
    })
}

/// Go `TestSetupExportsAndShutdown` with the R0.8 OTLP test: the three signals reach the
/// collector as protobuf with the headers and the resource of the environment. The server
/// span continues the `traceparent` of the request. The log records carry the trace ID,
/// and both logs are redacted. A second shutdown does nothing.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn telemetry_otlp_export_and_shutdown() {
    let (endpoint, received) = collector().await;
    let opts = Options {
        service_version: "v1".into(),
        environment: "test".into(),
        ..Options::from_lookup(
            "test-api",
            lookup(&[
                ("OTEL_EXPORTER_OTLP_ENDPOINT", &endpoint),
                ("OTEL_EXPORTER_OTLP_HEADERS", "x-test=present"),
                ("OTEL_SERVICE_NAME", "environment-api"),
                (
                    "OTEL_RESOURCE_ATTRIBUTES",
                    "service.name=attributes-api,team=platform",
                ),
                ("OTEL_TRACES_SAMPLER", "parentbased_always_on"),
                ("HOSTNAME", "test-host"),
            ]),
        )
    };
    let t = Telemetry::start(&opts).unwrap();
    assert!(t.enabled());
    let local = subscribe(&t);

    let injected: Arc<Mutex<HashMapText>> = Arc::default();
    let seen = injected.clone();
    let routes = Router::new().route(
        "/api/todos/{id}",
        get(move || {
            let seen = seen.clone();
            async move {
                let span = tracing::info_span!("work", password = "hidden-password");
                async {
                    tracing::info!(
                        nested.token = "hidden-token",
                        "postgres://app:hidden-url@db/test"
                    );
                }
                .instrument(span)
                .await;
                let mut carrier = HashMapText::default();
                opentelemetry::global::get_text_map_propagator(|p| {
                    p.inject_context(&Context::current(), &mut carrier)
                });
                *seen.lock().unwrap() = carrier;
                let counter = telemetry::meter("test")
                    .u64_counter("test.requests")
                    .build();
                counter.add(1, &[]);
                "{}"
            }
        }),
    );
    let app = App::new(&Base::default())
        .routes(routes)
        .with_telemetry(&t)
        .router();
    let trace_id = "0af7651916cd43dd8448eb211c80319c";
    let parent = "b7ad6b7169203331";
    let status = send(
        &app,
        "GET",
        "/api/todos/42",
        &[("traceparent", &format!("00-{trace_id}-{parent}-01"))],
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    let traceparent = injected.lock().unwrap().0.get("traceparent").cloned();
    assert!(
        traceparent
            .as_deref()
            .is_some_and(|tp| tp.starts_with(&format!("00-{trace_id}-"))),
        "{traceparent:?}"
    );

    t.shutdown().await.unwrap();
    assert!(!t.enabled(), "shutdown left telemetry enabled");
    t.shutdown().await.expect("a second shutdown does nothing");

    use opentelemetry_proto::tonic::collector::logs::v1::ExportLogsServiceRequest;
    use opentelemetry_proto::tonic::collector::metrics::v1::ExportMetricsServiceRequest;
    use opentelemetry_proto::tonic::collector::trace::v1::ExportTraceServiceRequest;
    use opentelemetry_proto::tonic::metrics::v1::metric::Data;
    let received = received.lock().unwrap().clone();
    let signals: BTreeSet<&str> = received.iter().map(|(s, _, _)| s.as_str()).collect();
    assert_eq!(signals, ["logs", "metrics", "traces"].into_iter().collect());
    for (_, headers, _) in &received {
        assert_eq!(headers["content-type"], "application/x-protobuf");
        assert_eq!(
            headers["x-test"], "present",
            "OTLP headers were not applied"
        );
    }

    // Traces: the resource and the server span with its remote parent.
    let spans: Vec<_> = received
        .iter()
        .filter(|(s, _, _)| s == "traces")
        .flat_map(|(_, _, b)| {
            ExportTraceServiceRequest::decode(b.clone())
                .unwrap()
                .resource_spans
        })
        .collect();
    let resource = spans[0].resource.as_ref().unwrap();
    for (key, want) in [
        ("service.name", "environment-api"),
        ("service.version", "v1"),
        ("deployment.environment.name", "test"),
        ("team", "platform"),
        ("host.name", "test-host"),
    ] {
        assert_eq!(
            proto_attr(&resource.attributes, key).as_deref(),
            Some(want),
            "{key}"
        );
    }
    let all: Vec<_> = spans
        .iter()
        .flat_map(|r| &r.scope_spans)
        .flat_map(|s| &s.spans)
        .collect();
    assert_eq!(all.len(), 2, "the server span and the work span: {all:?}");
    let server = all
        .iter()
        .find(|s| s.name == "GET /api/todos/{id}")
        .expect("the server span");
    assert_eq!(hex(&server.trace_id), trace_id);
    assert_eq!(hex(&server.parent_span_id), parent);
    assert_eq!(server.kind, 2, "SPAN_KIND_SERVER");
    assert_eq!(
        proto_attr(&server.attributes, "http.route").as_deref(),
        Some("/api/todos/{id}")
    );
    assert_eq!(
        proto_attr(&server.attributes, "http.response.status_code").as_deref(),
        Some("200")
    );

    // Metrics: the duration histogram and the counter of the handler.
    let metrics: Vec<_> = received
        .iter()
        .filter(|(s, _, _)| s == "metrics")
        .flat_map(|(_, _, b)| {
            ExportMetricsServiceRequest::decode(b.clone())
                .unwrap()
                .resource_metrics
        })
        .flat_map(|r| r.scope_metrics)
        .flat_map(|s| s.metrics)
        .collect();
    let names: BTreeSet<&str> = metrics.iter().map(|m| m.name.as_str()).collect();
    assert!(names.contains("test.requests"), "{names:?}");
    assert!(names.contains("tokio.runtime.workers"), "{names:?}");
    let duration = metrics
        .iter()
        .find(|m| m.name == "http.server.request.duration")
        .expect("the duration metric");
    assert_eq!(duration.unit, "s");
    let Some(Data::Histogram(h)) = &duration.data else {
        panic!("not a histogram")
    };
    let dp = &h.data_points[0];
    assert_eq!(dp.count, 1);
    assert_eq!(
        proto_attr(&dp.attributes, "http.route").as_deref(),
        Some("/api/todos/{id}")
    );
    assert_eq!(
        proto_attr(&dp.attributes, "http.request.method").as_deref(),
        Some("GET")
    );
    assert_eq!(
        proto_attr(&dp.attributes, "http.response.status_code").as_deref(),
        Some("200")
    );

    // Logs: the handler line and the access log line, with the trace ID, redacted.
    let logs: Vec<_> = received
        .iter()
        .filter(|(s, _, _)| s == "logs")
        .flat_map(|(_, _, b)| {
            ExportLogsServiceRequest::decode(b.clone())
                .unwrap()
                .resource_logs
        })
        .flat_map(|r| r.scope_logs)
        .flat_map(|s| s.log_records)
        .collect();
    assert_eq!(logs.len(), 2, "{logs:?}");
    for record in &logs {
        assert_eq!(hex(&record.trace_id), trace_id);
        assert_eq!(record.span_id.len(), 8);
    }
    let exported = format!("{logs:?}");
    let local = local.text();
    for data in [&exported, &local] {
        assert!(
            !data.contains("hidden-") && data.contains("****"),
            "not redacted: {data}"
        );
    }
    assert!(
        exported.contains("HTTP request"),
        "no access log: {exported}"
    );
    assert_eq!(local.matches("\"trace_id\"").count(), 2, "{local}");
}

/// A text map for the propagator.
#[derive(Debug, Default, Clone)]
struct HashMapText(BTreeMap<String, String>);

impl opentelemetry::propagation::Injector for HashMapText {
    fn set(&mut self, key: &str, value: String) {
        self.0.insert(key.to_string(), value);
    }
}

/// Go `TestEnvironmentSampler`: a zero ratio samples no root span, and the parent decision
/// stays.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn telemetry_environment_sampler() {
    let (endpoint, _received) = collector().await;
    let opts = Options::from_lookup(
        "test-api",
        lookup(&[
            ("OTEL_EXPORTER_OTLP_ENDPOINT", &endpoint),
            ("OTEL_TRACES_SAMPLER", "parentbased_traceidratio"),
            ("OTEL_TRACES_SAMPLER_ARG", "0"),
        ]),
    );
    let t = Telemetry::start(&opts).unwrap();
    let tracer = telemetry::tracer("test");
    let mut root = tracer.start("unsampled-root");
    assert!(!root.is_recording(), "a zero ratio sampled a root span");
    root.end();
    let parent = SpanContext::new(
        TraceId::from_bytes([1; 16]),
        SpanId::from_bytes([2; 8]),
        TraceFlags::SAMPLED,
        true,
        TraceState::default(),
    );
    let cx = Context::new().with_remote_span_context(parent);
    let mut child = tracer.start_with_context("sampled-child", &cx);
    assert!(child.is_recording(), "the parent decision was lost");
    child.set_attribute(KeyValue::new("test", "value"));
    child.end();
    t.shutdown().await.unwrap();
}

/// Go `TestSetupResourceFailure`: a bad resource value fails with a `Try:` line and
/// installs nothing.
#[tokio::test]
async fn telemetry_setup_resource_failure() {
    let opts = Options::from_lookup(
        "",
        lookup(&[
            ("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector.test"),
            ("OTEL_RESOURCE_ATTRIBUTES", "invalid"),
        ]),
    );
    let err = Telemetry::start(&opts).unwrap_err().to_string();
    assert!(
        err.contains("OTEL_RESOURCE_ATTRIBUTES") && err.contains("\nTry: "),
        "{err}"
    );
    let err = telemetry::init(&Base::default(), &opts)
        .unwrap_err()
        .to_string();
    assert!(err.contains("Try:"), "{err}");
    let mut span = telemetry::tracer("test").start("after-failure");
    assert!(
        !span.is_recording(),
        "a failed setup installed a tracer provider"
    );
    span.end();
}

/// Go `TestDisabledSetup`, `TestTelemetryDisabled`, and the disabled case of
/// `TestTelemetryLogger`: without an endpoint nothing is installed, and the app does not
/// change.
#[tokio::test]
async fn telemetry_disabled_setup() {
    let opts = Options::from_lookup(
        "test",
        lookup(&[
            ("OTEL_EXPORTER_OTLP_ENDPOINT", " "),
            (
                "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
                "http://collector.test/v1/traces",
            ),
        ]),
    );
    let t = Telemetry::start(&opts).unwrap();
    assert!(!t.enabled());
    assert!(t.providers().is_none());
    t.shutdown().await.unwrap();
    let cap = subscribe(&t);
    let app = App::new(&Base::default()).with_telemetry(&t).router();
    assert_eq!(send(&app, "GET", "/healthz", &[]).await, StatusCode::OK);
    let text = cap.text();
    assert!(text.contains("HTTP request"), "{text}");
    assert!(!text.contains("trace_id"), "{text}");
    let mut span = telemetry::tracer("test").start("disabled");
    assert!(!span.is_recording());
    span.end();
}

// ---------------------------------------------------------------------------------------
// The HTTP layer (Go http_test.go, telemetry_test.go, and app_test.go)
// ---------------------------------------------------------------------------------------

/// Go `TestHTTPTracingAndCorrelation`: the server span continues the remote parent, the
/// baggage reaches the handler, and the local log line has the trace and span IDs and
/// redacted fields.
#[tokio::test]
async fn telemetry_http_tracing_and_correlation() {
    let mem = Mem::install("test-api");
    let cap = mem.subscribe();
    let routes = Router::new().route(
        "/items/{id}",
        get(|| async {
            let cx = Context::current();
            assert_eq!(
                cx.baggage().get("tenant").map(|v| v.as_str().to_owned()),
                Some("test".to_string()),
                "the baggage was not extracted"
            );
            let span = tracing::info_span!("handler", password = "hidden");
            async { tracing::info!(nested.token = "hidden", "request") }
                .instrument(span)
                .await;
        }),
    );
    let app = App::new(&Base::default())
        .routes(routes)
        .with_telemetry(&mem.telemetry)
        .router();
    let status = send(
        &app,
        "GET",
        "/items/123",
        &[
            (
                "traceparent",
                "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01",
            ),
            ("baggage", "tenant=test"),
        ],
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    let spans = mem.spans();
    let server = spans
        .iter()
        .find(|s| s.span_kind == SpanKind::Server)
        .expect("the server span");
    assert_eq!(server.name, "GET /items/{id}");
    assert_eq!(server.parent_span_id.to_string(), "0123456789abcdef");
    assert!(server.parent_span_is_remote, "the remote parent was lost");
    let record = cap
        .records()
        .into_iter()
        .find(|r| r["msg"] == "request")
        .expect("the handler line");
    assert_eq!(record["trace_id"], "0123456789abcdef0123456789abcdef");
    assert_eq!(
        record["span_id"],
        server.span_context.span_id().to_string(),
        "{record}"
    );
    assert_eq!(record["password"], "****");
    assert_eq!(record["nested.token"], "****");
    assert!(record.get("otel.name").is_none(), "{record}");
}

/// Go `TestTelemetryRequest` and the enabled case of `TestTelemetryLogger`: one span, the
/// RED metrics with the route pattern, and the trace ID on the handler line and the access
/// log line.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn telemetry_request_metrics() {
    let mem = Arc::new(Mem::install("test-api"));
    let cap = mem.subscribe();
    let inner = mem.clone();
    let routes = Router::new().route(
        "/items/{id}",
        get(move || {
            let mem = inner.clone();
            async move {
                let live = tokio::task::block_in_place(|| mem.collect());
                let active = metric(&live, "http.server.active_requests").expect("active");
                let AggregatedMetrics::I64(MetricData::Sum(s)) = active.data() else {
                    panic!("not an i64 sum: {:?}", active.data())
                };
                let in_flight: i64 = s.data_points().map(|d| d.value()).sum();
                assert_eq!(in_flight, 1, "in flight");
                tracing::info!(secret = "hidden", "inside");
                StatusCode::SERVICE_UNAVAILABLE
            }
        }),
    );
    let app = App::new(&Base::default())
        .routes(routes)
        .with_telemetry(&mem.telemetry)
        .router();
    assert_eq!(
        send(&app, "GET", "/items/123", &[]).await,
        StatusCode::SERVICE_UNAVAILABLE
    );
    let spans = mem.spans();
    assert_eq!(spans.len(), 1, "{spans:?}");
    assert_eq!(spans[0].name, "GET /items/{id}");
    assert!(
        matches!(spans[0].status, opentelemetry::trace::Status::Error { .. }),
        "{:?}",
        spans[0].status
    );
    let text = cap.text();
    assert_eq!(text.matches("\"trace_id\"").count(), 2, "{text}");
    assert!(!text.contains("hidden"), "{text}");

    let rm = mem.collect();
    let count = metric(&rm, "http.server.request.count").expect("the request count");
    let AggregatedMetrics::U64(MetricData::Sum(s)) = count.data() else {
        panic!("not a u64 sum: {:?}", count.data())
    };
    let points: Vec<_> = s.data_points().collect();
    assert_eq!(points.len(), 1);
    assert_eq!(points[0].value(), 1);
    let attrs: Vec<KeyValue> = points[0].attributes().cloned().collect();
    assert_eq!(
        attr_of(&attrs, "http.route").as_deref(),
        Some("/items/{id}")
    );
    assert_eq!(
        attr_of(&attrs, "http.response.status_code").as_deref(),
        Some("503")
    );
    let active = metric(&rm, "http.server.active_requests").expect("active");
    let AggregatedMetrics::I64(MetricData::Sum(s)) = active.data() else {
        panic!("not an i64 sum")
    };
    assert_eq!(s.data_points().map(|d| d.value()).sum::<i64>(), 0);
    let duration = metric(&rm, telemetry::HTTP_DURATION).expect("the duration");
    let AggregatedMetrics::F64(MetricData::Histogram(h)) = duration.data() else {
        panic!("not an f64 histogram")
    };
    let points: Vec<_> = h.data_points().collect();
    assert_eq!(points.len(), 1);
    assert_eq!(points[0].count(), 1);
    let attrs: Vec<KeyValue> = points[0].attributes().cloned().collect();
    assert_eq!(
        attr_of(&attrs, "http.route").as_deref(),
        Some("/items/{id}")
    );
}

/// Go `TestHTTPMetricCardinality` and `TestHTTPMetricsUseFixedServerName`: unknown methods
/// share the `_OTHER` series, and the Host header adds no series.
#[tokio::test]
async fn telemetry_http_metric_cardinality() {
    let mem = Mem::install("test-api");
    let routes = Router::new().route(
        "/items/{id}",
        axum::routing::any(|| async { StatusCode::NO_CONTENT }),
    );
    let app = App::new(&Base::default())
        .routes(routes)
        .with_telemetry(&mem.telemetry)
        .router();
    for method in ["CUSTOM_ONE", "CUSTOM_TWO"] {
        send(&app, method, "/items/123", &[]).await;
    }
    for host in ["a.evil.com", "b.evil.com:1234", "c.evil.com:99"] {
        send(&app, "GET", "/items/1", &[("host", host)]).await;
    }
    let rm = mem.collect();
    let count = metric(&rm, "http.server.request.count").expect("the request count");
    let AggregatedMetrics::U64(MetricData::Sum(s)) = count.data() else {
        panic!("not a u64 sum")
    };
    let mut series: Vec<(String, u64)> = s
        .data_points()
        .map(|d| {
            let attrs: Vec<KeyValue> = d.attributes().cloned().collect();
            (attr_of(&attrs, "http.request.method").unwrap(), d.value())
        })
        .collect();
    series.sort();
    assert_eq!(
        series,
        vec![("GET".to_string(), 3), ("_OTHER".to_string(), 2)]
    );
}

/// The app exit hook flushes and stops the export (Go shutdown after `App.Run`).
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn telemetry_app_exit_hook_shuts_down() {
    let mem = Mem::install("test-api");
    let app = App::new(&Base::default())
        .with_telemetry(&mem.telemetry)
        .command(
            "noop",
            Command::new(|_| async { Ok::<(), anyhow::Error>(()) }),
        );
    assert!(mem.telemetry.enabled());
    app.run(vec!["noop".into()], std::future::pending())
        .await
        .unwrap();
    assert!(!mem.telemetry.enabled(), "the exit hook did not shut down");
}

// ---------------------------------------------------------------------------------------
// The metric names against the monitoring pack (R0.8)
// ---------------------------------------------------------------------------------------

/// One Prometheus series: the name and the labels.
type Series = (String, BTreeMap<String, String>);

/// Converts OTLP metrics to Prometheus series as the OTLP receivers of Prometheus and
/// Alloy (`otelcol.exporter.prometheus`) do with the default translation (underscore
/// escaping with suffixes):
/// - the name gets `_` for each character outside `[a-zA-Z0-9_:]`, then the unit suffix
///   (`s` is `seconds`; `{...}` units add nothing), then `_total` for a monotonic sum, or
///   `_bucket`, `_count`, and `_sum` for a histogram;
/// - attribute keys get `_` for each invalid character;
/// - `job` is `service.namespace/service.name`, or `service.name`.
fn prometheus_series(rm: &ResourceMetrics) -> Vec<Series> {
    fn sanitize(s: &str) -> String {
        let mut out: String = s
            .chars()
            .map(|c| {
                if c.is_ascii_alphanumeric() || c == '_' || c == ':' {
                    c
                } else {
                    '_'
                }
            })
            .collect();
        while out.contains("__") {
            out = out.replace("__", "_");
        }
        out
    }
    fn unit_suffix(unit: &str) -> Option<&'static str> {
        match unit {
            "s" => Some("seconds"),
            "ms" => Some("milliseconds"),
            "By" => Some("bytes"),
            _ => None,
        }
    }
    let resource: BTreeMap<String, String> = rm
        .resource()
        .iter()
        .map(|(k, v)| (k.as_str().to_owned(), v.as_str().into_owned()))
        .collect();
    let job = match (
        resource.get("service.namespace"),
        resource.get("service.name"),
    ) {
        (Some(ns), Some(name)) => format!("{ns}/{name}"),
        (None, Some(name)) => name.clone(),
        _ => String::new(),
    };
    let mut out = Vec::new();
    for sm in rm.scope_metrics() {
        for m in sm.metrics() {
            let mut name = sanitize(m.name());
            if let Some(suffix) = unit_suffix(m.unit())
                && !name.ends_with(suffix)
            {
                name = format!("{name}_{suffix}");
            }
            let labels_of = |attrs: &mut dyn Iterator<Item = &KeyValue>| {
                let mut l: BTreeMap<String, String> = attrs
                    .map(|kv| (sanitize(kv.key.as_str()), kv.value.as_str().into_owned()))
                    .collect();
                l.insert("job".into(), job.clone());
                l
            };
            match m.data() {
                AggregatedMetrics::F64(MetricData::Histogram(h)) => {
                    for dp in h.data_points() {
                        let labels = labels_of(&mut dp.attributes());
                        for le in dp
                            .bounds()
                            .map(|b| b.to_string())
                            .chain(["+Inf".to_owned()])
                        {
                            let mut l = labels.clone();
                            l.insert("le".into(), le);
                            out.push((format!("{name}_bucket"), l));
                        }
                        out.push((format!("{name}_count"), labels.clone()));
                        out.push((format!("{name}_sum"), labels));
                    }
                }
                AggregatedMetrics::I64(MetricData::Sum(s)) => {
                    let total = if s.is_monotonic() { "_total" } else { "" };
                    for dp in s.data_points() {
                        out.push((format!("{name}{total}"), labels_of(&mut dp.attributes())));
                    }
                }
                AggregatedMetrics::U64(MetricData::Sum(s)) => {
                    let total = if s.is_monotonic() { "_total" } else { "" };
                    for dp in s.data_points() {
                        out.push((format!("{name}{total}"), labels_of(&mut dp.attributes())));
                    }
                }
                AggregatedMetrics::F64(MetricData::Sum(s)) => {
                    let total = if s.is_monotonic() { "_total" } else { "" };
                    for dp in s.data_points() {
                        out.push((format!("{name}{total}"), labels_of(&mut dp.attributes())));
                    }
                }
                AggregatedMetrics::U64(MetricData::Gauge(g)) => {
                    for dp in g.data_points() {
                        out.push((name.clone(), labels_of(&mut dp.attributes())));
                    }
                }
                other => panic!("unexpected metric data {other:?}"),
            }
        }
    }
    out
}

fn monitoring_pack() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../spinup-packs/packs/monitoring/files/monitoring")
}

fn files(dir: &Path, out: &mut Vec<PathBuf>) {
    for e in std::fs::read_dir(dir).unwrap() {
        let p = e.unwrap().path();
        if p.is_dir() {
            files(&p, out);
        } else {
            out.push(p);
        }
    }
}

/// The HTTP metric names and labels that the monitoring pack queries: dashboards, alert
/// rules, and the Alloy configuration.
fn pack_names_and_labels() -> (BTreeSet<String>, BTreeSet<String>) {
    let name =
        regex::Regex::new(r"http_server_request_duration_seconds(_bucket|_count|_sum)?\b").unwrap();
    let by = regex::Regex::new(r"by \(([a-z_, ]+)\)").unwrap();
    let selector = regex::Regex::new(r"[{,]\s*([a-z_]+)\s*(=~|!~|!=|=)").unwrap();
    let mut paths = Vec::new();
    files(&monitoring_pack(), &mut paths);
    let (mut names, mut labels) = (BTreeSet::new(), BTreeSet::new());
    for p in paths {
        let text = std::fs::read_to_string(&p).unwrap_or_default();
        for line in text.lines().filter(|l| name.is_match(l)) {
            names.extend(name.find_iter(line).map(|m| m.as_str().to_owned()));
            for c in by.captures_iter(line) {
                labels.extend(c[1].split(',').map(|s| s.trim().to_owned()));
            }
            for c in selector.captures_iter(line) {
                labels.insert(c[1].to_owned());
            }
        }
    }
    (names, labels)
}

/// The R0.8 check: the HTTP metrics convert to the Prometheus names and labels that the
/// monitoring pack queries (`http_server_request_duration_seconds_count`,
/// `http_response_status_code`, `job=<app>-api`).
#[tokio::test]
async fn telemetry_metric_names_match_the_monitoring_pack() {
    let mem = Mem::install("todo-api");
    let app = App::new(&Base::default())
        .routes(api())
        .with_telemetry(&mem.telemetry)
        .router();
    assert_eq!(send(&app, "GET", "/api/todos/7", &[]).await, StatusCode::OK);
    assert_eq!(send(&app, "GET", "/api/todos/8", &[]).await, StatusCode::OK);
    assert_eq!(
        send(&app, "POST", "/api/todos", &[]).await,
        StatusCode::INTERNAL_SERVER_ERROR
    );
    assert_eq!(send(&app, "GET", "/nope", &[]).await, StatusCode::NOT_FOUND);

    let rm = mem.collect();
    // The OTLP side: the semantic convention name, unit, and bounds.
    let m = metric(&rm, telemetry::HTTP_DURATION).expect("the duration histogram");
    assert_eq!(m.unit(), "s");
    let AggregatedMetrics::F64(MetricData::Histogram(h)) = m.data() else {
        panic!("not an f64 histogram: {:?}", m.data())
    };
    let bounds: Vec<f64> = h.data_points().next().unwrap().bounds().collect();
    assert_eq!(bounds, telemetry::HTTP_DURATION_BOUNDS);
    let counts: u64 = h.data_points().map(|d| d.count()).sum();
    assert_eq!(counts, 4);

    // The Prometheus side: the names and labels that the pack queries.
    let series = prometheus_series(&rm);
    let count_series: BTreeSet<String> = series
        .iter()
        .filter(|(n, _)| n == "http_server_request_duration_seconds_count")
        .map(|(_, l)| format!("{l:?}"))
        .collect();
    let want = [
        r#"{"http_request_method": "GET", "http_response_status_code": "200", "http_route": "/api/todos/{id}", "job": "todo-api"}"#,
        r#"{"http_request_method": "GET", "http_response_status_code": "404", "job": "todo-api"}"#,
        r#"{"http_request_method": "POST", "http_response_status_code": "500", "http_route": "/api/todos", "job": "todo-api"}"#,
    ];
    assert_eq!(count_series, want.iter().map(|s| s.to_string()).collect());
    let names: BTreeSet<&str> = series.iter().map(|(n, _)| n.as_str()).collect();
    assert!(
        names.contains("http_server_request_count_total"),
        "{names:?}"
    );
    assert!(names.contains("http_server_active_requests"), "{names:?}");

    let (pack_names, pack_labels) = pack_names_and_labels();
    assert!(
        pack_names.contains("http_server_request_duration_seconds_count")
            && pack_names.contains("http_server_request_duration_seconds_bucket"),
        "the pack scan found {pack_names:?}"
    );
    for n in &pack_names {
        if n.ends_with("_count") || n.ends_with("_bucket") || n.ends_with("_sum") {
            assert!(
                names.contains(n.as_str()),
                "the pack queries {n}, the app has {names:?}"
            );
        }
    }
    let our_labels: BTreeSet<&str> = series
        .iter()
        .flat_map(|(_, l)| l.keys().map(String::as_str))
        .collect();
    for l in &pack_labels {
        assert!(
            our_labels.contains(l.as_str()),
            "the pack queries the label {l}, the app has {our_labels:?}"
        );
    }
}

// ---------------------------------------------------------------------------------------
// The database span layer (Go kit/db/telemetry_test.go)
// ---------------------------------------------------------------------------------------

/// A `sqlx::query` event becomes a client span under the current span, without SQL text.
#[tokio::test]
async fn telemetry_db_span_from_the_sqlx_event() {
    let mem = Mem::install("test-api");
    let _cap = mem.subscribe();
    assert!(
        tracing::enabled!(target: "sqlx::query", tracing::Level::DEBUG),
        "sqlx checks tracing::enabled! before it emits the event"
    );
    let parent = tracing::info_span!("request");
    async {
        tracing::debug!(
            target: "sqlx::query",
            summary = "select id from todos …",
            db.statement = "\n\nselect id from todos where secret = $1\n",
            rows_affected = 0u64,
            rows_returned = 3u64,
            elapsed_secs = 0.002,
        );
    }
    .instrument(parent)
    .await;
    let spans = mem.spans();
    let db = spans
        .iter()
        .find(|s| s.span_kind == SpanKind::Client)
        .unwrap_or_else(|| panic!("no database span in {spans:?}"));
    let request = spans
        .iter()
        .find(|s| s.name == "request")
        .expect("the request span");
    assert_eq!(db.name, "SELECT");
    assert_eq!(db.parent_span_id, request.span_context.span_id());
    assert_eq!(db.span_context.trace_id(), request.span_context.trace_id());
    assert_eq!(
        attr_of(&db.attributes, "db.system.name").as_deref(),
        Some("postgresql")
    );
    assert_eq!(
        attr_of(&db.attributes, "db.response.returned_rows").as_deref(),
        Some("3")
    );
    let text = format!("{db:?}");
    assert!(!text.contains("secret"), "the span has SQL text: {text}");
    let elapsed = db.end_time.duration_since(db.start_time).unwrap();
    assert!(elapsed.as_secs_f64() >= 0.0019, "{elapsed:?}");
}

/// Without telemetry the database layer records nothing (Go `TestPoolTelemetry`, disabled).
#[tokio::test]
async fn telemetry_db_layer_without_telemetry_records_nothing() {
    let subscriber = tracing_subscriber::registry().with(telemetry::db_layer());
    tracing::subscriber::set_global_default(subscriber).unwrap();
    tracing::debug!(target: "sqlx::query", summary = "SELECT 1", elapsed_secs = 0.001);
    let mut span = telemetry::tracer("test").start("check");
    assert!(!span.is_recording());
    span.end();
}

/// A real statement gives a database span under the request span (Go `TestPoolTelemetry`,
/// enabled). It needs Postgres.
#[sqlx::test(migrations = false)]
async fn pg_db_span(pool: sqlx::PgPool) {
    let mem = Mem::install("test-api");
    let _cap = mem.subscribe();
    let parent = tracing::info_span!("request");
    async {
        sqlx::query("SELECT 1").execute(&pool).await.unwrap();
    }
    .instrument(parent)
    .await;
    let spans = mem.spans();
    let request = spans
        .iter()
        .find(|s| s.name == "request")
        .expect("the request span");
    let db = spans
        .iter()
        .find(|s| s.span_kind == SpanKind::Client && s.name == "SELECT")
        .unwrap_or_else(|| panic!("no database span in {spans:?}"));
    assert_eq!(db.parent_span_id, request.span_context.span_id());
}

/// The log bridge drops events below the log level and the HTTP client events.
#[tokio::test]
async fn telemetry_log_bridge_filters() {
    let mem = Mem::install("test-api");
    let _cap = mem.subscribe();
    tracing::debug!("too low");
    tracing::info!(target: "hyper::proto", "client");
    tracing::warn!(api_key = "k", "kept");
    let logs = mem.logs.get_emitted_logs().unwrap();
    assert_eq!(logs.len(), 1, "{logs:?}");
    let text = format!("{:?}", logs[0].record);
    assert!(text.contains("kept") && !text.contains("\"k\""), "{text}");
}
