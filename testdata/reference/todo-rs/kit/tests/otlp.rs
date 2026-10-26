//! The OTLP HTTP export of the kit telemetry, against a fake collector.
//!
//! This test has its own test binary and installs a global subscriber. A
//! scoped subscriber (`set_default`) is not safe next to other tests: when
//! only one dispatcher is registered, tracing computes the cached interest
//! of a new callsite from the default dispatcher of the thread that hits it
//! first. A test thread without a subscriber then caches "never" for the
//! span callsite of the HTTP layer, and this test loses its spans.

use std::collections::BTreeSet;
use std::sync::{Arc, Mutex};

use axum::Router;
use axum::body::{Body, Bytes};
use axum::extract::{self, State};
use axum::http::{HeaderMap, Request, StatusCode};
use axum::routing::{get, post};
use kit::telemetry::{HttpMetrics, Options, Providers};
use opentelemetry::metrics::MeterProvider as _;
use prost::Message;
use tower::ServiceExt;
use tracing_subscriber::layer::SubscriberExt;

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

type Received = Arc<Mutex<Vec<(String, String, Bytes)>>>;

async fn collect(
    State(received): State<Received>,
    extract::Path(signal): extract::Path<String>,
    headers: HeaderMap,
    body: Bytes,
) -> StatusCode {
    let ct = headers
        .get("content-type")
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default()
        .to_owned();
    received.lock().unwrap().push((signal, ct, body));
    StatusCode::OK
}

fn attr(attrs: &[opentelemetry_proto::tonic::common::v1::KeyValue], key: &str) -> Option<String> {
    use opentelemetry_proto::tonic::common::v1::any_value::Value;
    attrs.iter().find(|kv| kv.key == key).and_then(|kv| {
        match kv.value.as_ref()?.value.as_ref()? {
            Value::StringValue(s) => Some(s.clone()),
            Value::IntValue(i) => Some(i.to_string()),
            other => Some(format!("{other:?}")),
        }
    })
}

/// The OTLP HTTP exporters send protobuf to `/v1/traces`, `/v1/metrics`,
/// and `/v1/logs`. The server span continues the `traceparent` of the
/// request, and the log record of the handler carries its trace ID.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn otlp_http_export_reaches_the_collector() {
    let received: Received = Arc::default();
    let collector = Router::new()
        .route("/v1/{signal}", post(collect))
        .with_state(received.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(listener, collector).await.unwrap() });

    let opts = Options {
        service_version: "1.2.3".into(),
        environment: "test".into(),
        ..Options::new("todo-api")
    };
    let providers = Providers::new(&opts, &format!("http://{addr}")).unwrap();
    opentelemetry::global::set_text_map_propagator(
        opentelemetry_sdk::propagation::TraceContextPropagator::new(),
    );
    tracing::subscriber::set_global_default(
        tracing_subscriber::registry().with(providers.layers()),
    )
    .expect("the only global subscriber of this test binary");
    let metrics = HttpMetrics::new(&providers.meter.meter(kit::telemetry::SCOPE));
    let app = kit::app::router_with_metrics(api(), None, metrics);
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
    providers.shutdown().await.unwrap();

    use opentelemetry_proto::tonic::collector::logs::v1::ExportLogsServiceRequest;
    use opentelemetry_proto::tonic::collector::metrics::v1::ExportMetricsServiceRequest;
    use opentelemetry_proto::tonic::collector::trace::v1::ExportTraceServiceRequest;
    use opentelemetry_proto::tonic::metrics::v1::metric::Data;
    let received = received.lock().unwrap().clone();
    let signals: BTreeSet<&str> = received.iter().map(|(s, _, _)| s.as_str()).collect();
    assert_eq!(signals, ["logs", "metrics", "traces"].into_iter().collect());
    for (_, ct, _) in &received {
        assert_eq!(ct, "application/x-protobuf");
    }

    // Traces: the server span continues the remote parent.
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
    assert_eq!(
        attr(&resource.attributes, "service.name").as_deref(),
        Some("todo-api")
    );
    assert_eq!(
        attr(&resource.attributes, "service.version").as_deref(),
        Some("1.2.3")
    );
    let span = spans
        .iter()
        .flat_map(|r| &r.scope_spans)
        .flat_map(|s| &s.spans)
        .find(|s| s.name == "GET /api/todos/{id}")
        .expect("the server span");
    assert_eq!(hex(&span.trace_id), trace_id);
    assert_eq!(hex(&span.parent_span_id), parent);
    assert_eq!(span.kind, 2, "SPAN_KIND_SERVER");
    assert_eq!(
        attr(&span.attributes, "http.route").as_deref(),
        Some("/api/todos/{id}")
    );
    assert_eq!(
        attr(&span.attributes, "http.response.status_code").as_deref(),
        Some("200")
    );

    // Metrics: the histogram with its unit and labels.
    let metric = received
        .iter()
        .filter(|(s, _, _)| s == "metrics")
        .flat_map(|(_, _, b)| {
            ExportMetricsServiceRequest::decode(b.clone())
                .unwrap()
                .resource_metrics
        })
        .flat_map(|r| r.scope_metrics)
        .flat_map(|s| s.metrics)
        .find(|m| m.name == "http.server.request.duration")
        .expect("the duration metric");
    assert_eq!(metric.unit, "s");
    let Some(Data::Histogram(h)) = metric.data else {
        panic!("not a histogram")
    };
    let dp = &h.data_points[0];
    assert_eq!(dp.count, 1);
    assert_eq!(
        attr(&dp.attributes, "http.route").as_deref(),
        Some("/api/todos/{id}")
    );
    assert_eq!(
        attr(&dp.attributes, "http.request.method").as_deref(),
        Some("GET")
    );
    assert_eq!(
        attr(&dp.attributes, "http.response.status_code").as_deref(),
        Some("200")
    );

    // Logs: the handler event and the access log line carry the trace ID.
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
    let bodies: Vec<String> = logs
        .iter()
        .map(|l| format!("{:?}", l.body.as_ref().and_then(|b| b.value.as_ref())))
        .collect();
    let loaded = logs
        .iter()
        .zip(&bodies)
        .find(|(_, b)| b.contains("todo loaded"))
        .unwrap_or_else(|| panic!("no handler log in {bodies:?}"))
        .0;
    assert_eq!(hex(&loaded.trace_id), trace_id);
    assert!(
        bodies.iter().any(|b| b.contains("request")),
        "no access log in {bodies:?}"
    );
    println!(
        "OTLP requests: {:?}",
        received
            .iter()
            .map(|(s, _, b)| (s.as_str(), b.len()))
            .collect::<Vec<_>>()
    );
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}
