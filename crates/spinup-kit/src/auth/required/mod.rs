//! The auth middleware, `Auth`, `Options`, `AuthUser`, and `User` (Go `kit/auth/auth.go`
//! and `required.go`).
//!
//! - Sessions live in the table `sessions` (see [`crate::auth::store`]). The store keeps
//!   a SHA-256 hash of each token. The cookie is named `session` and is HttpOnly,
//!   SameSite=Lax, Path=/, and Secure unless [`Options::secure`] is false (ADR 0008 §3).
//!   Login and registration rotate the token. Logout deletes the session and expires the
//!   cookie.
//! - Passwords are argon2id PHC strings with the Go parameters. The hashes run on the
//!   blocking pool behind the hash queue. A full queue answers 503 with Retry-After.
//! - After 5 failed logins for one email within 15 minutes, a login for that email gets
//!   429 with Retry-After. A successful login clears the count.
//! - [`Auth::layer`] applies, from the outside: the default CSP, the per-client rate
//!   limit (ADR 0016, before any session or database work), the cross-origin check, the
//!   session, and the user load.
//!
//! Wire it into an app:
//!
//! ```no_run
//! # async fn demo(pool: spinup_kit::db::PgPool) -> anyhow::Result<()> {
//! use axum::Router;
//! use axum::routing::get;
//! use spinup_kit::auth::{Auth, AuthUser, Options};
//!
//! let auth = Auth::new(pool, Options::default())?;
//! let api = Router::new()
//!     .route("/api/me", get(|AuthUser(user): AuthUser| async move { axum::Json(user) }))
//!     .merge(auth.routes());
//! let api = auth.layer(api);
//! # let _ = api;
//! auth.close().await;
//! # Ok(())
//! # }
//! ```

mod csrf;

use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Router;
use axum::extract::{FromRequestParts, OptionalFromRequestParts, Request, State};
use axum::http::request::Parts;
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use serde::{Deserialize, Serialize};
use sqlx::PgPool;
use time::OffsetDateTime;
use tokio::sync::{OnceCell, watch};
use tokio::task::JoinHandle;

use crate::auth::error::AuthError;
use crate::auth::password::{self, HashSlots, Params};
use crate::auth::ratelimit::{self, Limiter, RateLimits};
use crate::auth::store::{self, SESSION_USER_KEY, SessionChange, queries};
use crate::config::goduration;
use crate::httpx::{ApiError, Problem, RequestId};
use csrf::CrossOrigin;

pub use crate::auth::store::Session;

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
/// The number of password checks that can wait for each hash slot. A login that finds the
/// queue full gets 503 with Retry-After.
pub const DEFAULT_HASH_QUEUE: usize = 4;
/// The longest time a password check waits for a hash slot.
pub const DEFAULT_HASH_WAIT: Duration = Duration::from_secs(2);
/// The time between two deletions of expired sessions and login attempts.
pub const DEFAULT_CLEANUP: Duration = Duration::from_secs(5 * 60);
/// The time between two sweeps of the rate limit table and the log of its refusals.
pub const DEFAULT_SWEEP: Duration = Duration::from_secs(60);

/// The longest run of one deletion of expired rows.
const CLEANUP_LIMIT: Duration = Duration::from_secs(60);

/// The account that owns a session.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize, sqlx::FromRow)]
pub struct User {
    /// The primary key of the users row.
    pub id: i64,
    /// The normalized (trimmed, lower-case) email address.
    pub email: String,
}

/// A clock. Tests replace it.
pub type Clock = Arc<dyn Fn() -> OffsetDateTime + Send + Sync>;
/// An environment lookup. Tests replace it, so they never change the process
/// environment.
pub type Lookup = Arc<dyn Fn(&str) -> Option<String> + Send + Sync>;

