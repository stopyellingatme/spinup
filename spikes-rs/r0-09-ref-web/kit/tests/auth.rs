//! Database tests of kit auth: the session store and the cookie contract
//! (ADR 0008 §3), login, lockout, the hash queue, the cross-origin check,
//! and the CSP. They follow `kit/auth/auth_test.go`.

mod common;

use std::time::Duration;

use axum::http::StatusCode;
use common::*;
use kit::auth::password::{self, HashError};
use kit::auth::ratelimit::{Limit, RateLimits};
use sha2::{Digest, Sha256};
use sqlx::PgPool;

fn attrs(line: &str) -> Vec<String> {
    line.split(';')
        .skip(1)
        .map(|s| s.trim().to_owned())
        .collect()
}

#[sqlx::test(migrations = "tests/migrations")]
async fn register_sets_the_cookie_contract(pool: PgPool) {
    let f = fixture(pool.clone(), &[("AUTH_RATE_LIMIT", "off")], |_| {});
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/register").json(creds(" Person@Example.com ", GOOD)),
    )
    .await;
    assert_eq!(a.status, StatusCode::CREATED, "{}", a.body);
    assert_eq!(a.json()["user"]["email"], "person@example.com");
    assert_eq!(a.header("cache-control"), "no-store");
    let line = a.set_cookie("session").expect("Set-Cookie: session");
    let at = attrs(&line);
    // Name session, HttpOnly, SameSite=Lax, Path=/, persistent, and not Secure in dev.
    assert!(at.contains(&"HttpOnly".into()), "{line}");
    assert!(at.contains(&"SameSite=Lax".into()), "{line}");
    assert!(at.contains(&"Path=/".into()), "{line}");
    assert!(!at.contains(&"Secure".into()), "{line}");
    let max_age: i64 = at
        .iter()
        .find_map(|x| x.strip_prefix("Max-Age="))
        .expect("Max-Age")
        .parse()
        .unwrap();
    assert!((604_000..=604_800).contains(&max_age), "{line}");
    // The store keeps the SHA-256 of the cookie value, never the value.
    let value = a.session().unwrap();
    let (token, user_id): (String, Option<i64>) =
        sqlx::query_as("SELECT token, user_id FROM sessions")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(token, hex::encode(Sha256::digest(value.as_bytes())));
    assert_ne!(token, value);
    assert_eq!(user_id, a.json()["user"]["id"].as_i64());
}

#[sqlx::test(migrations = "tests/migrations")]
async fn the_cookie_is_secure_by_default(pool: PgPool) {
    let f = fixture(pool, &[("AUTH_RATE_LIMIT", "off")], |o| o.secure = true);
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/register").json(creds("a@example.com", GOOD)),
    )
    .await;
    let line = a.set_cookie("session").unwrap();
    assert!(attrs(&line).contains(&"Secure".into()), "{line}");
}

#[sqlx::test(migrations = "tests/migrations")]
async fn login_rotates_the_token_and_logout_ends_it(pool: PgPool) {
    let f = fixture(pool.clone(), &[("AUTH_RATE_LIMIT", "off")], |_| {});
    let first = register(&f.app, "a@example.com").await;
    let me = send(
        &f.app,
        Req::new("GET", "/api/me").cookie(format!("session={first}")),
    )
    .await;
    assert_eq!(me.status, StatusCode::OK, "{}", me.body);
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/login")
            .json(creds("a@example.com", GOOD))
            .cookie(format!("session={first}")),
    )
    .await;
    assert_eq!(a.status, StatusCode::OK, "{}", a.body);
    let second = a.session().expect("a new cookie");
    assert_ne!(first, second, "login must rotate the token");
    let old = send(
        &f.app,
        Req::new("GET", "/api/session").cookie(format!("session={first}")),
    )
    .await;
    assert_eq!(old.status, StatusCode::UNAUTHORIZED);
    assert_eq!(
        old.problem().detail.as_deref(),
        Some("No user is signed in.")
    );
    let now = send(
        &f.app,
        Req::new("GET", "/api/session").cookie(format!("session={second}")),
    )
    .await;
    assert_eq!(now.status, StatusCode::OK);
    assert_eq!(now.json()["user"]["email"], "a@example.com");

    let out = send(
        &f.app,
        Req::new("POST", "/api/auth/logout").cookie(format!("session={second}")),
    )
    .await;
    assert_eq!(out.status, StatusCode::NO_CONTENT);
    let removal = out.set_cookie("session").expect("a removal cookie");
    assert!(removal.contains("Max-Age=0"), "{removal}");
    let rows: i64 = sqlx::query_scalar("SELECT count(*) FROM sessions")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(rows, 0);
    let after = send(
        &f.app,
        Req::new("GET", "/api/me").cookie(format!("session={second}")),
    )
    .await;
    assert_eq!(after.status, StatusCode::UNAUTHORIZED);
}

