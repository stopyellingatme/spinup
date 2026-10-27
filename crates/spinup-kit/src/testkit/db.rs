//! Test databases (Go `kit/testkit/database.go` and the skip rule of `server.go`).
//!
//! Each test gets its own new database on the server of `DATABASE_URL`. The database is
//! dropped when the [`TestDb`] is dropped. Without `DATABASE_URL`, [`db`] prints
//! `skip: DATABASE_URL is not set` and returns `None`, so the test passes without a
//! database. With `CI` set, it panics instead.
//!
//! ```no_run
//! # async fn demo() {
//! let Some(db) = spinup_kit::testkit::db::db().await else { return };
//! sqlx::query("CREATE TABLE t (id int)").execute(db.pool()).await.unwrap();
//! # }
//! ```

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use sqlx::migrate::Migrator;
use sqlx::postgres::PgConnectOptions;
use sqlx::{AssertSqlSafe, Connection, PgConnection, PgPool};

use crate::db::{PoolOptions, open};

/// PostgreSQL NAMEDATALEN - 1.
pub(crate) const MAX_IDENT_LEN: usize = 63;
/// The bytes of the test name in a database name.
const MAX_NAME_PART: usize = 32;
const CLONE_ATTEMPTS: u32 = 50;
const CLONE_BACKOFF: Duration = Duration::from_millis(100);

/// The skip line without `DATABASE_URL`.
pub const SKIP_LINE: &str = "skip: DATABASE_URL is not set";

static DB_COUNTER: AtomicU64 = AtomicU64::new(0);

/// A database for one test. Drop it to drop the database.
#[derive(Debug)]
pub struct TestDb {
    pool: PgPool,
    url: String,
    name: String,
    server: String,
}

impl TestDb {
    /// The pool on the database. It has the defaults of [`crate::db::open`].
    pub fn pool(&self) -> &PgPool {
        &self.pool
    }

    /// The connection URL of the database (Go `testkit.URL`).
    pub fn url(&self) -> &str {
        &self.url
    }

    /// The name of the database, such as `t_db_pg_create_42_1`.
    pub fn name(&self) -> &str {
        &self.name
    }
}

impl Drop for TestDb {
    /// Drops the database `WITH (FORCE)`, which closes the connections of the pool. It runs
    /// on its own thread and runtime, because a drop cannot wait for a future.
    fn drop(&mut self) {
        let server = std::mem::take(&mut self.server);
        let name = std::mem::take(&mut self.name);
        let joined = std::thread::spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .map_err(|e| e.to_string())?;
            runtime.block_on(async {
                let mut admin = admin(&server).await?;
                let sql = format!("DROP DATABASE IF EXISTS {} WITH (FORCE)", quote(&name));
                let r = sqlx::query(AssertSqlSafe(sql))
                    .execute(&mut admin)
                    .await
                    .map(|_| ())
                    .map_err(|e| format!("drop database {name}: {e}"));
                let _ = admin.close().await;
                r
            })
        })
        .join();
        match joined {
            Ok(Ok(())) => {}
            Ok(Err(e)) => eprintln!("testkit: {e}"),
            Err(_) => eprintln!("testkit: the drop of a test database panicked"),
        }
    }
}

/// Returns a new, empty database on the server of `DATABASE_URL` (Go `testkit.DB`). Without
/// `DATABASE_URL` it prints [`SKIP_LINE`] to stderr and returns `None`.
///
/// # Panics
///
/// When `DATABASE_URL` is not set and `CI` is set, or when the server fails.
pub async fn db() -> Option<TestDb> {
    db_with(env).await
}

/// [`db`] with the environment from `lookup`. Tests of the skip rule pass a map.
///
/// # Panics
///
/// See [`db`].
pub async fn db_with(lookup: impl Fn(&str) -> Option<String>) -> Option<TestDb> {
    let server = server_url(&lookup)?;
    Some(new_database(&server, None).await)
}

