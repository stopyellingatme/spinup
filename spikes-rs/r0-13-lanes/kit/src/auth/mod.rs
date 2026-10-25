//! Users, passwords, and sessions for a spinup API, as the Go `kit/auth`.
//!
//! - Sessions use tower-sessions with [`store::PgSessionStore`] (table
//!   `sessions`). The store keeps a SHA-256 hash of each session ID. The
//!   cookie is named `session` and is HttpOnly, SameSite=Lax, Path=/, and
//!   Secure unless `Options::secure` is false (ADR 0008 §3). Login and
//!   registration rotate the ID. Logout deletes the session and the cookie.
//! - Passwords are argon2id PHC strings with the Go parameters. The hashes
//!   run on the blocking pool behind two semaphores (the hash queue). A full
//!   queue answers 503 with Retry-After.
//! - After 5 failed logins for one email within 15 minutes, a login for that
//!   email gets 429 with Retry-After.
//! - The middleware applies the per-client rate limit (ADR 0016), rejects
//!   unsafe cross-origin requests with 403, and sets a default CSP.
//!
//! Wire it into an app:
//!
//! ```ignore
//! let auth = Auth::new(pool.clone(), Options::default())?;
//! let api = api.merge(auth.routes());
//! let api = auth.layer(api);
//! ```
//!
//! Protect a handler with the [`CurrentUser`] extractor.

pub mod clientip;
pub mod csrf;
#[cfg(any(test, feature = "test-util"))]
pub mod fake_issuer;
pub mod goduration;
mod handlers;
pub mod oidc;
pub mod password;
mod queries;
pub mod ratelimit;
pub mod reset;
pub mod store;

use std::net::SocketAddr;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Router;
use axum::extract::{ConnectInfo, FromRequestParts, Request, State};
use axum::http::request::Parts;
use axum::http::{HeaderValue, StatusCode, header};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use serde::Serialize;
use sqlx::PgPool;
use time::OffsetDateTime;
use tokio::sync::watch;
use tokio::task::JoinHandle;
use tower_sessions::cookie::SameSite;
use tower_sessions::{Expiry, Session, SessionManagerLayer};

use crate::middleware::RequestId;
use crate::problem::Problem;
use clientip::{ClientKind, ProxySet};
use csrf::CrossOrigin;
use password::{HashSlots, Params};
use ratelimit::{Class, Limiter, RateLimits, RateSettings, Take, Taken};
use store::{PgSessionStore, SESSION_USER_KEY};

pub use handlers::normalize_email;
pub use ratelimit::Limit;

/// The name of the session cookie (ADR 0008 §3).
pub const COOKIE_NAME: &str = "session";
/// The absolute lifetime of a session.
pub const DEFAULT_LIFETIME: Duration = Duration::from_secs(7 * 24 * 3600);
/// The number of failed logins that locks an email.
pub const DEFAULT_LOCKOUT_THRESHOLD: i64 = 5;
/// The period in which failed logins count.
pub const DEFAULT_LOCKOUT_WINDOW: Duration = Duration::from_secs(15 * 60);
/// The Content-Security-Policy for API responses that set none.
pub const DEFAULT_CSP: &str = "default-src 'none'; frame-ancestors 'none'";
/// The number of password checks that can wait for each hash slot.
pub const DEFAULT_HASH_QUEUE: usize = 4;
/// The longest time a password check waits for a hash slot.
pub const DEFAULT_HASH_WAIT: Duration = Duration::from_secs(2);

const CLEANUP_INTERVAL: Duration = Duration::from_secs(300);
const LOG_SWEEP_INTERVAL: Duration = Duration::from_secs(60);
const WARN_INTERVAL_NS: i64 = 600_000_000_000;

/// An error of the auth setup. The text says what failed and ends with a
/// `Try:` line.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthError(pub String);

impl std::fmt::Display for AuthError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for AuthError {}

/// The account that owns a session.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, serde::Deserialize)]
pub struct User {
    /// The primary key of the users row.
    pub id: i64,
    /// The normalized (trimmed, lower-case) email address.
    pub email: String,
}

/// A clock. Tests replace it.
pub type Clock = Arc<dyn Fn() -> OffsetDateTime + Send + Sync>;
/// An environment lookup. Tests replace it.
pub type Lookup = Arc<dyn Fn(&str) -> Option<String> + Send + Sync>;

