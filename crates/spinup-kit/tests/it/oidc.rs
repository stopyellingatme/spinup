//! The tests of the OIDC sign-in (Go `kit/auth/oidc_test.go`) against the fake issuer of
//! the testkit. The Go tests replace the account link with a fake. Here the failure cases
//! run without a database (they never reach it), and the cases that link an account start
//! with `pg_` and use `#[sqlx::test]` with the migrations of `tests/it/migrations`.
//! `TestOIDCRedirect` is the unit test `oidc_redirect_table` in `src/auth/oidc/`.

use std::collections::HashMap;
use std::io;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Router;
use axum::http::StatusCode;
use spinup_kit::auth::oidc::fake_issuer::{CLIENT_ID, CLIENT_SECRET, FakeIssuer, Knobs};
use spinup_kit::auth::oidc::{self, OIDC_LIFETIME, OidcOptions, Provider};
use spinup_kit::auth::password::{self, Params};
use spinup_kit::auth::{Auth, COOKIE_NAME, Options};
use spinup_kit::testkit::http::{Answer, Req, send};
use sqlx::PgPool;
use time::OffsetDateTime;

const GOOD: &str = "correct horse battery";
const ORIGIN: &str = "http://app.test";
const INVALID: &str = "The sign-in response is invalid, expired, or has no verified email.\nTry: start sign-in again with a verified email.";

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
    /// The real time: the fake issuer signs tokens with the real time.
    fn new() -> Self {
        Self(Arc::new(Mutex::new(OffsetDateTime::now_utc())))
    }
    fn add(&self, d: Duration) {
        *self.0.lock().unwrap() += d;
    }
    fn now(&self) -> OffsetDateTime {
        *self.0.lock().unwrap()
    }
}

fn env_of(pairs: &[(&str, &str)]) -> HashMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| ((*k).to_owned(), (*v).to_owned()))
        .collect()
}

/// Options for tests: no rate limit, no Secure cookie, test parameters, the test clock,
/// and no cleanup.
fn auth_options(clock: &TestClock) -> Options {
    let env = env_of(&[("AUTH_RATE_LIMIT", "off")]);
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

fn no_files(_: &Path) -> io::Result<Vec<u8>> {
    Err(io::ErrorKind::NotFound.into())
}

fn http() -> reqwest::Client {
    reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(5))
        .build()
        .unwrap()
}

/// The OIDC settings of the provider `test` at `issuer`.
fn test_options(issuer: &str) -> OidcOptions {
    let env = env_of(&[
        ("AUTH_OIDC_PROVIDERS", "test"),
        ("PUBLIC_ORIGIN", ORIGIN),
        ("AUTH_OIDC_TEST_ISSUER", issuer),
        ("AUTH_OIDC_TEST_CLIENT_ID", CLIENT_ID),
        ("AUTH_OIDC_TEST_CLIENT_SECRET", CLIENT_SECRET),
        ("AUTH_OIDC_TEST_LABEL", "Test provider"),
    ]);
    let mut o = OidcOptions::from_env(|k| env.get(k).cloned(), no_files);
    assert!(o.error.is_none(), "{:?}", o.error);
    o.http = Some(http());
    o
}

struct Fixture {
    auth: Auth,
    app: Router,
    clock: TestClock,
}

/// The auth routes and the OIDC routes inside the auth layer.
fn fixture(pool: PgPool, o: OidcOptions) -> Fixture {
    let clock = TestClock::new();
    let auth = Auth::new(pool, auth_options(&clock)).unwrap();
    let api = auth.routes().merge(oidc::routes(&auth, o));
    let app = auth
        .layer(api)
        .layer(axum::middleware::from_fn(spinup_kit::httpx::request_id));
    Fixture { auth, app, clock }
}

/// A started flow: the flow cookie (`oidc_test=...`) and the callback path with the code
/// and the state.
struct Flow {
    cookie: String,
    callback: String,
}

