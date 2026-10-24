//! The test harness of the kit auth tests, as `newFixture` and `limitApp` in
//! the Go `kit/auth` tests.

#![allow(dead_code)]

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::body::Body;
use axum::extract::ConnectInfo;
use axum::http::{HeaderMap, Request, StatusCode, header};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};
use http_body_util::BodyExt;
use kit::auth::password::Params;
use kit::auth::{Auth, CurrentUser, Options};
use kit::problem::Problem;
use sqlx::PgPool;
use time::OffsetDateTime;
use tower::ServiceExt;

pub const GOOD: &str = "correct horse battery";

/// Cheap argon2id parameters for tests.
pub const TEST_PARAMS: Params = Params {
    memory: 64,
    time: 1,
    threads: 1,
    salt_len: 16,
    key_len: 32,
};

/// 2026-10-15 12:00:00 UTC.
pub fn t0() -> OffsetDateTime {
    time::macros::datetime!(2026-10-15 12:00 UTC)
}

/// A clock that tests move.
#[derive(Clone)]
pub struct TestClock(Arc<Mutex<OffsetDateTime>>);

impl TestClock {
    pub fn new(t: OffsetDateTime) -> Self {
        Self(Arc::new(Mutex::new(t)))
    }
    pub fn add(&self, d: Duration) {
        *self.0.lock().unwrap() += d;
    }
    pub fn now(&self) -> OffsetDateTime {
        *self.0.lock().unwrap()
    }
}

/// Options for tests: no Secure cookie, test parameters, an environment of
/// `env` only, a test clock, and no cleanup task.
pub fn options(env: &[(&str, &str)], clock: &TestClock) -> Options {
    let env: HashMap<String, String> = env
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
    let c = clock.clone();
    Options {
        secure: false,
        params: TEST_PARAMS,
        lookup: Arc::new(move |k| env.get(k).cloned()),
        now: Arc::new(move || c.now()),
        cleanup: Duration::ZERO,
        ..Options::default()
    }
}

/// A pool that never connects. Requests that reach it fail.
pub fn lazy_pool() -> PgPool {
    sqlx::postgres::PgPoolOptions::new()
        .acquire_timeout(Duration::from_millis(300))
        .connect_lazy("postgres://nobody@127.0.0.1:1/none")
        .unwrap()
}

/// The full app of a fixture: the auth routes, a protected route, a route
/// with its own CSP, and an unsafe route, behind the auth layer and the kit
/// middleware.
pub fn app(auth: &Auth) -> Router {
    let api = Router::new()
        .route("/api/things", post(|| async { StatusCode::CREATED }))
        .route(
            "/api/me",
            get(|CurrentUser(u): CurrentUser| async move { Json(u) }),
        )
        .route(
            "/api/framed",
            get(|| async {
                (
                    [(header::CONTENT_SECURITY_POLICY, "default-src 'self'")],
                    "framed",
                )
                    .into_response()
            }),
        )
        .merge(auth.routes());
    kit::app::router(auth.layer(api), None)
}

pub struct Fixture {
    pub auth: Auth,
    pub app: Router,
    pub pool: PgPool,
    pub clock: TestClock,
}

pub fn fixture(pool: PgPool, env: &[(&str, &str)], change: impl FnOnce(&mut Options)) -> Fixture {
    let clock = TestClock::new(OffsetDateTime::now_utc());
    let mut o = options(env, &clock);
    change(&mut o);
    let auth = Auth::new(pool.clone(), o).unwrap();
    let app = app(&auth);
    Fixture {
        auth,
        app,
        pool,
        clock,
    }
}

/// One request.
#[derive(Default, Clone)]
pub struct Req<'a> {
    pub method: &'a str,
    pub path: &'a str,
    pub body: Option<String>,
    pub cookie: Option<String>,
    pub remote: Option<&'a str>,
    pub headers: Vec<(&'a str, &'a str)>,
}

impl<'a> Req<'a> {
    pub fn new(method: &'a str, path: &'a str) -> Self {
        Self {
            method,
            path,
            ..Self::default()
        }
    }
    pub fn json(mut self, body: impl Into<String>) -> Self {
        self.body = Some(body.into());
        self
    }
    pub fn cookie(mut self, c: impl Into<String>) -> Self {
        self.cookie = Some(c.into());
        self
    }
    pub fn remote(mut self, r: &'a str) -> Self {
        self.remote = Some(r);
        self
    }
    pub fn header(mut self, k: &'a str, v: &'a str) -> Self {
        self.headers.push((k, v));
        self
    }
}

pub struct Answer {
    pub status: StatusCode,
    pub headers: HeaderMap,
    pub body: String,
}

impl Answer {
    pub fn header(&self, name: &str) -> &str {
        self.headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
    }

    pub fn problem(&self) -> Problem {
        assert_eq!(self.header("content-type"), "application/problem+json");
        serde_json::from_str(&self.body).unwrap()
    }

    pub fn json(&self) -> serde_json::Value {
        serde_json::from_str(&self.body).unwrap_or_else(|e| panic!("{e}: {}", self.body))
    }

    /// The Set-Cookie line of `name`.
    pub fn set_cookie(&self, name: &str) -> Option<String> {
        self.headers
            .get_all(header::SET_COOKIE)
            .iter()
            .filter_map(|v| v.to_str().ok())
            .find(|v| v.starts_with(&format!("{name}=")))
            .map(str::to_owned)
    }

    /// The value of the session cookie that the answer sets.
    pub fn session(&self) -> Option<String> {
        let line = self.set_cookie("session")?;
        let value = line.split(';').next()?.split_once('=')?.1.to_owned();
        (!value.is_empty()).then_some(value)
    }
}

pub fn creds(email: &str, password: &str) -> String {
    serde_json::json!({"email": email, "password": password}).to_string()
}

pub async fn send(app: &Router, r: Req<'_>) -> Answer {
    let mut b = Request::builder()
        .method(r.method)
        .uri(format!("http://api.test{}", r.path))
        .header(header::HOST, "api.test");
    if r.body.is_some()
        && !r
            .headers
            .iter()
            .any(|(k, _)| k.eq_ignore_ascii_case("content-type"))
    {
        b = b.header(header::CONTENT_TYPE, "application/json");
    }
    if let Some(c) = &r.cookie {
        b = b.header(header::COOKIE, c.as_str());
    }
    for (k, v) in &r.headers {
        b = b.header(*k, *v);
    }
    let mut req = b.body(r.body.map_or_else(Body::empty, Body::from)).unwrap();
    let remote: SocketAddr = r.remote.unwrap_or("192.0.2.1:1234").parse().unwrap();
    req.extensions_mut().insert(ConnectInfo(remote));
    let res = app.clone().oneshot(req).await.unwrap();
    let status = res.status();
    let headers = res.headers().clone();
    let body = res.into_body().collect().await.unwrap().to_bytes();
    Answer {
        status,
        headers,
        body: String::from_utf8_lossy(&body).into_owned(),
    }
}

/// Registers a user and returns the session cookie value.
pub async fn register(app: &Router, email: &str) -> String {
    let a = send(
        app,
        Req::new("POST", "/api/auth/register").json(creds(email, GOOD)),
    )
    .await;
    assert_eq!(a.status, StatusCode::CREATED, "{}", a.body);
    a.session().expect("a session cookie")
}