/// The settings of [`Auth::new`]. `Default` gives the Go defaults.
#[derive(Clone)]
pub struct Options {
    /// The Secure attribute of the session cookie. Turn it off only for local development
    /// over plain HTTP (`SPINUP_ENV=dev`).
    pub secure: bool,
    /// The absolute lifetime of a session. A login starts a new lifetime.
    pub lifetime: Duration,
    /// Ends a session after this time without a request that carries its cookie. Zero
    /// (the default) turns it off. With an idle timeout, every request with a session
    /// writes the session row again and sends a new cookie expiry.
    pub idle_timeout: Duration,
    /// Failed logins for one email within `window` that lock the email.
    pub threshold: i64,
    /// The lockout window.
    pub window: Duration,
    /// The argon2id parameters of new hashes.
    pub params: Params,
    /// Hashes that run at the same time. The default is
    /// [`password::default_hash_slots`].
    pub hash_concurrent: usize,
    /// Requests that can wait for a hash slot. The default is [`DEFAULT_HASH_QUEUE`] for
    /// each slot.
    pub hash_waiting: usize,
    /// The longest wait for a hash slot.
    pub hash_wait: Duration,
    /// Origins (`scheme://host[:port]`) that may send unsafe requests, such as the public
    /// origin of the web app. Empty strings are ignored.
    pub origins: Vec<String>,
    /// The per-client rate limits. Set fields win over the environment.
    pub rates: RateLimits,
    /// The trusted proxies. `None` reads AUTH_TRUSTED_PROXIES (default `private`).
    pub proxies: Option<Vec<String>>,
    /// The environment.
    pub lookup: Lookup,
    /// The clock of the lockout and the rate limit.
    pub now: Clock,
    /// The interval of the rate limit sweep and its log line. Zero turns it off.
    pub sweep: Duration,
    /// The interval of the deletion of expired rows. Zero turns it off.
    pub cleanup: Duration,
}

impl Default for Options {
    fn default() -> Self {
        let slots = password::default_hash_slots();
        Self {
            secure: true,
            lifetime: DEFAULT_LIFETIME,
            idle_timeout: Duration::ZERO,
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
            sweep: DEFAULT_SWEEP,
            cleanup: DEFAULT_CLEANUP,
        }
    }
}

impl std::fmt::Debug for Options {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Options")
            .field("secure", &self.secure)
            .field("lifetime", &self.lifetime)
            .field("idle_timeout", &self.idle_timeout)
            .field("threshold", &self.threshold)
            .field("window", &self.window)
            .field("params", &self.params)
            .field("hash_concurrent", &self.hash_concurrent)
            .field("hash_waiting", &self.hash_waiting)
            .field("hash_wait", &self.hash_wait)
            .field("origins", &self.origins)
            .field("rates", &self.rates)
            .field("proxies", &self.proxies)
            .field("sweep", &self.sweep)
            .field("cleanup", &self.cleanup)
            .finish_non_exhaustive()
    }
}

/// Registers and authenticates users and owns their sessions (Go `auth.Auth`). Clones
/// share one state.
#[derive(Clone)]
pub struct Auth(Arc<Inner>);

struct Inner {
    pool: PgPool,
    params: Params,
    dummy: OnceCell<String>,
    slots: HashSlots,
    threshold: i64,
    window: Duration,
    now: Clock,
    limiter: Option<Arc<Limiter>>,
    proxies: crate::auth::clientip::TrustedProxies,
    csrf: CrossOrigin,
    secure: bool,
    lifetime: Duration,
    idle_timeout: Duration,
    lookup: Lookup,
    stop: watch::Sender<bool>,
    tasks: Mutex<Vec<JoinHandle<()>>>,
}

impl std::fmt::Debug for Auth {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Auth")
            .field("secure", &self.0.secure)
            .field("lifetime", &self.0.lifetime)
            .field("rate_limit", &self.0.limiter.is_some())
            .finish_non_exhaustive()
    }
}

/// Reports whether the dummy hash task stopped because the runtime shut down. A cancel at
/// shutdown is not an error. A panic is.
fn dummy_failure_is_shutdown(e: &tokio::task::JoinError) -> bool {
    e.is_cancelled()
}

fn cannot_start(what: String, try_: &str) -> AuthError {
    AuthError(format!("auth: cannot start: {what}\nTry: {try_}"))
}

