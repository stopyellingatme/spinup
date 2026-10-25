//! Password reset tests (ADR 0013 §2), as `kit/auth/reset_test.go`. Mail
//! goes to the lettre file transport or to a local SMTP stub. Mailpit is
//! not available offline.

mod common;

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use axum::Router;
use axum::http::StatusCode;
use common::*;
use kit::auth::Auth;
use kit::auth::reset::ResetOptions;
use kit::mail::{FileMailer, Mailer, SmtpMailer, smtp_stub};
use sqlx::PgPool;

fn reset_app(pool: PgPool, mailer: Arc<dyn Mailer>) -> (Auth, Router, TestClock) {
    let clock = TestClock::new(time::OffsetDateTime::now_utc());
    let auth = Auth::new(pool, options(&[("AUTH_RATE_LIMIT", "off")], &clock)).unwrap();
    let reset = auth
        .reset_routes(ResetOptions {
            mailer,
            from: "app@example.com".into(),
            base_url: "https://app.example/reset/".into(),
        })
        .unwrap();
    let app = kit::app::router(auth.layer(auth.routes().merge(reset)), None);
    (auth, app, clock)
}

fn temp_dir() -> PathBuf {
    let d = std::env::temp_dir().join(format!("kit-reset-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn mails(dir: &Path) -> Vec<String> {
    let mut out: Vec<String> = std::fs::read_dir(dir)
        .unwrap()
        .map(|e| std::fs::read_to_string(e.unwrap().path()).unwrap())
        .collect();
    out.sort();
    out
}

/// Waits for `n` mails and returns the token of the last link.
async fn wait_for_token(read: impl Fn() -> Vec<String>, n: usize) -> String {
    for _ in 0..400 {
        let m = read();
        if m.len() >= n {
            let body = m.last().unwrap().replace("=\r\n", "").replace("=\n", "");
            let link = body
                .split_whitespace()
                .find(|w| w.starts_with("https://app.example/reset/"))
                .expect("a reset link");
            return link
                .trim_start_matches("https://app.example/reset/")
                .to_owned();
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("no mail after 4 s");
}

async fn request(app: &Router, email: &str) -> StatusCode {
    send(
        app,
        Req::new("POST", "/api/auth/reset/request")
            .json(serde_json::json!({"email": email}).to_string()),
    )
    .await
    .status
}

async fn confirm(app: &Router, token: &str, password: &str, cookie: Option<&str>) -> Answer {
    let mut r = Req::new("POST", "/api/auth/reset/confirm")
        .json(serde_json::json!({"token": token, "password": password}).to_string());
    if let Some(c) = cookie {
        r = r.cookie(format!("session={c}"));
    }
    send(app, r).await
}

#[sqlx::test(migrations = "tests/migrations")]
async fn reset_single_use_and_sessions(pool: PgPool) {
    let dir = temp_dir();
    let (auth, app, _) = reset_app(pool.clone(), Arc::new(FileMailer::new(&dir)));
    let session = register(&app, "a@example.com").await;
    let other = send(
        &app,
        Req::new("POST", "/api/auth/login").json(creds("a@example.com", GOOD)),
    )
    .await
    .session()
    .unwrap();
    // Unknown, malformed, and real requests all answer 202. Only the real one mails.
    assert_eq!(
        request(&app, "nobody@example.com").await,
        StatusCode::ACCEPTED
    );
    let bad = send(&app, Req::new("POST", "/api/auth/reset/request").json("{")).await;
    assert_eq!(bad.status, StatusCode::ACCEPTED);
    assert_eq!(request(&app, "A@Example.com").await, StatusCode::ACCEPTED);
    let token = wait_for_token(|| mails(&dir), 1).await;
    let mail = &mails(&dir)[0];
    assert!(mail.contains("Subject: Reset your password"), "{mail}");
    assert!(mail.contains("To: a@example.com"), "{mail}");
    assert!(!mail.contains(GOOD));

    let short = confirm(&app, &token, "short", None).await;
    assert_eq!(short.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert_eq!(
        short.problem().errors[0].message,
        "Use between 12 and 512 characters."
    );
    let garbage = confirm(&app, "not-a-token", "a new long password", None).await;
    assert_eq!(garbage.status, StatusCode::UNPROCESSABLE_ENTITY);

    let done = confirm(&app, &token, "a new long password", Some(&session)).await;
    assert_eq!(done.status, StatusCode::NO_CONTENT, "{}", done.body);
    assert!(done.set_cookie("session").unwrap().contains("Max-Age=0"));
    // Every session of the user ended.
    for c in [&session, &other] {
        let me = send(
            &app,
            Req::new("GET", "/api/session").cookie(format!("session={c}")),
        )
        .await;
        assert_eq!(me.status, StatusCode::UNAUTHORIZED);
    }
    let again = confirm(&app, &token, "another long password", None).await;
    assert_eq!(
        again.status,
        StatusCode::UNPROCESSABLE_ENTITY,
        "a token works once"
    );
    assert_eq!(
        again.problem().detail.as_deref(),
        Some("The reset link is invalid or expired. Request a new link.")
    );
    let old = send(
        &app,
        Req::new("POST", "/api/auth/login").json(creds("a@example.com", GOOD)),
    )
    .await;
    assert_eq!(old.status, StatusCode::UNAUTHORIZED);
    let new = send(
        &app,
        Req::new("POST", "/api/auth/login").json(creds("a@example.com", "a new long password")),
    )
    .await;
    assert_eq!(new.status, StatusCode::OK);
    assert_eq!(
        mails(&dir).len(),
        1,
        "no mail for unknown or malformed requests"
    );
    auth.close().await;
    std::fs::remove_dir_all(&dir).unwrap();
}

#[sqlx::test(migrations = "tests/migrations")]
async fn reset_expires(pool: PgPool) {
    let dir = temp_dir();
    let (auth, app, clock) = reset_app(pool, Arc::new(FileMailer::new(&dir)));
    register(&app, "a@example.com").await;
    request(&app, "a@example.com").await;
    let token = wait_for_token(|| mails(&dir), 1).await;
    clock.add(Duration::from_secs(3600));
    let a = confirm(&app, &token, "a new long password", None).await;
    assert_eq!(a.status, StatusCode::UNPROCESSABLE_ENTITY);
    auth.close().await;
    std::fs::remove_dir_all(&dir).unwrap();
}

#[sqlx::test(migrations = "tests/migrations")]
async fn reset_requests_are_limited_per_email(pool: PgPool) {
    let dir = temp_dir();
    let (auth, app, _) = reset_app(pool, Arc::new(FileMailer::new(&dir)));
    register(&app, "a@example.com").await;
    for _ in 0..8 {
        assert_eq!(request(&app, "a@example.com").await, StatusCode::ACCEPTED);
    }
    wait_for_token(|| mails(&dir), 5).await;
    tokio::time::sleep(Duration::from_millis(100)).await;
    assert_eq!(
        mails(&dir).len(),
        5,
        "the lockout threshold limits mails for one email"
    );
    auth.close().await;
    std::fs::remove_dir_all(&dir).unwrap();
}

#[sqlx::test(migrations = "tests/migrations")]
async fn reset_through_smtp(pool: PgPool) {
    let (port, inbox) = smtp_stub::start().await;
    let mailer = SmtpMailer::from_url(&format!("smtp://127.0.0.1:{port}")).unwrap();
    let (auth, app, _) = reset_app(pool, Arc::new(mailer));
    register(&app, "a@example.com").await;
    request(&app, "a@example.com").await;
    let token = wait_for_token(|| inbox.lock().unwrap().clone(), 1).await;
    let a = confirm(&app, &token, "a new long password", None).await;
    assert_eq!(a.status, StatusCode::NO_CONTENT);
    auth.close().await;
}

#[tokio::test]
async fn reset_routes_check_their_settings() {
    let clock = TestClock::new(t0());
    let auth = Auth::new(lazy_pool(), options(&[], &clock)).unwrap();
    let mailer: Arc<dyn Mailer> = Arc::new(FileMailer::new(std::env::temp_dir()));
    for (from, url) in [
        ("app@example.com", "ftp://app.example/reset"),
        ("app@example.com", "https://app.example/reset?x=1"),
        ("not an address", "https://app.example/reset"),
    ] {
        let err = auth
            .reset_routes(ResetOptions {
                mailer: mailer.clone(),
                from: from.into(),
                base_url: url.into(),
            })
            .err()
            .unwrap()
            .to_string();
        assert!(err.contains("\nTry: "), "{err}");
    }
    auth.close().await;
}