/// The settings of [`Auth::new`]. `Default` gives the Go defaults.
#[derive(Clone)]
pub struct Options {
    /// The Secure attribute of the session cookie. Turn it off only for
    /// local development over plain HTTP (`SPINUP_ENV=dev`).
    pub secure: bool,
    /// The absolute lifetime of a session.
    pub lifetime: Duration,
    /// Failed logins for one email within `window` that lock the email.
    pub threshold: i64,
    /// The lockout window.
    pub window: Duration,
    /// The argon2id parameters.
    pub params: Params,
    /// Hashes that run at the same time.
    pub hash_concurrent: usize,
    /// Requests that can wait for a hash slot.
    pub hash_waiting: usize,
    /// The longest wait for a hash slot.
    pub hash_wait: Duration,
    /// Origins that may send unsafe requests (the public web origin).
    pub origins: Vec<String>,
    /// The per-client rate limits. Set fields win over the environment.
    pub rates: RateLimits,
    /// The trusted proxies. `None` reads AUTH_TRUSTED_PROXIES (default
    /// `private`).
    pub proxies: Option<Vec<String>>,
    /// The environment.
    pub lookup: Lookup,
    /// The clock.
    pub now: Clock,
    /// The interval of the rate limit sweep and its log line.
    pub sweep: Duration,
    /// The interval of the deletion of expired rows. Zero turns it off.
    pub cleanup: Duration,
}

/// The default number of hash slots. The Rust argon2 crate computes the 4
/// lanes of one hash on one thread (the `parallel` feature needs rayon), so
/// one slot uses one CPU, not 4 as in Go. 40% of the CPUs keeps the other
/// requests fast, as the Go load test found.
pub fn default_hash_slots() -> usize {
    let procs = std::thread::available_parallelism().map_or(1, |n| n.get());
    (procs * 4 / 10).max(1)
}

impl Default for Options {
    fn default() -> Self {
        let slots = default_hash_slots();
        Self {
            secure: true,
            lifetime: DEFAULT_LIFETIME,
            threshold: DEFAULT_LOCKOUT_THRESHOLD,
            window: DEFAULT_LOCKOUT_WINDOW,
            params: password::DEFAULT_PARAMS,
            hash_concurrent: slots,
            hash_waiting: DEFAULT_HASH_QUEUE * slots,
            hash_wait: DEFAULT_HASH_WAIT,
            origins: Vec::new(),
            rates: RateLimits::default(),
            proxies: None,
            lookup: Arc::new(|k| std::env::var(k).ok()),
            now: Arc::new(OffsetDateTime::now_utc),
            sweep: LOG_SWEEP_INTERVAL,
            cleanup: CLEANUP_INTERVAL,
        }
    }
}

/// Registers and authenticates users and owns their sessions. Clones share
/// one state.
#[derive(Clone)]
pub struct Auth(Arc<Inner>);

struct Inner {
    pool: PgPool,
    params: Params,
    dummy: String,
    slots: HashSlots,
    threshold: i64,
    window: Duration,
    now: Clock,
    limits: Option<Limiter>,
    proxies: ProxySet,
    csrf: CrossOrigin,
    secure: bool,
    lifetime: Duration,
    lookup: Lookup,
    warned_at: AtomicI64,
    stop: watch::Sender<bool>,
    tasks: Mutex<Vec<JoinHandle<()>>>,
}

impl Auth {
    /// Returns an Auth on `pool`. The pool needs the tables of the auth
    /// migrations. It starts a task that deletes expired rows and sweeps the
    /// rate limit table. Call it inside a tokio runtime.
    pub fn new(pool: PgPool, o: Options) -> Result<Self, AuthError> {
        let settings = RateSettings::load(&*o.lookup)?;
        if o.lifetime.is_zero() {
            return Err(AuthError(
                "auth: cannot start: the session lifetime is zero\nTry: pass a positive lifetime"
                    .into(),
            ));
        }
        if o.hash_concurrent < 1 || o.hash_wait.is_zero() {
            return Err(AuthError(format!(
                "auth: cannot start: the hash limits need at least 1 slot and a positive wait, got {} and {:?}\nTry: correct hash_concurrent and hash_wait",
                o.hash_concurrent, o.hash_wait
            )));
        }
        if o.threshold < 1 || o.window.is_zero() {
            return Err(AuthError(format!(
                "auth: cannot start: the lockout needs a threshold of at least 1 and a positive window, got {} and {:?}\nTry: correct threshold and window",
                o.threshold, o.window
            )));
        }
        let (rates, clients) = ratelimit::resolve_rates(&o.rates, &settings)?;
        let trusted: Vec<String> = match &o.proxies {
            Some(p) => p.clone(),
            None if settings.proxies.is_empty() => vec!["private".into()],
            None => settings.proxies.split(',').map(str::to_owned).collect(),
        };
        let proxies = ProxySet::parse(&trusted)?;
        let mut csrf = CrossOrigin::default();
        for origin in o.origins.iter().filter(|s| !s.is_empty()) {
            csrf.add_trusted_origin(origin)?;
        }
        let procs = std::thread::available_parallelism().map_or(1, |n| n.get());
        let limits = (!rates.iter().all(|l| l.off)).then(|| Limiter::new(rates, clients, procs));
        // A random password that nobody knows. Logins for unknown emails verify it.
        let mut secret = [0u8; 32];
        aws_lc_rs::rand::fill(&mut secret).expect("the system random source works");
        let dummy = password::hash_password(&o.params, &hex::encode(secret));
        let (stop, _) = watch::channel(false);
        let auth = Self(Arc::new(Inner {
            pool,
            params: o.params,
            dummy,
            slots: HashSlots::new(o.hash_concurrent, o.hash_waiting, o.hash_wait),
            threshold: o.threshold,
            window: o.window,
            now: o.now.clone(),
            limits,
            proxies,
            csrf,
            secure: o.secure,
            lifetime: o.lifetime,
            lookup: o.lookup.clone(),
            warned_at: AtomicI64::new(0),
            stop,
            tasks: Mutex::new(Vec::new()),
        }));
        let task = tokio::spawn(auth.clone().cleanup_loop(o.cleanup, o.sweep));
        auth.track(task);
        Ok(auth)
    }