/// Starts a flow and follows the authorization URL as a browser would (Go `app.start`).
async fn start(app: &Router, fake: &FakeIssuer, next: &str) -> Flow {
    let path = format!(
        "/api/auth/oidc/test/start?next={}",
        form_urlencoded::byte_serialize(next.as_bytes()).collect::<String>()
    );
    let a = send(app, Req::get(path)).await;
    assert_eq!(a.status, StatusCode::SEE_OTHER, "start: {}", a.body);
    let line = a.set_cookie("oidc_test").expect("a flow cookie");
    for want in [
        "HttpOnly",
        "SameSite=Lax",
        "Max-Age=600",
        "Path=/api/auth/oidc/test/",
        "Expires=",
    ] {
        assert!(line.contains(want), "unsafe cookie: {line}");
    }
    assert!(!line.contains("Secure"), "{line}");
    assert!(a.set_cookie(COOKIE_NAME).is_none(), "start set a session");
    let location = a.header("location").to_owned();
    for want in [
        "code_challenge_method=S256",
        "response_type=code",
        "nonce=",
        "state=",
        "scope=openid+email+profile",
    ] {
        assert!(location.contains(want), "{location}");
    }
    let cookie = line.split(';').next().unwrap().to_owned();
    let callback = fake.authorize(&location).await.unwrap();
    let callback = callback
        .strip_prefix(ORIGIN)
        .expect("the callback URL of PUBLIC_ORIGIN")
        .to_owned();
    Flow { cookie, callback }
}

async fn callback(app: &Router, flow: &Flow, session: Option<&str>) -> Answer {
    let mut cookies = flow.cookie.clone();
    if let Some(s) = session {
        cookies = format!("{cookies}; {COOKIE_NAME}={s}");
    }
    let mut r = Req::get(flow.callback.clone());
    if !cookies.is_empty() {
        r = r.cookie(cookies);
    }
    send(app, r).await
}

/// Checks a refused callback: 400 with the Go text, no session, and the flow cookie
/// removed.
fn assert_refused(a: &Answer, name: &str) {
    assert_eq!(a.status, StatusCode::BAD_REQUEST, "{name}: {}", a.body);
    assert_eq!(a.problem().detail.as_deref(), Some(INVALID), "{name}");
    assert!(
        a.cookie_value(COOKIE_NAME).is_none(),
        "{name}: a session started"
    );
    let removal = a.set_cookie("oidc_test").expect("the flow cookie removal");
    assert!(removal.contains("Max-Age=0"), "{name}: {removal}");
}

async fn session_email(app: &Router, token: &str) -> Option<String> {
    let a = send(
        app,
        Req::get("/api/session").cookie(format!("{COOKIE_NAME}={token}")),
    )
    .await;
    (a.status == StatusCode::OK).then(|| a.json()["user"]["email"].as_str().unwrap().to_owned())
}

/// A change of a started flow, the fake issuer, or the clock.
type Mutation = fn(&mut Flow, &mut Knobs, &TestClock);

// TestOIDCFlow, the refused cases. They never reach the database.
#[tokio::test]
async fn oidc_flow_refusals() {
    let fake = FakeIssuer::start().await;
    let f = fixture(lazy_pool(), test_options(fake.url()));
    let cases: Vec<(&str, Mutation)> = vec![
        ("state mismatch", |fl, _, _| {
            fl.callback = fl.callback.replace("state=", "state=wrong")
        }),
        ("nonce mismatch", |_, k, _| k.nonce = Some("wrong".into())),
        ("unverified email", |_, k, _| k.unverified = true),
        ("wrong issuer", |_, k, _| {
            k.issuer = Some("https://evil.test".into())
        }),
        ("wrong audience", |_, k, _| {
            k.audience = Some("another-client".into())
        }),
        ("expired token", |_, k, _| k.expired = true),
        ("missing ID token", |_, k, _| k.missing_token = true),
        ("invalid signature", |_, k, _| k.bad_signature = true),
        ("invalid email", |_, k, _| {
            k.email = Some("not an email".into())
        }),
        ("empty subject", |_, k, _| k.subject = Some(String::new())),
        ("tampered cookie", |fl, _, _| {
            fl.cookie = "oidc_test=invalid".into()
        }),
        ("no cookie", |fl, _, _| fl.cookie.clear()),
        ("expired cookie", |_, _, clock| clock.add(OIDC_LIFETIME)),
        ("provider error", |fl, _, _| {
            fl.callback.push_str("&error=access_denied")
        }),
        ("two codes", |fl, _, _| fl.callback.push_str("&code=again")),
        ("two states", |fl, _, _| {
            fl.callback.push_str("&state=again")
        }),
    ];
    for (name, mutate) in cases {
        let mut flow = start(&f.app, &fake, "/account?tab=profile").await;
        let mut knobs = Knobs::default();
        mutate(&mut flow, &mut knobs, &f.clock);
        fake.set(|k| *k = knobs);
        let a = callback(&f.app, &flow, None).await;
        assert_refused(&a, name);
    }
    assert_eq!(fake.discoveries(), 1, "discovery not cached");
    f.auth.close().await;
}

