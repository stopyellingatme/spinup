//! The HTTP layers of the Go `httpx` middleware: request ID, access log, recover, security
//! headers, and the body limit.
//!
//! Each layer is an `axum::middleware::from_fn` function. The app applies them in the Go
//! order, from the outside in: [`request_id`], the telemetry layers, [`access_log`],
//! [`recover`], [`security_headers`], then the routes. [`stack`] applies the same order to
//! a router without the app (tests use it).

use std::any::Any;
use std::convert::Infallible;
use std::time::Instant;

use axum::Router;
use axum::body::{Body, HttpBody};
use axum::extract::{DefaultBodyLimit, FromRequestParts, MatchedPath, Request};
use axum::http::request::Parts;
use axum::http::{HeaderName, HeaderValue, StatusCode, header};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use tower::{Layer, ServiceExt};
use tower_http::catch_panic::{CatchPanicLayer, ResponseForPanic};
use tower_http::limit::RequestBodyLimitLayer;

use super::extract::TOO_LARGE;
use super::problem::Problem;

/// The request ID header.
pub static X_REQUEST_ID: HeaderName = HeaderName::from_static("x-request-id");

/// The request ID of a request (Go `httpx.RequestIDFrom`). The [`request_id`] layer puts it
/// in the request extensions. As an extractor it gives an empty ID when the layer is
/// absent.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct RequestId(pub String);

impl<S: Send + Sync> FromRequestParts<S> for RequestId {
    type Rejection = Infallible;

    async fn from_request_parts(parts: &mut Parts, _state: &S) -> Result<Self, Self::Rejection> {
        Ok(parts
            .extensions
            .get::<RequestId>()
            .cloned()
            .unwrap_or_default())
    }
}

/// Reports whether `id` is a safe request ID: 1 to 128 ASCII letters, digits, `.`, `_`, or
/// `-`.
pub fn valid_request_id(id: &str) -> bool {
    (1..=128).contains(&id.len())
        && id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'.' || c == b'_' || c == b'-')
}

/// Accepts a safe `X-Request-ID` or makes a random one (32 hex digits). It puts the ID in
/// the request extensions ([`RequestId`]) and in the response header.
pub async fn request_id(mut req: Request, next: Next) -> Response {
    let id = req
        .headers()
        .get(&X_REQUEST_ID)
        .and_then(|v| v.to_str().ok())
        .filter(|v| valid_request_id(v))
        .map(str::to_owned)
        .unwrap_or_else(|| uuid::Uuid::new_v4().simple().to_string());
    req.extensions_mut().insert(RequestId(id.clone()));
    let mut res = next.run(req).await;
    if let Ok(v) = HeaderValue::from_str(&id) {
        res.headers_mut().insert(X_REQUEST_ID.clone(), v);
    }
    res
}

/// Writes one info line for each request (Go `httpx.AccessLog`): `method`, `path`,
/// `route`, `status`, `bytes`, `duration_ms`, and `request_id`. The path has no query.
pub async fn access_log(req: Request, next: Next) -> Response {
    let start = Instant::now();
    let method = req.method().clone();
    let path = req.uri().path().to_owned();
    let route = req
        .extensions()
        .get::<MatchedPath>()
        .map(|m| m.as_str().to_owned())
        .unwrap_or_default();
    let id = request_id_of(&req);
    let res = next.run(req).await;
    let bytes = res.body().size_hint().exact().unwrap_or(0);
    tracing::info!(
        method = %method,
        path = %path,
        route = %route,
        status = res.status().as_u16(),
        bytes,
        duration_ms = start.elapsed().as_secs_f64() * 1000.0,
        request_id = %id,
        "HTTP request"
    );
    res
}

fn request_id_of(req: &Request) -> String {
    req.extensions()
        .get::<RequestId>()
        .map(|r| r.0.clone())
        .unwrap_or_default()
}

/// The text of a panic payload.
fn panic_text(payload: &(dyn Any + Send)) -> String {
    if let Some(s) = payload.downcast_ref::<String>() {
        s.clone()
    } else if let Some(s) = payload.downcast_ref::<&str>() {
        (*s).to_string()
    } else {
        "a value that is not text".into()
    }
}

/// The panic answer of [`recover`]: one error line, then a 500 problem.
#[derive(Clone)]
struct OnPanic {
    request_id: String,
}