    fn track(&self, task: JoinHandle<()>) {
        self.0.tasks.lock().expect("tasks lock").push(task);
    }

    /// Stops the background tasks. A second call does nothing.
    pub async fn close(&self) {
        // send_replace stores the value also when no task listens yet.
        self.0.stop.send_replace(true);
        let tasks: Vec<_> = std::mem::take(&mut *self.0.tasks.lock().expect("tasks lock"));
        for t in tasks {
            let _ = t.await;
        }
    }

    /// The current time of the auth clock.
    pub fn now(&self) -> OffsetDateTime {
        (self.0.now)()
    }

    /// The pool.
    pub fn pool(&self) -> &PgPool {
        &self.0.pool
    }

    /// The hash queue.
    pub fn slots(&self) -> &HashSlots {
        &self.0.slots
    }

    /// The rate limiter. `None` when every class is off.
    pub fn limiter(&self) -> Option<&Limiter> {
        self.0.limits.as_ref()
    }

    /// The Secure attribute of the cookies.
    pub fn secure(&self) -> bool {
        self.0.secure
    }

    pub(crate) fn lookup(&self, key: &str) -> Option<String> {
        (self.0.lookup)(key)
    }

    fn stopped(&self) -> watch::Receiver<bool> {
        self.0.stop.subscribe()
    }

    async fn cleanup_loop(self, interval: Duration, sweep: Duration) {
        let mut stop = self.stopped();
        let mut db_tick = (!interval.is_zero()).then(|| tokio::time::interval(interval));
        let mut sweep_tick =
            (self.0.limits.is_some() && !sweep.is_zero()).then(|| tokio::time::interval(sweep));
        // The first tick of an interval fires at once. Skip it.
        if let Some(t) = db_tick.as_mut() {
            t.tick().await;
        }
        if let Some(t) = sweep_tick.as_mut() {
            t.tick().await;
        }
        loop {
            // A stop that came before the task started is already in the value.
            if *stop.borrow_and_update() {
                return;
            }
            tokio::select! {
                _ = stop.changed() => return,
                _ = async { db_tick.as_mut().expect("set").tick().await }, if db_tick.is_some() => {
                    if let Err(e) = self.delete_expired().await {
                        tracing::warn!(error = %e, "auth: cannot delete expired sessions and login attempts");
                    }
                }
                _ = async { sweep_tick.as_mut().expect("set").tick().await }, if sweep_tick.is_some() => {
                    let now = self.now_ns();
                    if let Some(l) = self.limiter() {
                        l.sweep(now);
                    }
                    self.log_rejections();
                }
            }
        }
    }

    async fn delete_expired(&self) -> Result<(), sqlx::Error> {
        store::delete_expired(self.pool(), self.now()).await?;
        queries::delete_old_attempts(self.pool(), self.now() - self.0.window).await
    }

    fn now_ns(&self) -> i64 {
        i64::try_from(self.now().unix_timestamp_nanos()).unwrap_or(i64::MAX)
    }