fn go_duration(d: Duration) -> String {
    goduration::format(i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
}

impl Auth {
    /// Returns an Auth on `pool` (Go `auth.New`). The pool needs the tables of the auth
    /// migrations: `users`, `sessions`, and `login_attempts`. It starts a task that deletes
    /// expired rows and sweeps the rate limit table, and a task that computes the dummy
    /// hash for logins of unknown emails, so the start does not wait for it. Call it inside
    /// a tokio runtime. [`Auth::close`] stops the tasks.
    ///
    /// # Errors
    ///
    /// An [`AuthError`] with a `Try:` line for a bad option or a bad rate limit variable.
    pub fn new(pool: PgPool, o: Options) -> Result<Self, AuthError> {
        let settings = ratelimit::parse_env(&*o.lookup)?;
        if o.lifetime.is_zero() {
            return Err(cannot_start(
                format!(
                    "the session lifetime {} is not positive",
                    go_duration(o.lifetime)
                ),
                "set Options::lifetime to a positive duration",
            ));
        }
        if o.hash_concurrent < 1 || o.hash_wait.is_zero() {
            return Err(cannot_start(
                format!(
                    "the hash limits need at least 1 slot and a positive wait, got {}, {}, and {}",
                    o.hash_concurrent,
                    o.hash_waiting,
                    go_duration(o.hash_wait)
                ),
                "correct the values of Options::hash_concurrent, hash_waiting, and hash_wait",
            ));
        }
        if o.threshold < 1 || o.window.is_zero() {
            return Err(cannot_start(
                format!(
                    "the lockout needs a threshold of at least 1 and a positive window, got {} and {}",
                    o.threshold,
                    go_duration(o.window)
                ),
                "correct the values of Options::threshold and window",
            ));
        }
        if !password::check_params(&o.params) {
            let p = o.params;
            return Err(cannot_start(
                format!(
                    "the argon2id parameters m={},t={},p={} with a {}-byte salt and a {}-byte key are not valid",
                    p.memory, p.time, p.threads, p.salt_len, p.key_len
                ),
                "use auth::password::DEFAULT_PARAMS",
            ));
        }
        let (rates, clients) = ratelimit::resolve_rates(&o.rates, &settings)?;
        let trusted: Vec<String> = match &o.proxies {
            Some(p) => p.clone(),
            None if settings.proxies.is_empty() => vec!["private".into()],
            None => settings.proxies.split(',').map(str::to_owned).collect(),
        };
        let proxies = crate::auth::clientip::TrustedProxies::parse(&trusted)?;
        let mut csrf = CrossOrigin::default();
        for origin in o.origins.iter().filter(|s| !s.is_empty()) {
            csrf.add_trusted_origin(origin)?;
        }
        let procs = std::thread::available_parallelism().map_or(1, std::num::NonZero::get);
        let limiter =
            (!rates.iter().all(|l| l.off)).then(|| Arc::new(Limiter::new(rates, clients, procs)));
        let (stop, _) = watch::channel(false);
        let auth = Self(Arc::new(Inner {
            pool,
            params: o.params,
            dummy: OnceCell::new(),
            slots: HashSlots::new(o.hash_concurrent, o.hash_waiting, o.hash_wait),
            threshold: o.threshold,
            window: o.window,
            now: o.now.clone(),
            limiter,
            proxies,
            csrf,
            secure: o.secure,
            lifetime: o.lifetime,
            idle_timeout: o.idle_timeout,
            lookup: o.lookup.clone(),
            stop,
            tasks: Mutex::new(Vec::new()),
        }));
        // The dummy hash runs after the start (R0.11, R0.12): a login that comes first
        // waits for it.
        let a = auth.clone();
        auth.spawn(async move {
            a.dummy_hash().await;
        });
        auth.spawn(auth.clone().cleanup_loop(o.cleanup, o.sweep));
        Ok(auth)
    }

    /// Runs `task` in the background until it ends. [`Auth::close`] waits for it, so a
    /// task must also watch [`Auth::stopped`] when it can run long.
    pub fn spawn<F>(&self, task: F)
    where
        F: Future<Output = ()> + Send + 'static,
    {
        let handle = tokio::spawn(task);
        let mut tasks = self.0.tasks.lock().unwrap_or_else(|e| e.into_inner());
        tasks.retain(|t| !t.is_finished());
        tasks.push(handle);
    }

    /// Stops the background tasks and waits for them (Go `Close`). It does not close the
    /// pool. A second call does nothing.
    pub async fn close(&self) {
        // send_replace stores the value also when no task listens yet.
        self.0.stop.send_replace(true);
        let tasks: Vec<_> =
            std::mem::take(&mut *self.0.tasks.lock().unwrap_or_else(|e| e.into_inner()));
        for t in tasks {
            let _ = t.await;
        }
    }

    /// A receiver that turns `true` when [`Auth::close`] runs.
    pub fn stopped(&self) -> watch::Receiver<bool> {
        self.0.stop.subscribe()
    }

    /// The current time of the auth clock.
    pub fn now(&self) -> OffsetDateTime {
        (self.0.now)()
    }

    /// The current time of the auth clock in Unix nanoseconds, the clock of the rate limit.
    pub fn now_ns(&self) -> i64 {
        i64::try_from(self.now().unix_timestamp_nanos()).unwrap_or(i64::MAX)
    }

    /// The pool.
    pub fn pool(&self) -> &PgPool {
        &self.0.pool
    }

    /// The hash queue.
    pub fn slots(&self) -> &HashSlots {
        &self.0.slots
    }

    /// The argon2id parameters of new hashes.
    pub fn params(&self) -> Params {
        self.0.params
    }

    /// The lockout threshold and window.
    pub fn lockout(&self) -> (i64, Duration) {
        (self.0.threshold, self.0.window)
    }

    /// The rate limiter. `None` when every class is off.
    pub fn limiter(&self) -> Option<&Limiter> {
        self.0.limiter.as_deref()
    }

    /// The Secure attribute of the cookies.
    pub fn secure(&self) -> bool {
        self.0.secure
    }

    /// The absolute lifetime of a session.
    pub fn lifetime(&self) -> Duration {
        self.0.lifetime
    }

    /// Reads the environment of the options.
    pub fn lookup(&self, key: &str) -> Option<String> {
        (self.0.lookup)(key)
    }

    /// The hash that a login for an unknown email verifies, so the time does not reveal
    /// accounts. The first call waits for the background computation.
    ///
    /// A runtime shutdown cancels a computation that did not start. That cancel is not an
    /// error: the call logs it at debug level, returns an empty hash, and keeps the cache
    /// empty.
    pub async fn dummy_hash(&self) -> String {
        let params = self.0.params;
        let hash = self
            .0
            .dummy
            .get_or_try_init(|| async move {
                // A random password that nobody knows.
                let mut secret = [0u8; 32];
                if aws_lc_rs::rand::fill(&mut secret).is_err() {
                    panic!("auth: the system random source failed");
                }
                let pw = hex::encode(secret);
                let result =
                    tokio::task::spawn_blocking(move || password::hash_password(&params, &pw))
                        .await;
                match result {
                    Ok(hash) => Ok(hash),
                    Err(e) if dummy_failure_is_shutdown(&e) => Err(e),
                    Err(e) => {
                        // An empty hash fails to parse: such logins answer 500.
                        tracing::error!(error = %e, "auth: cannot compute the dummy hash");
                        Ok(String::new())
                    }
                }
            })
            .await;
        match hash {
            Ok(hash) => hash.clone(),
            Err(e) => {
                tracing::debug!(error = %e, "auth: the shutdown stopped the dummy hash");
                String::new()
            }
        }
    }

    async fn cleanup_loop(self, interval: Duration, sweep: Duration) {
        let mut stop = self.stopped();
        let mut db_tick = (!interval.is_zero()).then(|| tokio::time::interval(interval));
        let mut sweep_tick =
            (self.0.limiter.is_some() && !sweep.is_zero()).then(|| tokio::time::interval(sweep));
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
                _ = async { db_tick.as_mut().expect("checked by the guard").tick().await }, if db_tick.is_some() => {
                    // Close cancels a deletion that runs, as the Go context does.
                    let run = tokio::time::timeout(CLEANUP_LIMIT, self.delete_expired());
                    tokio::select! {
                        _ = stop.changed() => return,
                        r = run => match r {
                            Ok(Ok(())) => {}
                            Ok(Err(e)) => tracing::warn!(error = %e, "auth: cannot delete expired sessions and login attempts"),
                            Err(_) => tracing::warn!("auth: cannot delete expired sessions and login attempts: the deletion took more than a minute"),
                        },
                    }
                }
                _ = async { sweep_tick.as_mut().expect("checked by the guard").tick().await }, if sweep_tick.is_some() => {
                    if let Some(l) = self.limiter() {
                        l.sweep(self.now_ns());
                        l.log_rejections();
                    }
                }
            }
        }
    }

    /// Deletes the expired sessions and the failed logins outside the lockout window. The
    /// background task calls it every [`Options::cleanup`].
    ///
    /// # Errors
    ///
    /// A database error.
    pub async fn delete_expired(&self) -> Result<(), sqlx::Error> {
        store::delete_expired(self.pool()).await?;
        queries::delete_old_attempts(self.pool(), self.now() - self.0.window).await
    }

    /// Wraps `router` in the auth middleware (Go `Middleware`). In order, from the
    /// outside, it:
    ///
    /// - sets Content-Security-Policy to [`DEFAULT_CSP`] when the answer has none,
    /// - answers 429 with Retry-After to a client that used up its rate limit for a login,
    ///   registration, password reset, or OIDC callback, before any session or database
    ///   work (ADR 0016),
    /// - answers 403 to unsafe requests (not GET, HEAD, or OPTIONS) that a browser marks
    ///   as cross-site or same-site with Sec-Fetch-Site, or whose Origin differs from the
    ///   Host and the trusted origins,
    /// - loads the session from the cookie and saves it after the handler,
    /// - loads the signed-in user, so [`AuthUser`] gives it.
    pub fn layer(&self, router: Router) -> Router {
        let router = router
            .layer(middleware::from_fn_with_state(self.clone(), load_user))
            .layer(middleware::from_fn_with_state(self.clone(), session_layer))
            .layer(middleware::from_fn_with_state(self.clone(), cross_origin));
        let router = match &self.0.limiter {
            Some(limiter) => {
                let clock = self.clone();
                ratelimit::layer(
                    router,
                    limiter.clone(),
                    self.0.proxies.clone(),
                    Arc::new(move || clock.now_ns()),
                )
            }
            None => router,
        };
        router.layer(middleware::from_fn(default_csp))
    }

    /// The auth endpoints of ADR 0008 §3. They need [`Auth::layer`]. See
    /// [`crate::auth::routes::routes`].
    pub fn routes(&self) -> Router {
        crate::auth::routes::routes(self.clone())
    }

    /// Signs `user` in on a new session token (Go `startSession`). The old token stops
    /// working, which prevents session fixation. The auth layer saves the session after
    /// the handler.
    pub fn start_session(&self, session: &Session, user: &User) {
        session.clear();
        session.renew();
        session.insert(SESSION_USER_KEY, user.id);
    }

    /// The expiry of a stored session: the deadline, or the idle timeout when it ends
    /// first.
    fn expiry(&self, deadline: OffsetDateTime) -> OffsetDateTime {
        if self.0.idle_timeout.is_zero() {
            return deadline;
        }
        let idle = time::Duration::try_from(self.0.idle_timeout).unwrap_or(time::Duration::MAX);
        deadline.min(OffsetDateTime::now_utc().saturating_add(idle))
    }

    /// The `Set-Cookie` value of the session cookie (Go scs `WriteSessionCookie` with
    /// `net/http` `Cookie.String`). `None` writes the expired cookie of a logout.
    fn cookie(&self, token: &str, expiry: Option<OffsetDateTime>) -> String {
        let mut line = format!("{COOKIE_NAME}={token}; Path=/");
        match expiry {
            None => line.push_str("; Expires=Thu, 01 Jan 1970 00:00:01 GMT; Max-Age=0"),
            Some(expiry) => {
                // Round up to the next second, as scs does: 604800 for a new session.
                let expires =
                    OffsetDateTime::from_unix_timestamp(expiry.unix_timestamp().saturating_add(1))
                        .unwrap_or(expiry);
                let left = (expiry - OffsetDateTime::now_utc()).as_seconds_f64() + 1.0;
                // The float-to-int cast saturates; the value is a count of seconds.
                #[allow(clippy::cast_possible_truncation)]
                let max_age = (left as i64).max(1);
                line.push_str(&format!(
                    "; Expires={}; Max-Age={max_age}",
                    http_date(expires)
                ));
            }
        }
        line.push_str("; HttpOnly");
        if self.0.secure {
            line.push_str("; Secure");
        }
        line.push_str("; SameSite=Lax");
        line
    }
}

