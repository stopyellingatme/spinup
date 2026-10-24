//! Tests of the kit telemetry: the metric names against the monitoring pack
//! (in-memory exporter), and the OTLP HTTP export (a fake collector).

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use axum::Router;
use axum::body::{Body, Bytes};
use axum::extract::{self, State};
use axum::http::{HeaderMap, Request, StatusCode};
use axum::routing::{get, post};
use kit::telemetry::{HttpMetrics, Options, Providers};
use opentelemetry::KeyValue;
use opentelemetry::metrics::MeterProvider as _;
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::metrics::data::{AggregatedMetrics, MetricData, ResourceMetrics};
use opentelemetry_sdk::metrics::{InMemoryMetricExporter, PeriodicReader, SdkMeterProvider};
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

/// One Prometheus series: the name and the labels.
type Series = (String, BTreeMap<String, String>);

/// Converts OTLP metrics to Prometheus series as the OTLP receivers of
/// Prometheus and Alloy (`otelcol.exporter.prometheus`) do with the default
/// translation (underscore escaping with suffixes):
/// - the name gets `_` for each character outside `[a-zA-Z0-9_:]`, then the
///   unit suffix (`s` is `seconds`; `{...}` units add nothing), then `_total`
///   for a monotonic sum, or `_bucket`, `_count`, and `_sum` for a histogram;
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
                other => panic!("unexpected metric data {other:?}"),
            }
        }
    }
    out
}

fn monitoring_pack() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../internal/templates/packs/monitoring/files/monitoring")
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

/// The HTTP metric names and labels that the monitoring pack queries:
/// dashboards, alert rules, and the Alloy configuration.
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

#[tokio::test]
async fn metric_names_match_the_monitoring_pack() {
    let exporter = InMemoryMetricExporter::default();
    let provider = SdkMeterProvider::builder()
        .with_resource(
            Resource::builder_empty()
                .with_service_name("todo-api")
                .build(),
        )
        .with_reader(PeriodicReader::builder(exporter.clone()).build())
        .build();
    let metrics = HttpMetrics::new(&provider.meter(kit::telemetry::SCOPE));
    let app = kit::app::router_with_metrics(api(), None, metrics);

    assert_eq!(send(&app, "GET", "/api/todos/7", &[]).await, StatusCode::OK);
    assert_eq!(send(&app, "GET", "/api/todos/8", &[]).await, StatusCode::OK);
    assert_eq!(
        send(&app, "POST", "/api/todos", &[]).await,
        StatusCode::INTERNAL_SERVER_ERROR
    );
    assert_eq!(send(&app, "GET", "/nope", &[]).await, StatusCode::NOT_FOUND);
    provider.force_flush().unwrap();

    let finished = exporter.get_finished_metrics().unwrap();
    let rm = finished.last().expect("one export");
    // The OTLP side: the semantic convention name, unit, and bounds.
    let m = rm
        .scope_metrics()
        .flat_map(|s| s.metrics())
        .find(|m| m.name() == kit::telemetry::HTTP_DURATION)
        .expect("the duration histogram");
    assert_eq!(m.unit(), "s");
    let AggregatedMetrics::F64(MetricData::Histogram(h)) = m.data() else {
        panic!("not an f64 histogram: {:?}", m.data())
    };
    let bounds: Vec<f64> = h.data_points().next().unwrap().bounds().collect();
    assert_eq!(bounds, kit::telemetry::HTTP_DURATION_BOUNDS);
    let counts: u64 = h.data_points().map(|d| d.count()).sum();
    assert_eq!(counts, 4);

    // The Prometheus side: the names and labels that the pack queries.
    let series = prometheus_series(rm);
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

    let (pack_names, pack_labels) = pack_names_and_labels();
    assert!(
        pack_names.contains("http_server_request_duration_seconds_count")
            && pack_names.contains("http_server_request_duration_seconds_bucket"),
        "the pack scan found {pack_names:?}"
    );
    let ours: BTreeSet<&str> = series.iter().map(|(n, _)| n.as_str()).collect();
    for n in &pack_names {
        if n.ends_with("_count") || n.ends_with("_bucket") || n.ends_with("_sum") {
            assert!(
                ours.contains(n.as_str()),
                "the pack queries {n}, the app has {ours:?}"
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
    println!("pack metric names {pack_names:?}, labels {pack_labels:?}");
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
    let subscriber = tracing_subscriber::registry().with(providers.layers());
    let guard = tracing::subscriber::set_default(subscriber);
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
    drop(guard);
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
