//! The deploy information endpoint `GET /_deploy/info` (Go `kit/deployx`).
//!
//! `spinup deploy` reads it to check the running version. The endpoint trusts only the
//! direct peer address (`ConnectInfo`). A direct private or loopback peer is internal. A
//! request that a proxy forwarded (`Forwarded`, `X-Forwarded-For`, or `X-Real-IP`), or a
//! public peer, needs the bearer token from the token file.

use std::future::Future;
use std::net::{IpAddr, SocketAddr};
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::Arc;

use axum::Router;
use axum::extract::{ConnectInfo, Request};
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use serde::{Deserialize, Serialize};
use subtle::ConstantTimeEq;
use time::OffsetDateTime;

/// The path of the endpoint.
pub const PATH: &str = "/_deploy/info";

/// The variable that names the token file when [`Options::token_file`] is `None`.
pub const TOKEN_FILE_ENV: &str = "DEPLOYX_TOKEN_FILE";

/// A function that returns the current migration version of the database.
pub type MigrationVersionFn =
    Arc<dyn Fn() -> Pin<Box<dyn Future<Output = anyhow::Result<String>> + Send>> + Send + Sync>;

/// The options of the endpoint (Go `deployx.Options`).
#[derive(Clone, Default)]
pub struct Options {
    /// The version of the app build. The app sets it from its own version.
    pub version: String,
    /// The git SHA of the app build (Go reads `vcs.revision`; the Rust app passes
    /// `option_env!("SPINUP_APP_GIT_SHA")`). The app sets it from its own value when it is
    /// empty.
    pub git_sha: String,
    /// The file that holds the bearer token. `None` reads `DEPLOYX_TOKEN_FILE` when
    /// [`routes`] runs.
    pub token_file: Option<PathBuf>,
    /// Returns the current migration version. `None` reports an empty version.
    pub migration_version: Option<MigrationVersionFn>,
}

impl std::fmt::Debug for Options {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Options")
            .field("version", &self.version)
            .field("git_sha", &self.git_sha)
            .field("token_file", &self.token_file)
            .field("migration_version", &self.migration_version.is_some())
            .finish()
    }
}

impl Options {
    /// Sets the token file.
    #[must_use]
    pub fn token_file(mut self, path: impl Into<PathBuf>) -> Self {
        self.token_file = Some(path.into());
        self
    }

    /// Sets the migration version function.
    #[must_use]
    pub fn migration_version<F, Fut>(mut self, f: F) -> Self
    where
        F: Fn() -> Fut + Send + Sync + 'static,
        Fut: Future<Output = anyhow::Result<String>> + Send + 'static,
    {
        self.migration_version = Some(Arc::new(move || Box::pin(f())));
        self
    }
}

/// The answer of the endpoint (Go `deployx.Info`).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Info {
    /// The version of the app build.
    pub version: String,
    /// The git SHA of the app build.
    pub git_sha: String,
    /// The current database migration version.
    pub migration_version: String,
    /// The UTC time when [`routes`] made the router.
    #[serde(with = "time::serde::rfc3339")]
    pub started_at: OffsetDateTime,
}

struct State {
    options: Options,
    token_file: Option<PathBuf>,
    started_at: OffsetDateTime,
}

/// Returns a router with `GET /_deploy/info` (Go `deployx.New`). The router needs
/// `ConnectInfo<SocketAddr>` (the app serves with it). Without it, every request needs the
/// token.
pub fn routes(options: Options) -> Router {
    let token_file = options
        .token_file
        .clone()
        .or_else(|| std::env::var_os(TOKEN_FILE_ENV).map(PathBuf::from))
        .filter(|p| !p.as_os_str().is_empty());
    let state = Arc::new(State {
        options,
        token_file,
        started_at: OffsetDateTime::now_utc(),
    });
    Router::new().route(PATH, get(move |req: Request| info(state.clone(), req)))
}

fn forwarded(headers: &HeaderMap) -> bool {
    ["forwarded", "x-forwarded-for", "x-real-ip"]
        .iter()
        .any(|h| headers.get(*h).is_some_and(|v| !v.is_empty()))
}

fn internal_peer(ip: IpAddr) -> bool {
    match ip.to_canonical() {
        IpAddr::V4(v4) => v4.is_private() || v4.is_loopback(),
        IpAddr::V6(v6) => v6.is_unique_local() || v6.is_loopback(),
    }
}

fn authorized(headers: &HeaderMap, token_file: Option<&PathBuf>) -> bool {
    let Some(path) = token_file else { return false };
    let Ok(data) = std::fs::read(path) else {
        return false;
    };
    let text = String::from_utf8_lossy(&data);
    let token = text.trim();
    let given = headers
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.strip_prefix("Bearer "));
    match given {
        Some(given) if !token.is_empty() => bool::from(token.as_bytes().ct_eq(given.as_bytes())),
        _ => false,
    }
}

/// A plain text error answer, as Go `http.Error` writes it.
fn text_error(status: StatusCode, text: &str) -> Response {
    let mut res = (status, format!("{text}\n")).into_response();
    let h = res.headers_mut();
    h.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/plain; charset=utf-8"),
    );
    h.insert(
        "x-content-type-options",
        HeaderValue::from_static("nosniff"),
    );
    res
}

async fn info(state: Arc<State>, req: Request) -> Response {
    let peer = req
        .extensions()
        .get::<ConnectInfo<SocketAddr>>()
        .map(|c| c.0.ip());
    let internal = !forwarded(req.headers()) && peer.is_some_and(internal_peer);
    if !internal && !authorized(req.headers(), state.token_file.as_ref()) {
        return text_error(StatusCode::FORBIDDEN, "forbidden");
    }
    let mut info = Info {
        version: state.options.version.clone(),
        git_sha: state.options.git_sha.clone(),
        migration_version: String::new(),
        started_at: state.started_at,
    };
    if let Some(f) = &state.options.migration_version {
        match f().await {
            Ok(v) => info.migration_version = v,
            Err(e) => {
                tracing::warn!(error = %format!("{e:#}"), "deploy info: migration version unavailable");
                return text_error(
                    StatusCode::SERVICE_UNAVAILABLE,
                    "migration version unavailable",
                );
            }
        }
    }
    let mut body = serde_json::to_vec(&info).unwrap_or_default();
    body.push(b'\n');
    let mut res = (StatusCode::OK, body).into_response();
    let h = res.headers_mut();
    h.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    h.insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    res
}