// TestOIDCEmailVerifiedClaim, the refused values.
#[tokio::test]
async fn oidc_email_verified_refusals() {
    let fake = FakeIssuer::start().await;
    let f = fixture(lazy_pool(), test_options(fake.url()));
    for raw in [
        "false",
        r#""false""#,
        r#""TRUE""#,
        r#"" true ""#,
        r#""""#,
        "1",
        "null",
        "{}",
        "[true]",
    ] {
        let v: serde_json::Value = serde_json::from_str(raw).unwrap();
        fake.set(move |k| k.email_verified = Some(v));
        let flow = start(&f.app, &fake, "/account").await;
        let a = callback(&f.app, &flow, None).await;
        assert_refused(&a, raw);
    }
    f.auth.close().await;
}

// TestOIDCOpenRedirectAndProviderDown.
#[tokio::test]
async fn oidc_open_redirect_and_provider_down() {
    let fake = FakeIssuer::start().await;
    let f = fixture(lazy_pool(), test_options(fake.url()));
    let long = format!("/{}", "a".repeat(1024));
    for next in [
        "//evil.test",
        "https://evil.test",
        "/\\evil.test",
        "/%2fevil.test",
        "relative",
        long.as_str(),
    ] {
        let path = format!(
            "/api/auth/oidc/test/start?next={}",
            form_urlencoded::byte_serialize(next.as_bytes()).collect::<String>()
        );
        let a = send(&f.app, Req::get(path)).await;
        assert_eq!(a.status, StatusCode::BAD_REQUEST, "redirect {next}");
    }
    assert_eq!(
        fake.discoveries(),
        0,
        "redirect validation performed discovery"
    );
    let unknown = send(&f.app, Req::get("/api/auth/oidc/other/start")).await;
    assert_eq!(unknown.status, StatusCode::NOT_FOUND);
    assert!(unknown.body.contains("Try: choose a configured provider."));
    // The construction does not discover. A down provider fails only its own start.
    fake.close();
    tokio::time::sleep(Duration::from_millis(50)).await;
    let a = send(&f.app, Req::get("/api/auth/oidc/test/start")).await;
    assert_eq!(a.status, StatusCode::SERVICE_UNAVAILABLE, "{}", a.body);
    assert!(a.body.contains("Try:"), "{}", a.body);
    assert!(a.set_cookie("oidc_test").is_none());
    f.auth.close().await;
}

// TestOIDCEnvironment.
#[test]
fn oidc_environment() {
    let mut env = env_of(&[
        ("AUTH_OIDC_ACME_ISSUER", "https://issuer.example/tenant"),
        ("AUTH_OIDC_ACME_CLIENT_ID", "client"),
        ("AUTH_OIDC_ACME_CLIENT_SECRET_FILE", "/secrets/acme"),
        ("AUTH_OIDC_ACME_SCOPES", "groups,email offline_access"),
        ("AUTH_OIDC_ACME_LABEL", "Acme"),
    ]);
    let read = |p: &Path| -> io::Result<Vec<u8>> {
        assert_eq!(p, Path::new("/secrets/acme"));
        Ok(b"from-file\n".to_vec())
    };
    let p = Provider::from_env("acme", |k| env.get(k).cloned(), read);
    assert!(p.error.is_none(), "{:?}", p.error);
    assert_eq!(p.client_secret, "from-file");
    assert_eq!(p.label, "Acme");
    assert_eq!(
        p.scopes.join(","),
        "openid,email,profile,groups,offline_access"
    );
    assert!(
        !format!("{p:?}").contains("from-file"),
        "Debug shows the secret"
    );
    env.insert("AUTH_OIDC_ACME_CLIENT_SECRET".into(), "conflict".into());
    let p = Provider::from_env("acme", |k| env.get(k).cloned(), read);
    assert!(p.error.is_some(), "ambiguous secret accepted");
    assert!(!p.error.unwrap().contains("conflict"));
    // A missing or invalid issuer, and a missing client ID.
    for (key, value) in [
        ("AUTH_OIDC_ACME_ISSUER", ""),
        ("AUTH_OIDC_ACME_ISSUER", "ftp://issuer.example"),
        ("AUTH_OIDC_ACME_ISSUER", "https://issuer.example?x=1"),
        ("AUTH_OIDC_ACME_CLIENT_ID", ""),
    ] {
        let mut e = env_of(&[
            ("AUTH_OIDC_ACME_ISSUER", "https://issuer.example"),
            ("AUTH_OIDC_ACME_CLIENT_ID", "client"),
        ]);
        e.insert(key.into(), value.into());
        let p = Provider::from_env("acme", |k| e.get(k).cloned(), no_files);
        assert!(p.error.is_some(), "{key}={value} accepted");
        assert_eq!(p.label, "acme");
    }
}

