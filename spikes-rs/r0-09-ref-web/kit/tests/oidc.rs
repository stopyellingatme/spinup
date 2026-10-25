//! OIDC tests against the local fake issuer, as `kit/auth/oidc_test.go`.
//! The token and claim cases are the shared tables of
//! `kit::auth::fake_issuer`; `oidc-compare` runs the same tables with
//! openidconnect.

mod common;

use std::time::Duration;

use axum::http::StatusCode;
use common::*;
use kit::auth::fake_issuer::{
    CLIENT_ID, CLIENT_SECRET, EMAIL_VERIFIED_CASES, FakeIssuer, TOKEN_CASES,
};
use kit::auth::oidc::{self, Identity, ProviderConfig};
use sqlx::PgPool;

fn http() -> reqwest::Client {
    reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .build()
        .unwrap()
}

fn provider(issuer: &str) -> ProviderConfig {
    ProviderConfig {
        issuer: issuer.into(),
        client_id: CLIENT_ID.into(),
        secret: Some(CLIENT_SECRET.into()),
        redirect_uri: "http://app.test/api/auth/oidc/test/callback".into(),
        scopes: vec!["openid".into(), "email".into(), "profile".into()],
    }
}

/// One sign-in with oauth2 + jsonwebtoken: discovery, authorization, code
/// exchange with PKCE, and ID token verification.
async fn sign_in(fake: &FakeIssuer) -> Result<Identity, String> {
    let http = http();
    let p = provider(fake.url());
    let d = oidc::discover(&http, fake.url())
        .await
        .map_err(|e| e.to_string())?;
    let (state, nonce, verifier) = ("state-1", "nonce-1", "v".repeat(43));
    let url = oidc::authorize_url(&d, &p, state, nonce, &verifier).map_err(|e| e.to_string())?;
    let callback = fake.authorize(&url).await?;
    let q = oidc::query_map(&callback);
    assert_eq!(q["state"], state);
    let raw = oidc::exchange(&http, &d, &p, &q["code"], &verifier)
        .await
        .map_err(|e| e.to_string())?;
    oidc::verify_id_token(&http, &d, &p, &raw, nonce)
        .await
        .map_err(|e| e.to_string())
}

#[tokio::test]
async fn token_cases() {
    for (name, mutate, want) in TOKEN_CASES {
        let fake = FakeIssuer::start().await;
        if let Some(m) = mutate {
            fake.set(*m);
        }
        let got = sign_in(&fake).await;
        let ok = got.as_ref().is_ok_and(|id| id.email_verified);
        assert_eq!(ok, *want, "{name}: {got:?}");
        if *want {
            let id = got.unwrap();
            assert_eq!(id.subject, "subject-1");
            assert_eq!(id.email, "person@example.com");
        }
    }
}

#[tokio::test]
async fn email_verified_cases() {
    for (name, raw, want) in EMAIL_VERIFIED_CASES {
        let fake = FakeIssuer::start().await;
        let v: serde_json::Value = serde_json::from_str(raw).unwrap();
        fake.set(move |k| k.email_verified = Some(v));
        let got = sign_in(&fake).await;
        let ok = got.as_ref().is_ok_and(|id| id.email_verified);
        assert_eq!(ok, *want, "{name}: {got:?}");
    }
}

/// An app with the OIDC routes for the fake provider `test`.
fn oidc_app(pool: PgPool, fake: &FakeIssuer, clock: &TestClock) -> (kit::auth::Auth, axum::Router) {
    let issuer = fake.url().to_owned();
    let env = [
        ("AUTH_RATE_LIMIT", "off"),
        ("AUTH_OIDC_PROVIDERS", "test"),
        ("PUBLIC_ORIGIN", "http://app.test"),
        ("AUTH_OIDC_TEST_ISSUER", issuer.as_str()),
        ("AUTH_OIDC_TEST_CLIENT_ID", CLIENT_ID),
        ("AUTH_OIDC_TEST_CLIENT_SECRET", CLIENT_SECRET),
        ("AUTH_OIDC_TEST_LABEL", "Test provider"),
    ];
    let auth = kit::auth::Auth::new(pool, options(&env, clock)).unwrap();
    let api = auth.routes().merge(auth.oidc_routes_with(http()));
    let app = kit::app::router(auth.layer(api), None);
    (auth, app)
}