/// Formats an HTTP date, as Go `http.TimeFormat`.
fn http_date(t: OffsetDateTime) -> String {
    let format = time::macros::format_description!(
        "[weekday repr:short], [day] [month repr:short] [year] [hour]:[minute]:[second] GMT"
    );
    t.to_offset(time::UtcOffset::UTC)
        .format(format)
        .unwrap_or_default()
}

/// The value of the session cookie of a request (Go `Request.Cookie`).
fn cookie_token(headers: &HeaderMap) -> Option<String> {
    headers
        .get_all(header::COOKIE)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|line| line.split(';'))
        .filter_map(|pair| pair.trim().split_once('='))
        .find(|(name, _)| *name == COOKIE_NAME)
        .map(|(_, value)| {
            let v = value.trim();
            v.strip_prefix('"')
                .and_then(|v| v.strip_suffix('"'))
                .unwrap_or(v)
                .to_owned()
        })
        .filter(|v| !v.is_empty())
}

/// Sets `Cache-Control: no-store`.
pub(crate) fn no_store(res: &mut Response) {
    res.headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
}

/// A problem answer with `Cache-Control: no-store`.
pub(crate) fn problem(status: StatusCode, detail: &str) -> Response {
    let mut res = Problem::new(status, detail).into_response();
    no_store(&mut res);
    res
}

