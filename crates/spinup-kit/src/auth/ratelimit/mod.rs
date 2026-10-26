//! The per-client rate limit of the auth routes (ADR 0016), as
//! `kit/auth/ratelimit.go`. The algorithm is GCRA with one `i64` for each
//! bucket.

use std::collections::HashMap;
use std::net::IpAddr;
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::time::Duration;

use axum::http::Method;

use super::clientip::{Client, ClientKind, go_quote, mask};
use super::error::AuthError;
use crate::config::goduration;

/// One rate limit for one kind of request: `burst` requests at once, then
/// one more request each `every`. A zero `burst` or `every` keeps the
/// default of the class. `off` turns the class off.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct Limit {
    /// The number of requests that pass at once.
    pub burst: i64,
    /// The time for one more request.
    pub every: Duration,
    /// Turns the class off.
    pub off: bool,
}

impl Limit {
    /// A limit with a burst and an interval.
    pub const fn new(burst: i64, every: Duration) -> Self {
        Self {
            burst,
            every,
            off: false,
        }
    }

    /// A limit that turns the class off.
    pub const fn off() -> Self {
        Self {
            burst: 0,
            every: Duration::ZERO,
            off: true,
        }
    }

    fn every_ns(&self) -> i64 {
        i64::try_from(self.every.as_nanos()).unwrap_or(i64::MAX)
    }
}

/// The per-client limits of the auth endpoints. A zero field keeps the
/// default. `clients` is the number of entries in memory, for all classes.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct RateLimits {
    /// `POST /api/auth/login`: burst 20, every 3 s.
    pub login: Limit,
    /// `POST /api/auth/register`: burst 20, every 20 s.
    pub register: Limit,
    /// `POST /api/auth/reset/request`: burst 10, every 30 s.
    pub reset_request: Limit,
    /// `POST /api/auth/reset/confirm`: burst 10, every 30 s.
    pub reset_confirm: Limit,
    /// `GET /api/auth/oidc/{provider}/callback`: burst 100, every 1 s.
    pub oidc_callback: Limit,
    /// The maximum number of entries. The default is 65536.
    pub clients: i64,
}

impl RateLimits {
    fn limit(&self, c: Class) -> Limit {
        [
            self.login,
            self.register,
            self.reset_request,
            self.reset_confirm,
            self.oidc_callback,
        ][c as usize]
    }
}

/// A kind of limited request. Each class has its own buckets.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
#[repr(u8)]
pub enum Class {
    /// Login.
    Login = 0,
    /// Registration.
    Register = 1,
    /// Password reset request.
    ResetRequest = 2,
    /// Password reset confirmation.
    ResetConfirm = 3,
    /// OIDC callback.
    OidcCallback = 4,
}

/// The number of classes.
pub const NUM_CLASSES: usize = 5;

const ALL_CLASSES: [Class; NUM_CLASSES] = [
    Class::Login,
    Class::Register,
    Class::ResetRequest,
    Class::ResetConfirm,
    Class::OidcCallback,
];

struct ClassInfo {
    name: &'static str,
    env: &'static str,
    action: &'static str,
    def: Limit,
    /// A zero `every` means "scale with the number of CPUs".
    infra: Limit,
}

const fn secs(s: u64) -> Duration {
    Duration::from_secs(s)
}

static CLASSES: [ClassInfo; NUM_CLASSES] = [
    ClassInfo {
        name: "login",
        env: "AUTH_RATE_LIMIT_LOGIN",
        action: "sign-in attempts",
        def: Limit::new(20, secs(3)),
        infra: Limit::new(50, Duration::ZERO),
    },
    ClassInfo {
        name: "register",
        env: "AUTH_RATE_LIMIT_REGISTER",
        action: "sign-up attempts",
        def: Limit::new(20, secs(20)),
        infra: Limit::new(50, Duration::ZERO),
    },
    ClassInfo {
        name: "reset_request",
        env: "AUTH_RATE_LIMIT_RESET_REQUEST",
        action: "password reset requests",
        def: Limit::new(10, secs(30)),
        infra: Limit::new(50, Duration::ZERO),
    },
    ClassInfo {
        name: "reset_confirm",
        env: "AUTH_RATE_LIMIT_RESET_CONFIRM",
        action: "password reset attempts",
        def: Limit::new(10, secs(30)),
        infra: Limit::new(50, Duration::ZERO),
    },
    ClassInfo {
        name: "oidc_callback",
        env: "AUTH_RATE_LIMIT_OIDC_CALLBACK",
        action: "sign-in attempts",
        def: Limit::new(100, secs(1)),
        infra: Limit::new(200, Duration::from_millis(50)),
    },
];

