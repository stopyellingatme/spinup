//! HTTP test helpers (Go `kit/testkit/http.go`): send one request to a router, and read the
//! answer.
//!
//! ```
//! # tokio::runtime::Runtime::new().unwrap().block_on(async {
//! use axum::Router;
//! use axum::routing::get;
//! use spinup_kit::testkit::http::{Req, decode_json, send};
//!
//! let app = Router::new().route("/todos", get(|| async { axum::Json(vec![1, 2]) }));
//! let answer = send(&app, Req::get("/todos")).await;
//! assert_eq!(answer.status, 200);
//! assert_eq!(decode_json::<Vec<i32>>(&answer), vec![1, 2]);
//! # });
//! ```

use std::net::SocketAddr;

use axum::Router;
use axum::body::Body;
use axum::extract::ConnectInfo;
use axum::http::{HeaderMap, Request, StatusCode, header};
use http_body_util::BodyExt;
use serde::de::DeserializeOwned;
use tower::ServiceExt;

use crate::httpx::{PROBLEM_JSON, Problem};

/// The client address of a request without [`Req::remote`].
pub const DEFAULT_REMOTE: &str = "192.0.2.1:1234";

/// One request.
#[derive(Debug, Clone, Default)]
pub struct Req {
    /// The method, such as `GET`.
    pub method: String,
    /// The path and query, such as `/api/todos?limit=5`.
    pub path: String,
    /// The body. A request with a body gets `Content-Type: application/json`, unless
    /// [`Req::headers`] sets a content type.
    pub body: Option<String>,
    /// The `Cookie` header.
    pub cookie: Option<String>,
    /// The client address (`ConnectInfo`). The default is [`DEFAULT_REMOTE`].
    pub remote: Option<String>,
    /// More headers.
    pub headers: Vec<(String, String)>,
}

impl Req {
    /// Returns a request with a method and a path.
    pub fn new(method: impl Into<String>, path: impl Into<String>) -> Self {
        Self {
            method: method.into(),
            path: path.into(),
            ..Self::default()
        }
    }

    /// A `GET` request.
    pub fn get(path: impl Into<String>) -> Self {
        Self::new("GET", path)
    }

    /// A `POST` request.
    pub fn post(path: impl Into<String>) -> Self {
        Self::new("POST", path)
    }

    /// A `PUT` request.
    pub fn put(path: impl Into<String>) -> Self {
        Self::new("PUT", path)
    }

    /// A `PATCH` request.
    pub fn patch(path: impl Into<String>) -> Self {
        Self::new("PATCH", path)
    }

    /// A `DELETE` request.
    pub fn delete(path: impl Into<String>) -> Self {
        Self::new("DELETE", path)
    }

    /// Sets the body text.
    #[must_use]
    pub fn json(mut self, body: impl Into<String>) -> Self {
        self.body = Some(body.into());
        self
    }

    /// Sets the body to the JSON text of `value`.
    ///
    /// # Panics
    ///
    /// When `value` does not serialize.
    #[must_use]
    pub fn json_value(self, value: &impl serde::Serialize) -> Self {
        let text = serde_json::to_string(value)
            .unwrap_or_else(|e| panic!("testkit: encode the JSON body: {e}"));
        self.json(text)
    }

    /// Sets the `Cookie` header.
    #[must_use]
    pub fn cookie(mut self, c: impl Into<String>) -> Self {
        self.cookie = Some(c.into());
        self
    }

    /// Sets the client address, such as `203.0.113.7:4000`.
    #[must_use]
    pub fn remote(mut self, r: impl Into<String>) -> Self {
        self.remote = Some(r.into());
        self
    }

    /// Adds a header.
    #[must_use]
    pub fn header(mut self, name: impl Into<String>, value: impl Into<String>) -> Self {
        self.headers.push((name.into(), value.into()));
        self
    }
}

