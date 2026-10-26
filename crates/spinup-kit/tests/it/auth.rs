//! The tests of kit auth (Go `kit/auth/auth_test.go`, `password_test.go`
//! TestNormalizeEmail, `required_test.go`, and `authtest/authtest_test.go`), plus the
//! R0.7 spike tests of the rate limit inside the auth layer. A test that needs Postgres
//! starts with `pg_` and uses `#[sqlx::test]` with the migrations of `tests/it/migrations`.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::http::{StatusCode, header};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Router, middleware};
use spinup_kit::auth::password::{self, Params};
use spinup_kit::auth::ratelimit::{Limit, RateLimits};
use spinup_kit::auth::routes::normalize_email;
use spinup_kit::auth::store;
use spinup_kit::auth::{
    Auth, AuthUser, COOKIE_NAME, DEFAULT_CSP, DEFAULT_LOCKOUT_THRESHOLD, Options, User,
    require_user, with_user,
};
use spinup_kit::testkit::auth::as_user;
use spinup_kit::testkit::http::{Answer, Req, send};
use sqlx::PgPool;
use time::OffsetDateTime;

const GOOD: &str = "correct horse battery";
const OFF: &[(&str, &str)] = &[("AUTH_RATE_LIMIT", "off")];

/// Cheap argon2id parameters. `auth_password_default_params` covers the defaults.
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

/// Options for tests: no Secure cookie, test parameters, an environment of `env` only, a
/// test clock, and no cleanup task.
fn options(env: &[(&str, &str)], clock: &TestClock) -> Options {
    let env: HashMap<String, String> = env
        .iter()
        .map(|(k, v)| ((*k).to_owned(), (*v).to_owned()))
        .collect();
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

/// A pool that never connects. Requests that reach it fail.
fn lazy_pool() -> PgPool {
    sqlx::postgres::PgPoolOptions::new()
        .acquire_timeout(Duration::from_millis(300))
        .connect_lazy("postgres://nobody@127.0.0.1:1/none")
        .unwrap()
}

/// The app of a fixture, as Go `newFixture`: the auth routes, a route behind
/// `require_user`, an unsafe route, and a route with its own CSP, inside the auth layer
/// and the request ID layer.
fn app(auth: &Auth) -> Router {
    let api = Router::new()
        .route(
            "/api/private",
            get(|AuthUser(u): AuthUser| async move { format!("hello {}", u.email) })
                .layer(middleware::from_fn(require_user)),
        )
        .route("/api/things", post(|| async { StatusCode::CREATED }))
        .route(
            "/api/framed",
            get(|| async {
                (
                    [(header::CONTENT_SECURITY_POLICY, "default-src 'self'")],
                    "framed",
                )
                    .into_response()
            }),
        )
        .merge(auth.routes());
    auth.layer(api)
        .layer(middleware::from_fn(spinup_kit::httpx::request_id))
}

struct Fixture {
    auth: Auth,
    app: Router,
    clock: TestClock,
}

fn fixture(pool: PgPool, env: &[(&str, &str)], change: impl FnOnce(&mut Options)) -> Fixture {
    let clock = TestClock::new();
    let mut o = options(env, &clock);
    change(&mut o);
    let auth = Auth::new(pool, o).unwrap();
    let app = app(&auth);
    Fixture { auth, app, clock }
}

fn creds(email: &str, password: &str) -> String {
    serde_json::json!({"email": email, "password": password}).to_string()
}

fn cookie(token: &str) -> String {
    format!("{COOKIE_NAME}={token}")
}

fn attrs(line: &str) -> Vec<String> {
    line.split(';')
        .skip(1)
        .map(|s| s.trim().to_owned())
        .collect()
}

fn max_age(line: &str) -> i64 {
    attrs(line)
        .iter()
        .find_map(|x| x.strip_prefix("Max-Age=").map(str::to_owned))
        .unwrap_or_else(|| panic!("no Max-Age: {line}"))
        .parse()
        .unwrap()
}

fn field_names(a: &Answer) -> String {
    a.problem()
        .errors
        .iter()
        .map(|e| e.field.as_str())
        .collect::<Vec<_>>()
        .join(",")
}

async fn login(app: &Router, email: &str, pw: &str) -> Answer {
    send(app, Req::post("/api/auth/login").json(creds(email, pw))).await
}

/// Registers a user and returns the session token.
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

async fn count(pool: &PgPool, sql: &str) -> i64 {
    sqlx::query_scalar(sqlx::AssertSqlSafe(sql.to_owned()))
        .fetch_one(pool)
        .await
        .unwrap()
}

// TestRegisterSignsIn, with the cookie contract of ADR 0008 §3.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_register_signs_in(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |_| {});
    let a = send(
        &f.app,
        Req::post("/api/auth/register").json(creds("  Alice@Example.COM ", GOOD)),
    )
    .await;
    assert_eq!(a.status, StatusCode::CREATED, "{}", a.body);
    let user = a.json()["user"].clone();
    assert_eq!(user["email"], "alice@example.com");
    assert!(user["id"].as_i64().unwrap() > 0);
    assert_eq!(a.header("cache-control"), "no-store");
    assert_eq!(a.header("vary"), "Cookie");
    let line = a.set_cookie(COOKIE_NAME).expect("Set-Cookie: session");
    let at = attrs(&line);
    for want in ["HttpOnly", "SameSite=Lax", "Path=/"] {
        assert!(at.contains(&want.to_owned()), "{want}: {line}");
    }
    assert!(!at.contains(&"Secure".to_owned()), "{line}");
    assert!(at.iter().any(|x| x.starts_with("Expires=")), "{line}");
    // Go sends 604800 (tower-sessions sent 604799, REPORT.md:75).
    assert_eq!(max_age(&line), 604_800, "{line}");
    // The store holds the SHA-256 of the token, never the token.
    let token = a.cookie_value(COOKIE_NAME).unwrap();
    assert_eq!(token.len(), 43, "{token}");
    let (stored, user_id): (String, Option<i64>) =
        sqlx::query_as("SELECT token, user_id FROM sessions")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(stored, store::token_hash(&token));
    assert_eq!(stored.len(), 64);
    assert_ne!(stored, token);
    assert_eq!(user_id, user["id"].as_i64());

    let s = send(&f.app, Req::get("/api/session").cookie(cookie(&token))).await;
    assert_eq!(s.status, StatusCode::OK, "{}", s.body);
    assert_eq!(s.json()["user"], user);
    assert!(
        s.set_cookie(COOKIE_NAME).is_none(),
        "a read wrote the cookie"
    );
    let hash: String = sqlx::query_scalar("SELECT password_hash FROM users")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert!(
        hash.starts_with("$argon2id$v=19$") && !hash.contains(GOOD),
        "{hash}"
    );
    f.auth.close().await;
}

