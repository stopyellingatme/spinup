//! The per-client rate limit of the auth routes (ADR 0016), as
//! `kit/auth/ratelimit.go`. The algorithm is GCRA with one `i64` for each
//! bucket.

use std::collections::HashMap;
use std::net::IpAddr;
use std::sync::Mutex;
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::time::Duration;

use axum::http::Method;

use super::AuthError;
use super::clientip::{Client, ClientKind, go_quote, mask};
use super::goduration;

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

#[cfg(test)]
mod tests {
    //! The tests of ratelimit_test.go that do not need HTTP, with the Go
    //! values unchanged.

    use super::*;
    use crate::auth::clientip::parse_addr;
    use std::collections::HashMap as Map;
    use std::sync::Arc;
    use std::sync::atomic::AtomicUsize;
    use std::thread;

    const S: i64 = 1_000_000_000;
    const MIN: i64 = 60 * S;
    const HOUR: i64 = 60 * MIN;
    // 2026-10-15 12:00:00 UTC
    const T0: i64 = 1_792_065_600 * S;

    fn v4(s: &str) -> Client {
        Client::addr(parse_addr(s).unwrap())
    }

    fn addr4(a: u8, b: u8, c: u8, d: u8) -> Client {
        Client::addr(IpAddr::V4([a, b, c, d].into()))
    }

    fn test_limiter(rate: Limit, clients: usize) -> Limiter {
        Limiter::new([rate; NUM_CLASSES], clients, 1)
    }

    fn ok(t: &Take) -> bool {
        matches!(t, Take::Allowed(_))
    }

    fn wait(t: &Take) -> Option<Duration> {
        match t {
            Take::Refused(w) => Some(*w),
            Take::Allowed(_) => None,
        }
    }

    #[test]
    fn limiter_gcra() {
        let l = test_limiter(Limit::new(3, secs(10)), 1024);
        let who = v4("198.51.100.7");
        for i in 0..3 {
            assert!(
                ok(&l.take(Class::Login, &who, T0)),
                "request {} refused",
                i + 1
            );
        }
        assert_eq!(wait(&l.take(Class::Login, &who, T0)), Some(secs(10)));
        // A refused request takes no token: the wait does not grow.
        for _ in 0..5 {
            l.take(Class::Login, &who, T0);
        }
        assert_eq!(wait(&l.take(Class::Login, &who, T0 + 4 * S)), Some(secs(6)));
        assert!(
            ok(&l.take(Class::Login, &who, T0 + 10 * S)),
            "one token must return after every"
        );
        assert!(
            !ok(&l.take(Class::Login, &who, T0 + 10 * S)),
            "only one token returned"
        );
        // Another class and another client have their own buckets.
        assert!(ok(&l.take(Class::Register, &who, T0)));
        assert!(ok(&l.take(Class::Login, &v4("198.51.100.8"), T0)));
    }

    #[test]
    fn limiter_clock_step_back() {
        let l = test_limiter(Limit::new(2, secs(60)), 1024);
        let who = v4("198.51.100.7");
        let late = T0 + 24 * HOUR;
        l.take(Class::Login, &who, late);
        l.take(Class::Login, &who, late);
        let w = wait(&l.take(Class::Login, &who, T0)).expect("refused");
        assert!(w <= secs(60), "wait {w:?}, want at most 1m");
    }

    #[test]
    fn limiter_off() {
        let l = test_limiter(
            Limit {
                burst: 1,
                every: secs(3600),
                off: true,
            },
            1024,
        );
        for _ in 0..100 {
            assert!(ok(&l.take(Class::Login, &v4("198.51.100.7"), T0)));
        }
        assert_eq!(l.size(), 0);
    }

    #[test]
    fn limiter_bounded_memory() {
        let l = test_limiter(Limit::new(20, secs(3)), 1024);
        for i in 0..100_000u32 {
            l.take(
                Class::Login,
                &addr4(10 + (i >> 16) as u8, (i >> 8) as u8, i as u8, 1),
                T0,
            );
        }
        assert!(l.size() <= 1024, "the table holds {} entries", l.size());
    }