impl Class {
    /// The metric attribute and the log field.
    pub fn name(self) -> &'static str {
        CLASSES[self as usize].name
    }

    /// The text of the 429 detail.
    pub fn action(self) -> &'static str {
        CLASSES[self as usize].action
    }
}

/// The limited routes. The router, the reset routes, the OIDC routes, and
/// the middleware read these names, so the patterns cannot drift apart.
pub const PATH_REGISTER: &str = "/api/auth/register";
/// The login route.
pub const PATH_LOGIN: &str = "/api/auth/login";
/// The reset request route.
pub const PATH_RESET_REQUEST: &str = "/api/auth/reset/request";
/// The reset confirmation route.
pub const PATH_RESET_CONFIRM: &str = "/api/auth/reset/confirm";
/// The prefix of the OIDC routes.
pub const OIDC_PREFIX: &str = "/api/auth/oidc/";
/// The suffix of the OIDC callback.
pub const OIDC_CALLBACK_SUFFIX: &str = "/callback";

/// Returns the class of a request that the rate limit covers. `path` is the
/// escaped path, as axum matches it.
pub fn route_class(method: &Method, path: &str) -> Option<Class> {
    if method == Method::POST {
        return match path {
            PATH_REGISTER => Some(Class::Register),
            PATH_LOGIN => Some(Class::Login),
            PATH_RESET_REQUEST => Some(Class::ResetRequest),
            PATH_RESET_CONFIRM => Some(Class::ResetConfirm),
            _ => None,
        };
    }
    if method == Method::GET || method == Method::HEAD {
        let provider = path
            .strip_prefix(OIDC_PREFIX)?
            .strip_suffix(OIDC_CALLBACK_SUFFIX)?;
        if !provider.is_empty() && !provider.contains('/') {
            return Some(Class::OidcCallback);
        }
    }
    None
}

/// The GCRA form of a Limit. All fields are nanoseconds.
#[derive(Debug, Clone, Copy, Default)]
struct Rule {
    t: i64,
    tau: i64,
    full: i64,
    off: bool,
}

impl Rule {
    fn new(burst: i64, every: i64, off: bool) -> Self {
        Self {
            t: every,
            tau: (burst - 1).saturating_mul(every),
            full: burst.saturating_mul(every),
            off,
        }
    }
}

const LEVEL_HOST: u8 = 0;
const LEVEL_WIDE: u8 = 1;
const LEVEL_INFRA: u8 = 2;

/// The budget of a /48 bucket as a multiple of a /64 bucket.
pub const WIDE_FACTOR: i64 = 8;
/// The number of entries that an eviction reads.
const EVICT_SAMPLE: usize = 16;

const DEFAULT_CLIENTS: i64 = 65536;
const MIN_CLIENTS: i64 = 1024;
const MAX_CLIENTS: i64 = 4_194_304;
const MAX_BURST: i64 = 10000;
const MIN_EVERY: i64 = 1_000_000;
const MAX_EVERY: i64 = 24 * 3_600_000_000_000;
const MAX_SPAN: i64 = 7 * 24 * 3_600_000_000_000;

/// One bucket: level 0 is the address (IPv4) or the /64 (IPv6), level 1 is
/// the /48 of an IPv6 address, and level 2 is the shared infrastructure
/// bucket.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
struct Key {
    addr: Option<IpAddr>,
    class: Class,
    level: u8,
}

/// The tokens that one request took, so that a request which the hash queue
/// refuses can give them back once.
#[derive(Debug)]
pub struct Taken {
    keys: [Key; 2],
    step: [i64; 2],
    n: usize,
    done: AtomicBool,
}

/// The result of [`Limiter::take`].
#[derive(Debug)]
pub enum Take {
    /// The request passes. It carries its tokens, or none for an off class.
    Allowed(Option<Taken>),
    /// The request is refused. The value is the time to the next token.
    Refused(Duration),
}

/// One GCRA state (the theoretical arrival time) per bucket. The table never
/// holds more than `max` entries.
#[derive(Debug)]
pub struct Limiter {
    tat: Mutex<HashMap<Key, i64>>,
    max: usize,
    host: [Rule; NUM_CLASSES],
    wide: [Rule; NUM_CLASSES],
    infra: [Rule; NUM_CLASSES],
    rejected: [AtomicI64; NUM_CLASSES],
}

