//! The tests of ratelimit_test.go that do not need HTTP, with the Go
//! values unchanged.

use axum::http::Method;
use spinup_kit::auth::clientip::{Client, TrustedProxies, parse_addr};
use spinup_kit::auth::ratelimit::*;
use std::collections::HashMap as Map;
use std::net::IpAddr;
use std::sync::atomic::Ordering;
use std::time::Duration;
fn secs(s: u64) -> Duration {
    Duration::from_secs(s)
}
const ALL_CLASSES: [Class; 5] = [
    Class::Login,
    Class::Register,
    Class::ResetRequest,
    Class::ResetConfirm,
    Class::OidcCallback,
];
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

// The middleware cases from the spike use an in-process router.
use axum::{
    Router,
    body::{Body, to_bytes},
    extract::connect_info::MockConnectInfo,
    http::{Request, StatusCode},
    routing::{get, post},
};
use tower::ServiceExt;

#[derive(Default)]
struct Options {
    rates: RateLimits,
    proxies: Option<Vec<String>>,
}

#[derive(Clone)]
struct TestClock(Arc<std::sync::atomic::AtomicI64>);
impl TestClock {
    fn new() -> Self {
        Self(Arc::new(std::sync::atomic::AtomicI64::new(T0)))
    }
    fn add(&self, duration: Duration) {
        self.0
            .fetch_add(duration.as_nanos() as i64, Ordering::Relaxed);
    }
}

const HOUR_DURATION: Duration = Duration::from_secs(3600);
fn limit_app(
    env: &[(&str, &str)],
    change: impl FnOnce(&mut Options),
) -> (Arc<Limiter>, Router, TestClock) {
    let clock = TestClock::new();
    let mut options = Options::default();
    change(&mut options);
    let settings = parse_env(&|key| {
        env.iter()
            .find(|(k, _)| *k == key)
            .map(|(_, v)| v.to_string())
    })
    .unwrap();
    let (rates, clients) = resolve_rates(&options.rates, &settings).unwrap();
    let proxies = options.proxies.unwrap_or_else(|| {
        if settings.proxies.is_empty() {
            vec!["private".into()]
        } else {
            settings.proxies.split(',').map(str::to_owned).collect()
        }
    });
    let limiter = Arc::new(Limiter::new(rates, clients, 1));
    let ok = || async { StatusCode::NO_CONTENT };
    let router = Router::new()
        .route("/api/auth/register", post(ok))
        .route("/api/auth/login", post(ok))
        .route("/api/auth/reset/request", post(ok))
        .route("/api/auth/reset/confirm", post(ok))
        .route("/api/auth/oidc/{provider}/callback", get(ok))
        .route("/api/auth/oidc/{provider}/start", get(ok))
        .route("/api/session", get(ok));
    let now = clock.clone();
    let router = layer(
        router,
        limiter.clone(),
        TrustedProxies::parse(&proxies).unwrap(),
        Arc::new(move || now.0.load(Ordering::Relaxed)),
    );
    (limiter, router, clock)
}

struct Answer {
    status: StatusCode,
    headers: axum::http::HeaderMap,
    body: String,
}
impl Answer {
    fn header(&self, key: &str) -> &str {
        self.headers
            .get(key)
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default()
    }
}
async fn hit(app: &Router, method: &str, path: &str, remote: &str, xff: &[&str]) -> Answer {
    let mut request = Request::builder().method(method).uri(path);
    for value in xff {
        request = request.header("x-forwarded-for", *value);
    }
    let response = app
        .clone()
        .layer(MockConnectInfo(
            remote.parse::<std::net::SocketAddr>().unwrap(),
        ))
        .oneshot(request.body(Body::empty()).unwrap())
        .await
        .unwrap();
    let (parts, body) = response.into_parts();
    Answer {
        status: parts.status,
        headers: parts.headers,
        body: String::from_utf8(to_bytes(body, usize::MAX).await.unwrap().to_vec()).unwrap(),
    }
}
fn tight() -> RateLimits {
    let l = |b| Limit::new(b, HOUR_DURATION);
    RateLimits {
        login: l(2),
        register: l(3),
        reset_request: l(2),
        reset_confirm: l(2),
        oidc_callback: l(2),
        clients: 0,
    }
}