#[sqlx::test(migrations = "tests/migrations")]
async fn register_validation_and_conflict(pool: PgPool) {
    let f = fixture(pool, &[("AUTH_RATE_LIMIT", "off")], |_| {});
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/register").json(creds("nope", "short")),
    )
    .await;
    assert_eq!(a.status, StatusCode::UNPROCESSABLE_ENTITY);
    let p = a.problem();
    assert_eq!(p.detail.as_deref(), Some("The request has invalid fields."));
    let fields: Vec<_> = p.errors.iter().map(|e| e.field.as_str()).collect();
    assert_eq!(fields, ["email", "password"]);
    assert_eq!(p.errors[1].message, "Use at least 12 characters.");

    register(&f.app, "dup@example.com").await;
    let again = send(
        &f.app,
        Req::new("POST", "/api/auth/register").json(creds("DUP@example.com", GOOD)),
    )
    .await;
    assert_eq!(again.status, StatusCode::CONFLICT);
    assert_eq!(
        again.problem().errors[0].message,
        "This email address is already registered."
    );

    let bad_type = send(
        &f.app,
        Req::new("POST", "/api/auth/register")
            .json(creds("a@example.com", GOOD))
            .header("content-type", "text/plain"),
    )
    .await;
    assert_eq!(bad_type.status, StatusCode::UNSUPPORTED_MEDIA_TYPE);
    for (body, detail) in [
        ("", "The request body must contain one JSON value."),
        (
            r#"{"email":"a@example.com","password":"x","admin":true}"#,
            "The request body must contain valid JSON with only known fields and correct value types.",
        ),
        (
            r#"{"email":"a@example.com"} {}"#,
            "The request body must contain only one JSON value.",
        ),
    ] {
        let a = send(&f.app, Req::new("POST", "/api/auth/register").json(body)).await;
        assert_eq!(a.status, StatusCode::BAD_REQUEST, "{body}");
        assert_eq!(a.problem().detail.as_deref(), Some(detail), "{body}");
    }
}

#[sqlx::test(migrations = "tests/migrations")]
async fn login_failures_look_the_same(pool: PgPool) {
    let f = fixture(pool, &[("AUTH_RATE_LIMIT", "off")], |_| {});
    register(&f.app, "a@example.com").await;
    for (email, pw) in [
        ("a@example.com", "wrong password 1"),
        ("b@example.com", GOOD),
    ] {
        let a = send(
            &f.app,
            Req::new("POST", "/api/auth/login").json(creds(email, pw)),
        )
        .await;
        assert_eq!(a.status, StatusCode::UNAUTHORIZED);
        assert_eq!(
            a.problem().detail.as_deref(),
            Some("The email address or password is incorrect.")
        );
        assert!(a.session().is_none());
    }
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/login").json(creds("a@example.com", "")),
    )
    .await;
    assert_eq!(a.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert_eq!(a.problem().errors[0].message, "Enter your password.");
}

#[sqlx::test(migrations = "tests/migrations")]
async fn lockout_and_recovery(pool: PgPool) {
    let f = fixture(pool, &[("AUTH_RATE_LIMIT", "off")], |_| {});
    register(&f.app, "a@example.com").await;
    let login = |pw: &'static str| {
        let app = f.app.clone();
        async move {
            send(
                &app,
                Req::new("POST", "/api/auth/login").json(creds("a@example.com", pw)),
            )
            .await
        }
    };
    for _ in 0..5 {
        assert_eq!(
            login("wrong password 1").await.status,
            StatusCode::UNAUTHORIZED
        );
    }
    let locked = login(GOOD).await;
    assert_eq!(locked.status, StatusCode::TOO_MANY_REQUESTS);
    let retry: i64 = locked.header("retry-after").parse().unwrap();
    assert!((1..=900).contains(&retry), "Retry-After {retry}");
    assert_eq!(
        locked.problem().detail.as_deref(),
        Some("Too many failed logins for this email address. Try again later.")
    );
    f.clock.add(Duration::from_secs(15 * 60 + 1));
    assert_eq!(login(GOOD).await.status, StatusCode::OK);
    // A successful login clears the count.
    for _ in 0..4 {
        login("wrong password 1").await;
    }
    assert_eq!(login(GOOD).await.status, StatusCode::OK);
}