// TestSecureCookieByDefault.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_secure_cookie(pool: PgPool) {
    let f = fixture(pool, OFF, |o| o.secure = true);
    let a = send(
        &f.app,
        Req::post("/api/auth/register").json(creds("secure@example.com", GOOD)),
    )
    .await;
    let line = a.set_cookie(COOKIE_NAME).unwrap();
    assert!(attrs(&line).contains(&"Secure".to_owned()), "{line}");
}

// TestCookieSecurityFromEnvironment: the Secure flag follows SPINUP_ENV.
#[tokio::test]
async fn auth_cookie_security_from_environment() {
    for (env, secure) in [("", true), ("dev", false), ("production", true)] {
        let base: spinup_kit::config::Base = spinup_kit::config::load_from(
            |k| (k == "SPINUP_ENV" && !env.is_empty()).then(|| env.to_owned()),
            |_| Err(std::io::ErrorKind::NotFound.into()),
        )
        .unwrap();
        let mut o = options(&[], &TestClock::new());
        o.secure = base.env != "dev";
        let a = Auth::new(lazy_pool(), o).unwrap();
        assert_eq!(a.secure(), secure, "SPINUP_ENV={env:?}");
        a.close().await;
    }
    assert!(Options::default().secure);
}

// TestRegisterValidation.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_register_validation(pool: PgPool) {
    let f = fixture(pool, OFF, |_| {});
    let long_email = format!("{}@x.io", "a".repeat(250));
    let short = "p".repeat(password::MIN_PASSWORD_LENGTH - 1);
    let long = "p".repeat(password::MAX_PASSWORD_LENGTH + 1);
    for (name, body, ctype, status, fields) in [
        ("empty object", "{}".to_owned(), "", 422, "email,password"),
        ("bad email", creds("not-an-email", GOOD), "", 422, "email"),
        (
            "display name",
            creds("Alice <alice@example.com>", GOOD),
            "",
            422,
            "email",
        ),
        ("long email", creds(&long_email, GOOD), "", 422, "email"),
        (
            "short password",
            creds("a@example.com", &short),
            "",
            422,
            "password",
        ),
        (
            "long password",
            creds("a@example.com", &long),
            "",
            422,
            "password",
        ),
        (
            "unknown field",
            r#"{"email":"a@example.com","password":"correct horse battery","admin":"true"}"#
                .to_owned(),
            "",
            400,
            "",
        ),
        ("not JSON", "email=a@example.com".to_owned(), "", 400, ""),
        (
            "form body",
            "email=a@example.com".to_owned(),
            "application/x-www-form-urlencoded",
            415,
            "",
        ),
        (
            "two values",
            r#"{"email":"a@example.com"} {}"#.to_owned(),
            "",
            400,
            "",
        ),
        (
            "wrong type",
            r#"{"email":1,"password":"x"}"#.to_owned(),
            "",
            400,
            "",
        ),
        ("empty body", String::new(), "", 400, ""),
    ] {
        let mut r = Req::post("/api/auth/register").json(body);
        if !ctype.is_empty() {
            r = r.header("content-type", ctype);
        }
        let a = send(&f.app, r).await;
        assert_eq!(a.status.as_u16(), status, "{name}: {}", a.body);
        assert_eq!(field_names(&a), fields, "{name}");
        assert!(a.set_cookie(COOKIE_NAME).is_none(), "{name}: a cookie");
    }
    let details: Vec<(String, &str)> = vec![
        (
            String::new(),
            "The request body must contain one JSON value.",
        ),
        (
            r#"{"email":"a@example.com","password":"x","admin":true}"#.to_owned(),
            "The request body must contain valid JSON with only known fields and correct value types.",
        ),
        (
            r#"{"email":"a@example.com"} {}"#.to_owned(),
            "The request body must contain only one JSON value.",
        ),
    ];
    for (body, detail) in details {
        let a = send(&f.app, Req::post("/api/auth/register").json(body.clone())).await;
        assert_eq!(a.problem().detail.as_deref(), Some(detail), "{body}");
    }
    let a = send(
        &f.app,
        Req::post("/api/auth/register").json(creds("nope", "short")),
    )
    .await;
    let p = a.problem();
    assert_eq!(p.detail.as_deref(), Some("The request has invalid fields."));
    assert_eq!(p.errors[0].message, "Enter a valid email address.");
    assert_eq!(p.errors[1].message, "Use at least 12 characters.");
    let a = send(
        &f.app,
        Req::post("/api/auth/register").json(creds("a@example.com", &long)),
    )
    .await;
    assert_eq!(a.problem().errors[0].message, "Use at most 512 characters.");
    // Exactly the limits pass, counted in characters, not bytes.
    for (email, pw) in [
        ("min@example.com", "é".repeat(password::MIN_PASSWORD_LENGTH)),
        ("max@example.com", "p".repeat(password::MAX_PASSWORD_LENGTH)),
    ] {
        let a = send(
            &f.app,
            Req::post("/api/auth/register").json(creds(email, &pw)),
        )
        .await;
        assert_eq!(a.status, StatusCode::CREATED, "{email}: {}", a.body);
    }
}