    #[test]
    fn limiter_eviction_keeps_penalised_keys() {
        let l = test_limiter(Limit::new(2, secs(3600)), 1024);
        let hot = v4("198.51.100.99");
        for _ in 0..2 {
            l.take(Class::Login, &hot, T0);
        }
        assert!(!ok(&l.take(Class::Login, &hot, T0)));
        for i in 0..20000u32 {
            l.take(Class::Login, &addr4(10, (i >> 8) as u8, i as u8, 1), T0);
        }
        assert!(
            !ok(&l.take(Class::Login, &hot, T0)),
            "eviction removed the penalised key"
        );
    }

    #[test]
    fn limiter_eviction_prefers_full_buckets() {
        let l = test_limiter(Limit::new(2, secs(1)), 1024);
        let hot = v4("198.51.100.99");
        l.take(Class::Login, &hot, T0 + HOUR);
        l.take(Class::Login, &hot, T0 + HOUR);
        for i in 0..1023u32 {
            l.take(Class::Login, &addr4(10, (i >> 8) as u8, i as u8, 1), T0);
        }
        let now = T0 + HOUR;
        for i in 0..5000u32 {
            l.take(Class::Login, &addr4(172, (i >> 8) as u8, i as u8, 1), now);
        }
        assert!(
            !ok(&l.take(Class::Login, &hot, now)),
            "the hot key was evicted before the full buckets"
        );
    }

    #[test]
    fn limiter_sweep_and_refund() {
        let l = test_limiter(Limit::new(2, secs(60)), 1024);
        for i in 0..10u8 {
            l.take(Class::Login, &addr4(10, 0, 0, i), T0);
        }
        assert_eq!(l.sweep(T0), 0);
        assert_eq!(l.size(), 10);
        assert_eq!(l.sweep(T0 + 2 * MIN), 10);
        assert_eq!(l.size(), 0);
        let who = v4("198.51.100.7");
        let _tk1 = l.take(Class::Login, &who, T0);
        let Take::Allowed(Some(tk2)) = l.take(Class::Login, &who, T0) else {
            panic!("refused")
        };
        assert!(
            !ok(&l.take(Class::Login, &who, T0)),
            "the bucket is not empty"
        );
        l.refund(&tk2, T0);
        l.refund(&tk2, T0);
        assert!(
            ok(&l.take(Class::Login, &who, T0)),
            "the refund gave nothing"
        );
        assert!(
            !ok(&l.take(Class::Login, &who, T0)),
            "a second refund of the same request gave a second token"
        );
    }

    #[test]
    fn limiter_concurrent() {
        let l = Arc::new(test_limiter(Limit::new(5, secs(3600)), 1024));
        let passes = Arc::new(AtomicUsize::new(0));
        let handles: Vec<_> = (0..64)
            .map(|g| {
                let l = l.clone();
                let passes = passes.clone();
                thread::spawn(move || {
                    let who = v4(&format!("198.51.100.{}", g % 8));
                    for _ in 0..50 {
                        if ok(&l.take(Class::Login, &who, T0)) {
                            passes.fetch_add(1, Ordering::Relaxed);
                        }
                    }
                })
            })
            .collect();
        for h in handles {
            h.join().unwrap();
        }
        assert_eq!(passes.load(Ordering::Relaxed), 8 * 5);
    }

    #[test]
    fn limiter_ipv6_groups() {
        let l = test_limiter(Limit::new(3, secs(3600)), 1024);
        let a = v4("2001:db8:1:2::1");
        let same_net = v4("2001:db8:1:2:ffff::9");
        let other_net = v4("2001:db8:1:3::1");
        for _ in 0..3 {
            l.take(Class::Login, &a, T0);
        }
        assert!(
            !ok(&l.take(Class::Login, &same_net, T0)),
            "two addresses of one /64 do not share a bucket"
        );
        assert!(
            ok(&l.take(Class::Login, &other_net, T0)),
            "two /64 networks share a bucket"
        );
    }

