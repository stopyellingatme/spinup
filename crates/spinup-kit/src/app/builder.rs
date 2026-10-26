//! The [`App`] builder and the router that it builds (Go `app.New` and its options).

use std::collections::{BTreeMap, BTreeSet};
use std::convert::Infallible;
use std::future::Future;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use axum::Router;
use axum::extract::{MatchedPath, Request};
use axum::http::StatusCode;
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::{Route, get};
use serde::Serialize;
use tower::{Layer, Service};

use super::{BoxFuture, Command};
use crate::config::Base;
use crate::deployx;
use crate::httpx::{self, Problem, RequestId};

/// A router transform: a layer, or a fallback.
pub(crate) type RouterFn = Arc<dyn Fn(Router) -> Router + Send + Sync>;
type ReadinessFn = Arc<dyn Fn() -> BoxFuture<anyhow::Result<()>> + Send + Sync>;
pub(crate) type HookFn = Box<dyn FnOnce() -> BoxFuture<anyhow::Result<()>> + Send>;

/// The time limit of one readiness check.
pub(crate) const READINESS_TIMEOUT: Duration = Duration::from_secs(2);

/// The route patterns that the app knows, such as `GET /healthz` (Go `devRoutes`). A clone
/// shares the same set.
#[derive(Debug, Clone, Default)]
pub struct RoutePatterns(Arc<Mutex<BTreeSet<String>>>);

impl RoutePatterns {
    /// The patterns, sorted.
    pub fn list(&self) -> Vec<String> {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .iter()
            .cloned()
            .collect()
    }

    /// Adds a pattern, such as `GET /api/docs`.
    pub fn add(&self, pattern: impl Into<String>) {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert(pattern.into());
    }
}

/// An API app: the app routes plus the kit routes, the kit layers, readiness checks,
/// commands, and shutdown hooks (Go `app.App`).
///
/// Build it with [`App::new`] and the builder methods, then call [`App::run`] (or let
/// [`super::main`] call it). The built-in routes are `GET /healthz`, `GET /readyz`, and
/// `GET /version`. An unknown path gets a 404 problem, and a known path with another
/// method gets a 405 problem.
///
/// The layers run in the Go order, from the outside in: request ID, the telemetry slot,
/// access log, recover, security headers, the layers of [`App::layer`], then the routes.
///
/// `App` holds no database pool. Readiness checks and shutdown hooks are closures.
pub struct App {
    pub(crate) config: Base,
    name: String,
    version: String,
    git_sha: String,
    rust: String,
    addr: Option<String>,
    routers: Vec<Router>,
    internal: Vec<Router>,
    layers: Vec<RouterFn>,
    outer_layers: Vec<RouterFn>,
    fallback: Option<RouterFn>,
    readiness: BTreeMap<String, ReadinessFn>,
    pub(crate) commands: BTreeMap<String, Command>,
    pub(crate) shutdown: Vec<HookFn>,
    pub(crate) exit: Vec<HookFn>,
    deploy: Option<deployx::Options>,
    patterns: RoutePatterns,
    record_patterns: bool,
}

impl std::fmt::Debug for App {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("App")
            .field("name", &self.name)
            .field("version", &self.version)
            .field("addr", &self.listen_addr())
            .field("commands", &self.commands.keys().collect::<Vec<_>>())
            .field("readiness", &self.readiness.keys().collect::<Vec<_>>())
            .finish_non_exhaustive()
    }
}

#[derive(Serialize)]
struct VersionBody<'a> {
    name: &'a str,
    version: &'a str,
    rust: &'a str,
}

impl App {
    /// Returns an app with the settings of `config`: the port (0 gives 8080) and the
    /// shutdown timeout (0 gives 20 seconds). The app does not start the log: call
    /// `kit::logx::init` or `kit::telemetry::init` first.
    pub fn new(config: &Base) -> Self {
        let patterns = RoutePatterns::default();
        for p in ["GET /healthz", "GET /readyz", "GET /version"] {
            patterns.add(p);
        }
        Self {
            config: config.clone(),
            name: String::new(),
            version: String::new(),
            git_sha: String::new(),
            rust: String::new(),
            addr: None,
            routers: Vec::new(),
            internal: Vec::new(),
            layers: Vec::new(),
            outer_layers: Vec::new(),
            fallback: None,
            readiness: BTreeMap::new(),
            commands: BTreeMap::new(),
            shutdown: Vec::new(),
            exit: Vec::new(),
            deploy: None,
            patterns,
            record_patterns: false,
        }
    }

    /// Sets the app name of `/version` (Go `WithName`).
    #[must_use]
    pub fn name(mut self, name: impl Into<String>) -> Self {
        self.name = name.into();
        self
    }

    /// Sets the app version of `/version` and `/_deploy/info` (Go `WithVersion`). The app
    /// passes `option_env!("SPINUP_APP_VERSION")`.
    #[must_use]
    pub fn version(mut self, version: impl Into<String>) -> Self {
        self.version = version.into();
        self
    }

