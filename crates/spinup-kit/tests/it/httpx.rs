//! The `httpx` tests of the kit (Go `kit/httpx/httpx_test.go`, `required_test.go`, and the
//! golden files `kit/httpx/testdata/*.golden`, read in place until R5).

use std::io::Write;
use std::sync::{Arc, Mutex};

use axum::Router;
use axum::body::Body;
use axum::http::{HeaderMap, HeaderValue, Request, StatusCode};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use http_body_util::BodyExt;
use serde::Deserialize;
use spinup_kit::httpx::{
    self, ApiError, ApiResult, FieldError, Path, Problem, Query, RequestId, ValidJson, page,
};
use spinup_kit::logx;
use tower::ServiceExt;
use tracing_subscriber::fmt::MakeWriter;
use tracing_subscriber::layer::SubscriberExt;
use utoipa::ToSchema;

/// One answer: status, headers, and body text.
pub(crate) struct Answer {
    pub status: StatusCode,
    pub headers: HeaderMap,
    pub body: String,
}

/// Sends `req` to `router`.
pub(crate) async fn send(router: Router, req: Request<Body>) -> Answer {
    let res = router.oneshot(req).await.expect("the router is infallible");
    let status = res.status();
    let headers = res.headers().clone();
    let bytes = res
        .into_body()
        .collect()
        .await
        .expect("the body can be read")
        .to_bytes();
    Answer {
        status,
        headers,
        body: String::from_utf8_lossy(&bytes).into_owned(),
    }
}

/// A GET request.
pub(crate) fn get_req(path: &str) -> Request<Body> {
    Request::get(path)
        .body(Body::empty())
        .expect("valid request")
}

/// A buffer that the log layer writes to.
#[derive(Clone, Default)]
pub(crate) struct Capture(Arc<Mutex<Vec<u8>>>);

impl Capture {
    /// The JSON log records so far.
    pub fn records(&self) -> Vec<serde_json::Value> {
        self.text()
            .lines()
            .map(|l| serde_json::from_str(l).expect("each log line is JSON"))
            .collect()
    }

    /// The log text so far.
    pub fn text(&self) -> String {
        String::from_utf8_lossy(&self.0.lock().expect("lock")).into_owned()
    }
}

impl Write for Capture {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        self.0.lock().expect("lock").extend_from_slice(buf);
        Ok(buf.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

impl<'a> MakeWriter<'a> for Capture {
    type Writer = Capture;
    fn make_writer(&'a self) -> Self::Writer {
        self.clone()
    }
}

/// Sends the log of this thread to a buffer, with `options`.
pub(crate) fn capture_with(options: logx::Options) -> (tracing::subscriber::DefaultGuard, Capture) {
    let cap = Capture::default();
    let subscriber = tracing_subscriber::registry().with(logx::layer_with(options, cap.clone()));
    (tracing::subscriber::set_default(subscriber), cap)
}

/// Sends the JSON log of this thread at debug level to a buffer.
pub(crate) fn capture() -> (tracing::subscriber::DefaultGuard, Capture) {
    capture_with(logx::Options {
        level: tracing::Level::DEBUG,
        ..logx::Options::default()
    })
}

#[derive(Debug, Deserialize, ToSchema)]
#[allow(dead_code)]
struct Named {
    #[serde(default)]
    name: String,
}

async fn decode_named(ValidJson(n): ValidJson<Named>) -> ApiResult<String> {
    Ok(n.name)
}

async fn private_panic() -> &'static str {
    panic!("private panic")
}

async fn secret_panic() -> &'static str {
    panic!("secret")
}

fn golden_router(case: &str) -> Router {
    let router = match case {
        "panic" => Router::new().route("/items", post(private_panic)),
        "validation" => Router::new().route(
            "/items",
            post(|| async {
                Problem::new(StatusCode::UNPROCESSABLE_ENTITY, "Validation failed.")
                    .with_errors(vec![FieldError::new("name", "is required")])
            }),
        ),
        _ => Router::new().route("/items", post(decode_named)),
    };
    let router = if case == "large" {
        httpx::max_body(router, 5)
    } else {
        router
    };
    httpx::stack(router)
}