/// A 500 problem with `Cache-Control: no-store`. The error goes to the log.
pub(crate) fn internal_error(action: &str, err: &dyn std::fmt::Display) -> Response {
    tracing::error!(error = %err, "auth: cannot {action}");
    let mut res = Problem::status(StatusCode::INTERNAL_SERVER_ERROR).into_response();
    no_store(&mut res);
    res
}

/// The 401 answer of the auth routes and [`require_user`].
pub(crate) fn unauthorized() -> Response {
    problem(StatusCode::UNAUTHORIZED, "No user is signed in.")
}

/// The 500 answer when the auth layer did not run. Without it, no session exists.
pub(crate) fn not_installed(parts: &Parts) -> Response {
    let id = parts
        .extensions
        .get::<RequestId>()
        .map(|r| r.0.clone())
        .unwrap_or_default();
    tracing::error!(
        request_id = %id,
        "auth: the auth middleware is not installed; wrap the router with Auth::layer"
    );
    let mut res = Problem::status(StatusCode::INTERNAL_SERVER_ERROR).into_response();
    no_store(&mut res);
    res
}

/// The outermost auth layer: the default CSP.
async fn default_csp(req: Request, next: Next) -> Response {
    let mut res = next.run(req).await;
    if !res.headers().contains_key(header::CONTENT_SECURITY_POLICY) {
        res.headers_mut().insert(
            header::CONTENT_SECURITY_POLICY,
            HeaderValue::from_static(DEFAULT_CSP),
        );
    }
    res
}