// TestOIDCRoutesEnvironment: a missing issuer affects only its provider, without
// discovery at the start.
#[tokio::test]
async fn oidc_routes_environment() {
    let env = env_of(&[
        ("AUTH_OIDC_PROVIDERS", "acme, other"),
        ("PUBLIC_ORIGIN", ORIGIN),
        (
            "AUTH_OIDC_COOKIE_KEY",
            "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
        ),
        ("AUTH_OIDC_ACME_ISSUER", "https://issuer.example"),
        ("AUTH_OIDC_ACME_CLIENT_ID", "client"),
        ("AUTH_OIDC_ACME_LABEL", "Acme"),
    ]);
    let o = OidcOptions::from_env(|k| env.get(k).cloned(), no_files);
    assert!(o.error.is_none(), "{:?}", o.error);
    assert_eq!(o.cookie_key, Some([0u8; 32]));
    let f = fixture(lazy_pool(), o);
    for (path, status) in [
        ("/api/auth/oidc/providers", StatusCode::OK),
        (
            "/api/auth/oidc/other/start",
            StatusCode::SERVICE_UNAVAILABLE,
        ),
        (
            "/api/auth/oidc/other/callback",
            StatusCode::SERVICE_UNAVAILABLE,
        ),
        ("/api/auth/oidc/unknown/start", StatusCode::NOT_FOUND),
    ] {
        let a = send(&f.app, Req::get(path)).await;
        assert_eq!(a.status, status, "{path}: {}", a.body);
        assert_eq!(a.header("cache-control"), "no-store", "{path}");
        if status == StatusCode::OK {
            assert_eq!(
                a.json(),
                serde_json::json!([
                    {"name": "acme", "label": "Acme"},
                    {"name": "other", "label": "other"}
                ])
            );
            assert!(!a.body.contains("client"), "{}", a.body);
        }
    }
    f.auth.close().await;
}

// The configuration errors of Go `oidcRoutes`: every provider route answers 503.
#[tokio::test]
async fn oidc_configuration_errors_answer_503() {
    let base = [
        ("AUTH_OIDC_PROVIDERS", "test"),
        ("PUBLIC_ORIGIN", ORIGIN),
        ("AUTH_OIDC_TEST_ISSUER", "https://issuer.test"),
        ("AUTH_OIDC_TEST_CLIENT_ID", "x"),
    ];
    for (key, value, secure) in [
        ("AUTH_OIDC_PROVIDERS", "test,Bad", false),
        ("AUTH_OIDC_PROVIDERS", "test,test", false),
        ("AUTH_OIDC_COOKIE_KEY", "c2hvcnQ", false),
        ("PUBLIC_ORIGIN", "http://app.test/path", false),
        // Secure cookies need an https origin.
        ("PUBLIC_ORIGIN", ORIGIN, true),
    ] {
        let mut env = env_of(&base);
        env.insert(key.into(), value.into());
        let o = OidcOptions::from_env(|k| env.get(k).cloned(), no_files);
        let clock = TestClock::new();
        let auth = Auth::new(
            lazy_pool(),
            Options {
                secure,
                ..auth_options(&clock)
            },
        )
        .unwrap();
        let app = auth.layer(oidc::routes(&auth, o));
        let a = send(&app, Req::get("/api/auth/oidc/test/start")).await;
        assert_eq!(a.status, StatusCode::SERVICE_UNAVAILABLE, "{key}={value}");
        assert!(
            a.body.contains("Try: check AUTH_OIDC settings"),
            "{}",
            a.body
        );
        auth.close().await;
    }
}