/// Starts a flow: the flow cookie and the callback path with code and state.
async fn start(app: &axum::Router, fake: &FakeIssuer, next: &str) -> (String, String) {
    let path = format!(
        "/api/auth/oidc/test/start?next={}",
        form_urlencoded::byte_serialize(next.as_bytes()).collect::<String>()
    );
    let a = send(app, Req::new("GET", &path)).await;
    assert_eq!(a.status, StatusCode::SEE_OTHER, "{}", a.body);
    let line = a.set_cookie("oidc_test").expect("a flow cookie");
    for want in [
        "HttpOnly",
        "SameSite=Lax",
        "Max-Age=600",
        "Path=/api/auth/oidc/test/",
    ] {
        assert!(line.contains(want), "{line}");
    }
    let cookie = line.split(';').next().unwrap().to_owned();
    let callback = fake.authorize(a.header("location")).await.unwrap();
    let callback = callback.strip_prefix("http://app.test").unwrap().to_owned();
    (cookie, callback)
}

/// A change of the flow cookie, the callback path, or the clock.
type FlowMutation = fn(&mut String, &mut String, &TestClock);

#[sqlx::test(migrations = "tests/migrations")]
async fn oidc_flow(pool: PgPool) {
    let fake = FakeIssuer::start().await;
    let clock = TestClock::new(time::OffsetDateTime::now_utc());
    let (auth, app) = oidc_app(pool.clone(), &fake, &clock);

    let providers = send(&app, Req::new("GET", "/api/auth/oidc/providers")).await;
    assert_eq!(
        providers.json(),
        serde_json::json!([{"name": "test", "label": "Test provider"}])
    );

    // Success: a linked user, a session, and the return path.
    let (cookie, callback) = start(&app, &fake, "/account?tab=profile").await;
    let ok = send(&app, Req::new("GET", &callback).cookie(cookie.clone())).await;
    assert_eq!(ok.status, StatusCode::SEE_OTHER, "{}", ok.body);
    assert_eq!(ok.header("location"), "/account?tab=profile");
    let session = ok.session().expect("a session");
    assert!(ok.set_cookie("oidc_test").unwrap().contains("Max-Age=0"));
    let me = send(
        &app,
        Req::new("GET", "/api/session").cookie(format!("session={session}")),
    )
    .await;
    assert_eq!(me.json()["user"]["email"], "person@example.com");
    // A replayed code fails.
    let replay = send(&app, Req::new("GET", &callback).cookie(cookie)).await;
    assert_eq!(replay.status, StatusCode::BAD_REQUEST);

    // The flow cases of the kit, not of the token.
    let cases: Vec<(&str, FlowMutation)> = vec![
        ("state mismatch", |_, cb, _| {
            *cb = cb.replace("state=", "state=wrong")
        }),
        ("tampered cookie", |c, _, _| *c = "oidc_test=invalid".into()),
        ("no cookie", |c, _, _| c.clear()),
        ("expired cookie", |_, _, clk| {
            clk.add(kit::auth::oidc::OIDC_LIFETIME)
        }),
        ("provider error", |_, cb, _| {
            cb.push_str("&error=access_denied")
        }),
        ("two codes", |_, cb, _| cb.push_str("&code=again")),
    ];
    for (name, mutate) in cases {
        let (mut cookie, mut callback) = start(&app, &fake, "/account").await;
        mutate(&mut cookie, &mut callback, &clock);
        let mut r = Req::new("GET", &callback);
        if !cookie.is_empty() {
            r = r.cookie(cookie.clone());
        }
        let a = send(&app, r).await;
        assert_eq!(a.status, StatusCode::BAD_REQUEST, "{name}: {}", a.body);
        assert!(a.session().is_none(), "{name}");
        assert!(a.body.contains("\\nTry: start sign-in again"), "{name}");
        clock.add(Duration::ZERO);
    }
    // Discovery runs once for the provider.
    assert_eq!(fake.discoveries(), 1);
    let users: i64 = sqlx::query_scalar("SELECT count(*) FROM users")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(users, 1);
    auth.close().await;
}