// TestRegisterDuplicate.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_register_duplicate(pool: PgPool) {
    let f = fixture(pool, OFF, |_| {});
    register(&f.app, "bob@example.com").await;
    let a = send(
        &f.app,
        Req::post("/api/auth/register").json(creds(" BOB@example.com", "another long password")),
    )
    .await;
    assert_eq!(a.status, StatusCode::CONFLICT, "{}", a.body);
    let p = a.problem();
    assert_eq!(
        p.detail.as_deref(),
        Some("An account with this email address already exists.")
    );
    assert_eq!(field_names(&a), "email");
    assert_eq!(
        p.errors[0].message,
        "This email address is already registered."
    );
    assert!(a.set_cookie(COOKIE_NAME).is_none());
}

// TestLoginRotatesToken.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_login_rotates_token(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |_| {});
    let first = register(&f.app, "carol@example.com").await;
    let a = send(
        &f.app,
        Req::post("/api/auth/login")
            .json(creds("Carol@Example.com", GOOD))
            .cookie(cookie(&first)),
    )
    .await;
    assert_eq!(a.status, StatusCode::OK, "{}", a.body);
    assert_eq!(a.json()["user"]["email"], "carol@example.com");
    let second = a.cookie_value(COOKIE_NAME).expect("a new token");
    assert_ne!(first, second, "login must rotate the token");
    let old = send(&f.app, Req::get("/api/session").cookie(cookie(&first))).await;
    assert_eq!(old.status, StatusCode::UNAUTHORIZED);
    assert_eq!(
        old.problem().detail.as_deref(),
        Some("No user is signed in.")
    );
    let now = send(&f.app, Req::get("/api/session").cookie(cookie(&second))).await;
    assert_eq!(now.status, StatusCode::OK);
    assert_eq!(count(&pool, "SELECT count(*) FROM sessions").await, 1);
    // A cookie that the server never issued gets no session (no fixation).
    let a = send(
        &f.app,
        Req::post("/api/auth/login")
            .json(creds("carol@example.com", GOOD))
            .cookie(cookie("attacker-chosen")),
    )
    .await;
    let c = a.cookie_value(COOKIE_NAME).expect("a new token");
    assert_ne!(c, "attacker-chosen");
}

// TestLoginFailures.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_login_failures(pool: PgPool) {
    let f = fixture(pool, OFF, |_| {});
    register(&f.app, "dave@example.com").await;
    for (name, email) in [
        ("wrong password", "dave@example.com"),
        ("unknown email", "nobody@example.com"),
    ] {
        let a = login(&f.app, email, "wrong password!").await;
        assert_eq!(a.status, StatusCode::UNAUTHORIZED, "{name}");
        assert_eq!(
            a.problem().detail.as_deref(),
            Some("The email address or password is incorrect."),
            "{name}"
        );
        assert!(a.set_cookie(COOKIE_NAME).is_none(), "{name}");
    }
    let a = login(&f.app, "dave@example.com", "").await;
    assert_eq!(a.status, StatusCode::UNPROCESSABLE_ENTITY);
    assert_eq!(field_names(&a), "password");
    assert_eq!(a.problem().errors[0].message, "Enter your password.");
}

// TestLockoutAndRecovery.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_lockout_and_recovery(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |_| {});
    register(&f.app, "erin@example.com").await;
    for i in 0..DEFAULT_LOCKOUT_THRESHOLD {
        let a = login(&f.app, "erin@example.com", "wrong password!").await;
        assert_eq!(a.status, StatusCode::UNAUTHORIZED, "failure {}", i + 1);
        f.clock.add(Duration::from_secs(60));
    }
    // The sixth attempt is locked, even with the right password.
    let a = login(&f.app, "erin@example.com", GOOD).await;
    assert_eq!(a.status, StatusCode::TOO_MANY_REQUESTS, "{}", a.body);
    assert_eq!(
        a.problem().detail.as_deref(),
        Some("Too many failed logins for this email address. Try again later.")
    );
    // The first failure was at 12:00, the window opens at 12:15, and now is 12:05.
    assert_eq!(a.header("retry-after"), "600");
    // Other emails are not locked.
    let other = login(&f.app, "other@example.com", "wrong password!").await;
    assert_eq!(other.status, StatusCode::UNAUTHORIZED);

    f.clock.add(Duration::from_secs(10 * 60 + 1));
    let a = login(&f.app, "erin@example.com", GOOD).await;
    assert_eq!(a.status, StatusCode::OK, "{}", a.body);
    assert_eq!(
        count(
            &pool,
            "SELECT count(*) FROM login_attempts WHERE email = 'erin@example.com'"
        )
        .await,
        0
    );
    // A success cleared the count: four more failures do not lock.
    for _ in 0..DEFAULT_LOCKOUT_THRESHOLD - 1 {
        let a = login(&f.app, "erin@example.com", "wrong password!").await;
        assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    }
    assert_eq!(
        login(&f.app, "erin@example.com", GOOD).await.status,
        StatusCode::OK
    );
}

// TestLockoutOption, with the cleanup of old attempts.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_lockout_option(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |o| {
        o.threshold = 2;
        o.window = Duration::from_secs(3600);
    });
    for _ in 0..2 {
        login(&f.app, "frank@example.com", "wrong password!").await;
    }
    let a = login(&f.app, "frank@example.com", "wrong password!").await;
    assert_eq!(a.status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(a.header("retry-after"), "3600");
    // The cleanup deletes the attempts that left the window, and expired sessions.
    sqlx::query(
        "INSERT INTO sessions (token, user_id, data, expiry) VALUES ('old', NULL, '{}', now() - interval '1 second')",
    )
    .execute(&pool)
    .await
    .unwrap();
    f.clock.add(Duration::from_secs(2 * 3600));
    f.auth.delete_expired().await.unwrap();
    assert_eq!(count(&pool, "SELECT count(*) FROM login_attempts").await, 0);
    assert_eq!(count(&pool, "SELECT count(*) FROM sessions").await, 0);
}