/// The cross-origin check.
async fn cross_origin(State(auth): State<Auth>, req: Request, next: Next) -> Response {
    let host = req
        .headers()
        .get(header::HOST)
        .and_then(|v| v.to_str().ok())
        .map(str::to_owned)
        .or_else(|| req.uri().authority().map(ToString::to_string))
        .unwrap_or_default();
    if !auth.0.csrf.check(req.method(), req.headers(), &host) {
        return problem(
            StatusCode::FORBIDDEN,
            "Cross-origin requests that change state are not allowed.",
        );
    }
    next.run(req).await
}

/// Loads the session before the handler and commits it after the handler (Go scs
/// `LoadAndSave`). A store failure answers 500 and drops the answer of the handler.
async fn session_layer(State(auth): State<Auth>, mut req: Request, next: Next) -> Response {
    let pool = auth.pool();
    let idle = !auth.0.idle_timeout.is_zero();
    let session = match cookie_token(req.headers()) {
        Some(token) => match store::load(pool, &token).await {
            Err(e) => return internal_error("use the session store", &e),
            Ok(Some(record)) => Session::loaded(token, record, auth.0.lifetime, idle),
            Ok(None) => Session::new(auth.0.lifetime),
        },
        None => Session::new(auth.0.lifetime),
    };
    req.extensions_mut().insert(session.clone());
    let mut res = next.run(req).await;
    res.headers_mut()
        .append(header::VARY, HeaderValue::from_static("Cookie"));
    let (stale, line) = match session.change() {
        SessionChange::None => return res,
        SessionChange::Save {
            token,
            record,
            stale,
        } => {
            for t in &stale {
                if let Err(e) = store::delete(pool, t).await {
                    return internal_error("use the session store", &e);
                }
            }
            let expiry = auth.expiry(record.deadline);
            if let Err(e) = store::save(pool, &token, &record, expiry).await {
                return internal_error("use the session store", &e);
            }
            (Vec::new(), auth.cookie(&token, Some(expiry)))
        }
        SessionChange::Destroy { stale } => (stale, auth.cookie("", None)),
    };
    for t in &stale {
        if let Err(e) = store::delete(pool, t).await {
            return internal_error("use the session store", &e);
        }
    }
    match HeaderValue::from_str(&line) {
        Ok(v) => {
            res.headers_mut().append(header::SET_COOKIE, v);
            res.headers_mut().append(
                header::CACHE_CONTROL,
                HeaderValue::from_static("no-cache=\"Set-Cookie\""),
            );
            res
        }
        Err(e) => internal_error("write the session cookie", &e),
    }
}