    fn log_rejections(&self) {
        let Some(l) = self.limiter() else { return };
        let counts = l.take_rejected();
        if counts.iter().all(|&n| n == 0) {
            return;
        }
        let parts: Vec<String> = ratelimit_classes()
            .iter()
            .zip(counts)
            .filter(|(_, n)| *n > 0)
            .map(|(c, n)| format!("{}={n}", c.name()))
            .collect();
        tracing::warn!(
            refused = %parts.join(" "),
            try_ = "raise AUTH_RATE_LIMIT_<CLASS> if real users hit the limit, or set AUTH_TRUSTED_PROXIES if all clients show one address",
            "auth: rate limited requests in the last minute"
        );
    }

    fn warn_infrastructure(&self) {
        let now = self.now_ns();
        let last = self.0.warned_at.load(Ordering::Relaxed);
        if last != 0 && now - last < WARN_INTERVAL_NS {
            return;
        }
        if self
            .0
            .warned_at
            .compare_exchange(last, now, Ordering::AcqRel, Ordering::Relaxed)
            .is_err()
        {
            return;
        }
        tracing::warn!(
            try_ = "make the web service send X-Forwarded-For (ADDRESS_HEADER=X-Forwarded-For), or set AUTH_TRUSTED_PROXIES",
            "auth: requests from a trusted proxy have no client address, so they share one rate limit"
        );
    }

    /// The session layer with the cookie contract of ADR 0008 §3.
    fn session_layer(&self) -> SessionManagerLayer<PgSessionStore> {
        SessionManagerLayer::new(PgSessionStore::new(self.0.pool.clone()))
            .with_name(COOKIE_NAME)
            .with_http_only(true)
            .with_same_site(SameSite::Lax)
            .with_secure(self.0.secure)
            .with_path("/")
    }

    /// Wraps `router` in the auth middleware. In order, from the outside:
    /// the default CSP, the rate limit (before any session or database
    /// work), the cross-origin check, the session, and the user load.
    pub fn layer(&self, router: Router) -> Router {
        router
            .layer(middleware::from_fn_with_state(self.clone(), load_user))
            .layer(self.session_layer())
            .layer(middleware::from_fn_with_state(self.clone(), front))
    }

    /// The auth endpoints of ADR 0008 §3:
    ///
    /// ```text
    /// POST /api/auth/register  {"email","password"} -> 201 {"user"}, 409, 422, 429, 503
    /// POST /api/auth/login     {"email","password"} -> 200 {"user"}, 401, 422, 429, 503
    /// POST /api/auth/logout                         -> 204
    /// GET  /api/session                             -> 200 {"user"}, 401
    /// ```
    pub fn routes(&self) -> Router {
        handlers::routes(self.clone())
    }

    /// Signs `user` in on a new session ID. The old ID stops working.
    pub async fn start_session(
        &self,
        session: &Session,
        user: &User,
    ) -> Result<(), tower_sessions::session::Error> {
        session.cycle_id().await?;
        session.clear().await;
        session.insert(SESSION_USER_KEY, user.id).await?;
        let lifetime = time::Duration::try_from(self.0.lifetime).unwrap_or(time::Duration::WEEK);
        session.set_expiry(Some(Expiry::AtDateTime(
            OffsetDateTime::now_utc() + lifetime,
        )));
        Ok(())
    }
}

fn ratelimit_classes() -> [Class; ratelimit::NUM_CLASSES] {
    [
        Class::Login,
        Class::Register,
        Class::ResetRequest,
        Class::ResetConfirm,
        Class::OidcCallback,
    ]
}

/// The marker that the auth middleware ran.
#[derive(Debug, Clone, Copy)]
struct Installed;

/// The marker of a 503 from the hash queue. The middleware gives the rate
/// limit tokens of the request back.
#[derive(Debug, Clone, Copy)]
pub(crate) struct HashBusy;

/// Sets `Cache-Control: no-store`.
pub(crate) fn no_store(res: &mut Response) {
    res.headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
}

/// A problem response with `Cache-Control: no-store`.
pub(crate) fn problem(status: StatusCode, detail: &str) -> Response {
    let mut res = Problem::new(status, detail).into_response();
    no_store(&mut res);
    res
}

/// A 500 problem. The error is logged with the request ID.
pub(crate) fn internal_error(action: &str, err: &dyn std::fmt::Display) -> Response {
    tracing::error!(error = %err, "auth: cannot {action}");
    let mut res = Problem::status(StatusCode::INTERNAL_SERVER_ERROR).into_response();
    no_store(&mut res);
    res
}

