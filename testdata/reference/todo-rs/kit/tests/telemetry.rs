//! The metric names of the kit telemetry against the monitoring pack
//! (in-memory exporter). The OTLP export test is in `otlp.rs`.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use axum::Router;
use axum::body::Body;
use axum::http::{Request, StatusCode};
use axum::routing::{get, post};
use kit::telemetry::HttpMetrics;
use opentelemetry::KeyValue;
use opentelemetry::metrics::MeterProvider as _;
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::metrics::data::{AggregatedMetrics, MetricData, ResourceMetrics};
use opentelemetry_sdk::metrics::{InMemoryMetricExporter, PeriodicReader, SdkMeterProvider};
use tower::ServiceExt;

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
        .join("../../../../internal/templates/packs/monitoring/files/monitoring")
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