// TestConcurrentLoginLockout: 20 parallel wrong logins for one email check at most 5
// passwords. The other 15 get 429 while the hash slots are held.
async fn concurrent_login_lockout(pool: PgPool, known: bool) {
    const ATTEMPTS: usize = 20;
    let f = fixture(pool.clone(), OFF, |o| {
        o.hash_concurrent = 2;
        o.hash_waiting = ATTEMPTS;
        o.hash_wait = Duration::from_secs(60);
    });
    if known {
        register(&f.app, "burst@example.com").await;
    }
    // Hold every hash slot until all requests are admitted or refused.
    let held: Vec<_> = (0..2).map(|_| f.auth.slots().hold().unwrap()).collect();
    let (tx, mut rx) = tokio::sync::mpsc::channel(ATTEMPTS);
    for i in 0..ATTEMPTS {
        let (app, tx) = (f.app.clone(), tx.clone());
        tokio::spawn(async move {
            let email = if i % 2 == 0 {
                "  BURST@Example.COM  "
            } else {
                "burst@example.com"
            };
            let _ = tx.send(login(&app, email, "wrong password!").await).await;
        });
    }
    let locked = DEFAULT_LOCKOUT_THRESHOLD as usize;
    for _ in 0..ATTEMPTS - locked {
        let a = tokio::time::timeout(Duration::from_secs(20), rx.recv())
            .await
            .expect("logins did not return 429")
            .unwrap();
        assert_eq!(a.status, StatusCode::TOO_MANY_REQUESTS, "{}", a.body);
        assert_eq!(a.header("retry-after"), "900");
    }
    assert!(
        tokio::time::timeout(Duration::from_millis(200), rx.recv())
            .await
            .is_err(),
        "a login passed the held hash slots"
    );
    let sql = "SELECT count(*) FROM login_attempts WHERE email = 'burst@example.com'";
    assert_eq!(count(&pool, sql).await, DEFAULT_LOCKOUT_THRESHOLD);
    drop(held);
    for _ in 0..locked {
        let a = rx.recv().await.unwrap();
        assert_eq!(a.status, StatusCode::UNAUTHORIZED, "{}", a.body);
    }
    assert_eq!(count(&pool, sql).await, DEFAULT_LOCKOUT_THRESHOLD);
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_concurrent_login_lockout_known(pool: PgPool) {
    concurrent_login_lockout(pool, true).await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_concurrent_login_lockout_unknown(pool: PgPool) {
    concurrent_login_lockout(pool, false).await;
}

fn assert_busy(a: &Answer) {
    assert_eq!(a.status, StatusCode::SERVICE_UNAVAILABLE, "{}", a.body);
    assert_eq!(a.header("retry-after"), "1");
    assert_eq!(
        a.problem().detail.as_deref(),
        Some("The server is busy. Try again in a moment.")
    );
}

// TestBusyHashSlots "full queue": a refused login and registration do no database work.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_busy_hash_slots_full_queue(pool: PgPool) {
    let limits = |o: &mut Options| {
        o.hash_concurrent = 1;
        o.hash_waiting = 0;
        o.hash_wait = Duration::from_secs(60);
    };
    let f = fixture(pool.clone(), OFF, limits);
    register(&f.app, "busy@example.com").await;
    let place = f.auth.slots().admit().unwrap(); // the only place is taken
    assert_busy(&login(&f.app, "busy@example.com", GOOD).await);
    assert_busy(
        &send(
            &f.app,
            Req::post("/api/auth/register").json(creds("new@example.com", GOOD)),
        )
        .await,
    );
    assert_eq!(count(&pool, "SELECT count(*) FROM login_attempts").await, 0);
    // The same answer from an Auth whose pool cannot connect: no pool connection.
    let mut o = options(OFF, &TestClock::new());
    limits(&mut o);
    let dead = Auth::new(lazy_pool(), o).unwrap();
    let _dead_place = dead.slots().admit().unwrap();
    assert_busy(&login(&app(&dead), "busy@example.com", GOOD).await);
    dead.close().await;
    drop(place);
    let a = login(&f.app, "busy@example.com", GOOD).await;
    assert_eq!(a.status, StatusCode::OK, "{}", a.body);
}

// TestBusyHashSlots "wait ends": logins that checked no password do not count.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_busy_hash_slots_wait_ends(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |o| {
        o.hash_concurrent = 1;
        o.hash_waiting = 1;
        o.hash_wait = Duration::from_millis(20);
    });
    register(&f.app, "slow@example.com").await;
    let held = f.auth.slots().hold().unwrap(); // the only slot runs a long hash
    for _ in 0..=DEFAULT_LOCKOUT_THRESHOLD {
        assert_busy(&login(&f.app, "slow@example.com", "wrong password!").await);
    }
    assert_eq!(count(&pool, "SELECT count(*) FROM login_attempts").await, 0);
    drop(held);
    let a = login(&f.app, "slow@example.com", GOOD).await;
    assert_eq!(a.status, StatusCode::OK, "{}", a.body);
}