#[sqlx::test(migrations = "tests/migrations")]
async fn open_redirect_and_provider_down(pool: PgPool) {
    let fake = FakeIssuer::start().await;
    let clock = TestClock::new(time::OffsetDateTime::now_utc());
    let (_auth, app) = oidc_app(pool, &fake, &clock);
    for next in [
        "//evil.test",
        "https://evil.test",
        "/\\evil.test",
        "/%2fevil.test",
    ] {
        let path = format!(
            "/api/auth/oidc/test/start?next={}",
            form_urlencoded::byte_serialize(next.as_bytes()).collect::<String>()
        );
        let a = send(&app, Req::new("GET", &path)).await;
        assert_eq!(a.status, StatusCode::BAD_REQUEST, "{next}");
    }
    assert_eq!(
        fake.discoveries(),
        0,
        "redirect validation performed discovery"
    );
    let unknown = send(&app, Req::new("GET", "/api/auth/oidc/other/start")).await;
    assert_eq!(unknown.status, StatusCode::NOT_FOUND);
    // A down provider fails only its own routes, with a Try line.
    fake.close();
    tokio::time::sleep(Duration::from_millis(20)).await;
    let a = send(&app, Req::new("GET", "/api/auth/oidc/test/start")).await;
    assert_eq!(a.status, StatusCode::SERVICE_UNAVAILABLE, "{}", a.body);
    assert!(a.body.contains("Try:"));
}

#[sqlx::test(migrations = "tests/migrations")]
async fn configuration_errors_answer_503(pool: PgPool) {
    let clock = TestClock::new(t0());
    let env = [
        ("AUTH_RATE_LIMIT", "off"),
        ("AUTH_OIDC_PROVIDERS", "test,Bad"),
        ("AUTH_OIDC_TEST_ISSUER", "https://issuer.test"),
        ("AUTH_OIDC_TEST_CLIENT_ID", "x"),
    ];
    let auth = kit::auth::Auth::new(pool, options(&env, &clock)).unwrap();
    let app = kit::app::router(auth.layer(auth.oidc_routes_with(http())), None);
    let a = send(&app, Req::new("GET", "/api/auth/oidc/test/start")).await;
    assert_eq!(a.status, StatusCode::SERVICE_UNAVAILABLE);
}

#[sqlx::test(migrations = "tests/migrations")]
async fn linking_an_existing_account(pool: PgPool) {
    let fake = FakeIssuer::start().await;
    let clock = TestClock::new(time::OffsetDateTime::now_utc());
    let (auth, app) = oidc_app(pool.clone(), &fake, &clock);
    // An account with a password and a session exists for the email.
    let old = register(&app, "person@example.com").await;
    let (cookie, callback) = start(&app, &fake, "/account").await;
    let a = send(&app, Req::new("GET", &callback).cookie(cookie)).await;
    assert_eq!(a.status, StatusCode::SEE_OTHER, "{}", a.body);
    let (hash, sessions): (Option<String>, i64) =
        sqlx::query_as("SELECT password_hash, (SELECT count(*) FROM sessions) FROM users")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert!(hash.is_none(), "the password must end");
    assert_eq!(sessions, 1, "only the new session remains");
    let gone = send(
        &app,
        Req::new("GET", "/api/session").cookie(format!("session={old}")),
    )
    .await;
    assert_eq!(gone.status, StatusCode::UNAUTHORIZED);
    // A password login for the account now fails like an unknown account.
    let login = send(
        &app,
        Req::new("POST", "/api/auth/login").json(creds("person@example.com", GOOD)),
    )
    .await;
    assert_eq!(login.status, StatusCode::UNAUTHORIZED);
    // The known identity signs in to the same account.
    let (cookie, callback) = start(&app, &fake, "/account").await;
    let again = send(&app, Req::new("GET", &callback).cookie(cookie)).await;
    assert_eq!(again.status, StatusCode::SEE_OTHER);
    let ids: i64 = sqlx::query_scalar("SELECT count(*) FROM user_identities")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(ids, 1);
    auth.close().await;
}
