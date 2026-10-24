//! The HTTP tests of the rate limit (ADR 0016): TestRateLimitMiddleware,
//! TestRateLimitWithoutLimiter, TestRateLimitTrustedProxies, and
//! TestNewRateLimitErrors of `kit/auth/ratelimit_test.go`, with the Go
//! values unchanged. Stub handlers answer 204. No request reaches a database.

mod common;

use std::time::Duration;

use axum::Router;
use axum::http::StatusCode;
use axum::routing::{get, post};
use common::*;
use kit::auth::ratelimit::{Limit, RateLimits};
use kit::auth::{Auth, Options};

const HOUR: Duration = Duration::from_secs(3600);

fn limit_app(env: &[(&str, &str)], change: impl FnOnce(&mut Options)) -> (Auth, Router, TestClock) {
    let clock = TestClock::new(t0());
    let mut o = options(env, &clock);
    change(&mut o);
    let auth = Auth::new(lazy_pool(), o).unwrap();
    let ok = || async { StatusCode::NO_CONTENT };
    let mux = Router::new()
        .route("/api/auth/register", post(ok))
        .route("/api/auth/login", post(ok))
        .route("/api/auth/reset/request", post(ok))
        .route("/api/auth/reset/confirm", post(ok))
        .route("/api/auth/oidc/{provider}/callback", get(ok))
        .route("/api/auth/oidc/{provider}/start", get(ok))
        .route("/api/session", get(ok));
    let app = kit::app::router(auth.layer(mux), None);
    (auth, app, clock)
}

async fn hit(app: &Router, method: &str, path: &str, remote: &str, xff: &[&str]) -> Answer {
    let mut r = Req::new(method, path).remote(remote);
    for v in xff {
        r = r.header("x-forwarded-for", v);
    }
    send(app, r).await
}

fn tight() -> RateLimits {
    let l = |b| Limit::new(b, HOUR);
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
    clk.add(HOUR);
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
    assert!(a.limiter().is_none());
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
        o.rates.login = Limit::new(1, HOUR)
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
    let limit = |o: &mut Options| o.rates.login = Limit::new(1, HOUR);
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

    // An invalid item stops the start.
    let clock = TestClock::new(t0());
    let err = Auth::new(
        lazy_pool(),
        options(&[("AUTH_TRUSTED_PROXIES", "private,10.0.0.0/33")], &clock),
    )
    .err()
    .unwrap()
    .to_string();
    assert!(
        err.contains("10.0.0.0/33") && err.contains("\nTry: "),
        "{err}"
    );
}

#[tokio::test]
async fn new_rate_limit_errors() {
    let clock = TestClock::new(t0());
    for env in [
        [("AUTH_RATE_LIMIT_LOGIN", "burst=0")],
        [("AUTH_RATE_LIMIT", "perhaps")],
        [("AUTH_RATE_LIMIT_CLIENTS", "many")],
    ] {
        let err = Auth::new(lazy_pool(), options(&env, &clock))
            .err()
            .unwrap_or_else(|| panic!("{env:?}: New succeeded"))
            .to_string();
        assert!(
            err.starts_with("auth: cannot start: ") && err.contains("\nTry: "),
            "{env:?}: {err}"
        );
    }
}

#[tokio::test]
async fn sweep_runs_and_counts_refusals() {
    let (a, h, clk) = limit_app(&[], |o| {
        o.rates.login = Limit::new(1, Duration::from_secs(60));
        o.sweep = Duration::from_millis(5);
    });
    hit(&h, "POST", "/api/auth/login", "203.0.113.5:4000", &[]).await;
    hit(&h, "POST", "/api/auth/login", "203.0.113.5:4000", &[]).await; // refused
    clk.add(Duration::from_secs(600));
    let l = a.limiter().unwrap();
    for _ in 0..1000 {
        if l.size() == 0 && l.rejected(kit::auth::ratelimit::Class::Login) == 0 {
            a.close().await;
            return;
        }
        tokio::time::sleep(Duration::from_millis(5)).await;
    }
    panic!("the sweep did not run: {} entries", l.size());
}