impl Limiter {
    /// Returns a limiter for the rates, at most `clients` entries, and
    /// `procs` CPUs.
    pub fn new(rates: [Limit; NUM_CLASSES], clients: usize, procs: usize) -> Self {
        let mut host = [Rule::default(); NUM_CLASSES];
        let mut wide = [Rule::default(); NUM_CLASSES];
        let mut infra = [Rule::default(); NUM_CLASSES];
        for c in ALL_CLASSES {
            let i = c as usize;
            let r = rates[i];
            host[i] = Rule::new(r.burst, r.every_ns(), r.off);
            // The /48 bucket has 8 times the budget of the /64 bucket.
            wide[i] = Rule::new(r.burst * WIDE_FACTOR, r.every_ns() / WIDE_FACTOR, r.off);
            let mut inf = CLASSES[i].infra;
            if inf.every.is_zero() {
                // About twice the hash capacity of the machine: one hash takes 0.16 CPU-seconds.
                inf.every =
                    Duration::from_nanos(MIN_EVERY.max(320_000_000 / procs.max(1) as i64) as u64);
            }
            infra[i] = Rule::new(inf.burst, inf.every_ns(), r.off);
        }
        Self {
            tat: Mutex::new(HashMap::new()),
            max: clients,
            host,
            wide,
            infra,
            rejected: Default::default(),
        }
    }

    /// Reports whether one request of class `c` is allowed at `now`
    /// (nanoseconds). A refused request changes no bucket. An IPv6 client
    /// takes a token from its /64 bucket and from its /48 bucket, in one lock.
    pub fn take(&self, c: Class, who: &Client, now: i64) -> Take {
        let i = c as usize;
        let mut keys = [Key {
            addr: None,
            class: c,
            level: LEVEL_INFRA,
        }; 2];
        let mut rules = [self.infra[i]; 2];
        let mut n = 1;
        match (who.kind, who.addr) {
            (ClientKind::Addr, Some(a @ IpAddr::V4(_))) => {
                keys[0] = Key {
                    addr: Some(a),
                    class: c,
                    level: LEVEL_HOST,
                };
                rules[0] = self.host[i];
            }
            (ClientKind::Addr, Some(a)) => {
                keys[0] = Key {
                    addr: Some(mask(a, 64)),
                    class: c,
                    level: LEVEL_HOST,
                };
                rules[0] = self.host[i];
                keys[1] = Key {
                    addr: Some(mask(a, 48)),
                    class: c,
                    level: LEVEL_WIDE,
                };
                rules[1] = self.wide[i];
                n = 2;
            }
            _ => {}
        }
        if rules[0].off {
            return Take::Allowed(None);
        }
        let mut tat = self.tat.lock().expect("limiter lock");
        let mut next = [0i64; 2];
        let mut wait = 0i64;
        let mut ok = true;
        for j in 0..n {
            let mut t = tat.get(&keys[j]).copied().unwrap_or(now).max(now);
            // A clock that stepped back cannot lock a key for longer than one full bucket.
            t = t.min(now.saturating_add(rules[j].full));
            if t - now > rules[j].tau {
                ok = false;
                wait = wait.max(t - now - rules[j].tau);
                continue;
            }
            next[j] = t + rules[j].t;
        }
        if !ok {
            self.rejected[i].fetch_add(1, Ordering::Relaxed);
            return Take::Refused(Duration::from_nanos(wait as u64));
        }
        for j in 0..n {
            if !tat.contains_key(&keys[j]) && tat.len() >= self.max {
                evict(&mut tat, now);
            }
            tat.insert(keys[j], next[j]);
        }
        Take::Allowed(Some(Taken {
            keys,
            step: [rules[0].t, rules[1].t],
            n,
            done: AtomicBool::new(false),
        }))
    }

    /// Gives back the tokens of a request, once.
    pub fn refund(&self, tk: &Taken, now: i64) {
        if tk.done.swap(true, Ordering::AcqRel) {
            return;
        }
        let mut tat = self.tat.lock().expect("limiter lock");
        for j in 0..tk.n {
            if let Some(t) = tat.get_mut(&tk.keys[j]) {
                *t -= tk.step[j];
                if *t <= now {
                    tat.remove(&tk.keys[j]);
                }
            }
        }
    }