#[sqlx::test(migrations = "tests/migrations")]
async fn a_503_from_the_hash_queue_gives_the_token_back(pool: PgPool) {
    let f = fixture(pool, &[], |o| {
        o.hash_concurrent = 1;
        o.hash_waiting = 0;
        o.hash_wait = Duration::from_secs(60);
        o.rates = RateLimits {
            login: Limit::new(1, Duration::from_secs(3600)),
            ..RateLimits::default()
        };
    });
    let held = f.auth.slots().admit().unwrap(); // the only place is taken
    let login = || {
        send(
            &f.app,
            Req::new("POST", "/api/auth/login")
                .json(creds("busy@example.com", GOOD))
                .remote("192.0.2.1:1234"),
        )
    };
    for i in 0..3 {
        let a = login().await;
        assert_eq!(a.status, StatusCode::SERVICE_UNAVAILABLE, "request {i}");
        assert_eq!(a.header("retry-after"), "1");
    }
    drop(held);
    assert_eq!(login().await.status, StatusCode::UNAUTHORIZED);
    assert_eq!(login().await.status, StatusCode::TOO_MANY_REQUESTS);
}

#[sqlx::test(migrations = "tests/migrations")]
async fn a_rate_limited_login_reaches_no_query(pool: PgPool) {
    let f = fixture(pool.clone(), &[], |o| {
        o.rates = RateLimits {
            login: Limit::new(2, Duration::from_secs(3600)),
            ..RateLimits::default()
        }
    });
    for i in 0..2 {
        let a = send(
            &f.app,
            Req::new("POST", "/api/auth/login").json(creds(&format!("user{i}@example.com"), GOOD)),
        )
        .await;
        assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    }
    let before: i64 = sqlx::query_scalar("SELECT count(*) FROM login_attempts")
        .fetch_one(&pool)
        .await
        .unwrap();
    // The same routes on a pool that cannot connect: a refused request
    // answers 429 with no database work, also with a bogus session cookie.
    let dead = kit::auth::Auth::new(lazy_pool(), {
        let mut o = options(&[], &f.clock);
        o.rates = RateLimits {
            login: Limit::new(2, Duration::from_secs(3600)),
            ..RateLimits::default()
        };
        o
    })
    .unwrap();
    let dead_app = app(&dead);
    let mut statuses = Vec::new();
    for i in 0..22 {
        let a = send(
            &dead_app,
            Req::new("POST", "/api/auth/login")
                .json(creds(&format!("user{i}@example.com"), GOOD))
                .cookie("session=AAAAAAAAAAAAAAAAAAAAAA"),
        )
        .await;
        statuses.push(a.status.as_u16());
    }
    // The first two reach the session store, which cannot connect (500).
    assert_eq!(&statuses[..2], &[500, 500]);
    assert!(statuses[2..].iter().all(|&s| s == 429), "{statuses:?}");
    let after: i64 = sqlx::query_scalar("SELECT count(*) FROM login_attempts")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(before, after);
    let other = send(
        &f.app,
        Req::new("POST", "/api/auth/login")
            .json(creds("user9@example.com", GOOD))
            .remote("192.0.2.9:1"),
    )
    .await;
    assert_eq!(other.status, StatusCode::UNAUTHORIZED);
    dead.close().await;
}

#[sqlx::test(migrations = "tests/migrations")]
async fn register_has_its_own_bucket(pool: PgPool) {
    let f = fixture(pool, &[], |o| {
        o.rates = RateLimits {
            register: Limit::new(1, Duration::from_secs(3600)),
            ..RateLimits::default()
        }
    });
    register(&f.app, "one@example.com").await;
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/register").json(creds("two@example.com", GOOD)),
    )
    .await;
    assert_eq!(a.status, StatusCode::TOO_MANY_REQUESTS);
    assert!(a.body.contains("sign-up attempts"), "{}", a.body);
    let login = send(
        &f.app,
        Req::new("POST", "/api/auth/login").json(creds("one@example.com", GOOD)),
    )
    .await;
    assert_eq!(login.status, StatusCode::OK);
}