// TestHashLimitsValidation and TestNewErrors. Rust types cannot hold a nil pool, a
// negative waiting count, or a negative idle timeout.
#[tokio::test]
async fn auth_new_errors() {
    let clock = TestClock::new();
    type Change = Box<dyn Fn(&mut Options)>;
    let cases: Vec<(&str, Change, &str)> = vec![
        (
            "no slots",
            Box::new(|o| o.hash_concurrent = 0),
            "Try: correct the values of Options::hash_concurrent",
        ),
        (
            "no wait",
            Box::new(|o| o.hash_wait = Duration::ZERO),
            "Try: correct the values of Options::hash_concurrent",
        ),
        (
            "bad origin",
            Box::new(|o| o.origins = vec!["localhost:5173/path".into()]),
            "cannot trust origin",
        ),
        (
            "zero lifetime",
            Box::new(|o| o.lifetime = Duration::ZERO),
            "the session lifetime 0s is not positive",
        ),
        (
            "zero threshold",
            Box::new(|o| o.threshold = 0),
            "the lockout needs a threshold",
        ),
        (
            "zero window",
            Box::new(|o| o.window = Duration::ZERO),
            "the lockout needs a threshold",
        ),
        (
            "bad params",
            Box::new(|o| o.params.salt_len = 2),
            "the argon2id parameters",
        ),
        (
            "bad proxies",
            Box::new(|o| o.proxies = Some(vec!["not-an-address".into()])),
            "AUTH_TRUSTED_PROXIES",
        ),
    ];
    for (name, change, want) in cases {
        let mut o = options(&[], &clock);
        change(&mut o);
        let err = Auth::new(lazy_pool(), o).unwrap_err().to_string();
        assert!(err.contains(want), "{name}: {err}");
        assert!(err.starts_with("auth: "), "{name}: {err}");
        assert!(err.contains("\nTry: "), "{name}: {err}");
    }
    let env = Auth::new(
        lazy_pool(),
        options(&[("AUTH_RATE_LIMIT", "sometimes")], &clock),
    )
    .unwrap_err()
    .to_string();
    assert!(
        env.contains("AUTH_RATE_LIMIT") && env.contains("\nTry: "),
        "{env}"
    );
}

// TestLogout.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_logout(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |_| {});
    let token = register(&f.app, "grace@example.com").await;
    let a = send(&f.app, Req::post("/api/auth/logout").cookie(cookie(&token))).await;
    assert_eq!(a.status, StatusCode::NO_CONTENT, "{}", a.body);
    let line = a.set_cookie(COOKIE_NAME).expect("an expired cookie");
    assert!(line.starts_with("session=;"), "{line}");
    assert_eq!(max_age(&line), 0, "{line}");
    assert!(
        line.contains("Expires=Thu, 01 Jan 1970 00:00:01 GMT"),
        "{line}"
    );
    let s = send(&f.app, Req::get("/api/session").cookie(cookie(&token))).await;
    assert_eq!(s.status, StatusCode::UNAUTHORIZED);
    assert_eq!(count(&pool, "SELECT count(*) FROM sessions").await, 0);
    // Logout without a session also succeeds.
    let a = send(&f.app, Req::post("/api/auth/logout")).await;
    assert_eq!(a.status, StatusCode::NO_CONTENT);
}

// TestSessionAndRequireUser.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_session_and_require_user(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |_| {});
    let a = send(&f.app, Req::get("/api/session")).await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    assert_eq!(a.problem().status, 401);
    assert_eq!(a.header("cache-control"), "no-store");
    let a = send(
        &f.app,
        Req::get("/api/session").cookie(cookie("unknown-token")),
    )
    .await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    let a = send(&f.app, Req::get("/api/private")).await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    assert_eq!(a.problem().detail.as_deref(), Some("No user is signed in."));
    let token = register(&f.app, "heidi@example.com").await;
    let a = send(&f.app, Req::get("/api/private").cookie(cookie(&token))).await;
    assert_eq!(a.status, StatusCode::OK);
    assert_eq!(a.body, "hello heidi@example.com");
    // A deleted user loses the session. The spike schema deletes the sessions of the
    // user with the user (ON DELETE CASCADE), so the cookie finds no session.
    sqlx::query("DELETE FROM users WHERE email = 'heidi@example.com'")
        .execute(&pool)
        .await
        .unwrap();
    let a = send(&f.app, Req::get("/api/session").cookie(cookie(&token))).await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    assert_eq!(count(&pool, "SELECT count(*) FROM sessions").await, 0);
    // A session whose user is gone (a row without user_id) ends with an expired cookie,
    // as in Go.
    let gone = store::new_token();
    let record = store::Record {
        deadline: OffsetDateTime::now_utc() + Duration::from_secs(3600),
        values: serde_json::json!({ store::SESSION_USER_KEY: 999_999 })
            .as_object()
            .unwrap()
            .clone(),
    };
    sqlx::query(
        "INSERT INTO sessions (token, user_id, data, expiry) VALUES ($1, NULL, $2, now() + interval '1 hour')",
    )
    .bind(store::token_hash(&gone))
    .bind(serde_json::to_value(&record).unwrap())
    .execute(&pool)
    .await
    .unwrap();
    let a = send(&f.app, Req::get("/api/private").cookie(cookie(&gone))).await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    let line = a.set_cookie(COOKIE_NAME).expect("an expired cookie");
    assert_eq!(max_age(&line), 0, "{line}");
    assert_eq!(count(&pool, "SELECT count(*) FROM sessions").await, 0);
}

// TestCrossOriginProtection.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_cross_origin_protection(pool: PgPool) {
    let f = fixture(pool, OFF, |o| {
        o.origins = vec!["http://localhost:5173".into(), String::new()];
    });
    for (name, method, headers, want) in [
        (
            "cross-site fetch",
            "POST",
            vec![("sec-fetch-site", "cross-site")],
            403,
        ),
        (
            "same-site fetch",
            "POST",
            vec![("sec-fetch-site", "same-site")],
            403,
        ),
        (
            "foreign origin",
            "POST",
            vec![("origin", "https://evil.example")],
            403,
        ),
        ("null origin", "POST", vec![("origin", "null")], 403),
        (
            "cross-site with trusted origin",
            "POST",
            vec![
                ("sec-fetch-site", "cross-site"),
                ("origin", "http://localhost:5173"),
            ],
            201,
        ),
        (
            "trusted origin (SvelteKit server)",
            "POST",
            vec![("origin", "http://localhost:5173")],
            201,
        ),
        (
            "same origin",
            "POST",
            vec![
                ("sec-fetch-site", "same-origin"),
                ("origin", "http://api.test"),
            ],
            201,
        ),
        (
            "origin equals host",
            "POST",
            vec![("origin", "http://api.test")],
            201,
        ),
        ("no browser headers", "POST", vec![], 201),
        (
            "safe method cross-site",
            "GET",
            vec![("sec-fetch-site", "cross-site")],
            401,
        ),
    ] {
        let path = if method == "GET" {
            "/api/session"
        } else {
            "/api/things"
        };
        let mut r = Req::new(method, path);
        for (k, v) in headers {
            r = r.header(k, v);
        }
        let a = send(&f.app, r).await;
        assert_eq!(a.status.as_u16(), want, "{name}: {}", a.body);
        if want == 403 {
            let p = a.problem();
            assert_eq!(p.status, 403, "{name}");
            assert_eq!(
                p.detail.as_deref(),
                Some("Cross-origin requests that change state are not allowed.")
            );
        }
    }
    // The login endpoint is protected too.
    let a = send(
        &f.app,
        Req::post("/api/auth/login")
            .json(creds("a@example.com", GOOD))
            .header("sec-fetch-site", "cross-site"),
    )
    .await;
    assert_eq!(a.status, StatusCode::FORBIDDEN);
    assert!(a.set_cookie(COOKIE_NAME).is_none());
}