/// Returns a new database that holds the kit migrations and the migrations of `app`
/// (Go `testkit.DBWithMigrations`). The first call for a migration set builds a template
/// database. Each call then clones it, which is fast. Without `DATABASE_URL` it acts as
/// [`db`].
///
/// # Panics
///
/// See [`db`], and when a migration fails.
pub async fn db_with_migrations(app: &Migrator) -> Option<TestDb> {
    db_with_migrations_with(env, app).await
}

/// [`db_with_migrations`] with the environment from `lookup`.
///
/// # Panics
///
/// See [`db_with_migrations`].
pub async fn db_with_migrations_with(
    lookup: impl Fn(&str) -> Option<String>,
    app: &Migrator,
) -> Option<TestDb> {
    let server = server_url(&lookup)?;
    let template = super::template::ensure(&server, app).await;
    Some(new_database(&server, Some(&template)).await)
}

fn env(key: &str) -> Option<String> {
    std::env::var(key).ok()
}

/// Returns the server URL, or `None` after the skip line.
fn server_url(lookup: &impl Fn(&str) -> Option<String>) -> Option<String> {
    if let Some(url) = lookup("DATABASE_URL").filter(|u| !u.is_empty()) {
        return Some(url);
    }
    if lookup("CI").is_some_and(|v| !v.is_empty()) {
        panic!(
            "testkit: DATABASE_URL is not set (CI is set, so this is a failure)\nTry: export DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable"
        );
    }
    eprintln!("{SKIP_LINE}");
    None
}

/// Connects to the server URL. The error never shows the URL.
pub(crate) async fn admin(server: &str) -> Result<PgConnection, String> {
    let options: PgConnectOptions = server.parse().map_err(|_| {
        "DATABASE_URL is not valid (it is not shown because it can hold a password)\nTry: use the form postgres://user:password@host:5432/dbname?sslmode=disable".to_owned()
    })?;
    PgConnection::connect_with(&options)
        .await
        .map_err(|e| format!("connect to DATABASE_URL: {e}\nTry: check that the database runs"))
}

/// Creates a database, from `template` when it is set, and returns a pool on it.
async fn new_database(server: &str, template: Option<&str>) -> TestDb {
    let thread = std::thread::current();
    let test = match thread.name() {
        Some("main") | None => "test",
        Some(n) => n,
    };
    let name = db_name(
        test,
        std::process::id(),
        DB_COUNTER.fetch_add(1, Ordering::Relaxed) + 1,
    );
    let mut sql = format!("CREATE DATABASE {}", quote(&name));
    if let Some(t) = template {
        sql.push_str(&format!(" TEMPLATE {}", quote(t)));
    }
    let mut admin = admin(server)
        .await
        .unwrap_or_else(|e| panic!("testkit: {e}"));
    let created = exec_retry(&mut admin, &sql).await;
    let _ = admin.close().await;
    if let Err(e) = created {
        panic!("testkit: create database {name}: {e}");
    }
    let url = with_database(server, &name).unwrap_or_else(|e| panic!("testkit: {e}"));
    let pool = open(&url, PoolOptions::default()).unwrap_or_else(|e| panic!("testkit: {e}"));
    TestDb {
        pool,
        url,
        name,
        server: server.to_owned(),
    }
}

/// Runs `sql`. It retries while the template database is busy (`55006` object in use), for
/// example while another process clones it.
async fn exec_retry(conn: &mut PgConnection, sql: &str) -> Result<(), sqlx::Error> {
    let mut last = None;
    for _ in 0..CLONE_ATTEMPTS {
        match sqlx::query(AssertSqlSafe(sql.to_owned()))
            .execute(&mut *conn)
            .await
        {
            Ok(_) => return Ok(()),
            Err(e) => {
                let busy = e
                    .as_database_error()
                    .and_then(|d| d.code())
                    .is_some_and(|c| c == "55006");
                if !busy {
                    return Err(e);
                }
                last = Some(e);
                tokio::time::sleep(CLONE_BACKOFF).await;
            }
        }
    }
    Err(last.unwrap_or(sqlx::Error::PoolTimedOut))
}