    /// Sets the git SHA of `/_deploy/info`. The app passes
    /// `option_env!("SPINUP_APP_GIT_SHA")`.
    #[must_use]
    pub fn git_sha(mut self, sha: impl Into<String>) -> Self {
        self.git_sha = sha.into();
        self
    }

    /// Sets the Rust version of `/version`. The app passes `option_env!("SPINUP_APP_RUST")`.
    #[must_use]
    pub fn rust_version(mut self, rust: impl Into<String>) -> Self {
        self.rust = rust.into();
        self
    }

    /// Sets the listen address, such as `127.0.0.1:8080` (Go `WithAddr`). The default is
    /// `0.0.0.0:<PORT>`.
    #[must_use]
    pub fn addr(mut self, addr: impl Into<String>) -> Self {
        self.addr = Some(addr.into());
        self
    }

    /// Adds app routes (Go `WithRoutes`). The router has no state: apply the state before
    /// the call. Routes merge in call order.
    #[must_use]
    pub fn routes(mut self, router: Router) -> Self {
        self.routers.push(router);
        self
    }

    /// Adds a layer inside the kit layers, around all routes (Go `WithMiddleware`). The
    /// first layer is the outermost of these layers.
    #[must_use]
    pub fn layer<L>(mut self, layer: L) -> Self
    where
        L: Layer<Route> + Clone + Send + Sync + 'static,
        L::Service: Service<Request> + Clone + Send + Sync + 'static,
        <L::Service as Service<Request>>::Response: IntoResponse + 'static,
        <L::Service as Service<Request>>::Error: Into<Infallible> + 'static,
        <L::Service as Service<Request>>::Future: Send + 'static,
    {
        self.layers
            .push(Arc::new(move |router: Router| router.layer(layer.clone())));
        self
    }

    /// Adds a readiness check of `/readyz` (Go `WithReadiness`). A second check with the same
    /// name replaces the first. Checks run one at a time in name order, each with a 2 second
    /// limit. A failed check gives a 503 and a warning line in the log.
    #[must_use]
    pub fn readiness<F, Fut>(mut self, name: impl Into<String>, check: F) -> Self
    where
        F: Fn() -> Fut + Send + Sync + 'static,
        Fut: Future<Output = anyhow::Result<()>> + Send + 'static,
    {
        self.readiness
            .insert(name.into(), Arc::new(move || Box::pin(check())));
        self
    }

    /// Registers a named command (Go `WithCommand`). A second command with the same name
    /// replaces the first. A registered name wins over a built-in command.
    #[must_use]
    pub fn command(mut self, name: impl Into<String>, command: Command) -> Self {
        self.commands.insert(name.into(), command);
        self
    }

    /// Adds a shutdown hook (Go `WithShutdown`). Hooks run in reverse order after the server
    /// stops, and they share the deadline of the shutdown timeout.
    #[must_use]
    pub fn shutdown<F, Fut>(mut self, hook: F) -> Self
    where
        F: FnOnce() -> Fut + Send + 'static,
        Fut: Future<Output = anyhow::Result<()>> + Send + 'static,
    {
        self.shutdown.push(Box::new(move || Box::pin(hook())));
        self
    }

    /// Mounts `GET /_deploy/info` (Go `WithDeployInfo`). The endpoint reports the app
    /// version, and the app git SHA when `options.git_sha` is empty.
    #[must_use]
    pub fn deploy_info(mut self, options: deployx::Options) -> Self {
        self.deploy = Some(options);
        self.patterns.add(format!("GET {}", deployx::PATH));
        self
    }

    /// The listen address: the [`App::addr`] value, or `0.0.0.0:<PORT>`.
    pub fn listen_addr(&self) -> String {
        match &self.addr {
            Some(a) if !a.is_empty() => a.clone(),
            _ => format!("0.0.0.0:{}", self.port()),
        }
    }

    pub(crate) fn port(&self) -> u16 {
        if self.config.port == 0 {
            8080
        } else {
            self.config.port
        }
    }

    pub(crate) fn shutdown_timeout(&self) -> Duration {
        if self.config.shutdown_timeout.is_zero() {
            Duration::from_secs(20)
        } else {
            self.config.shutdown_timeout
        }
    }

    pub(crate) fn version_json(&self) -> String {
        serde_json::to_string(&VersionBody {
            name: &self.name,
            version: &self.version,
            rust: &self.rust,
        })
        .unwrap_or_default()
    }

