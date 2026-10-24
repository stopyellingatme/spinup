//! A replay of `testdata/script/auth_ratelimit.txtar` against the Rust
//! binary: the same environment, the same requests, and the same expected
//! texts. The Go script uses `bin/api` and curl. This test runs the
//! `todo-api` binary on a test database and sends the requests with reqwest.

use std::process::{Command, Stdio};
use std::time::Duration;

use sqlx::PgPool;

const BIN: &str = env!("CARGO_BIN_EXE_todo-api");

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

/// Runs the binary with only `env` and returns (success, stderr).
fn start_fails(env: &[(&str, &str)]) -> (bool, String) {
    let out = Command::new(BIN)
        .env_clear()
        .envs(env.iter().copied())
        .output()
        .unwrap();
    (
        out.status.success(),
        String::from_utf8_lossy(&out.stderr).into_owned(),
    )
}

#[test]
fn bad_values_stop_the_start_with_a_try_line() {
    let port = free_port().to_string();
    let base = [
        ("SPINUP_ENV", "dev"),
        ("PORT", port.as_str()),
        ("DATABASE_URL", "postgres://nobody@127.0.0.1:1/none"),
    ];
    // env AUTH_RATE_LIMIT_LOGIN=burst=0
    let mut env = base.to_vec();
    env.push(("AUTH_RATE_LIMIT_LOGIN", "burst=0"));
    let (ok, stderr) = start_fails(&env);
    assert!(!ok);
    assert!(
        stderr.contains(r#"AUTH_RATE_LIMIT_LOGIN "burst=0" is not valid"#),
        "{stderr}"
    );
    assert!(
        stderr.contains("Try: set AUTH_RATE_LIMIT_LOGIN=burst=20,every=3s"),
        "{stderr}"
    );
    // env AUTH_RATE_LIMIT_LOGIN=burst=2,every=1h and AUTH_TRUSTED_PROXIES=bad-proxy
    let mut env = base.to_vec();
    env.push(("AUTH_RATE_LIMIT_LOGIN", "burst=2,every=1h"));
    env.push(("AUTH_TRUSTED_PROXIES", "bad-proxy"));
    let (ok, stderr) = start_fails(&env);
    assert!(!ok);
    assert!(
        stderr.contains(r#"AUTH_TRUSTED_PROXIES item "bad-proxy""#),
        "{stderr}"
    );
    assert!(
        stderr.contains("Try: use items such as private"),
        "{stderr}"
    );
}

/// The URL of the test database of `pool`, from DATABASE_URL.
fn database_url(pool: &PgPool) -> String {
    let base = std::env::var("DATABASE_URL").expect("DATABASE_URL");
    let db = pool.connect_options().get_database().unwrap().to_owned();
    let (head, query) = match base.split_once('?') {
        Some((h, q)) => (h.to_owned(), format!("?{q}")),
        None => (base.clone(), String::new()),
    };
    let root = &head[..head.rfind('/').unwrap()];
    format!("{root}/{db}{query}")
}

struct Server(std::process::Child);

impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

#[sqlx::test]
async fn each_client_has_its_own_login_budget(pool: PgPool) {
    let port = free_port();
    let child = Command::new(BIN)
        .env_clear()
        .env("SPINUP_ENV", "dev")
        .env("PORT", port.to_string())
        .env("DATABASE_URL", database_url(&pool))
        .env("AUTH_RATE_LIMIT_LOGIN", "burst=2,every=1h")
        .env("AUTH_TRUSTED_PROXIES", "")
        .env("LOG_LEVEL", "error")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let _server = Server(child);
    let base = format!("http://127.0.0.1:{port}");
    let http = reqwest::Client::new();
    let mut up = false;
    for _ in 0..300 {
        if let Ok(r) = http.get(format!("{base}/healthz")).send().await
            && r.status() == 200
        {
            up = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert!(up, "the server did not answer /healthz");

    let login = |email: &str, xff: Option<&str>| {
        let mut r = http
            .post(format!("{base}/api/auth/login"))
            .header("content-type", "application/json")
            .body(format!(
                r#"{{"email":"{email}","password":"correct horse battery"}}"#
            ));
        if let Some(x) = xff {
            r = r.header("x-forwarded-for", x);
        }
        r.send()
    };

    // Two logins pass the limit and fail the password check. The third gets 429.
    assert_eq!(
        login("a1@example.com", Some("198.51.100.1"))
            .await
            .unwrap()
            .status(),
        401
    );
    assert_eq!(
        login("a2@example.com", Some("198.51.100.1"))
            .await
            .unwrap()
            .status(),
        401
    );
    let limited = login("a3@example.com", Some("198.51.100.1")).await.unwrap();
    assert_eq!(limited.status(), 429);
    assert_eq!(limited.headers()["retry-after"], "3600");
    let body = limited.text().await.unwrap();
    assert!(body.contains("Try again in 3600 seconds"), "{body}");

    // Another client has its own budget.
    assert_eq!(
        login("b1@example.com", Some("198.51.100.2"))
            .await
            .unwrap()
            .status(),
        401
    );

    // A forged entry on the left does not give the client a new budget.
    assert_eq!(
        login("a4@example.com", Some("10.0.0.9, 198.51.100.1"))
            .await
            .unwrap()
            .status(),
        429
    );

    // A local request without a client address (curl, a health check) has no limit.
    for email in ["c1@example.com", "c2@example.com", "c3@example.com"] {
        assert_eq!(login(email, None).await.unwrap().status(), 401);
    }

    // The session route has no limit.
    let s = http
        .get(format!("{base}/api/session"))
        .header("x-forwarded-for", "198.51.100.1")
        .send()
        .await
        .unwrap();
    assert_eq!(s.status(), 401);
}