/// Returns `t_`, the sanitized test name, the process ID, and a counter. The process ID
/// keeps names unique when several test processes share one server. The result is at most
/// 63 bytes.
pub fn db_name(test_name: &str, pid: u32, n: u64) -> String {
    let suffix = format!("_{pid}_{n}");
    let mut part = sanitize(test_name);
    let limit = MAX_NAME_PART.min(MAX_IDENT_LEN.saturating_sub(2 + suffix.len()));
    part.truncate(limit);
    format!("t_{part}{suffix}")
}

/// Lowercases `s` and maps every byte outside `[a-z0-9]` to `_`, with no repeated `_` and
/// none at either end.
fn sanitize(s: &str) -> String {
    let mut out = String::new();
    let mut under = false;
    for c in s.to_lowercase().chars() {
        if c.is_ascii_lowercase() || c.is_ascii_digit() {
            out.push(c);
            under = false;
        } else if !under && !out.is_empty() {
            out.push('_');
            under = true;
        }
    }
    if out.ends_with('_') {
        out.pop();
    }
    out
}

/// Returns `server` with its database set to `name`. Only the URL form is valid, because
/// sqlx reads no other form.
///
/// # Errors
///
/// A URL that does not parse. The error never shows the URL.
pub fn with_database(server: &str, name: &str) -> Result<String, String> {
    let invalid = || {
        "DATABASE_URL is not a valid URL (it is not shown because it can hold a password)"
            .to_owned()
    };
    if !(server.starts_with("postgres://") || server.starts_with("postgresql://")) {
        return Err(invalid());
    }
    let mut u = url::Url::parse(server).map_err(|_| invalid())?;
    u.set_path(&format!("/{name}"));
    Ok(u.to_string())
}

/// Quotes an identifier.
pub(crate) fn quote(ident: &str) -> String {
    format!("\"{}\"", ident.replace('"', "\"\""))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn testkit_db_name() {
        let cases = [
            ("TestTodos", 42, 1, "t_testtodos_42_1".to_owned()),
            (
                "TestTodos/create_one",
                42,
                7,
                "t_testtodos_create_one_42_7".to_owned(),
            ),
            (
                "Test--Weird  Chars!!",
                1,
                1,
                "t_test_weird_chars_1_1".to_owned(),
            ),
            ("/", 9, 3, "t__9_3".to_owned()),
            ("db::pg_create", 5, 2, "t_db_pg_create_5_2".to_owned()),
        ];
        for (test, pid, n, want) in cases {
            let got = db_name(test, pid, n);
            assert_eq!(got, want, "db_name({test:?})");
            assert!(got.len() <= MAX_IDENT_LEN);
        }
        let long = db_name(&"X".repeat(200), 4_194_304, 999_999_999);
        assert_eq!(long, format!("t_{}_4194304_999999999", "x".repeat(32)));
        let huge = db_name(&"X".repeat(200), u32::MAX, u64::MAX);
        assert!(huge.len() <= MAX_IDENT_LEN, "{huge}");
    }

    #[test]
    fn testkit_with_database() {
        let cases = [
            (
                "postgres://u:p@h:5432/app?sslmode=disable",
                "postgres://u:p@h:5432/t_x?sslmode=disable",
            ),
            ("postgresql://u@h/app", "postgresql://u@h/t_x"),
        ];
        for (input, want) in cases {
            assert_eq!(with_database(input, "t_x").as_deref(), Ok(want));
        }
        let err = with_database("host=h user=u password=s3cr3t dbname=app", "t_x")
            .err()
            .unwrap_or_default();
        assert!(
            err.contains("not a valid URL") && !err.contains("s3cr3t"),
            "{err}"
        );
    }

    #[test]
    fn testkit_quote() {
        assert_eq!(quote("t_x"), "\"t_x\"");
        assert_eq!(quote("a\"b"), "\"a\"\"b\"");
    }
}
