//! The app: built-in routes, middleware, the server with graceful shutdown,
//! logging, and the `healthcheck` command, as the Go `kit/app` package.

use std::collections::BTreeMap;
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use axum::extract::State;
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::get;
use axum::{Json, Router, middleware};
use serde_json::json;
use sqlx::PgPool;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::Notify;

use crate::config::Config;
use crate::middleware::{access_log, request_id, security_headers};
use crate::problem::Problem;

/// An API app: the API routes plus the kit routes and middleware.
pub struct App {
    config: Config,
    router: Router,
    pool: Option<PgPool>,
}

impl App {
    /// Returns an app that serves `api` (a router with its state applied).
    /// With a pool, `/readyz` checks the database.
    pub fn new(config: Config, api: Router, pool: Option<PgPool>) -> Self {
        let router = router(api, pool.clone());
        Self {
            config,
            router,
            pool,
        }
    }

    /// The complete router, for tests.
    pub fn router(&self) -> Router {
        self.router.clone()
    }

    /// Serves HTTP on `PORT` until SIGINT or SIGTERM.
    pub async fn serve(self) -> anyhow::Result<()> {
        let addr = format!("0.0.0.0:{}", self.config.port);
        let listener = TcpListener::bind(&addr).await.map_err(|e| {
            anyhow::anyhow!(
                "cannot listen on {addr}: {e}\nTry: select an available port with PORT and restart"
            )
        })?;
        self.serve_on(listener, shutdown_signal()).await
    }

    /// Serves HTTP on `listener` until `shutdown` completes. Then it stops
    /// new connections and waits for open requests, at most
    /// `SHUTDOWN_TIMEOUT`. At the end it closes the pool.
    pub async fn serve_on(
        self,
        listener: TcpListener,
        shutdown: impl Future<Output = ()> + Send + 'static,
    ) -> anyhow::Result<()> {
        tracing::info!(addr = %listener.local_addr()?, "HTTP server listening");
        let started = Arc::new(Notify::new());
        let notify = started.clone();
        // The peer address (ConnectInfo) is the client of the auth rate limit.
        let service = self
            .router
            .into_make_service_with_connect_info::<std::net::SocketAddr>();
        let server = axum::serve(listener, service).with_graceful_shutdown(async move {
            shutdown.await;
            notify.notify_one();
        });
        let timeout = self.config.shutdown_timeout;
        let result = tokio::select! {
            r = server => r.map_err(|e| anyhow::anyhow!("HTTP server failed: {e}\nTry: read the server logs and restart")),
            _ = async { started.notified().await; tokio::time::sleep(timeout).await } => Err(anyhow::anyhow!(
                "HTTP shutdown did not finish in {timeout:?}\nTry: raise SHUTDOWN_TIMEOUT, or find the slow request in the logs"
            )),
        };
        if let Some(pool) = self.pool {
            pool.close().await;
        }
        tracing::info!("HTTP server stopped");
        result
    }
}

/// Builds the router: `/healthz`, `/readyz`, the API routes, a problem 404
/// for unknown paths, and the middleware (request ID outermost).
pub fn router(api: Router, pool: Option<PgPool>) -> Router {
    Router::new()
        .route("/healthz", get(|| async { Json(json!({"status": "ok"})) }))
        .route("/readyz", get(ready).with_state(pool))
        .merge(api)
        .fallback(|| async { Problem::status(StatusCode::NOT_FOUND) })
        .layer(middleware::from_fn(security_headers))
        .layer(middleware::from_fn(access_log))
        .layer(middleware::from_fn(request_id))
}

async fn ready(State(pool): State<Option<PgPool>>) -> impl IntoResponse {
    let mut checks = BTreeMap::new();
    let mut ok = true;
    if let Some(pool) = pool {
        let check = tokio::time::timeout(
            Duration::from_secs(2),
            sqlx::query_scalar::<_, i32>("SELECT 1").fetch_one(&pool),
        )
        .await;
        let pass = matches!(check, Ok(Ok(1)));
        if !pass {
            tracing::warn!(check = "db", "readiness check failed");
        }
        ok &= pass;
        checks.insert("db", if pass { "ok" } else { "fail" });
    }
    let status = if ok {
        StatusCode::OK
    } else {
        StatusCode::SERVICE_UNAVAILABLE
    };
    (
        status,
        Json(json!({"status": if ok { "ok" } else { "fail" }, "checks": checks})),
    )
}

