//! Request ID, request logging, and the security headers, as the Go
//! `httpx` middleware.

use std::time::Instant;

use axum::body::Body;
use axum::extract::{MatchedPath, Request};
use axum::http::{HeaderName, HeaderValue, header};
use axum::middleware::Next;
use axum::response::Response;

use crate::problem::Problem;

/// The request ID header.
pub static X_REQUEST_ID: HeaderName = HeaderName::from_static("x-request-id");

/// The request ID of a request, in the request extensions.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RequestId(pub String);

fn valid_request_id(id: &str) -> bool {
    (1..=128).contains(&id.len())
        && id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'.' || c == b'_' || c == b'-')
}

/// Accepts a safe `X-Request-ID` or makes a random one. It sets the
/// response header, and it adds the ID to a problem response body, as the Go
/// `WriteProblem` does.
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
    if let Some(p) = res.extensions_mut().remove::<Problem>() {
        let p = Problem {
            request_id: Some(id.clone()),
            ..p
        };
        let mut body = serde_json::to_vec(&p).unwrap_or_default();
        body.push(b'\n');
        res.headers_mut().remove(header::CONTENT_LENGTH);
        *res.body_mut() = Body::from(body);
    }
    if let Ok(v) = HeaderValue::from_str(&id) {
        res.headers_mut().insert(X_REQUEST_ID.clone(), v);
    }
    res
}

/// Logs one line for each request: method, path, route, status,
/// duration_ms, and request_id.
pub async fn access_log(req: Request, next: Next) -> Response {
    let start = Instant::now();
    let method = req.method().clone();
    let path = req.uri().path().to_owned();
    let route = req
        .extensions()
        .get::<MatchedPath>()
        .map(|m| m.as_str().to_owned())
        .unwrap_or_default();
    let id = req
        .extensions()
        .get::<RequestId>()
        .map(|r| r.0.clone())
        .unwrap_or_default();
    let res = next.run(req).await;
    tracing::info!(
        method = %method,
        path = %path,
        route = %route,
        status = res.status().as_u16(),
        duration_ms = start.elapsed().as_secs_f64() * 1000.0,
        request_id = %id,
        "request"
    );
    res
}

/// Sets the security headers of the Go `SecurityHeaders` middleware.
pub async fn security_headers(req: Request, next: Next) -> Response {
    let mut res = next.run(req).await;
    let h = res.headers_mut();
    h.insert(
        "x-content-type-options",
        HeaderValue::from_static("nosniff"),
    );
    h.insert("x-frame-options", HeaderValue::from_static("DENY"));
    h.insert(
        "referrer-policy",
        HeaderValue::from_static("strict-origin-when-cross-origin"),
    );
    res
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn request_id_rules() {
        assert!(valid_request_id("abc-1.2_3"));
        assert!(!valid_request_id(""));
        assert!(!valid_request_id("a b"));
        assert!(!valid_request_id(&"a".repeat(129)));
    }
}