    /// Deletes the entries whose bucket is full.
    pub fn sweep(&self, now: i64) -> usize {
        let mut tat = self.tat.lock().expect("limiter lock");
        let before = tat.len();
        tat.retain(|_, t| *t > now);
        before - tat.len()
    }

    /// The number of entries.
    pub fn size(&self) -> usize {
        self.tat.lock().expect("limiter lock").len()
    }

    /// Returns and resets the number of refusals of each class.
    pub fn take_rejected(&self) -> [i64; NUM_CLASSES] {
        std::array::from_fn(|i| self.rejected[i].swap(0, Ordering::Relaxed))
    }

    /// The number of refusals of class `c` since the last `take_rejected`.
    pub fn rejected(&self, c: Class) -> i64 {
        self.rejected[c as usize].load(Ordering::Relaxed)
    }
}

/// Frees one place. An entry whose bucket is full goes first. Without one,
/// the entry with the smallest tat in a sample goes, because it has the
/// shortest penalty left.
fn evict(tat: &mut HashMap<Key, i64>, now: i64) {
    let mut victim = None;
    let mut least = i64::MAX;
    for (k, &t) in tat.iter().take(EVICT_SAMPLE) {
        if t <= now {
            victim = Some(*k);
            break;
        }
        if t < least {
            victim = Some(*k);
            least = t;
        }
    }
    if let Some(k) = victim {
        tat.remove(&k);
    }
}

/// The environment variables of the rate limit.
#[derive(Debug, Clone, Default)]
pub struct RateSettings {
    switch: String,
    specs: [String; NUM_CLASSES],
    clients: i64,
    /// AUTH_TRUSTED_PROXIES.
    pub proxies: String,
}

impl RateSettings {
    /// Reads the environment through `lookup`.
    pub fn load(lookup: &dyn Fn(&str) -> Option<String>) -> Result<Self, AuthError> {
        let get = |k: &str| lookup(k).unwrap_or_default();
        let mut s = Self {
            switch: get("AUTH_RATE_LIMIT"),
            specs: std::array::from_fn(|i| get(CLASSES[i].env)),
            clients: 0,
            proxies: get("AUTH_TRUSTED_PROXIES"),
        };
        let raw = get("AUTH_RATE_LIMIT_CLIENTS");
        if !raw.trim().is_empty() {
            s.clients = raw.trim().parse().map_err(|_| {
                AuthError(format!(
                    "auth: cannot start: AUTH_RATE_LIMIT_CLIENTS {} is not a whole number\nTry: set AUTH_RATE_LIMIT_CLIENTS=65536, or remove it to use the default",
                    go_quote(&raw)
                ))
            })?;
        }
        Ok(s)
    }
}

fn env_error(name: &str, value: &str, why: &str, try_: &str) -> AuthError {
    AuthError(format!(
        "auth: cannot start: {name} {} is not valid: {why}\nTry: {try_}",
        go_quote(value)
    ))
}

/// Reads the value of AUTH_RATE_LIMIT.
fn parse_switch(s: &str) -> Result<bool, AuthError> {
    match s.trim().to_ascii_lowercase().as_str() {
        "" | "on" | "true" | "1" => Ok(false),
        "off" | "false" | "0" => Ok(true),
        _ => Err(env_error(
            "AUTH_RATE_LIMIT",
            s,
            "use on or off",
            "set AUTH_RATE_LIMIT=off to turn the rate limit off, or remove it to use the default",
        )),
    }
}

/// Reads `off` or `burst=<n>,every=<duration>`. Each key is optional.
pub fn parse_limit_spec(s: &str) -> Result<Limit, String> {
    let mut l = Limit::default();
    let s = s.trim();
    if s.is_empty() {
        return Ok(l);
    }
    if s.eq_ignore_ascii_case("off") {
        return Ok(Limit::off());
    }
    for part in s.split(',') {
        let Some((key, value)) = part.trim().split_once('=') else {
            return Err("use off or burst=<n>,every=<duration>".into());
        };
        let key = key.trim().to_ascii_lowercase();
        let value = value.trim();
        if key == "burst" && l.burst == 0 {
            let n: i64 = value
                .parse()
                .map_err(|_| "burst must be a whole number".to_string())?;
            l.burst = n;
            if !(1..=MAX_BURST).contains(&n) {
                return Err(format!("burst must be 1 to {MAX_BURST}"));
            }
        } else if key == "every" && l.every.is_zero() {
            let d = goduration::parse(value)
                .ok_or_else(|| "every must be a duration such as 3s".to_string())?;
            if !(MIN_EVERY..=MAX_EVERY).contains(&d) {
                return Err(format!(
                    "every must be {} to {}",
                    goduration::format(MIN_EVERY),
                    goduration::format(MAX_EVERY)
                ));
            }
            l.every = Duration::from_nanos(d as u64);
        } else {
            return Err(format!("the key {} is unknown or repeated", go_quote(&key)));
        }
    }
    Ok(l)
}

