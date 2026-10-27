//! The tests of the password reset (Go `kit/auth/reset_test.go`), plus the R0.7 spike
//! tests of the settings and of a delivery through SMTP. A test that needs Postgres starts
//! with `pg_` and uses `#[sqlx::test]` with the migrations of `tests/it/migrations`.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Router;
use axum::http::StatusCode;
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD as B64URL;
use sha2::{Digest, Sha256};
use spinup_kit::auth::password::{MAX_PASSWORD_LENGTH, Params};
use spinup_kit::auth::reset::{self, ResetOptions};
use spinup_kit::auth::{
    Auth, COOKIE_NAME, DEFAULT_LOCKOUT_THRESHOLD, DEFAULT_LOCKOUT_WINDOW, Options,
};
use spinup_kit::mail::stub::SmtpStub;
use spinup_kit::mail::{Capture, Config, Mailer, Message, Templates, async_trait};
use spinup_kit::testkit::http::{Answer, Req, send};
use sqlx::PgPool;
use time::OffsetDateTime;
use tokio::sync::Notify;

const GOOD: &str = "correct horse battery";
const NEW: &str = "new correct password";
const LINK: &str = "https://app.example/reset/";

/// Cheap argon2id parameters.
const TEST_PARAMS: Params = Params {
    memory: 64,
    time: 1,
    threads: 1,
    salt_len: 16,
    key_len: 32,
};

/// A clock that tests move.
#[derive(Clone)]
struct TestClock(Arc<Mutex<OffsetDateTime>>);

impl TestClock {
    /// 2026-10-15 12:00:00 UTC, the start of the Go fixture clock.
    fn new() -> Self {
        Self(Arc::new(Mutex::new(
            time::macros::datetime!(2026-10-15 12:00 UTC),
        )))
    }
    fn add(&self, d: Duration) {
        *self.0.lock().unwrap() += d;
    }
    fn now(&self) -> OffsetDateTime {
        *self.0.lock().unwrap()
    }
}

/// Options for tests: no rate limit, no Secure cookie, test parameters, and no cleanup.
fn options(clock: &TestClock) -> Options {
    let env: HashMap<String, String> = [("AUTH_RATE_LIMIT".to_owned(), "off".to_owned())].into();
    let c = clock.clone();
    Options {
        secure: false,
        params: TEST_PARAMS,
        lookup: Arc::new(move |k| env.get(k).cloned()),
        now: Arc::new(move || c.now()),
        cleanup: Duration::ZERO,
        ..Options::default()
    }
}

/// A pool that never connects.
fn lazy_pool() -> PgPool {
    sqlx::postgres::PgPoolOptions::new()
        .acquire_timeout(Duration::from_millis(300))
        .connect_lazy("postgres://nobody@127.0.0.1:1/none")
        .unwrap()
}

fn reset_options(mailer: Arc<dyn Mailer>) -> ResetOptions {
    ResetOptions {
        mailer: Some(mailer),
        from: "app@example.com".into(),
        base_url: "https://app.example/reset/".into(),
        templates: None,
    }
}

struct Fixture {
    auth: Auth,
    app: Router,
    clock: TestClock,
}

/// The app of Go `resetFixture`: the auth routes and the reset routes inside the auth
/// layer.
fn fixture(pool: PgPool, o: ResetOptions) -> Fixture {
    let clock = TestClock::new();
    let auth = Auth::new(pool, options(&clock)).unwrap();
    let resets = reset::routes(&auth, o).unwrap();
    let app = auth
        .layer(auth.routes().merge(resets))
        .layer(axum::middleware::from_fn(spinup_kit::httpx::request_id));
    Fixture { auth, app, clock }
}

fn creds(email: &str, password: &str) -> String {
    serde_json::json!({"email": email, "password": password}).to_string()
}

fn cookie(token: &str) -> String {
    format!("{COOKIE_NAME}={token}")
}

async fn register(app: &Router, email: &str) -> String {
    let a = send(
        app,
        Req::post("/api/auth/register").json(creds(email, GOOD)),
    )
    .await;
    assert_eq!(
        a.status,
        StatusCode::CREATED,
        "register {email}: {}",
        a.body
    );
    a.cookie_value(COOKIE_NAME).expect("a session cookie")
}

async fn login(app: &Router, email: &str, pw: &str) -> Answer {
    send(app, Req::post("/api/auth/login").json(creds(email, pw))).await
}

async fn request(app: &Router, email: &str) -> Answer {
    send(
        app,
        Req::post("/api/auth/reset/request").json(serde_json::json!({"email": email}).to_string()),
    )
    .await
}