/// Puts the user of the session into the request extensions. The session of a deleted
/// user ends.
async fn load_user(State(auth): State<Auth>, mut req: Request, next: Next) -> Response {
    let session = req.extensions().get::<Session>().cloned();
    if let Some(session) = session
        && let Some(id) = session.user_id()
    {
        match queries::user_by_id(auth.pool(), id).await {
            Err(e) => return internal_error("load the session user", &e),
            Ok(Some(user)) => {
                req.extensions_mut().insert(user);
            }
            Ok(None) => session.destroy(),
        }
    }
    next.run(req).await
}

/// The signed-in user (Go `auth.UserFrom`). A handler that takes it answers 401 with the
/// Go text of the generated handlers when no user is signed in. Take
/// `Option<AuthUser>` for a route that also serves anonymous requests.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthUser(pub User);

impl<S: Send + Sync> FromRequestParts<S> for AuthUser {
    type Rejection = ApiError;

    async fn from_request_parts(parts: &mut Parts, _: &S) -> Result<Self, Self::Rejection> {
        parts
            .extensions
            .get::<User>()
            .cloned()
            .map(AuthUser)
            .ok_or_else(ApiError::unauthorized)
    }
}

impl<S: Send + Sync> OptionalFromRequestParts<S> for AuthUser {
    type Rejection = std::convert::Infallible;

    async fn from_request_parts(parts: &mut Parts, _: &S) -> Result<Option<Self>, Self::Rejection> {
        Ok(parts.extensions.get::<User>().cloned().map(AuthUser))
    }
}

/// Answers 401 when the request has no signed-in user. Otherwise it calls the next
/// handler (Go `auth.Required`). Use it with `axum::middleware::from_fn` inside
/// [`Auth::layer`].
pub async fn require_user(req: Request, next: Next) -> Response {
    if req.extensions().get::<User>().is_none() {
        return unauthorized();
    }
    next.run(req).await
}

/// Sets the signed-in user of `req` (Go `auth.WithUser`). Use it only in tests and
/// trusted middleware.
pub fn with_user<B>(mut req: axum::http::Request<B>, user: User) -> axum::http::Request<B> {
    req.extensions_mut().insert(user);
    req
}

#[cfg(test)]
mod dummy_tests {
    use super::dummy_failure_is_shutdown;

    #[tokio::test]
    async fn a_cancel_is_shutdown_and_a_panic_is_an_error() {
        let pending = tokio::spawn(std::future::pending::<()>());
        pending.abort();
        let cancelled = pending.await.unwrap_err();
        assert!(dummy_failure_is_shutdown(&cancelled), "{cancelled}");

        let panicked = tokio::task::spawn_blocking(|| panic!("boom"))
            .await
            .unwrap_err();
        assert!(!dummy_failure_is_shutdown(&panicked), "{panicked}");
    }
}