/// Completes on SIGINT or SIGTERM.
pub async fn shutdown_signal() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    #[cfg(unix)]
    let term = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut s) => {
                s.recv().await;
            }
            Err(_) => std::future::pending::<()>().await,
        }
    };
    #[cfg(not(unix))]
    let term = std::future::pending::<()>();
    tokio::select! {
        _ = ctrl_c => {},
        _ = term => {},
    }
}

/// Starts JSON logging to stderr at `LOG_LEVEL`.
pub fn init_logging(config: &Config) {
    let filter = tracing_subscriber::EnvFilter::new(&config.log_level);
    let _ = tracing_subscriber::fmt()
        .json()
        .with_writer(std::io::stderr)
        .with_env_filter(filter)
        .try_init();
}

/// The `healthcheck` command. Distroless images have no curl, so the Docker
/// health check runs the app binary: it asks the running server on the same
/// container for `/healthz` and fails unless it answers 200 within 2 s.
pub async fn healthcheck(port: u16) -> anyhow::Result<()> {
    let ask = async {
        let mut s = TcpStream::connect(("127.0.0.1", port)).await?;
        s.write_all(b"GET /healthz HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
            .await?;
        let mut buf = Vec::new();
        s.read_to_end(&mut buf).await?;
        Ok::<_, std::io::Error>(buf)
    };
    let buf = match tokio::time::timeout(Duration::from_secs(2), ask).await {
        Ok(Ok(buf)) => buf,
        Ok(Err(e)) => {
            anyhow::bail!("healthcheck: {e}\nTry: check that the server is running on port {port}")
        }
        Err(_) => anyhow::bail!(
            "healthcheck: no answer in 2s\nTry: check that the server is running on port {port}"
        ),
    };
    let head = String::from_utf8_lossy(&buf);
    let status = head.split_whitespace().nth(1).unwrap_or("");
    if status != "200" {
        anyhow::bail!("healthcheck: /healthz answered {status:?}\nTry: read the server logs");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::http::Request;
    use http_body_util::BodyExt;
    use tower::ServiceExt;

    #[tokio::test]
    async fn healthz_and_problem_404_with_request_id() {
        let app = router(Router::new(), None);
        let res = app
            .clone()
            .oneshot(Request::get("/healthz").body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(res.status(), 200);
        assert!(res.headers().contains_key("x-request-id"));

        let res = app
            .oneshot(
                Request::get("/nope")
                    .header("x-request-id", "abc")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(res.status(), 404);
        assert_eq!(res.headers()["content-type"], "application/problem+json");
        let body = res.into_body().collect().await.unwrap().to_bytes();
        let p: Problem = serde_json::from_slice(&body).unwrap();
        assert_eq!(p.request_id.as_deref(), Some("abc"));
        assert_eq!(p.title, "Not Found");
    }

    #[tokio::test]
    async fn graceful_shutdown_and_healthcheck() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let stop = Arc::new(Notify::new());
        let wait = stop.clone();
        let app = App::new(Config::default(), Router::new(), None);
        let server = tokio::spawn(app.serve_on(listener, async move { wait.notified().await }));
        healthcheck(port).await.unwrap();
        stop.notify_one();
        server.await.unwrap().unwrap();
        let err = healthcheck(port).await.unwrap_err().to_string();
        assert!(err.contains("\nTry: "), "{err}");
    }

    #[tokio::test]
    async fn unauthorized_is_a_problem() {
        use crate::problem::ApiError;
        let api = Router::new().route(
            "/api/me",
            get(|| async { Err::<(), _>(ApiError::unauthorized()) }),
        );
        let res = router(api, None)
            .oneshot(Request::get("/api/me").body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(res.status(), 401);
        assert_eq!(res.headers()["content-type"], "application/problem+json");
    }
}