async fn confirm(app: &Router, token: &str, password: &str, session: Option<&str>) -> Answer {
    let mut r = Req::post("/api/auth/reset/confirm")
        .json(serde_json::json!({"token": token, "password": password}).to_string());
    if let Some(s) = session {
        r = r.cookie(cookie(s));
    }
    send(app, r).await
}

async fn session_status(app: &Router, token: &str) -> StatusCode {
    send(app, Req::get("/api/session").cookie(cookie(token)))
        .await
        .status
}

/// Finds the token of the trusted reset link in a text.
fn token_in(text: &str) -> Option<String> {
    text.split_whitespace()
        .find_map(|w| w.strip_prefix(LINK))
        .map(str::to_owned)
}

/// Waits for the first captured message and returns its token (Go `waitResetToken`).
async fn wait_token(c: &Capture) -> String {
    for _ in 0..500 {
        if let Some(m) = c.messages().first() {
            return token_in(&m.text).expect("the message has the trusted reset URL");
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("reset delivery did not finish");
}

async fn count(pool: &PgPool, sql: &str) -> i64 {
    sqlx::query_scalar(sqlx::AssertSqlSafe(sql.to_owned()))
        .fetch_one(pool)
        .await
        .unwrap()
}

// TestResetRoutesWithoutMailer.
#[tokio::test]
async fn reset_routes_without_mailer() {
    let clock = TestClock::new();
    let auth = Auth::new(lazy_pool(), options(&clock)).unwrap();
    let app = reset::routes(&auth, ResetOptions::default()).unwrap();
    for path in ["/api/auth/reset/request", "/api/auth/reset/confirm"] {
        let a = send(&app, Req::post(path).json("{}")).await;
        assert_eq!(a.status, StatusCode::NOT_FOUND, "{path}");
    }
    auth.close().await;
}

// The settings checks of Go `ResetRoutes`.
#[tokio::test]
async fn reset_routes_check_their_settings() {
    let clock = TestClock::new();
    let auth = Auth::new(lazy_pool(), options(&clock)).unwrap();
    let mailer: Arc<dyn Mailer> = Arc::new(Capture::new());
    let broken = Templates::new([("password_reset.subject.tmpl", "Reset")]);
    for (from, url, templates) in [
        ("app@example.com", "ftp://app.example/reset", None),
        ("app@example.com", "https://app.example/reset?x=1", None),
        ("app@example.com", "https://user@app.example/reset", None),
        ("app@example.com", "/reset", None),
        ("not an address", "https://app.example/reset", None),
        ("app@example.com", "https://app.example/reset", Some(broken)),
    ] {
        let err = reset::routes(
            &auth,
            ResetOptions {
                mailer: Some(mailer.clone()),
                from: from.into(),
                base_url: url.into(),
                templates,
            },
        )
        .unwrap_err()
        .to_string();
        assert!(
            err.starts_with("auth: cannot enable password reset: "),
            "{err}"
        );
        assert!(err.contains("\nTry: "), "{err}");
    }
    auth.close().await;
}

// The routes answer 500 without the auth layer, as Go `endpoint` does.
#[tokio::test]
async fn reset_routes_need_the_auth_layer() {
    let clock = TestClock::new();
    let auth = Auth::new(lazy_pool(), options(&clock)).unwrap();
    let app = reset::routes(&auth, reset_options(Arc::new(Capture::new()))).unwrap();
    let a = request(&app, "a@example.com").await;
    assert_eq!(a.status, StatusCode::INTERNAL_SERVER_ERROR);
    auth.close().await;
}

// TestResetSingleUseAndSessions.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_single_use_and_sessions(pool: PgPool) {
    let capture = Capture::new();
    let f = fixture(pool.clone(), reset_options(Arc::new(capture.clone())));
    let first = register(&f.app, "reset@example.com").await;
    let second = login(&f.app, "reset@example.com", GOOD)
        .await
        .cookie_value(COOKIE_NAME)
        .expect("a second session");
    let other = register(&f.app, "other@example.com").await;
    for _ in 0..DEFAULT_LOCKOUT_THRESHOLD {
        login(&f.app, "reset@example.com", "incorrect password").await;
    }
    assert_eq!(
        login(&f.app, "reset@example.com", GOOD).await.status,
        StatusCode::TOO_MANY_REQUESTS
    );
    // Unknown and malformed requests answer the same and send nothing.
    for body in [r#"{"email":"nobody@example.com"}"#, "{", r#"{"x":1}"#] {
        let a = send(&f.app, Req::post("/api/auth/reset/request").json(body)).await;
        assert_eq!(a.status, StatusCode::ACCEPTED, "{body}");
        assert!(a.body.is_empty(), "{body}: {}", a.body);
    }
    let a = request(&f.app, " RESET@example.com ").await;
    assert_eq!(a.status, StatusCode::ACCEPTED);
    assert!(a.body.is_empty(), "{}", a.body);
    assert_eq!(a.header("cache-control"), "no-store");
    let token = wait_token(&capture).await;
    let raw = B64URL.decode(token.as_bytes()).unwrap();
    assert_eq!(raw.len(), 32);
    let m = &capture.messages()[0];
    assert_eq!(m.to, "reset@example.com");
    assert_eq!(m.from, "app@example.com");
    assert_eq!(m.subject, "Reset your password");
    assert_eq!(
        m.text,
        format!("Reset your password: {LINK}{token}\nThis link expires in one hour.")
    );
    let (stored, expiry): (Vec<u8>, OffsetDateTime) =
        sqlx::query_as("SELECT token_hash, expires_at FROM password_resets")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(stored, Sha256::digest(&raw).to_vec(), "wrong token digest");
    assert_eq!(
        expiry,
        f.clock.now() + Duration::from_secs(3600),
        "wrong expiry"
    );
    for pw in ["short".to_owned(), "x".repeat(MAX_PASSWORD_LENGTH + 1)] {
        let a = confirm(&f.app, &token, &pw, None).await;
        assert_eq!(
            a.status,
            StatusCode::UNPROCESSABLE_ENTITY,
            "password validation"
        );
        assert_eq!(
            a.problem().errors[0].message,
            "Use between 12 and 512 characters."
        );
    }
    let bad = send(&f.app, Req::post("/api/auth/reset/confirm").json("{")).await;
    assert_eq!(bad.status, StatusCode::BAD_REQUEST);
    assert_eq!(
        bad.problem().detail.as_deref(),
        Some("Enter a reset token and a new password.")
    );
    let garbage = confirm(&f.app, "not-a-token", NEW, None).await;
    assert_eq!(garbage.status, StatusCode::UNPROCESSABLE_ENTITY);
    let done = confirm(&f.app, &token, NEW, Some(&first)).await;
    assert_eq!(
        done.status,
        StatusCode::NO_CONTENT,
        "confirm: {}",
        done.body
    );
    assert!(
        done.set_cookie(COOKIE_NAME).unwrap().contains("Max-Age=0"),
        "the reset session cookie must end"
    );
    for c in [&first, &second] {
        assert_eq!(
            session_status(&f.app, c).await,
            StatusCode::UNAUTHORIZED,
            "session survived"
        );
    }
    assert_eq!(
        session_status(&f.app, &other).await,
        StatusCode::OK,
        "other session removed"
    );
    let again = confirm(&f.app, &token, "another correct password", None).await;
    assert_eq!(
        again.status,
        StatusCode::UNPROCESSABLE_ENTITY,
        "reused token"
    );
    assert_eq!(
        again.problem().detail.as_deref(),
        Some("The reset link is invalid or expired. Request a new link.")
    );
    assert_eq!(
        login(&f.app, "reset@example.com", GOOD).await.status,
        StatusCode::UNAUTHORIZED
    );
    let new = login(&f.app, "reset@example.com", NEW).await;
    assert_eq!(new.status, StatusCode::OK, "new login: {}", new.body);
    assert_eq!(
        capture.messages().len(),
        1,
        "no mail for unknown or malformed requests"
    );
    f.auth.close().await;
}

// TestResetExpiredAndConcurrent.
async fn expired_or_concurrent(pool: PgPool, expired: bool) {
    let c = Capture::new();
    let f = fixture(pool, reset_options(Arc::new(c.clone())));
    register(&f.app, "reset@example.com").await;
    request(&f.app, "reset@example.com").await;
    let token = wait_token(&c).await;
    if expired {
        f.clock.add(Duration::from_secs(3600));
    }
    let tasks: Vec<_> = (0..2)
        .map(|_| {
            let (app, token) = (f.app.clone(), token.clone());
            tokio::spawn(async move { confirm(&app, &token, NEW, None).await.status })
        })
        .collect();
    let mut successes = 0;
    for t in tasks {
        match t.await.unwrap() {
            StatusCode::NO_CONTENT => successes += 1,
            StatusCode::UNPROCESSABLE_ENTITY => {}
            s => panic!("confirm: {s}"),
        }
    }
    assert_eq!(successes, usize::from(!expired));
    f.auth.close().await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_expired(pool: PgPool) {
    expired_or_concurrent(pool, true).await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_concurrent(pool: PgPool) {
    expired_or_concurrent(pool, false).await;
}

/// A mailer that waits until the test releases it (Go `blockedResetMailer`).
#[derive(Clone, Default)]
struct Blocked {
    started: Arc<Notify>,
    release: Arc<Notify>,
}

#[async_trait]
impl Mailer for Blocked {
    async fn send(&self, _: &Message) -> anyhow::Result<()> {
        self.started.notify_one();
        self.release.notified().await;
        Ok(())
    }
}

// TestResetRequestDoesNotWaitForDelivery.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_request_does_not_wait_for_delivery(pool: PgPool) {
    let mailer = Blocked::default();
    let f = fixture(pool, reset_options(Arc::new(mailer.clone())));
    register(&f.app, "reset@example.com").await;
    assert_eq!(
        request(&f.app, "reset@example.com").await.status,
        StatusCode::ACCEPTED
    );
    tokio::time::timeout(Duration::from_secs(5), mailer.started.notified())
        .await
        .expect("delivery did not start");
    for email in ["reset@example.com", "unknown@example.com", "invalid"] {
        let a = tokio::time::timeout(Duration::from_secs(5), request(&f.app, email))
            .await
            .expect("the request waited for the delivery");
        assert_eq!(a.status, StatusCode::ACCEPTED, "{email}");
        assert!(a.body.is_empty(), "{email}: {}", a.body);
    }
    // Close cancels the blocked delivery.
    tokio::time::timeout(Duration::from_secs(5), f.auth.close())
        .await
        .expect("close waited for the blocked delivery");
}

// TestResetRequestLimits: the per-email limit, the global limit, and the window. The
// global reservations count the admitted requests.
async fn limits(pool: PgPool, requests: usize, same_email: bool, concurrent: bool, want: i64) {
    let f = fixture(pool.clone(), reset_options(Arc::new(Capture::new())));
    let one = |i: usize| {
        let app = f.app.clone();
        async move {
            let email = if same_email {
                " UNKNOWN@example.com ".to_owned()
            } else {
                format!("unknown{i}@example.com")
            };
            let r = Req::post("/api/auth/reset/request")
                .json(serde_json::json!({"email": email}).to_string())
                .header("X-Forwarded-For", format!("198.51.100.{}", i % 250))
                .remote("203.0.113.1:12345");
            let a = send(&app, r).await;
            assert_eq!(a.status, StatusCode::ACCEPTED, "request {i}: {}", a.body);
            assert!(a.body.is_empty(), "request {i}: {}", a.body);
        }
    };
    if concurrent {
        let tasks: Vec<_> = (0..requests).map(|i| tokio::spawn(one(i))).collect();
        for t in tasks {
            t.await.unwrap();
        }
    } else {
        for i in 0..requests {
            one(i).await;
        }
    }
    let global = "SELECT count(*) FROM login_attempts WHERE email = 'reset-global'";
    assert_eq!(count(&pool, global).await, want, "global reservations");
    // Both buckets admit requests again at the window boundary.
    f.clock.add(DEFAULT_LOCKOUT_WINDOW);
    one(0).await;
    assert_eq!(
        count(&pool, global).await,
        want + 1,
        "admitted after the window"
    );
    f.auth.close().await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_limits_shared_web_peer(pool: PgPool) {
    limits(pool, 10, false, false, 10).await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_limits_per_email(pool: PgPool) {
    limits(pool, 10, true, false, DEFAULT_LOCKOUT_THRESHOLD).await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_limits_global(pool: PgPool) {
    limits(pool, 110, false, false, 100).await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_limits_concurrent_global(pool: PgPool) {
    limits(pool, 120, false, true, 100).await;
}

// The R0.7 spike test: a delivery through SMTP with the reset templates.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_reset_through_smtp_with_templates(pool: PgPool) {
    let stub = SmtpStub::start().await;
    let mailer = spinup_kit::mail::new(&Config {
        url: stub.url(),
        ..Config::default()
    })
    .unwrap();
    let templates = Templates::new([
        ("password_reset.subject.tmpl", "Reset your password\n"),
        (
            "password_reset.txt.tmpl",
            "Open {{ url }} to reset your password.\n",
        ),
        (
            "password_reset.html.tmpl",
            "<p><a href=\"{{ url }}\">Reset</a></p>\n",
        ),
    ]);
    let o = ResetOptions {
        templates: Some(templates),
        ..reset_options(mailer)
    };
    let f = fixture(pool, o);
    register(&f.app, "a@example.com").await;
    request(&f.app, "a@example.com").await;
    let mut token = None;
    for _ in 0..500 {
        if let Some(m) = stub.messages().first() {
            // Quoted-printable can wrap long lines with a soft break.
            token = token_in(&m.replace("=\n", ""));
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let mail = stub.messages()[0].clone();
    assert!(mail.contains("Subject: Reset your password"), "{mail}");
    assert!(mail.contains("multipart/alternative"), "{mail}");
    assert!(!mail.contains(GOOD));
    let token = token.expect("a reset link in the mail");
    let a = confirm(&f.app, &token, NEW, None).await;
    assert_eq!(a.status, StatusCode::NO_CONTENT, "{}", a.body);
    f.auth.close().await;
}