// TestDefaultCSP. No route here touches the database.
#[tokio::test]
async fn auth_default_csp() {
    let a = Auth::new(lazy_pool(), options(OFF, &TestClock::new())).unwrap();
    let app = app(&a);
    let s = send(&app, Req::get("/api/session")).await;
    assert_eq!(s.header("content-security-policy"), DEFAULT_CSP);
    let s = send(&app, Req::get("/api/framed")).await;
    assert_eq!(s.header("content-security-policy"), "default-src 'self'");
    a.close().await;
}

// TestRehashOnLogin: one place in the queue, so the rehash uses the place of its login.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_rehash_on_login(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |_| {});
    register(&f.app, "ivan@example.com").await;
    let newer = fixture(pool.clone(), OFF, |o| {
        o.hash_concurrent = 1;
        o.hash_waiting = 0;
        o.hash_wait = Duration::from_secs(60);
        o.params = Params {
            memory: 128,
            time: 2,
            ..TEST_PARAMS
        };
    });
    let a = login(&newer.app, "ivan@example.com", GOOD).await;
    assert_eq!(a.status, StatusCode::OK, "{}", a.body);
    let hash: String =
        sqlx::query_scalar("SELECT password_hash FROM users WHERE email = 'ivan@example.com'")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert!(hash.starts_with("$argon2id$v=19$m=128,t=2,p=1$"), "{hash}");
    // The old Auth still accepts the new hash.
    assert_eq!(
        login(&f.app, "ivan@example.com", GOOD).await.status,
        StatusCode::OK
    );
}

// TestMiddlewareMissing: the auth routes without Auth::layer answer 500.
#[tokio::test]
async fn auth_middleware_missing() {
    let a = Auth::new(lazy_pool(), options(OFF, &TestClock::new())).unwrap();
    for (method, path) in [
        ("GET", "/api/session"),
        ("POST", "/api/auth/logout"),
        ("POST", "/api/auth/login"),
    ] {
        let r = send(
            &a.routes(),
            Req::new(method, path).json(creds("a@example.com", GOOD)),
        )
        .await;
        assert_eq!(r.status, StatusCode::INTERNAL_SERVER_ERROR, "{path}");
        assert_eq!(r.problem().status, 500);
    }
    a.close().await;
}

// TestSessionStoreFailure.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_session_store_failure(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |_| {});
    let token = register(&f.app, "judy@example.com").await;
    pool.close().await;
    let a = send(&f.app, Req::get("/api/session").cookie(cookie(&token))).await;
    assert_eq!(a.status, StatusCode::INTERNAL_SERVER_ERROR, "{}", a.body);
    assert_eq!(a.problem().status, 500);
    assert_eq!(a.header("cache-control"), "no-store");
}

// TestIdleTimeout: every request extends the session and sends the cookie again.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_idle_timeout(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |o| {
        o.idle_timeout = Duration::from_secs(3600);
        o.lifetime = Duration::from_secs(24 * 3600);
    });
    let token = register(&f.app, "ken@example.com").await;
    let a = send(&f.app, Req::get("/api/session").cookie(cookie(&token))).await;
    assert_eq!(a.status, StatusCode::OK, "{}", a.body);
    let line = a.set_cookie(COOKIE_NAME).expect("a cookie");
    assert_eq!(a.cookie_value(COOKIE_NAME).as_deref(), Some(token.as_str()));
    let age = max_age(&line);
    assert!((3590..=3601).contains(&age), "{line}");
    let left: f64 =
        sqlx::query_scalar("SELECT extract(epoch FROM expiry - now())::float8 FROM sessions")
            .fetch_one(&pool)
            .await
            .unwrap();
    assert!(left <= 3601.0 && left > 3500.0, "{left}");
}

// R0.7: a 503 of the hash queue gives the rate limit token back.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_busy_answer_refunds_the_rate_limit(pool: PgPool) {
    let f = fixture(pool, &[], |o| {
        o.hash_concurrent = 1;
        o.hash_waiting = 0;
        o.hash_wait = Duration::from_secs(60);
        o.rates = RateLimits {
            login: Limit::new(1, Duration::from_secs(3600)),
            ..RateLimits::default()
        };
    });
    let held = f.auth.slots().admit().unwrap();
    for i in 0..3 {
        let a = login(&f.app, "busy@example.com", GOOD).await;
        assert_eq!(a.status, StatusCode::SERVICE_UNAVAILABLE, "request {i}");
    }
    drop(held);
    assert_eq!(
        login(&f.app, "busy@example.com", GOOD).await.status,
        StatusCode::UNAUTHORIZED
    );
    let a = login(&f.app, "busy@example.com", GOOD).await;
    assert_eq!(a.status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(a.header("cache-control"), "no-store");
    assert_eq!(a.header("content-security-policy"), DEFAULT_CSP);
    assert!(a.body.contains("sign-in attempts"), "{}", a.body);
}