#[tokio::test]
async fn rate_limit_middleware() {
    let (_a, h, clk) = limit_app(&[], |o| o.rates = tight());
    const PROXY: &str = "172.18.0.4:5000";
    const LOGIN: &str = "/api/auth/login";

    // 429 body and headers.
    for _ in 0..2 {
        assert_eq!(
            hit(&h, "POST", LOGIN, PROXY, &["198.51.100.7"])
                .await
                .status,
            204
        );
    }
    let rec = hit(&h, "POST", LOGIN, PROXY, &["198.51.100.7"]).await;
    assert_eq!(rec.status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(rec.header("retry-after"), "3600");
    assert_eq!(rec.header("content-type"), "application/problem+json");
    assert_eq!(rec.header("cache-control"), "no-store");
    assert!(
        rec.body
            .contains("Too many sign-in attempts from your network. Try again in 3600 seconds.")
            && !rec.body.contains("198.51.100.7"),
        "{}",
        rec.body
    );

    // Another client is not affected.
    assert_eq!(
        hit(&h, "POST", LOGIN, PROXY, &["198.51.100.8"])
            .await
            .status,
        204
    );

    // A forged left entry does not change the client.
    assert_eq!(
        hit(&h, "POST", LOGIN, PROXY, &["10.0.0.9, 198.51.100.7"])
            .await
            .status,
        429
    );
    assert_eq!(
        hit(&h, "POST", LOGIN, PROXY, &["198.51.100.200, 198.51.100.7"])
            .await
            .status,
        429
    );

    // A direct client cannot choose its key.
    for _ in 0..2 {
        hit(&h, "POST", LOGIN, "203.0.113.5:4000", &["10.0.0.1"]).await;
    }
    assert_eq!(
        hit(&h, "POST", LOGIN, "203.0.113.5:4000", &["198.51.100.77"])
            .await
            .status,
        429
    );

    // Loopback without a header is never limited.
    for _ in 0..100 {
        assert_eq!(
            hit(&h, "POST", LOGIN, "127.0.0.1:5000", &[]).await.status,
            204
        );
    }

    // A proxy without a client address shares one large bucket.
    let mut passes = 0;
    for _ in 0..80 {
        if hit(&h, "POST", "/api/auth/reset/confirm", PROXY, &[])
            .await
            .status
            == 204
        {
            passes += 1;
        }
    }
    assert_eq!(passes, 50, "want the infrastructure burst 50");

    // Classes are separate.
    for path in [
        "/api/auth/register",
        "/api/auth/reset/request",
        "/api/auth/reset/confirm",
    ] {
        assert_eq!(
            hit(&h, "POST", path, PROXY, &["198.51.100.7"]).await.status,
            204,
            "{path}"
        );
    }
    hit(
        &h,
        "POST",
        "/api/auth/reset/request",
        PROXY,
        &["198.51.100.7"],
    )
    .await;
    assert_eq!(
        hit(
            &h,
            "POST",
            "/api/auth/reset/request",
            PROXY,
            &["198.51.100.7"]
        )
        .await
        .status,
        429
    );
    assert_eq!(
        hit(
            &h,
            "POST",
            "/api/auth/reset/confirm",
            PROXY,
            &["198.51.100.7"]
        )
        .await
        .status,
        204
    );

    // Routes without a limit.
    for _ in 0..20 {
        for path in ["/api/session", "/api/auth/oidc/google/start"] {
            assert_eq!(
                hit(&h, "GET", path, PROXY, &["198.51.100.7"]).await.status,
                204,
                "{path}"
            );
        }
    }

    // The OIDC callback redirects to the login page.
    let path = "/api/auth/oidc/google/callback?code=x&state=y";
    for _ in 0..2 {
        assert_eq!(
            hit(&h, "GET", path, PROXY, &["198.51.100.9"]).await.status,
            204
        );
    }
    let rec = hit(&h, "GET", path, PROXY, &["198.51.100.9"]).await;
    assert_eq!(rec.status, StatusCode::SEE_OTHER);
    assert_eq!(rec.header("location"), "/login?error=rate_limited");
    assert!(!rec.header("retry-after").is_empty());

    // A token returns after every.
    clk.add(HOUR_DURATION);
    assert_eq!(
        hit(&h, "POST", LOGIN, PROXY, &["198.51.100.7"])
            .await
            .status,
        204
    );
}

#[tokio::test]
async fn rate_limit_without_limiter() {
    let (a, h, _) = limit_app(&[("AUTH_RATE_LIMIT", "off")], |_| {});
    assert!(a.size() == 0);
    for _ in 0..100 {
        assert_eq!(
            hit(&h, "POST", "/api/auth/login", "203.0.113.5:4000", &[])
                .await
                .status,
            204
        );
    }
    // A code option wins over the environment.
    let (_, h, _) = limit_app(&[("AUTH_RATE_LIMIT", "off")], |o| {
        o.rates.login = Limit::new(1, HOUR_DURATION)
    });
    hit(&h, "POST", "/api/auth/login", "203.0.113.5:4000", &[]).await;
    assert_eq!(
        hit(&h, "POST", "/api/auth/login", "203.0.113.5:4000", &[])
            .await
            .status,
        429
    );
    assert_eq!(
        hit(&h, "POST", "/api/auth/register", "203.0.113.5:4000", &[])
            .await
            .status,
        204,
        "the class stays off"
    );
}

#[tokio::test]
async fn rate_limit_trusted_proxies() {
    let limit = |o: &mut Options| o.rates.login = Limit::new(1, HOUR_DURATION);
    // none limits loopback.
    let (_, h, _) = limit_app(&[], |o| {
        limit(o);
        o.proxies = Some(vec!["none".into()]);
    });
    hit(&h, "POST", "/api/auth/login", "127.0.0.1:1", &[]).await;
    assert_eq!(
        hit(&h, "POST", "/api/auth/login", "127.0.0.1:1", &[])
            .await
            .status,
        429
    );

    // From the environment.
    let (_, h, _) = limit_app(&[("AUTH_TRUSTED_PROXIES", "172.18.0.0/16")], limit);
    hit(
        &h,
        "POST",
        "/api/auth/login",
        "172.18.0.4:1",
        &["198.51.100.1"],
    )
    .await;
    assert_eq!(
        hit(
            &h,
            "POST",
            "/api/auth/login",
            "172.18.0.9:1",
            &["198.51.100.1"]
        )
        .await
        .status,
        429
    );
    // 10.0.0.2 is not trusted here, so its header is ignored.
    hit(
        &h,
        "POST",
        "/api/auth/login",
        "10.0.0.2:1",
        &["198.51.100.1"],
    )
    .await;
    assert_eq!(
        hit(
            &h,
            "POST",
            "/api/auth/login",
            "10.0.0.2:1",
            &["198.51.100.2"]
        )
        .await
        .status,
        429
    );

    // The option wins over the environment.
    let (_, h, _) = limit_app(&[("AUTH_TRUSTED_PROXIES", "none")], |o| {
        limit(o);
        o.proxies = Some(vec!["loopback".into()]);
    });
    for _ in 0..5 {
        assert_eq!(
            hit(&h, "POST", "/api/auth/login", "127.0.0.1:1", &[])
                .await
                .status,
            204
        );
    }
}

#[test]
fn new_ratelimit_errors() {
    for (key, value) in [
        ("AUTH_RATE_LIMIT_LOGIN", "burst=0"),
        ("AUTH_RATE_LIMIT", "perhaps"),
        ("AUTH_RATE_LIMIT_CLIENTS", "many"),
    ] {
        let result = parse_env(&|name| (name == key).then(|| value.to_owned()))
            .and_then(|settings| resolve_rates(&RateLimits::default(), &settings));
        let error = result.unwrap_err().to_string();
        assert!(error.starts_with("auth: cannot start: "), "{error}");
        assert!(error.contains(key) && error.contains("\nTry: "), "{error}");
    }
}

#[tokio::test]
async fn ratelimit_hash_busy_refunds_only_queue_refusals() {
    use axum::response::IntoResponse;
    let limiter = Arc::new(test_limiter(Limit::new(1, secs(3600)), 1024));
    let busy = Arc::new(std::sync::atomic::AtomicBool::new(true));
    let queue_busy = busy.clone();
    let router = Router::new().route(
        PATH_LOGIN,
        post(move || {
            let busy = queue_busy.clone();
            async move {
                if busy.load(Ordering::Relaxed) {
                    let mut response = StatusCode::SERVICE_UNAVAILABLE.into_response();
                    response.extensions_mut().insert(HashBusy);
                    response
                } else {
                    StatusCode::UNAUTHORIZED.into_response()
                }
            }
        }),
    );
    let router = layer(
        router,
        limiter,
        TrustedProxies::parse(&["none"]).unwrap(),
        Arc::new(|| T0),
    );
    for _ in 0..3 {
        assert_eq!(
            hit(&router, "POST", PATH_LOGIN, "192.0.2.1:1234", &[])
                .await
                .status,
            503
        );
    }
    busy.store(false, Ordering::Relaxed);
    assert_eq!(
        hit(&router, "POST", PATH_LOGIN, "192.0.2.1:1234", &[])
            .await
            .status,
        401
    );
    assert_eq!(
        hit(&router, "POST", PATH_LOGIN, "192.0.2.1:1234", &[])
            .await
            .status,
        429
    );
    assert_eq!(
        hit(&router, "POST", PATH_LOGIN, "192.0.2.9:1", &[])
            .await
            .status,
        401
    );
}

#[derive(Clone)]
struct LogCapture(Arc<std::sync::Mutex<Vec<u8>>>);
impl std::io::Write for LogCapture {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(bytes);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}
impl LogCapture {
    fn text(&self) -> String {
        String::from_utf8(self.0.lock().unwrap().clone()).unwrap()
    }
}
fn log_capture() -> (LogCapture, impl tracing::Subscriber + Send + Sync) {
    let capture = LogCapture(Arc::default());
    let writer = capture.clone();
    let subscriber = tracing_subscriber::fmt()
        .without_time()
        .with_ansi(false)
        .with_writer(move || writer.clone())
        .finish();
    (capture, subscriber)
}

#[test]
fn ratelimit_sweep_and_log() {
    let (capture, subscriber) = log_capture();
    tracing::subscriber::with_default(subscriber, || {
        let limiter = test_limiter(Limit::new(1, secs(60)), 1024);
        let who = v4("203.0.113.5");
        limiter.take(Class::Login, &who, T0);
        limiter.take(Class::Login, &who, T0);
        assert_eq!(limiter.sweep(T0 + 10 * MIN), 1);
        limiter.log_rejections();
        assert_eq!(limiter.rejected(Class::Login), 0);
        limiter.log_rejections();
    });
    let output = capture.text();
    assert_eq!(
        output
            .matches("rate limited requests in the last minute")
            .count(),
        1
    );
    assert!(
        output.contains("login=1") && output.contains("try_="),
        "{output}"
    );
    assert!(!output.contains("203.0.113.5"), "{output}");
}

#[tokio::test]
async fn ratelimit_warns_once_for_infrastructure() {
    use tracing::instrument::WithSubscriber;
    let (capture, subscriber) = log_capture();
    async {
        let (_, router, clock) = limit_app(&[], |_| {});
        for _ in 0..5 {
            hit(&router, "POST", PATH_LOGIN, "172.18.0.4:5000", &[]).await;
        }
        assert_eq!(capture.text().matches("share one rate limit").count(), 1);
        clock.add(secs(600));
        hit(&router, "POST", PATH_LOGIN, "172.18.0.4:5000", &[]).await;
    }
    .with_subscriber(subscriber)
    .await;
    let output = capture.text();
    assert_eq!(output.matches("share one rate limit").count(), 2);
    assert!(!output.contains("172.18.0.4"), "{output}");
}

#[tokio::test]
async fn ratelimit_refusal_rounding_and_auth_error_response() {
    use axum::response::IntoResponse;
    for (duration, seconds, unit) in [
        (Duration::ZERO, 1, "second"),
        (Duration::from_nanos(1), 1, "second"),
        (Duration::from_millis(1001), 2, "seconds"),
    ] {
        let (actual, detail) = too_many_detail(Class::ResetConfirm, duration);
        assert_eq!(actual, seconds);
        assert_eq!(
            detail,
            format!(
                "Too many password reset attempts from your network. Try again in {seconds} {unit}."
            )
        );
    }
    let error = spinup_kit::auth::error::AuthError("private configuration\nTry: correct it".into());
    assert_eq!(error.to_string(), "private configuration\nTry: correct it");
    let response = error.into_response();
    assert_eq!(response.status(), 500);
    assert_eq!(response.headers()["cache-control"], "no-store");
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    assert!(
        !String::from_utf8(body.to_vec())
            .unwrap()
            .contains("private configuration")
    );
}