impl ResponseForPanic for OnPanic {
    type ResponseBody = Body;

    fn response_for_panic(&mut self, payload: Box<dyn Any + Send + 'static>) -> Response {
        tracing::error!(
            request_id = %self.request_id,
            panic = %panic_text(payload.as_ref()),
            "HTTP handler panicked"
        );
        Problem::status(StatusCode::INTERNAL_SERVER_ERROR).into_response()
    }
}

/// Turns a handler panic into a 500 problem and logs one error line with the request ID (Go
/// `httpx.Recover`, with `tower-http` `catch-panic`). The answer holds no panic text.
///
/// It also adds the request ID to the body of every problem answer, as the Go
/// `WriteProblem` does. It runs inside [`access_log`], so the logged `bytes` count is the
/// size of the final body.
pub async fn recover(req: Request, next: Next) -> Response {
    let id = request_id_of(&req);
    let on_panic = OnPanic {
        request_id: id.clone(),
    };
    let inner = tower::service_fn(move |req: Request| {
        let next = next.clone();
        async move { Ok::<_, Infallible>(next.run(req).await) }
    });
    let res = match CatchPanicLayer::custom(on_panic)
        .layer(inner)
        .oneshot(req)
        .await
    {
        Ok(res) => res.map(Body::new),
        Err(never) => match never {},
    };
    with_request_id(res, &id)
}

/// Writes `id` into the body of a problem answer.
fn with_request_id(mut res: Response, id: &str) -> Response {
    if id.is_empty() {
        return res;
    }
    let Some(problem) = res.extensions_mut().get_mut::<Problem>() else {
        return res;
    };
    if problem.request_id.is_some() {
        return res;
    }
    problem.request_id = Some(id.to_string());
    let body = problem.body();
    res.headers_mut().remove(header::CONTENT_LENGTH);
    *res.body_mut() = Body::from(body);
    res
}

/// The security headers of the Go `httpx.SecurityHeaders`. A header that the handler set
/// stays.
pub const SECURITY_HEADERS: [(&str, &str); 4] = [
    ("x-content-type-options", "nosniff"),
    ("referrer-policy", "strict-origin-when-cross-origin"),
    ("x-frame-options", "DENY"),
    (
        "content-security-policy",
        "default-src 'none'; frame-ancestors 'none'",
    ),
];

/// Sets [`SECURITY_HEADERS`] on each answer that does not have them yet.
pub async fn security_headers(req: Request, next: Next) -> Response {
    let mut res = next.run(req).await;
    let headers = res.headers_mut();
    for (name, value) in SECURITY_HEADERS {
        if !headers.contains_key(name) {
            headers.insert(name, HeaderValue::from_static(value));
        }
    }
    res
}

/// Limits the request body of `router` to `limit` bytes (Go `httpx.MaxBody`, with
/// `tower-http` `limit`). A larger `Content-Length` gets a 413 problem at once. A larger
/// streamed body makes [`super::ValidJson`] answer a 413 problem. The axum default limit
/// (2 MiB) no longer applies to the router.
pub fn max_body<S>(router: Router<S>, limit: usize) -> Router<S>
where
    S: Clone + Send + Sync + 'static,
{
    router
        .layer(DefaultBodyLimit::disable())
        .layer(RequestBodyLimitLayer::new(limit))
        .layer(middleware::from_fn(too_large_problem))
}

/// Turns the plain 413 answer of `RequestBodyLimit` into a problem.
async fn too_large_problem(req: Request, next: Next) -> Response {
    let res = next.run(req).await;
    if res.status() == StatusCode::PAYLOAD_TOO_LARGE && res.extensions().get::<Problem>().is_none()
    {
        return Problem::new(StatusCode::PAYLOAD_TOO_LARGE, TOO_LARGE).into_response();
    }
    res
}

/// Applies the kit layers to `router` in the app order: [`request_id`] outermost, then
/// [`access_log`], [`recover`], and [`security_headers`].
pub fn stack(router: Router) -> Router {
    router
        .layer(middleware::from_fn(security_headers))
        .layer(middleware::from_fn(recover))
        .layer(middleware::from_fn(access_log))
        .layer(middleware::from_fn(request_id))
}