fn too_many(c: Class, wait: Duration) -> Response {
    let (seconds, detail) = ratelimit::too_many_detail(c, wait);
    let mut res = if c == Class::OidcCallback {
        // The callback is a browser navigation. A problem page would end the
        // sign-in flow in raw JSON, so go to the login page with an error code.
        let mut r = StatusCode::SEE_OTHER.into_response();
        r.headers_mut().insert(
            header::LOCATION,
            HeaderValue::from_static("/login?error=rate_limited"),
        );
        r
    } else {
        Problem::new(StatusCode::TOO_MANY_REQUESTS, detail).into_response()
    };
    no_store(&mut res);
    res.headers_mut()
        .insert(header::RETRY_AFTER, HeaderValue::from(seconds));
    res
}

/// The outer auth middleware: CSP, the rate limit, and the cross-origin check.
async fn front(State(auth): State<Auth>, mut req: Request, next: Next) -> Response {
    req.extensions_mut().insert(Installed);
    let mut taken: Option<Taken> = None;
    if let (Some(lim), Some(class)) = (
        auth.limiter(),
        ratelimit::route_class(req.method(), req.uri().path()),
    ) {
        let remote = req
            .extensions()
            .get::<ConnectInfo<SocketAddr>>()
            .map(|c| c.0.to_string())
            .unwrap_or_default();
        let xff: Vec<&str> = req
            .headers()
            .get_all("x-forwarded-for")
            .iter()
            .filter_map(|v| v.to_str().ok())
            .collect();
        let who = auth.0.proxies.resolve(&remote, &xff);
        if who.kind != ClientKind::Exempt {
            if who.kind == ClientKind::Infra {
                auth.warn_infrastructure();
            }
            match lim.take(class, &who, auth.now_ns()) {
                Take::Refused(wait) => {
                    tracing::debug!(class = class.name(), "auth: rate limited");
                    return with_csp(too_many(class, wait));
                }
                Take::Allowed(tk) => taken = tk,
            }
        }
    }
    let host = req
        .headers()
        .get(header::HOST)
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned)
        .or_else(|| req.uri().authority().map(|a| a.to_string()))
        .unwrap_or_default();
    if !auth.0.csrf.check(req.method(), req.headers(), &host) {
        return with_csp(problem(
            StatusCode::FORBIDDEN,
            "Cross-origin requests that change state are not allowed.",
        ));
    }
    let res = next.run(req).await;
    if res.extensions().get::<HashBusy>().is_some()
        && let (Some(lim), Some(tk)) = (auth.limiter(), taken.as_ref())
    {
        lim.refund(tk, auth.now_ns());
    }
    with_csp(res)
}

fn with_csp(mut res: Response) -> Response {
    if !res.headers().contains_key(header::CONTENT_SECURITY_POLICY) {
        res.headers_mut().insert(
            header::CONTENT_SECURITY_POLICY,
            HeaderValue::from_static(DEFAULT_CSP),
        );
    }
    res
}

/// Puts the user of the session into the request extensions. A session of a
/// deleted user is ended.
async fn load_user(
    State(auth): State<Auth>,
    session: Session,
    mut req: Request,
    next: Next,
) -> Response {
    match session.get::<i64>(SESSION_USER_KEY).await {
        Err(e) => return internal_error("use the session store", &e),
        Ok(Some(id)) => match queries::user_by_id(auth.pool(), id).await {
            Err(e) => return internal_error("load the session user", &e),
            Ok(Some(user)) => {
                req.extensions_mut().insert(user);
            }
            Ok(None) => {
                if let Err(e) = session.flush().await {
                    return internal_error("end the session of a deleted user", &e);
                }
            }
        },
        Ok(None) => {}
    }
    next.run(req).await
}

/// The signed-in user. A handler that takes it answers 401 when no user is
/// signed in, and 500 when the auth middleware is missing.
#[derive(Debug, Clone)]
pub struct CurrentUser(pub User);

impl<S: Send + Sync> FromRequestParts<S> for CurrentUser {
    type Rejection = Response;

    async fn from_request_parts(parts: &mut Parts, _: &S) -> Result<Self, Self::Rejection> {
        if parts.extensions.get::<Installed>().is_none() {
            let id = parts
                .extensions
                .get::<RequestId>()
                .map(|r| r.0.clone())
                .unwrap_or_default();
            tracing::error!(request_id = %id, "auth: the auth middleware is not installed; wrap the router with auth.layer()");
            return Err(Problem::status(StatusCode::INTERNAL_SERVER_ERROR).into_response());
        }
        parts
            .extensions
            .get::<User>()
            .cloned()
            .map(CurrentUser)
            .ok_or_else(unauthorized)
    }
}

/// The 401 answer.
pub(crate) fn unauthorized() -> Response {
    problem(StatusCode::UNAUTHORIZED, "No user is signed in.")
}