// Without providers, the routes are absent (Go `oidcRoutes` returns early).
#[tokio::test]
async fn oidc_without_providers_has_no_routes() {
    let o = OidcOptions::from_env(|_| None, no_files);
    assert!(o.providers.is_empty() && o.error.is_none());
    assert_eq!(o.public_origin, "http://localhost:5173");
    let clock = TestClock::new();
    let auth = Auth::new(lazy_pool(), auth_options(&clock)).unwrap();
    let app = oidc::routes(&auth, o);
    let a = send(&app, Req::get("/api/auth/oidc/providers")).await;
    assert_eq!(a.status, StatusCode::NOT_FOUND);
    auth.close().await;
}

// The routes answer 500 without the auth layer, as Go `endpoint` does.
#[tokio::test]
async fn oidc_routes_need_the_auth_layer() {
    let fake = FakeIssuer::start().await;
    let clock = TestClock::new();
    let auth = Auth::new(lazy_pool(), auth_options(&clock)).unwrap();
    let app = oidc::routes(&auth, test_options(fake.url()));
    let a = send(&app, Req::get("/api/auth/oidc/test/start")).await;
    assert_eq!(a.status, StatusCode::INTERNAL_SERVER_ERROR);
    auth.close().await;
}

// TestOIDCFlow, the success case: a linked user, a session, the return path, and a
// replay that fails.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_flow_success(pool: PgPool) {
    let fake = FakeIssuer::start().await;
    let f = fixture(pool.clone(), test_options(fake.url()));
    let flow = start(&f.app, &fake, "/account?tab=profile").await;
    let ok = callback(&f.app, &flow, None).await;
    assert_eq!(ok.status, StatusCode::SEE_OTHER, "{}", ok.body);
    assert_eq!(ok.header("location"), "/account?tab=profile");
    assert!(ok.set_cookie("oidc_test").unwrap().contains("Max-Age=0"));
    let session = ok.cookie_value(COOKIE_NAME).expect("a session");
    assert_eq!(
        session_email(&f.app, &session).await.as_deref(),
        Some("person@example.com")
    );
    let replay = callback(&f.app, &flow, None).await;
    assert_refused(&replay, "code replay");
    assert_eq!(fake.discoveries(), 1, "discovery not cached");
    assert_eq!(fake.token_requests(), 2);
    let users: i64 = sqlx::query_scalar("SELECT count(*) FROM users")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(users, 1);
    f.auth.close().await;
}

// TestOIDCEmailVerifiedClaim, the accepted values.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_email_verified_accepted(pool: PgPool) {
    let fake = FakeIssuer::start().await;
    let f = fixture(pool, test_options(fake.url()));
    for raw in ["true", r#""true""#] {
        let v: serde_json::Value = serde_json::from_str(raw).unwrap();
        fake.set(move |k| k.email_verified = Some(v));
        let flow = start(&f.app, &fake, "/account").await;
        let a = callback(&f.app, &flow, None).await;
        assert_eq!(a.status, StatusCode::SEE_OTHER, "{raw}: {}", a.body);
        assert!(a.cookie_value(COOKIE_NAME).is_some(), "{raw}");
    }
    f.auth.close().await;
}

async fn register(app: &Router, email: &str) -> String {
    let body = serde_json::json!({"email": email, "password": GOOD}).to_string();
    let a = send(app, Req::post("/api/auth/register").json(body)).await;
    assert_eq!(
        a.status,
        StatusCode::CREATED,
        "register {email}: {}",
        a.body
    );
    a.cookie_value(COOKIE_NAME).expect("a session cookie")
}

async fn login(app: &Router, email: &str) -> Answer {
    let body = serde_json::json!({"email": email, "password": GOOD}).to_string();
    send(app, Req::post("/api/auth/login").json(body)).await
}