    #[test]
    fn limiter_ipv6_wide_bucket() {
        let l = test_limiter(Limit::new(20, secs(3)), 65536);
        let mut passes = 0;
        for i in 0..100_000u32 {
            let b = [
                0x20,
                0x01,
                0x0d,
                0xb8,
                0,
                1,
                (i >> 8) as u8,
                i as u8,
                0,
                0,
                0,
                0,
                0,
                0,
                (i >> 16) as u8,
                1,
            ];
            if ok(&l.take(Class::Login, &Client::addr(IpAddr::V6(b.into())), T0)) {
                passes += 1;
            }
        }
        assert_eq!(passes, 20 * WIDE_FACTOR);
        assert!(l.size() as i64 <= 20 * WIDE_FACTOR + 1, "{}", l.size());
    }

    #[test]
    fn limiter_infrastructure_bucket() {
        let mut rates = [Limit::default(); NUM_CLASSES];
        rates[0] = Limit::new(2, secs(3600));
        let l = Limiter::new(rates, 1024, 1);
        let infra = Client::infra();
        let passes = (0..100)
            .filter(|_| ok(&l.take(Class::Login, &infra, T0)))
            .count();
        assert_eq!(passes, 50);
        assert!(ok(&l.take(Class::Login, &v4("198.51.100.7"), T0)));
    }

    #[test]
    fn parse_limit_spec_table() {
        let none = Limit::default();
        for (input, want, bad) in [
            ("", none, ""),
            ("off", Limit::off(), ""),
            ("OFF", Limit::off(), ""),
            ("burst=5", Limit { burst: 5, ..none }, ""),
            (
                "every=2s",
                Limit {
                    every: secs(2),
                    ..none
                },
                "",
            ),
            ("burst=60, every=1s", Limit::new(60, secs(1)), ""),
            ("burst=0", none, "burst must be 1 to 10000"),
            ("burst=10001", none, "burst must be 1 to 10000"),
            ("burst=x", none, "whole number"),
            ("every=0s", none, "every must be"),
            ("every=48h", none, "every must be"),
            ("every=3", none, "duration"),
            ("burst=1,burst=2", none, "unknown or repeated"),
            ("rate=1", none, "unknown or repeated"),
            ("20", none, "use off or burst"),
        ] {
            match parse_limit_spec(input) {
                Ok(got) => assert!(bad.is_empty() && got == want, "{input:?}: {got:?}"),
                Err(e) => assert!(!bad.is_empty() && e.contains(bad), "{input:?}: {e}"),
            }
        }
    }

    fn settings(pairs: &[(&str, &str)]) -> RateSettings {
        let m: Map<String, String> = pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        RateSettings::load(&|k| m.get(k).cloned()).unwrap()
    }

    #[test]
    fn resolve_rates_defaults() {
        let (rates, clients) = resolve_rates(&RateLimits::default(), &settings(&[])).unwrap();
        assert_eq!(rates[0], Limit::new(20, secs(3)));
        assert_eq!(rates[1], Limit::new(20, secs(20)));
        assert_eq!(rates[2], Limit::new(10, secs(30)));
        assert_eq!(rates[3], Limit::new(10, secs(30)));
        assert_eq!(rates[4], Limit::new(100, secs(1)));
        assert_eq!(clients, 65536);
    }

    #[test]
    fn resolve_rates_precedence() {
        let env = settings(&[
            ("AUTH_RATE_LIMIT_LOGIN", "burst=5,every=1s"),
            ("AUTH_RATE_LIMIT_REGISTER", "burst=7"),
            ("AUTH_RATE_LIMIT_CLIENTS", "2048"),
        ]);
        let opt = RateLimits {
            login: Limit {
                burst: 9,
                ..Limit::default()
            },
            ..RateLimits::default()
        };
        let (rates, clients) = resolve_rates(&opt, &env).unwrap();
        assert_eq!(rates[0], Limit::new(9, secs(1)));
        assert_eq!(rates[1], Limit::new(7, secs(20)));
        assert_eq!(clients, 2048);
        let (_, clients) = resolve_rates(
            &RateLimits {
                clients: 4096,
                ..RateLimits::default()
            },
            &env,
        )
        .unwrap();
        assert_eq!(clients, 4096);
    }