#[tokio::test]
async fn httpx_golden_errors() {
    let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../kit/httpx/testdata");
    for (case, body) in [
        ("syntax", r#"{"name":"#),
        ("unknown", r#"{"other":1}"#),
        ("large", r#"{"name":"too large"}"#),
        ("empty", ""),
        ("validation", ""),
        ("panic", ""),
    ] {
        let req = Request::post("/items")
            .header("x-request-id", "golden-request")
            .body(Body::from(body))
            .unwrap();
        let a = send(golden_router(case), req).await;
        let got = format!(
            "HTTP {}\nContent-Type: {}\nX-Request-ID: {}\n\n{}",
            a.status.as_u16(),
            a.headers["content-type"].to_str().unwrap(),
            a.headers["x-request-id"].to_str().unwrap(),
            a.body
        );
        let want = std::fs::read_to_string(dir.join(format!("{case}.golden"))).unwrap();
        assert_eq!(got, want, "case {case}");
    }
}

#[tokio::test]
async fn httpx_max_body_content_length_is_a_problem() {
    let router = httpx::stack(httpx::max_body(
        Router::new().route("/items", post(decode_named)),
        5,
    ));
    let req = Request::post("/items")
        .header("content-length", "20")
        .header("x-request-id", "r1")
        .body(Body::from(r#"{"name":"too large"}"#))
        .unwrap();
    let a = send(router, req).await;
    assert_eq!(a.status, 413);
    assert_eq!(a.headers["content-type"], "application/problem+json");
    assert!(a.body.contains(httpx::TOO_LARGE), "{}", a.body);
    assert!(a.body.contains(r#""request_id":"r1""#), "{}", a.body);
}

#[derive(Debug, Deserialize, ToSchema)]
#[allow(dead_code)]
struct Input {
    title: String,
    done: bool,
    #[serde(default)]
    note: Option<String>,
    kind: Kind,
}

#[derive(Debug, Deserialize, ToSchema)]
#[serde(rename_all = "lowercase")]
enum Kind {
    Low,
    High,
}

fn status_and_errors(body: &str) -> (u16, Option<String>, Vec<String>) {
    let err = httpx::decode::<Input>(body.as_bytes()).unwrap_err().0;
    (
        err.status,
        err.detail,
        err.errors
            .iter()
            .map(|e| format!("{}: {}", e.field, e.message))
            .collect(),
    )
}

#[test]
fn httpx_required_comes_from_the_schema() {
    assert_eq!(httpx::required_fields::<Input>(), ["title", "done", "kind"]);
}

#[test]
fn httpx_decode_rules() {
    let ok = r#""done": true, "kind": "low""#;
    for (body, status, detail) in [
        ("", 400, httpx::ONE_VALUE),
        ("   ", 400, httpx::ONE_VALUE),
        ("{", 400, httpx::BAD_JSON),
        (r#"{"title":"#, 400, httpx::BAD_JSON),
        (r#"{} {}"#, 400, httpx::ONLY_ONE_VALUE),
        (r#"{} x"#, 400, httpx::BAD_JSON),
        ("[]", 422, httpx::NOT_OBJECT),
        ("true", 422, httpx::NOT_OBJECT),
        ("42", 422, httpx::NOT_OBJECT),
    ] {
        let (s, d, _) = status_and_errors(body);
        assert_eq!((s, d.as_deref()), (status, Some(detail)), "body {body:?}");
    }
    // Unknown properties: the Go DisallowUnknownFields rule.
    let (s, d, _) = status_and_errors(&format!(r#"{{"title": "a", {ok}, "other": 1}}"#));
    assert_eq!((s, d.as_deref()), (400, Some(httpx::BAD_JSON)));
    // Missing and null required properties, sorted by name.
    for body in [
        "null",
        "{}",
        r#"{"title": null, "done": null, "kind": null}"#,
    ] {
        assert_eq!(
            status_and_errors(body),
            (
                422,
                Some("The request has invalid values.".into()),
                vec![
                    "done: is required".into(),
                    "kind: is required".into(),
                    "title: is required".into()
                ]
            ),
            "body {body}"
        );
    }
    // Exact key names, and the last of two equal keys.
    let (s, _, e) = status_and_errors(&format!(r#"{{"TITLE": "a", {ok}}}"#));
    assert_eq!((s, e), (422, vec!["title: is required".to_string()]));
    let (s, _, e) = status_and_errors(&format!(r#"{{"title": "a", "title": null, {ok}}}"#));
    assert_eq!((s, e), (422, vec!["title: is required".to_string()]));
    // Types and enums.
    assert_eq!(
        status_and_errors(r#"{"title": 42, "done": true, "kind": "low"}"#).2,
        vec!["title: has the wrong type".to_string()]
    );
    assert_eq!(
        status_and_errors(r#"{"title": "a", "done": true, "kind": "mid"}"#).2,
        vec!["kind: must be one of: low, high".to_string()]
    );
    // Zero values and optional nulls pass.
    let v: Input =
        httpx::decode(br#"{"title": "", "done": false, "kind": "high", "note": null}"#).unwrap();
    assert_eq!(v.title, "");
    assert!(matches!(v.kind, Kind::High));
}

#[derive(Debug, Deserialize, ToSchema)]
struct Empty {}

#[test]
fn httpx_decode_without_required_fields() {
    assert!(httpx::decode::<Empty>(b"{}").is_ok());
    assert!(httpx::decode::<Empty>(b"null").is_ok());
    let open: serde_json::Value = httpx::decode(br#"{"any": 1}"#).unwrap();
    assert_eq!(open["any"], 1);
}

#[tokio::test]
async fn httpx_request_id() {
    let router = Router::new()
        .route("/", get(|id: RequestId| async move { id.0 }))
        .layer(axum::middleware::from_fn(httpx::request_id));
    let mut generated = std::collections::HashSet::new();
    for incoming in [
        "safe_ID-1.2".to_string(),
        "x".repeat(128),
        String::new(),
        "bad id".into(),
        "é".into(),
        "x".repeat(129),
    ] {
        let mut req = get_req("/");
        if let Ok(v) = HeaderValue::from_str(&incoming) {
            req.headers_mut().insert("x-request-id", v);
        }
        let a = send(router.clone(), req).await;
        let got = a.headers["x-request-id"].to_str().unwrap().to_string();
        assert_eq!(a.body, got, "the extension equals the header");
        assert!(httpx::valid_request_id(&got));
        if httpx::valid_request_id(&incoming) {
            assert_eq!(got, incoming);
        } else {
            assert_eq!(got.len(), 32);
            assert!(generated.insert(got), "duplicate generated ID");
        }
    }
    // Without the layer, the extractor gives an empty ID.
    let bare = Router::new().route("/", get(|id: RequestId| async move { id.0 }));
    assert_eq!(send(bare, get_req("/")).await.body, "");
}

#[tokio::test]
async fn httpx_security_headers_keep_handler_values() {
    let router = Router::new()
        .route(
            "/",
            get(|| async { ([("x-frame-options", "SAMEORIGIN")], "ok") }),
        )
        .layer(axum::middleware::from_fn(httpx::security_headers));
    let a = send(router, get_req("/")).await;
    for (k, v) in [
        ("x-content-type-options", "nosniff"),
        ("referrer-policy", "strict-origin-when-cross-origin"),
        ("x-frame-options", "SAMEORIGIN"),
        (
            "content-security-policy",
            "default-src 'none'; frame-ancestors 'none'",
        ),
    ] {
        assert_eq!(a.headers[k], v, "{k}");
    }
}

#[tokio::test]
async fn httpx_recover_logs_one_line_without_the_secret() {
    let (_guard, logs) = capture();
    let router = httpx::stack(Router::new().route("/", get(secret_panic)));
    let a = send(router, get_req("/")).await;
    assert_eq!(a.status, 500);
    assert!(!a.body.contains("secret"), "{}", a.body);
    let id = a.headers["x-request-id"].to_str().unwrap();
    let records = logs.records();
    let errors: Vec<_> = records.iter().filter(|r| r["level"] == "ERROR").collect();
    assert_eq!(errors.len(), 1, "{records:?}");
    assert_eq!(errors[0]["msg"], "HTTP handler panicked");
    assert_eq!(errors[0]["request_id"], id);
    // The access log line comes after the panic line, with the final status and size.
    let access = records.last().unwrap();
    assert_eq!(access["msg"], "HTTP request");
    assert_eq!(access["level"], "INFO");
    assert_eq!(access["status"], 500);
    assert_eq!(access["bytes"], a.body.len());
}

#[tokio::test]
async fn httpx_access_log() {
    for status in [200u16, 201, 204] {
        let (_guard, logs) = capture();
        let handler = move || async move {
            let code = StatusCode::from_u16(status).unwrap();
            if status == 204 {
                code.into_response()
            } else {
                (code, "hello").into_response()
            }
        };
        let router = httpx::stack(Router::new().route("/items", post(handler)));
        let req = Request::post("/items?secret=hidden")
            .header("x-request-id", "access-id")
            .body(Body::empty())
            .unwrap();
        send(router, req).await;
        let records = logs.records();
        assert_eq!(records.len(), 1, "{records:?}");
        let r = &records[0];
        assert_eq!(r["status"], status);
        assert_eq!(r["bytes"], if status == 204 { 0 } else { 5 });
        assert_eq!(r["method"], "POST");
        assert_eq!(r["path"], "/items");
        assert_eq!(r["route"], "/items");
        assert_eq!(r["request_id"], "access-id");
        assert!(r["duration_ms"].is_number());
        assert!(!logs.text().contains("hidden"));
    }
}

#[test]
fn httpx_problem_defaults_follow_go_write_problem() {
    let p = Problem::status(StatusCode::NOT_FOUND).normalized();
    assert_eq!(
        serde_json::to_value(&p).unwrap(),
        serde_json::json!({"type": "about:blank", "title": "Not Found", "status": 404})
    );
    assert_eq!(
        Problem::status(StatusCode::PAYLOAD_TOO_LARGE)
            .normalized()
            .title,
        "Request Entity Too Large"
    );
    assert_eq!(Problem::default().normalized().status, 500);
    let res = ApiError::invalid(vec![FieldError::new("title", "is required")]).into_response();
    assert_eq!(res.status(), 422);
    assert_eq!(res.headers()["content-type"], httpx::PROBLEM_JSON);
    assert_eq!(
        ApiError::not_found().to_string(),
        "Not Found: The record does not exist."
    );
    assert_eq!(ApiError::internal().to_string(), "Internal Server Error");
}

#[test]
fn httpx_api_error_from_anyhow() {
    let internal = ApiError::from(anyhow::anyhow!("internal secret"));
    assert_eq!(internal.problem().status, 500);
    assert_eq!(internal.problem().detail, None);
    let want = ApiError::new(StatusCode::UNPROCESSABLE_ENTITY, "invalid input");
    let wrapped = anyhow::Error::new(want.clone()).context("wrapped");
    assert_eq!(ApiError::from(wrapped), want);
}

#[derive(Debug, Deserialize)]
struct ListParams {
    limit: Option<i32>,
}

#[tokio::test]
async fn httpx_path_and_query_name_the_parameter() {
    let router = httpx::stack(
        Router::new()
            .route(
                "/items/{id}",
                get(|Path(id): Path<i64>| async move { id.to_string() }),
            )
            .route(
                "/items",
                get(|Query(q): Query<ListParams>| async move {
                    let (limit, _) = page::resolve(q.limit, None)?;
                    Ok::<_, ApiError>(limit.to_string())
                }),
            ),
    );
    let a = send(router.clone(), get_req("/items/7")).await;
    assert_eq!((a.status.as_u16(), a.body.as_str()), (200, "7"));
    for path in ["/items/abc", "/items?limit=x"] {
        let a = send(router.clone(), get_req(path)).await;
        assert_eq!(a.status, 400, "{path}");
        let p: Problem = serde_json::from_str(&a.body).unwrap();
        assert_eq!(
            p.detail.as_deref(),
            Some("A parameter has an invalid format.")
        );
        let field = if path.contains("limit") {
            "limit"
        } else {
            "id"
        };
        assert_eq!(
            p.errors,
            vec![FieldError::new(field, "has an invalid format")]
        );
        assert!(p.request_id.is_some());
    }
    let a = send(router.clone(), get_req("/items")).await;
    assert_eq!(a.body, "50");
    let a = send(router, get_req("/items?limit=500")).await;
    assert_eq!(a.status, 422);
}

#[test]
fn httpx_page_limits() {
    assert_eq!(page::resolve(None, None).unwrap(), (50, 0));
    assert_eq!(page::resolve(Some(200), Some(3)).unwrap(), (200, 3));
    let err = page::resolve(Some(201), Some(-1)).unwrap_err();
    assert_eq!(
        err.0.errors,
        vec![
            FieldError::new("limit", "must be from 1 to 200"),
            FieldError::new("offset", "must not be negative")
        ]
    );
}