async fn password_hash(pool: &PgPool, email: &str) -> Option<String> {
    sqlx::query_scalar("SELECT password_hash FROM users WHERE email = $1")
        .bind(email)
        .fetch_one(pool)
        .await
        .unwrap()
}

// TestOIDCAccounts.
async fn accounts(pool: PgPool, existing: bool) {
    let fake = FakeIssuer::start().await;
    let f = fixture(pool.clone(), test_options(fake.url()));
    let (mut old, mut attacker, mut unrelated, mut existing_id) = (None, None, None, None);
    if existing {
        old = Some(register(&f.app, "person@example.com").await);
        let l = login(&f.app, "person@example.com").await;
        assert_eq!(l.status, StatusCode::OK, "attacker login: {}", l.body);
        attacker = l.cookie_value(COOKIE_NAME);
        unrelated = Some(register(&f.app, "unrelated@example.com").await);
        existing_id = Some(
            sqlx::query_scalar::<_, i64>("SELECT id FROM users WHERE email = 'person@example.com'")
                .fetch_one(&pool)
                .await
                .unwrap(),
        );
    }
    let flow = start(&f.app, &fake, "/account").await;
    let a = callback(&f.app, &flow, old.as_deref()).await;
    assert_eq!(a.status, StatusCode::SEE_OTHER, "callback: {}", a.body);
    let (id, hash): (i64, Option<String>) = sqlx::query_as(
        "SELECT u.id, u.password_hash FROM users u JOIN user_identities i ON i.user_id = u.id WHERE i.provider = 'test' AND i.subject = 'subject-1'",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    if let Some(e) = existing_id {
        assert_eq!(id, e, "existing account not preserved");
    }
    assert!(hash.is_none(), "linked account has a password");
    let session = a.cookie_value(COOKIE_NAME).expect("no session");
    assert_eq!(
        session_email(&f.app, &session).await.as_deref(),
        Some("person@example.com")
    );
    if existing {
        for token in [old.as_deref().unwrap(), attacker.as_deref().unwrap()] {
            assert!(
                session_email(&f.app, token).await.is_none(),
                "old session remains valid"
            );
        }
        assert!(
            session_email(&f.app, unrelated.as_deref().unwrap())
                .await
                .is_some(),
            "unrelated session removed"
        );
    }
    // An email change never moves a known subject to another account.
    fake.set(|k| k.email = Some("different@example.com".into()));
    let flow = start(&f.app, &fake, "/account").await;
    let again = callback(&f.app, &flow, None).await;
    assert_eq!(again.status, StatusCode::SEE_OTHER, "{}", again.body);
    let s = again.cookie_value(COOKIE_NAME).unwrap();
    assert_eq!(
        session_email(&f.app, &s).await.as_deref(),
        Some("person@example.com"),
        "identity changed"
    );
    let l = login(&f.app, "person@example.com").await;
    assert_eq!(
        l.status,
        StatusCode::UNAUTHORIZED,
        "passwordless login: {}",
        l.body
    );
    assert_eq!(
        l.problem().detail.as_deref(),
        Some("The email address or password is incorrect.")
    );
    f.auth.close().await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_accounts_new(pool: PgPool) {
    accounts(pool, false).await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_accounts_existing(pool: PgPool) {
    accounts(pool, true).await;
}

// TestOIDCUnverifiedEmailNotLinked.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_unverified_email_not_linked(pool: PgPool) {
    let fake = FakeIssuer::start().await;
    let f = fixture(pool.clone(), test_options(fake.url()));
    let token = register(&f.app, "person@example.com").await;
    fake.set(|k| {
        k.subject = Some("attacker".into());
        k.unverified = true;
    });
    let flow = start(&f.app, &fake, "/account").await;
    let a = callback(&f.app, &flow, None).await;
    assert_refused(&a, "unverified email");
    let ids: i64 = sqlx::query_scalar("SELECT count(*) FROM user_identities")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(ids, 0, "unverified email linked");
    assert!(password_hash(&pool, "person@example.com").await.is_some());
    assert!(session_email(&f.app, &token).await.is_some());
    f.auth.close().await;
}

// TestOIDCConcurrentLink.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_concurrent_link(pool: PgPool) {
    let fake = Arc::new(FakeIssuer::start().await);
    let f = fixture(pool.clone(), test_options(fake.url()));
    let mut flows = Vec::new();
    for _ in 0..8 {
        flows.push(start(&f.app, &fake, "/account").await);
    }
    let tasks: Vec<_> = flows
        .into_iter()
        .map(|flow| {
            let app = f.app.clone();
            tokio::spawn(async move { callback(&app, &flow, None).await })
        })
        .collect();
    for t in tasks {
        let a = t.await.unwrap();
        assert_eq!(a.status, StatusCode::SEE_OTHER, "{}", a.body);
    }
    let (users, identities): (i64, i64) = sqlx::query_as(
        "SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM user_identities)",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!((users, identities), (1, 1));
    f.auth.close().await;
}

// TestOIDCKnownIdentityPreservesCredentials.
#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_known_identity_preserves_credentials(pool: PgPool) {
    let fake = FakeIssuer::start().await;
    let f = fixture(pool.clone(), test_options(fake.url()));
    let flow = start(&f.app, &fake, "/account").await;
    assert_eq!(
        callback(&f.app, &flow, None).await.status,
        StatusCode::SEE_OTHER
    );
    // A password reset can restore a password after the first link.
    let hash = password::hash_password(&TEST_PARAMS, GOOD);
    sqlx::query("UPDATE users SET password_hash = $1 WHERE email = 'person@example.com'")
        .bind(&hash)
        .execute(&pool)
        .await
        .unwrap();
    let l = login(&f.app, "person@example.com").await;
    assert_eq!(l.status, StatusCode::OK, "password login: {}", l.body);
    let session = l.cookie_value(COOKIE_NAME).unwrap();
    fake.set(|k| k.email = Some("different@example.com".into()));
    let flow = start(&f.app, &fake, "/account").await;
    let again = callback(&f.app, &flow, None).await;
    assert_eq!(again.status, StatusCode::SEE_OTHER, "{}", again.body);
    assert_eq!(
        password_hash(&pool, "person@example.com").await.as_deref(),
        Some(hash.as_str()),
        "the known identity removed the restored password"
    );
    assert!(
        session_email(&f.app, &session).await.is_some(),
        "the known identity removed another session"
    );
    f.auth.close().await;
}

// TestOIDCLinkRollback.
async fn rollback(pool: PgPool, sql: &str) {
    let fake = FakeIssuer::start().await;
    let f = fixture(pool.clone(), test_options(fake.url()));
    let token = register(&f.app, "person@example.com").await;
    let original = password_hash(&pool, "person@example.com").await;
    sqlx::raw_sql(sqlx::AssertSqlSafe(sql.to_owned()))
        .execute(&pool)
        .await
        .unwrap();
    fake.set(|k| k.subject = Some("rejected".into()));
    let flow = start(&f.app, &fake, "/account").await;
    let a = callback(&f.app, &flow, None).await;
    assert_eq!(
        a.status,
        StatusCode::INTERNAL_SERVER_ERROR,
        "the link succeeded despite a store failure"
    );
    assert!(a.cookie_value(COOKIE_NAME).is_none());
    assert_eq!(
        password_hash(&pool, "person@example.com").await,
        original,
        "the failed link changed the password"
    );
    assert!(
        session_email(&f.app, &token).await.is_some(),
        "the failed link removed the session"
    );
    let ids: i64 = sqlx::query_scalar("SELECT count(*) FROM user_identities")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(ids, 0, "the failed link kept an identity");
    f.auth.close().await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_link_rollback_identity_insert(pool: PgPool) {
    rollback(
        pool,
        "ALTER TABLE user_identities ADD CHECK (subject <> 'rejected')",
    )
    .await;
}

#[sqlx::test(migrations = "tests/it/migrations")]
async fn pg_oidc_link_rollback_session_deletion(pool: PgPool) {
    rollback(
        pool,
        "CREATE FUNCTION reject_session_delete() RETURNS trigger LANGUAGE plpgsql AS $$ \
         BEGIN RAISE EXCEPTION 'session deletion failed'; END; $$; \
         CREATE TRIGGER reject_session_delete BEFORE DELETE ON sessions FOR EACH ROW \
         EXECUTE FUNCTION reject_session_delete()",
    )
    .await;
}