    /// The complete router with the kit routes and all layers (Go `App.Handler`). Tests
    /// send requests to it. [`App::run`] serves it.
    pub fn router(&self) -> Router {
        let checks: Arc<Vec<(String, ReadinessFn)>> = Arc::new(
            self.readiness
                .iter()
                .map(|(k, v)| (k.clone(), v.clone()))
                .collect(),
        );
        let version = self.version_json();
        let mut router = Router::new()
            .route(
                "/healthz",
                get(|| async { axum::Json(serde_json::json!({"status": "ok"})) }),
            )
            .route(
                "/readyz",
                get(move |id: RequestId| ready(checks.clone(), id)),
            )
            .route(
                "/version",
                get(move || {
                    let body = version.clone();
                    async move {
                        (
                            [(axum::http::header::CONTENT_TYPE, "application/json")],
                            body,
                        )
                    }
                }),
            );
        for r in &self.routers {
            router = router.merge(r.clone());
        }
        for r in &self.internal {
            router = router.merge(r.clone());
        }
        if let Some(options) = &self.deploy {
            let mut options = options.clone();
            options.version.clone_from(&self.version);
            if options.git_sha.is_empty() {
                options.git_sha.clone_from(&self.git_sha);
            }
            router = router.merge(deployx::routes(options));
        }
        router = router
            .fallback(|| async { Problem::status(StatusCode::NOT_FOUND) })
            .method_not_allowed_fallback(|| async {
                Problem::status(StatusCode::METHOD_NOT_ALLOWED)
            });
        if let Some(f) = &self.fallback {
            router = f(router);
        }
        if self.record_patterns {
            router = router.layer(middleware::from_fn_with_state(
                self.patterns.clone(),
                record_pattern,
            ));
        }
        for f in self.layers.iter().rev() {
            router = f(router);
        }
        router = router
            .layer(middleware::from_fn(httpx::security_headers))
            .layer(middleware::from_fn(httpx::recover))
            .layer(middleware::from_fn(httpx::access_log));
        for f in self.outer_layers.iter().rev() {
            router = f(router);
        }
        router.layer(middleware::from_fn(httpx::request_id))
    }

    // Hooks for the feature modules of the kit (see the module documentation).

    /// Merges a stateless router of kit routes under all app layers. Add its patterns with
    /// [`App::add_route_pattern`].
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn mount_internal(&mut self, router: Router) {
        self.internal.push(router);
    }

    /// Adds a route pattern, such as `GET /api/docs`, for the dev tools.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn add_route_pattern(&mut self, pattern: impl Into<String>) {
        self.patterns.add(pattern);
    }

    /// The known route patterns. The set grows while the app serves when
    /// [`App::record_route_patterns`] is on.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn route_patterns(&self) -> RoutePatterns {
        self.patterns.clone()
    }

    /// Adds the pattern of each matched request to the route patterns (Go `recordRoute`).
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn record_route_patterns(&mut self) {
        self.record_patterns = true;
    }

    /// Adds a router transform to the layer slot between the request ID layer and the access
    /// log layer. The first transform is the outermost.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn push_outer_layer(
        &mut self,
        f: impl Fn(Router) -> Router + Send + Sync + 'static,
    ) {
        self.outer_layers.push(Arc::new(f));
    }

    /// Replaces the problem 404 fallback. `f` gets the router with all routes and the
    /// default fallbacks, before the layers.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn set_fallback(&mut self, f: impl Fn(Router) -> Router + Send + Sync + 'static) {
        self.fallback = Some(Arc::new(f));
    }

    /// Adds a hook that runs after any command or serve, after the shutdown hooks. Exit
    /// hooks run in reverse order.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn on_exit<F, Fut>(&mut self, hook: F)
    where
        F: FnOnce() -> Fut + Send + 'static,
        Fut: Future<Output = anyhow::Result<()>> + Send + 'static,
    {
        self.exit.push(Box::new(move || Box::pin(hook())));
    }

    /// The settings that the app was built with.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn config(&self) -> &Base {
        &self.config
    }

    /// The app name.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn app_name(&self) -> &str {
        &self.name
    }

    /// The app version.
    #[allow(dead_code, reason = "a hook for the feature modules")]
    pub(crate) fn app_version(&self) -> &str {
        &self.version
    }
}

#[derive(Serialize)]
struct ReadyBody<'a> {
    status: &'a str,
    checks: BTreeMap<&'a str, &'a str>,
}

async fn ready(checks: Arc<Vec<(String, ReadinessFn)>>, id: RequestId) -> Response {
    let mut results = BTreeMap::new();
    let mut ok = true;
    for (name, check) in checks.iter() {
        let error = match tokio::time::timeout(READINESS_TIMEOUT, check()).await {
            Ok(Ok(())) => None,
            Ok(Err(e)) => Some(format!("{e:#}")),
            Err(_) => Some("the check did not finish in 2s".to_string()),
        };
        let pass = error.is_none();
        if let Some(error) = error {
            tracing::warn!(check = %name, request_id = %id.0, error = %error, "readiness check failed");
        }
        ok &= pass;
        results.insert(name.as_str(), if pass { "ok" } else { "fail" });
    }
    let status = if ok {
        StatusCode::OK
    } else {
        StatusCode::SERVICE_UNAVAILABLE
    };
    let body = ReadyBody {
        status: if ok { "ok" } else { "fail" },
        checks: results,
    };
    (status, axum::Json(body)).into_response()
}

async fn record_pattern(
    axum::extract::State(patterns): axum::extract::State<RoutePatterns>,
    req: Request,
    next: Next,
) -> Response {
    let pattern = req
        .extensions()
        .get::<MatchedPath>()
        .map(|m| format!("{} {}", req.method(), m.as_str()));
    let res = next.run(req).await;
    if let Some(p) = pattern {
        patterns.add(p);
    }
    res
}