/// Applies the fields that `over` sets. A non-zero burst or every turns the
/// class on, unless `over` turns it off.
fn overlay(mut base: Limit, over: Limit) -> Limit {
    if over.burst != 0 {
        base.burst = over.burst;
        base.off = false;
    }
    if !over.every.is_zero() {
        base.every = over.every;
        base.off = false;
    }
    if over.off {
        base.off = true;
    }
    base
}

/// Merges the defaults, the environment, and the code option. The option
/// wins over the environment, and the environment wins over the default.
pub fn resolve_rates(
    opt: &RateLimits,
    s: &RateSettings,
) -> Result<([Limit; NUM_CLASSES], usize), AuthError> {
    let all_off = parse_switch(&s.switch)?;
    let mut rates = [Limit::default(); NUM_CLASSES];
    for c in ALL_CLASSES {
        let i = c as usize;
        let info = &CLASSES[i];
        let raw = &s.specs[i];
        let from_env = parse_limit_spec(raw).map_err(|why| {
            env_error(
                info.env,
                raw,
                &why,
                &format!(
                    "set {}=burst={},every={}, or remove it to use the default",
                    info.env,
                    info.def.burst,
                    goduration::format(info.def.every_ns())
                ),
            )
        })?;
        let mut l = overlay(info.def, from_env);
        if all_off {
            l.off = true;
        }
        let o = opt.limit(c);
        if o.burst < 0 {
            return Err(AuthError(format!(
                "auth: cannot start: the {} limit has a negative burst or interval\nTry: pass a positive burst and every to with_rate_limits",
                info.name
            )));
        }
        l = overlay(l, o);
        if !l.off {
            let every = l.every_ns();
            if !(1..=MAX_BURST).contains(&l.burst)
                || !(MIN_EVERY..=MAX_EVERY).contains(&every)
                || l.burst.saturating_mul(every) > MAX_SPAN
            {
                return Err(AuthError(format!(
                    "auth: cannot start: the {} limit burst={},every={} is not valid: burst must be 1 to {MAX_BURST}, every must be {} to {}, and burst times every at most {}\nTry: correct {} or the values of with_rate_limits",
                    info.name,
                    l.burst,
                    goduration::format(every),
                    goduration::format(MIN_EVERY),
                    goduration::format(MAX_EVERY),
                    goduration::format(MAX_SPAN),
                    info.env
                )));
            }
        }
        rates[i] = l;
    }
    let mut clients = DEFAULT_CLIENTS;
    if s.clients != 0 {
        clients = s.clients;
        if !(MIN_CLIENTS..=MAX_CLIENTS).contains(&clients) {
            return Err(env_error(
                "AUTH_RATE_LIMIT_CLIENTS",
                &clients.to_string(),
                &format!("use {MIN_CLIENTS} to {MAX_CLIENTS}"),
                "set AUTH_RATE_LIMIT_CLIENTS=65536, or remove it to use the default",
            ));
        }
    }
    if opt.clients != 0 {
        clients = opt.clients;
        if !(MIN_CLIENTS..=MAX_CLIENTS).contains(&clients) {
            return Err(AuthError(format!(
                "auth: cannot start: RateLimits.clients {clients} is out of range\nTry: use {MIN_CLIENTS} to {MAX_CLIENTS} in with_rate_limits"
            )));
        }
    }
    Ok((rates, clients as usize))
}

/// The 429 detail of a refused request.
pub fn too_many_detail(c: Class, wait: Duration) -> (u64, String) {
    let seconds = (wait.as_secs_f64().ceil() as u64).max(1);
    let unit = if seconds == 1 { "second" } else { "seconds" };
    (
        seconds,
        format!(
            "Too many {} from your network. Try again in {seconds} {unit}.",
            c.action()
        ),
    )
}