#[sqlx::test(migrations = "tests/migrations")]
async fn cross_origin_login_is_refused(pool: PgPool) {
    let f = fixture(pool, &[("AUTH_RATE_LIMIT", "off")], |o| {
        o.origins = vec!["http://localhost:5173".into(), String::new()]
    });
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/login")
            .json(creds("a@example.com", GOOD))
            .header("sec-fetch-site", "cross-site"),
    )
    .await;
    assert_eq!(a.status, StatusCode::FORBIDDEN);
    assert_eq!(a.problem().status, 403);
    assert!(a.session().is_none());
    let trusted = send(
        &f.app,
        Req::new("POST", "/api/things").header("origin", "http://localhost:5173"),
    )
    .await;
    assert_eq!(trusted.status, StatusCode::CREATED);
}

#[sqlx::test(migrations = "tests/migrations")]
async fn default_csp(pool: PgPool) {
    let f = fixture(pool, &[("AUTH_RATE_LIMIT", "off")], |_| {});
    let a = send(&f.app, Req::new("GET", "/api/session")).await;
    assert_eq!(a.header("content-security-policy"), kit::auth::DEFAULT_CSP);
    let b = send(&f.app, Req::new("GET", "/api/framed")).await;
    assert_eq!(b.header("content-security-policy"), "default-src 'self'");
}

#[sqlx::test(migrations = "tests/migrations")]
async fn rehash_on_login(pool: PgPool) {
    let f = fixture(pool.clone(), &[("AUTH_RATE_LIMIT", "off")], |o| {
        o.params = password::Params {
            memory: 128,
            ..TEST_PARAMS
        }
    });
    let old = password::hash_password(&TEST_PARAMS, GOOD);
    sqlx::query("INSERT INTO users (email, password_hash) VALUES ('old@example.com', $1)")
        .bind(&old)
        .execute(&pool)
        .await
        .unwrap();
    let a = send(
        &f.app,
        Req::new("POST", "/api/auth/login").json(creds("old@example.com", GOOD)),
    )
    .await;
    assert_eq!(a.status, StatusCode::OK);
    let stored: String = sqlx::query_scalar("SELECT password_hash FROM users")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert!(stored.contains("m=128,"), "{stored}");
}

#[sqlx::test(migrations = "tests/migrations")]
async fn a_deleted_user_ends_the_session(pool: PgPool) {
    let f = fixture(pool.clone(), &[("AUTH_RATE_LIMIT", "off")], |_| {});
    let c = register(&f.app, "gone@example.com").await;
    sqlx::query("DELETE FROM users")
        .execute(&pool)
        .await
        .unwrap();
    let a = send(
        &f.app,
        Req::new("GET", "/api/me").cookie(format!("session={c}")),
    )
    .await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
}

#[sqlx::test(migrations = "tests/migrations")]
async fn the_middleware_is_required(pool: PgPool) {
    let auth = kit::auth::Auth::new(pool, options(&[], &TestClock::new(t0()))).unwrap();
    // The routes without auth.layer(): the user extractor answers 500, and
    // the session extractor of tower-sessions fails too.
    let bare = axum::Router::new().route(
        "/api/me",
        axum::routing::get(
            |kit::auth::CurrentUser(u): kit::auth::CurrentUser| async move { axum::Json(u) },
        ),
    );
    let a = send(&bare, Req::new("GET", "/api/me")).await;
    assert_eq!(a.status, StatusCode::INTERNAL_SERVER_ERROR);
    let b = send(&auth.routes(), Req::new("GET", "/api/session")).await;
    assert_eq!(b.status, StatusCode::INTERNAL_SERVER_ERROR);
    auth.close().await;
}

#[tokio::test]
async fn hash_slots_answer_busy() {
    let slots = password::HashSlots::new(1, 0, Duration::from_millis(20));
    let _p = slots.admit().unwrap();
    assert_eq!(slots.run(|| ()).await.unwrap_err(), HashError::Busy);
}