/// The answer to a request.
#[derive(Debug, Clone)]
pub struct Answer {
    /// The status.
    pub status: StatusCode,
    /// The headers.
    pub headers: HeaderMap,
    /// The body as text (invalid UTF-8 becomes U+FFFD).
    pub body: String,
}

impl Answer {
    /// The value of the header `name`, or `""`.
    pub fn header(&self, name: &str) -> &str {
        self.headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    }

    /// The body as JSON.
    ///
    /// # Panics
    ///
    /// When the body is not JSON.
    pub fn json(&self) -> serde_json::Value {
        decode_json(self)
    }

    /// The body as a problem.
    ///
    /// # Panics
    ///
    /// When the content type is not `application/problem+json`, or the body is not a
    /// problem.
    pub fn problem(&self) -> Problem {
        assert_eq!(
            self.header("content-type"),
            PROBLEM_JSON,
            "testkit: the answer is not a problem (status {}): {}",
            self.status,
            self.body
        );
        decode_json(self)
    }

    /// The `Set-Cookie` line of the cookie `name`.
    pub fn set_cookie(&self, name: &str) -> Option<String> {
        let prefix = format!("{name}=");
        self.headers
            .get_all(header::SET_COOKIE)
            .iter()
            .filter_map(|v| v.to_str().ok())
            .find(|v| v.starts_with(&prefix))
            .map(str::to_owned)
    }

    /// The value that the answer sets for the cookie `name`. An empty value (a removal) is
    /// `None`.
    pub fn cookie_value(&self, name: &str) -> Option<String> {
        let line = self.set_cookie(name)?;
        let value = line.split(';').next()?.split_once('=')?.1.to_owned();
        (!value.is_empty()).then_some(value)
    }
}

/// Sends one request to `app` and returns the answer (Go `testkit.Do`). The request has the
/// host `api.test` and the client address of [`Req::remote`].
///
/// # Panics
///
/// When the request is not valid, such as a bad header name.
pub async fn send(app: &Router, r: Req) -> Answer {
    let mut b = Request::builder()
        .method(r.method.as_str())
        .uri(format!("http://api.test{}", r.path))
        .header(header::HOST, "api.test");
    let has_type = r
        .headers
        .iter()
        .any(|(k, _)| k.eq_ignore_ascii_case("content-type"));
    if r.body.is_some() && !has_type {
        b = b.header(header::CONTENT_TYPE, "application/json");
    }
    if let Some(c) = &r.cookie {
        b = b.header(header::COOKIE, c.as_str());
    }
    for (k, v) in &r.headers {
        b = b.header(k.as_str(), v.as_str());
    }
    let mut req = b
        .body(r.body.map_or_else(Body::empty, Body::from))
        .unwrap_or_else(|e| panic!("testkit: build the request: {e}"));
    let remote: SocketAddr = r
        .remote
        .as_deref()
        .unwrap_or(DEFAULT_REMOTE)
        .parse()
        .unwrap_or_else(|e| panic!("testkit: the client address is not valid: {e}"));
    req.extensions_mut().insert(ConnectInfo(remote));
    let res = match app.clone().oneshot(req).await {
        Ok(res) => res,
        Err(never) => match never {},
    };
    let status = res.status();
    let headers = res.headers().clone();
    let body = res
        .into_body()
        .collect()
        .await
        .unwrap_or_else(|e| panic!("testkit: read the answer body: {e}"))
        .to_bytes();
    Answer {
        status,
        headers,
        body: String::from_utf8_lossy(&body).into_owned(),
    }
}

/// Decodes the body of `answer` into a `T` (Go `testkit.DecodeJSON`).
///
/// # Panics
///
/// When the body is not valid JSON for `T`. The message has the status and the body.
pub fn decode_json<T: DeserializeOwned>(answer: &Answer) -> T {
    serde_json::from_str(&answer.body).unwrap_or_else(|e| {
        panic!(
            "testkit: decode JSON answer (status {}): {e}\nbody: {}",
            answer.status, answer.body
        )
    })
}