// R0.7: the rate limit runs before any session or database work.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_rate_limited_login_reaches_no_query(pool: PgPool) {
    let rates = RateLimits {
        login: Limit::new(2, Duration::from_secs(3600)),
        ..RateLimits::default()
    };
    let f = fixture(pool.clone(), &[], |o| o.rates = rates);
    for i in 0..2 {
        let a = login(&f.app, &format!("user{i}@example.com"), GOOD).await;
        assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    }
    let before = count(&pool, "SELECT count(*) FROM login_attempts").await;
    // The same routes on a pool that cannot connect: a refused request answers 429 with
    // no database work, also with a bogus session cookie.
    let mut o = options(&[], &f.clock);
    o.rates = rates;
    let dead = Auth::new(lazy_pool(), o).unwrap();
    let dead_app = app(&dead);
    let mut statuses = Vec::new();
    for i in 0..6 {
        let a = send(
            &dead_app,
            Req::post("/api/auth/login")
                .json(creds(&format!("user{i}@example.com"), GOOD))
                .cookie(cookie("AAAAAAAAAAAAAAAAAAAAAA")),
        )
        .await;
        statuses.push(a.status.as_u16());
    }
    // The first two reach the session store, which cannot connect (500).
    assert_eq!(statuses, [500, 500, 429, 429, 429, 429]);
    assert_eq!(
        count(&pool, "SELECT count(*) FROM login_attempts").await,
        before
    );
    // Another client has its own bucket.
    let other = send(
        &f.app,
        Req::post("/api/auth/login")
            .json(creds("user9@example.com", GOOD))
            .remote("192.0.2.9:1"),
    )
    .await;
    assert_eq!(other.status, StatusCode::UNAUTHORIZED);
    dead.close().await;
}

// R0.7: register and login have their own buckets.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_register_has_its_own_bucket(pool: PgPool) {
    let f = fixture(pool, &[], |o| {
        o.rates = RateLimits {
            register: Limit::new(1, Duration::from_secs(3600)),
            ..RateLimits::default()
        };
    });
    register(&f.app, "one@example.com").await;
    let a = send(
        &f.app,
        Req::post("/api/auth/register").json(creds("two@example.com", GOOD)),
    )
    .await;
    assert_eq!(a.status, StatusCode::TOO_MANY_REQUESTS);
    assert!(a.body.contains("sign-up attempts"), "{}", a.body);
    assert_eq!(
        login(&f.app, "one@example.com", GOOD).await.status,
        StatusCode::OK
    );
}

// The cleanup task deletes expired rows without a call, and close stops it.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_auth_cleanup_task(pool: PgPool) {
    let f = fixture(pool.clone(), OFF, |o| o.cleanup = Duration::from_millis(50));
    sqlx::query(
        "INSERT INTO sessions (token, user_id, data, expiry) VALUES ('old', NULL, '{}', now() - interval '1 second')",
    )
    .execute(&pool)
    .await
    .unwrap();
    let started = Instant::now();
    while count(&pool, "SELECT count(*) FROM sessions").await != 0 {
        assert!(started.elapsed() < Duration::from_secs(10), "no cleanup");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    tokio::time::timeout(Duration::from_secs(5), f.auth.close())
        .await
        .expect("close did not stop the tasks");
    f.auth.close().await;
}

// R0.11 and R0.12: Auth::new does not wait for the dummy hash. It runs after the start.
#[tokio::test]
async fn auth_dummy_hash_runs_after_the_start() {
    let mut o = options(OFF, &TestClock::new());
    o.params = password::DEFAULT_PARAMS;
    let started = Instant::now();
    let a = Auth::new(lazy_pool(), o).unwrap();
    let new_took = started.elapsed();
    let dummy = a.dummy_hash().await;
    let hash_took = started.elapsed();
    assert!(
        dummy.starts_with("$argon2id$v=19$m=65536,t=3,p=4$"),
        "{dummy}"
    );
    assert!(
        new_took < hash_took / 2,
        "Auth::new took {new_took:?} of {hash_took:?}"
    );
    assert_eq!(a.dummy_hash().await, dummy, "the dummy hash changed");
    let (ok, _) = password::verify_password(&dummy, GOOD).unwrap();
    assert!(!ok);
    a.close().await;
}

// TestRequired: require_user answers 401 without a user and needs no auth layer.
#[tokio::test]
async fn auth_required() {
    let routes = Router::new().route(
        "/",
        get(|| async { StatusCode::NO_CONTENT }).layer(middleware::from_fn(require_user)),
    );
    let a = send(&routes, Req::get("/")).await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    assert_eq!(a.header("content-type"), "application/problem+json");
    assert_eq!(a.header("cache-control"), "no-store");
    let p = a.problem();
    assert_eq!(p.status, 401);
    assert_eq!(p.detail.as_deref(), Some("No user is signed in."));
    let a = send(&as_user(routes, 42), Req::get("/")).await;
    assert_eq!(a.status, StatusCode::NO_CONTENT);
}

// TestWithUser, and the AuthUser extractor without a user.
#[tokio::test]
async fn auth_with_user() {
    use axum::extract::FromRequestParts;
    let want = User {
        id: 42,
        email: "user@example.com".into(),
    };
    let req = with_user(axum::http::Request::new(()), want.clone());
    let (mut parts, ()) = req.into_parts();
    let AuthUser(got) = AuthUser::from_request_parts(&mut parts, &()).await.unwrap();
    assert_eq!(got, want);
    let routes = Router::new()
        .route("/", get(|AuthUser(u): AuthUser| async move { u.email }))
        .route(
            "/maybe",
            get(|u: Option<AuthUser>| async move { format!("{}", u.is_some()) }),
        );
    let a = send(&routes, Req::get("/")).await;
    assert_eq!(a.status, StatusCode::UNAUTHORIZED);
    assert_eq!(
        a.problem().detail.as_deref(),
        Some("A signed-in user is required.")
    );
    assert_eq!(send(&routes, Req::get("/maybe")).await.body, "false");
}