/// Reads rate settings through an injected environment lookup.
pub fn parse_env(lookup: &dyn Fn(&str) -> Option<String>) -> Result<RateSettings, AuthError> {
    RateSettings::load(lookup)
}

/// A response marker for a request that the password hash queue refused.
/// The rate layer refunds this request's tokens once.
#[derive(Debug, Clone, Copy)]
pub struct HashBusy;

/// A clock that returns nanoseconds. Tests can replace it.
pub type Clock = std::sync::Arc<dyn Fn() -> i64 + Send + Sync>;

#[derive(Clone)]
struct LayerState {
    limiter: std::sync::Arc<Limiter>,
    proxies: super::clientip::TrustedProxies,
    now: Clock,
    warned_at: std::sync::Arc<Mutex<Option<i64>>>,
}

/// Wraps a router with the rate check before its session and database layers.
/// The caller owns the limiter and calls `sweep` and `log_rejections` each minute.
pub fn layer(
    router: axum::Router,
    limiter: std::sync::Arc<Limiter>,
    proxies: super::clientip::TrustedProxies,
    now: Clock,
) -> axum::Router {
    router.layer(axum::middleware::from_fn_with_state(
        LayerState {
            limiter,
            proxies,
            now,
            warned_at: Default::default(),
        },
        front,
    ))
}

async fn front(
    axum::extract::State(state): axum::extract::State<LayerState>,
    request: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    let Some(class) = route_class(request.method(), request.uri().path()) else {
        return next.run(request).await;
    };
    let who = super::clientip::client_addr(&state.proxies, &request);
    if who.kind == ClientKind::Exempt {
        return next.run(request).await;
    }
    let now = (state.now)();
    if who.kind == ClientKind::Infra {
        let mut last = state.warned_at.lock().expect("infrastructure warning lock");
        if last.is_none_or(|last| now.saturating_sub(last) >= 600_000_000_000) {
            *last = Some(now);
            tracing::warn!(
                try_ = "make the web service send X-Forwarded-For (ADDRESS_HEADER=X-Forwarded-For), or set AUTH_TRUSTED_PROXIES",
                "auth: requests from a trusted proxy have no client address, so they share one rate limit"
            );
        }
    }
    let taken = match state.limiter.take(class, &who, now) {
        Take::Refused(wait) => return too_many(class, wait),
        Take::Allowed(taken) => taken,
    };
    let response = next.run(request).await;
    if response.extensions().get::<HashBusy>().is_some()
        && let Some(taken) = taken
    {
        state.limiter.refund(&taken, (state.now)());
    }
    response
}

/// Returns the ADR 0016 refusal answer, including the OIDC redirect.
pub fn too_many(class: Class, wait: Duration) -> axum::response::Response {
    use axum::http::{HeaderValue, StatusCode, header};
    use axum::response::IntoResponse;
    #[cfg(feature = "telemetry")]
    crate::telemetry::meter("spinup/kit/auth")
        .u64_counter("auth.rate_limited")
        .build()
        .add(1, &[opentelemetry::KeyValue::new("class", class.name())]);
    let (seconds, detail) = too_many_detail(class, wait);
    let mut response = if class == Class::OidcCallback {
        let mut response = StatusCode::SEE_OTHER.into_response();
        response.headers_mut().insert(
            header::LOCATION,
            HeaderValue::from_static("/login?error=rate_limited"),
        );
        response
    } else {
        crate::httpx::Problem::new(StatusCode::TOO_MANY_REQUESTS, detail).into_response()
    };
    response
        .headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    response
        .headers_mut()
        .insert(header::RETRY_AFTER, HeaderValue::from(seconds));
    response
}

impl Limiter {
    /// Logs and resets the refusal counts. The log contains no client address.
    pub fn log_rejections(&self) {
        let counts = self.take_rejected();
        if counts.iter().all(|&n| n == 0) {
            return;
        }
        let parts: Vec<String> = ALL_CLASSES
            .iter()
            .zip(counts)
            .filter(|(_, n)| *n > 0)
            .map(|(class, n)| format!("{}={n}", class.name()))
            .collect();
        tracing::warn!(
            refused = %parts.join(" "),
            try_ = "raise AUTH_RATE_LIMIT_<CLASS> if real users hit the limit, or set AUTH_TRUSTED_PROXIES if all clients show one address",
            "auth: rate limited requests in the last minute"
        );
    }
}