    #[test]
    fn resolve_rates_switch_off() {
        let env = settings(&[
            ("AUTH_RATE_LIMIT", "off"),
            ("AUTH_RATE_LIMIT_LOGIN", "burst=5"),
        ]);
        let opt = RateLimits {
            register: Limit {
                burst: 3,
                ..Limit::default()
            },
            ..RateLimits::default()
        };
        let (rates, _) = resolve_rates(&opt, &env).unwrap();
        for c in ALL_CLASSES {
            assert_eq!(rates[c as usize].off, c != Class::Register, "{}", c.name());
        }
        let (rates, _) = resolve_rates(
            &RateLimits {
                login: Limit::off(),
                ..RateLimits::default()
            },
            &settings(&[]),
        )
        .unwrap();
        assert!(rates[0].off && !rates[1].off);
    }

    /// The environment, the option, and a text of the error.
    type ErrCase = (Vec<(&'static str, &'static str)>, RateLimits, &'static str);

    #[test]
    fn resolve_rates_errors() {
        let cases: Vec<ErrCase> = vec![
            (
                vec![("AUTH_RATE_LIMIT_LOGIN", "burst=0")],
                RateLimits::default(),
                r#"AUTH_RATE_LIMIT_LOGIN "burst=0" is not valid"#,
            ),
            (
                vec![("AUTH_RATE_LIMIT", "maybe")],
                RateLimits::default(),
                r#"AUTH_RATE_LIMIT "maybe""#,
            ),
            (
                vec![("AUTH_RATE_LIMIT_CLIENTS", "5")],
                RateLimits::default(),
                "AUTH_RATE_LIMIT_CLIENTS",
            ),
            (
                vec![],
                RateLimits {
                    login: Limit {
                        burst: -1,
                        ..Limit::default()
                    },
                    ..RateLimits::default()
                },
                "negative",
            ),
            (
                vec![],
                RateLimits {
                    clients: 3,
                    ..RateLimits::default()
                },
                "clients",
            ),
            (
                vec![("AUTH_RATE_LIMIT_LOGIN", "burst=10000,every=24h")],
                RateLimits::default(),
                "at most",
            ),
        ];
        for (env, opt, want) in cases {
            let err = resolve_rates(&opt, &settings(&env))
                .unwrap_err()
                .to_string();
            assert!(
                err.contains(want) && err.contains("\nTry: "),
                "{env:?}: {err}"
            );
        }
    }

    // The exact texts of testdata/script/auth_ratelimit.txtar.
    #[test]
    fn txtar_error_texts() {
        let err = resolve_rates(
            &RateLimits::default(),
            &settings(&[("AUTH_RATE_LIMIT_LOGIN", "burst=0")]),
        )
        .unwrap_err()
        .to_string();
        assert!(
            err.contains(r#"AUTH_RATE_LIMIT_LOGIN "burst=0" is not valid"#),
            "{err}"
        );
        assert!(
            err.contains("Try: set AUTH_RATE_LIMIT_LOGIN=burst=20,every=3s"),
            "{err}"
        );
        let (secs_, detail) = too_many_detail(Class::Login, secs(3600));
        assert_eq!(secs_, 3600);
        assert!(detail.contains("Try again in 3600 seconds"));
    }

    #[test]
    fn route_class_table() {
        let get = Method::GET;
        let post = Method::POST;
        for (m, p, want) in [
            (&post, "/api/auth/login", Some(Class::Login)),
            (&post, "/api/auth/register", Some(Class::Register)),
            (&post, "/api/auth/reset/request", Some(Class::ResetRequest)),
            (&post, "/api/auth/reset/confirm", Some(Class::ResetConfirm)),
            (
                &get,
                "/api/auth/oidc/google/callback",
                Some(Class::OidcCallback),
            ),
            (
                &Method::HEAD,
                "/api/auth/oidc/google/callback",
                Some(Class::OidcCallback),
            ),
            (
                &get,
                "/api/auth/oidc/g%2Fx/callback",
                Some(Class::OidcCallback),
            ),
            (&get, "/api/auth/oidc/google/start", None),
            (&get, "/api/auth/oidc/providers", None),
            (&get, "/api/auth/oidc//callback", None),
            (&get, "/api/auth/oidc/a/b/callback", None),
            (&get, "/api/auth/login", None),
            (&post, "/api/auth/logout", None),
            (&get, "/api/session", None),
            (&post, "/api/auth/login/", None),
        ] {
            assert_eq!(route_class(m, p), want, "{m} {p}");
        }
    }
}