// authtest TestAs.
#[tokio::test]
async fn auth_testkit_as_user() {
    let routes: Router<u8> = Router::new().route(
        "/",
        get(|AuthUser(u): AuthUser| async move {
            assert_eq!(u.id, 42);
            StatusCode::NO_CONTENT
        }),
    );
    let app = as_user(routes, 42).with_state(7);
    let a = send(&app, Req::get("/")).await;
    assert_eq!(a.status, StatusCode::NO_CONTENT);
}

// TestNormalizeEmail.
#[test]
fn auth_normalize_email() {
    let long = format!("{}@{}.com", "a".repeat(64), "b".repeat(186));
    for (input, want, ok) in [
        ("alice@example.com", "alice@example.com", true),
        ("  Alice@Example.COM\t", "alice@example.com", true),
        ("a.b+tag@sub.example.org", "a.b+tag@sub.example.org", true),
        ("", "", false),
        ("   ", "", false),
        ("alice", "alice", false),
        ("alice@", "alice@", false),
        ("@example.com", "@example.com", false),
        (
            "Alice <alice@example.com>",
            "alice <alice@example.com>",
            false,
        ),
        ("<alice@example.com>", "<alice@example.com>", false),
        ("a@b@example.com", "a@b@example.com", false),
        (r#""a b"@example.com"#, r#""a b"@example.com"#, false),
        (
            "alice@example.com, bob@example.com",
            "alice@example.com, bob@example.com",
            false,
        ),
        (long.as_str(), "", false),
    ] {
        let (got, got_ok) = normalize_email(input);
        assert_eq!(got_ok, ok, "{input:?}");
        if !want.is_empty() {
            assert_eq!(got, want, "{input:?}");
        }
    }
}

/// One run of the load test: logins/s, busy answers, and the probe latency (p50, p99).
async fn load_run(
    app: &Router,
    token: &str,
    logins: usize,
    secs: u64,
) -> (f64, usize, Duration, Duration) {
    let until = Instant::now() + Duration::from_secs(secs);
    let mut tasks = Vec::new();
    for i in 0..logins {
        let app = app.clone();
        tasks.push(tokio::spawn(async move {
            // One user for each client: parallel logins of one email trip the lockout.
            let email = format!("load{i}@example.com");
            let (mut ok, mut busy) = (0usize, 0usize);
            while Instant::now() < until {
                let a = login(&app, &email, GOOD).await;
                match a.status {
                    StatusCode::OK => ok += 1,
                    StatusCode::SERVICE_UNAVAILABLE => {
                        busy += 1;
                        tokio::time::sleep(Duration::from_millis(50)).await;
                    }
                    s => panic!("login: {s}: {}", a.body),
                }
            }
            (ok, busy, Vec::new())
        }));
    }
    for _ in 0..4 {
        let (app, token) = (app.clone(), token.to_owned());
        tasks.push(tokio::spawn(async move {
            let mut times = Vec::new();
            while Instant::now() < until {
                let started = Instant::now();
                let a = send(&app, Req::get("/api/session").cookie(cookie(&token))).await;
                assert_eq!(a.status, StatusCode::OK, "{}", a.body);
                times.push(started.elapsed());
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
            (0, 0, times)
        }));
    }
    let (mut ok, mut busy, mut times) = (0, 0, Vec::new());
    for t in tasks {
        let (o, b, mut ts) = t.await.unwrap();
        ok += o;
        busy += b;
        times.append(&mut ts);
    }
    times.sort();
    let pick = |q: f64| {
        // The index is inside the vector.
        #[allow(
            clippy::cast_possible_truncation,
            clippy::cast_sign_loss,
            clippy::cast_precision_loss
        )]
        let i = ((times.len() as f64 - 1.0) * q).round() as usize;
        times.get(i).copied().unwrap_or_default()
    };
    #[allow(clippy::cast_precision_loss)]
    let rate = ok as f64 / secs as f64;
    (rate, busy, pick(0.5), pick(0.99))
}

// The load test that decides the default hash slots (REPORT.md:76). Logins with the
// default argon2id parameters fill the slots, and 4 probes read /api/session (a session
// load and a user query). It needs an optimized argon2. Run it alone:
//
//   cargo nextest run --locked -p spinup-kit --cargo-profile release \
//     --config profile.release.lto=false --config profile.release.codegen-units=16 \
//     --run-ignored only -E 'test(pg_auth_load_hash_slots)' --no-capture
#[tokio::test(flavor = "multi_thread")]
#[ignore = "a load test of about a minute: run it alone with an optimized build"]
async fn pg_auth_load_hash_slots() {
    let migrations = sqlx::migrate!("tests/it/migrations");
    let procs = std::thread::available_parallelism().map_or(1, std::num::NonZero::get);
    let mut counts = vec![0, 1];
    for pct in [2, 4, 6, 8, 10] {
        counts.push((procs * pct / 10).max(1));
    }
    counts.dedup();
    println!(
        "procs={procs} default slots={}",
        password::default_hash_slots()
    );
    for slots in counts {
        let Some(db) = spinup_kit::testkit::db::db_with_migrations(&migrations).await else {
            return;
        };
        let mut o = options(OFF, &TestClock::new());
        o.params = password::DEFAULT_PARAMS;
        o.hash_concurrent = slots.max(1);
        o.hash_waiting = spinup_kit::auth::DEFAULT_HASH_QUEUE * slots.max(1);
        o.hash_wait = spinup_kit::auth::DEFAULT_HASH_WAIT;
        let auth = Auth::new(db.pool().clone(), o).unwrap();
        let app = app(&auth);
        let token = register(&app, "probe@example.com").await;
        let logins = if slots == 0 { 0 } else { 2 * procs };
        for i in 0..logins {
            register(&app, &format!("load{i}@example.com")).await;
        }
        let (rate, busy, p50, p99) = load_run(&app, &token, logins, 8).await;
        println!(
            "slots={slots:>2} logins/s={rate:>6.1} busy={busy:>5} probe p50={p50:>10.2?} p99={p99:>10.2?}"
        );
        auth.close().await;
    }
}
