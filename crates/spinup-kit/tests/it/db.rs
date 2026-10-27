//! The `db` and `testkit` tests of the kit (Go `kit/db/*_test.go` and
//! `kit/testkit/testkit_test.go`). A test that needs Postgres has a name that starts with
//! `pg_`. It gets its own database from `spinup_kit::testkit::db`.

use std::borrow::Cow;
use std::collections::HashMap;
use std::time::Duration;

use axum::Router;
use axum::http::{HeaderMap, Method, StatusCode, Uri};
use axum::routing::any;
use spinup_kit::db::{
    self, KIT_MIGRATIONS_TABLE, Migrations, PgPool, PoolOptions, kit_migrator, migration_name,
};
use spinup_kit::httpx::ApiError;
use spinup_kit::testkit::db::{TestDb, db_with};
use spinup_kit::testkit::http::{Req, decode_json, send};
use sqlx::SqlSafeStr;
use sqlx::migrate::{Migration, MigrationType, Migrator};

// ---------------------------------------------------------------------------------------
// Helpers

/// A test database, or `None` (with the skip line) without `DATABASE_URL`.
async fn test_db() -> Option<TestDb> {
    spinup_kit::testkit::db::db().await
}

fn pair(
    version: i64,
    description: &'static str,
    up: &'static str,
    down: &'static str,
) -> [Migration; 2] {
    [
        Migration::new(
            version,
            Cow::Borrowed(description),
            MigrationType::ReversibleUp,
            up.into_sql_str(),
            false,
        ),
        Migration::new(
            version,
            Cow::Borrowed(description),
            MigrationType::ReversibleDown,
            down.into_sql_str(),
            false,
        ),
    ]
}

fn two() -> Vec<Migration> {
    let mut v = Vec::new();
    v.extend(pair(
        1,
        "users",
        "CREATE TABLE users (id bigint PRIMARY KEY, email text NOT NULL);",
        "DROP TABLE users;",
    ));
    v.extend(pair(
        2,
        "notes",
        "CREATE TABLE notes (id bigint PRIMARY KEY, body text NOT NULL);",
        "DROP TABLE notes;",
    ));
    v
}

fn three() -> Vec<Migration> {
    let mut v = two();
    v.extend(pair(
        3,
        "tags",
        "CREATE TABLE tags (id bigint PRIMARY KEY);",
        "DROP TABLE tags;",
    ));
    v
}

fn migrations(list: Vec<Migration>, url: &str) -> Migrations {
    Migrations::new(Migrator::with_migrations(list)).database_url(url)
}

async fn run(m: &Migrations, args: &[&str]) -> String {
    let args: Vec<String> = args.iter().map(|s| (*s).to_owned()).collect();
    let mut out = Vec::new();
    if let Err(e) = m.run(&args, &mut out).await {
        panic!(
            "migrate {args:?}: {e:#}\noutput:\n{}",
            String::from_utf8_lossy(&out)
        );
    }
    String::from_utf8(out).unwrap()
}

async fn run_err(m: &Migrations, args: &[&str]) -> String {
    let args: Vec<String> = args.iter().map(|s| (*s).to_owned()).collect();
    match m.run(&args, &mut Vec::new()).await {
        Ok(()) => panic!("migrate {args:?}: no error"),
        Err(e) => format!("{e:#}"),
    }
}

/// The names of the kit migrations.
fn kit_names() -> Vec<String> {
    kit_migrator()
        .iter()
        .filter(|m| m.migration_type.is_up_migration())
        .map(|m| migration_name(m.version, &m.description))
        .collect()
}

fn lines(state: &str, names: &[String]) -> String {
    names.iter().map(|n| format!("{state}  {n}\n")).collect()
}

fn names(list: &[&str]) -> Vec<String> {
    list.iter().map(|s| (*s).to_owned()).collect()
}

async fn show(pool: &PgPool, param: &str) -> String {
    sqlx::query_scalar(sqlx::AssertSqlSafe(format!("SHOW {param}")))
        .fetch_one(pool)
        .await
        .unwrap()
}

/// Waits until the pool has no connection in use.
async fn wait_idle(pool: &PgPool) -> usize {
    for _ in 0..40 {
        let busy = pool.size() as usize - pool.num_idle();
        if busy == 0 {
            return 0;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    pool.size() as usize - pool.num_idle()
}

fn with_query(url: &str, kv: &str) -> String {
    if url.contains('?') {
        format!("{url}&{kv}")
    } else {
        format!("{url}?{kv}")
    }
}

// ---------------------------------------------------------------------------------------
// The pool (Go open_test.go)

#[tokio::test]
async fn open_errors() {
    let e = db::open("", PoolOptions::default())
        .unwrap_err()
        .to_string();
    assert!(e.contains("the database URL is empty"), "{e}");
    assert!(
        e.contains("\nTry: set DATABASE_URL (see .env.example)"),
        "{e}"
    );
    for bad in [
        "postgres://app:s3cr3t-pw@db:notaport/app",
        "host=db user=app password=s3cr3t-pw",
    ] {
        let e = db::open(bad, PoolOptions::default())
            .unwrap_err()
            .to_string();
        assert!(e.contains("not valid") && e.contains("\nTry: "), "{e}");
        assert!(
            !e.contains("s3cr3t-pw"),
            "the error leaks the password: {e}"
        );
    }
}

#[tokio::test]
async fn open_is_lazy() {
    // Nothing listens on port 1. open must still succeed.
    let pool = db::open(
        "postgres://app:pw@127.0.0.1:1/app?sslmode=disable",
        PoolOptions::default(),
    )
    .unwrap();
    assert_eq!(pool.size(), 0);
}

#[tokio::test]
async fn open_pool_size() {
    let base = "postgres://app:pw@127.0.0.1:1/app?sslmode=disable";
    let cases = [
        (base.to_owned(), 0, db::DEFAULT_MAX_CONNS),
        (base.to_owned(), 3, 3),
        (with_query(base, "pool_max_conns=7"), 0, 7),
        (with_query(base, "pool_max_conns=7"), 9, 9),
    ];
    for (url, opt, want) in cases {
        let pool = db::open(&url, PoolOptions::default().max_conns(opt)).unwrap();
        assert_eq!(pool.options().get_max_connections(), want, "{url} {opt}");
    }
}

#[tokio::test]
async fn open_rejects_bad_max_conns() {
    let base = "postgres://app:pw@127.0.0.1:1/app?sslmode=disable";
    let e = db::open(base, PoolOptions::default().max_conns(i32::MAX as u32 + 1))
        .unwrap_err()
        .to_string();
    assert!(e.contains("Try: set DATABASE_MAX_CONNS"), "{e}");
    for q in ["pool_max_conns=0", "pool_max_conns=x"] {
        let e = db::open(&with_query(base, q), PoolOptions::default())
            .unwrap_err()
            .to_string();
        assert!(e.contains("Try: set DATABASE_MAX_CONNS"), "{q}: {e}");
    }
}

#[tokio::test]
async fn pg_open_runtime_params() {
    let Some(tdb) = test_db().await else { return };
    let base = tdb.url().to_owned();
    type Case = (String, PoolOptions, Vec<(&'static str, &'static str)>);
    let cases: Vec<Case> = vec![
        (
            base.clone(),
            PoolOptions::default(),
            vec![
                ("statement_timeout", "30s"),
                ("lock_timeout", "5s"),
                ("application_name", "spinup-app"),
            ],
        ),
        (
            base.clone(),
            PoolOptions::default()
                .statement_timeout(Duration::from_secs(2))
                .lock_timeout(Duration::from_millis(1500))
                .app_name("todo-api")
                .max_conns(3),
            vec![
                ("statement_timeout", "2s"),
                ("lock_timeout", "1500ms"),
                ("application_name", "todo-api"),
            ],
        ),
        (
            with_query(&base, "lock_timeout=750"),
            PoolOptions::default(),
            vec![("statement_timeout", "30s"), ("lock_timeout", "750ms")],
        ),
        (
            with_query(&base, "lock_timeout=750"),
            PoolOptions::default().lock_timeout(Duration::from_secs(1)),
            vec![("lock_timeout", "1s")],
        ),
        (
            with_query(&base, "application_name=from-url"),
            PoolOptions::default(),
            vec![("application_name", "from-url")],
        ),
    ];
    for (url, opts, want) in cases {
        let pool = db::open(&url, opts.clone()).unwrap();
        for (param, value) in want {
            assert_eq!(
                show(&pool, param).await,
                value,
                "SHOW {param} with {opts:?}"
            );
        }
        pool.close().await;
    }
}

#[tokio::test]
async fn pg_pool_from_config() {
    let Some(tdb) = test_db().await else { return };
    let config = spinup_kit::config::Base {
        database_url: tdb.url().to_owned(),
        database_max_conns: 4,
        ..spinup_kit::config::Base::default()
    };
    let pool = db::pool(&config).unwrap();
    assert_eq!(pool.options().get_max_connections(), 4);
    assert_eq!(show(&pool, "application_name").await, "spinup-app");
    pool.close().await;
}

// ---------------------------------------------------------------------------------------
// Transactions (Go tx_test.go)

async fn items(tdb: &TestDb) -> PgPool {
    let pool = tdb.pool().clone();
    sqlx::query("CREATE TABLE items (id int PRIMARY KEY)")
        .execute(&pool)
        .await
        .unwrap();
    pool
}

async fn count(pool: &PgPool) -> i64 {
    sqlx::query_scalar("SELECT count(*) FROM items")
        .fetch_one(pool)
        .await
        .unwrap()
}

#[tokio::test]
async fn pg_in_tx_commits_on_ok() {
    let Some(tdb) = test_db().await else { return };
    let pool = items(&tdb).await;
    let n: i64 = db::in_tx(&pool, |conn| {
        Box::pin(async move {
            sqlx::query("INSERT INTO items VALUES (1)")
                .execute(&mut *conn)
                .await?;
            Ok::<_, sqlx::Error>(7)
        })
    })
    .await
    .unwrap();
    assert_eq!(n, 7);
    assert_eq!(count(&pool).await, 1);
    assert_eq!(wait_idle(&pool).await, 0);
}

#[tokio::test]
async fn pg_in_tx_rolls_back_on_error() {
    let Some(tdb) = test_db().await else { return };
    let pool = items(&tdb).await;
    let err = db::in_tx(&pool, |conn| {
        Box::pin(async move {
            sqlx::query("INSERT INTO items VALUES (1)")
                .execute(&mut *conn)
                .await?;
            Err::<(), _>(anyhow::anyhow!("boom"))
        })
    })
    .await
    .unwrap_err();
    assert_eq!(err.to_string(), "boom");
    assert_eq!(count(&pool).await, 0);
    assert_eq!(wait_idle(&pool).await, 0);
}

#[tokio::test]
async fn pg_in_tx_rolls_back_on_panic() {
    let Some(tdb) = test_db().await else { return };
    let pool = items(&tdb).await;
    let p = pool.clone();
    let joined = tokio::spawn(async move {
        db::in_tx(&p, |conn| {
            Box::pin(async move {
                sqlx::query("INSERT INTO items VALUES (1)")
                    .execute(&mut *conn)
                    .await?;
                if std::hint::black_box(true) {
                    panic!("kaboom");
                }
                Ok::<(), sqlx::Error>(())
            })
        })
        .await
    })
    .await;
    let err = joined.unwrap_err();
    assert!(err.is_panic());
    let value = err.into_panic();
    assert_eq!(value.downcast_ref::<&str>(), Some(&"kaboom"));
    assert_eq!(count(&pool).await, 0);
    assert_eq!(wait_idle(&pool).await, 0);
}

#[tokio::test]
async fn pg_in_tx_rolls_back_on_cancel() {
    let Some(tdb) = test_db().await else { return };
    let pool = items(&tdb).await;
    let (inserted_tx, inserted_rx) = tokio::sync::oneshot::channel::<()>();
    let p = pool.clone();
    let task = tokio::spawn(async move {
        db::in_tx(&p, |conn| {
            Box::pin(async move {
                sqlx::query("INSERT INTO items VALUES (1)")
                    .execute(&mut *conn)
                    .await?;
                let _ = inserted_tx.send(());
                std::future::pending::<()>().await;
                Ok::<_, sqlx::Error>(())
            })
        })
        .await
    });
    inserted_rx.await.unwrap();
    task.abort();
    assert!(task.await.unwrap_err().is_cancelled());
    assert_eq!(count(&pool).await, 0);
    assert_eq!(wait_idle(&pool).await, 0);
    // The pool closes without a stuck connection.
    tokio::time::timeout(Duration::from_secs(3), pool.close())
        .await
        .expect("the pool closes after a cancelled transaction");
}

// ---------------------------------------------------------------------------------------
// The migrator and the migrate command (Go migrate_test.go and command_test.go)

#[tokio::test]
async fn pg_migrate_command_lifecycle() {
    let Some(tdb) = test_db().await else { return };
    let m = migrations(two(), tdb.url());
    let kit = kit_names();
    let app = names(&["00001_users", "00002_notes"]);
    let all: Vec<String> = kit.iter().chain(app.iter()).cloned().collect();

    assert_eq!(run(&m, &["status"]).await, lines("pending", &all));
    assert_eq!(
        run(&m, &["up"]).await,
        format!("migrate: {} pending\n{}", all.len(), lines("applied", &all))
    );
    assert_eq!(run(&m, &["up"]).await, "migrate: no pending migrations\n");
    assert_eq!(run(&m, &["status"]).await, lines("applied", &all));
    assert_eq!(run(&m, &["down"]).await, "rolled back  00002_notes\n");
    assert_eq!(
        run(&m, &["status"]).await,
        format!(
            "{}applied  00001_users\npending  00002_notes\n",
            lines("applied", &kit)
        )
    );
    assert_eq!(run(&m, &["down"]).await, "rolled back  00001_users\n");
    assert_eq!(run(&m, &["down"]).await, "migrate: nothing to roll back\n");
    assert_eq!(
        run(&m, &["up"]).await,
        format!("migrate: 2 pending\n{}", lines("applied", &app))
    );

    // The kit and the app keep their versions in their own tables.
    let pool = tdb.pool();
    let kit_rows: i64 = sqlx::query_scalar(sqlx::AssertSqlSafe(format!(
        "SELECT count(*) FROM {KIT_MIGRATIONS_TABLE}"
    )))
    .fetch_one(pool)
    .await
    .unwrap();
    assert_eq!(kit_rows, kit.len() as i64);
    let app_rows: i64 = sqlx::query_scalar("SELECT count(*) FROM _sqlx_migrations")
        .fetch_one(pool)
        .await
        .unwrap();
    assert_eq!(app_rows, 2);
}

#[tokio::test]
async fn pg_migrate_command_no_migrations() {
    let Some(tdb) = test_db().await else { return };
    let m = migrations(Vec::new(), tdb.url());
    let kit = kit_names();
    assert_eq!(run(&m, &["status"]).await, lines("pending", &kit));
    assert_eq!(
        run(&m, &["up"]).await,
        format!("migrate: {} pending\n{}", kit.len(), lines("applied", &kit))
    );
    assert_eq!(run(&m, &["up"]).await, "migrate: no pending migrations\n");
    assert_eq!(run(&m, &["down"]).await, "migrate: nothing to roll back\n");
    assert_eq!(run(&m, &["redo"]).await, "migrate: nothing to redo\n");
    assert_eq!(run(&m, &["version"]).await, "0\n");
    assert_eq!(
        run(&m, &["to", "0"]).await,
        "migrate: already at version 0\n"
    );
    assert_eq!(run(&m, &["status"]).await, lines("applied", &kit));
}

#[tokio::test]
async fn pg_migrate_command_redo_to_version() {
    let Some(tdb) = test_db().await else { return };
    let m = migrations(three(), tdb.url());
    let kit = lines("applied", &kit_names());
    let steps: [(&[&str], String); 12] = [
        (&["version"], "0\n".into()),
        (&["redo"], "migrate: nothing to redo\n".into()),
        (
            &["to", "2"],
            format!("{kit}applied  00001_users\napplied  00002_notes\n"),
        ),
        (&["version"], "2\n".into()),
        (&["to", "2"], "migrate: already at version 2\n".into()),
        (
            &["redo"],
            "rolled back  00002_notes\napplied  00002_notes\n".into(),
        ),
        (
            &["status"],
            format!("{kit}applied  00001_users\napplied  00002_notes\npending  00003_tags\n"),
        ),
        (&["to", "3"], "applied  00003_tags\n".into()),
        (
            &["to", "1"],
            "rolled back  00003_tags\nrolled back  00002_notes\n".into(),
        ),
        (&["version"], "1\n".into()),
        (&["to", "0"], "rolled back  00001_users\n".into()),
        (&["version"], "0\n".into()),
    ];
    for (i, (args, want)) in steps.iter().enumerate() {
        assert_eq!(run(&m, args).await, *want, "step {i}: migrate {args:?}");
    }
}

#[tokio::test]
async fn pg_migrate_command_to_unknown_version() {
    let Some(tdb) = test_db().await else { return };
    let m = migrations(two(), tdb.url());
    let e = run_err(&m, &["to", "9"]).await;
    assert!(
        e.contains("no migration file has version 9") && e.contains("\nTry: "),
        "{e}"
    );
}

#[tokio::test]
async fn migrate_command_errors() {
    // No database: the usage errors come before the connection.
    let m = migrations(two(), "postgres://app:pw@127.0.0.1:1/app");
    let cases: [(&[&str], &[&str]); 7] = [
        (&[], &["up, down, status, redo, to, or version", "\nTry: "]),
        (
            &["sideways"],
            &[
                "\"sideways\"",
                "up, down, status, redo, to, or version",
                "\nTry: ",
            ],
        ),
        (&["to"], &["exactly one version", "\nTry: migrate to 1"]),
        (
            &["to", "x1"],
            &["\"x1\" is not a migration version", "\nTry: "],
        ),
        (&["to", "-1"], &["is not a migration version", "\nTry: "]),
        (&["version", "2"], &["unexpected arguments", "\nTry: "]),
        (&["up", "now"], &["unexpected arguments", "\nTry: "]),
    ];
    for (args, want) in cases {
        let e = run_err(&m, args).await;
        for w in want {
            assert!(e.contains(w), "migrate {args:?}: {e:?} lacks {w:?}");
        }
    }
    let bad = migrations(two(), "postgres://app:s3cr3t-pw@db:notaport/app");
    let e = run_err(&bad, &["up"]).await;
    assert!(e.contains("not valid") && e.contains("\nTry: "), "{e}");
    assert!(
        !e.contains("s3cr3t-pw"),
        "the error leaks the password: {e}"
    );
}

#[tokio::test]
async fn pg_migrate_command_env_url() {
    let Some(tdb) = test_db().await else { return };
    let env: HashMap<String, String> =
        HashMap::from([("DATABASE_URL".to_owned(), tdb.url().to_owned())]);
    let m = Migrations::new(Migrator::with_migrations(two())).lookup(move |k| env.get(k).cloned());
    let mut want = lines("pending", &kit_names());
    want.push_str("pending  00001_users\npending  00002_notes\n");
    assert_eq!(run(&m, &["status"]).await, want);
}

#[tokio::test]
async fn migrate_command_empty_env_url() {
    let m = Migrations::new(Migrator::with_migrations(two())).lookup(|_| None);
    let e = run_err(&m, &["status"]).await;
    assert!(
        e.contains("Try: set DATABASE_URL (see .env.example)"),
        "{e}"
    );
}

#[tokio::test]
async fn pg_migrator_pending() {
    let Some(tdb) = test_db().await else { return };
    let m = migrations(two(), tdb.url());
    let mut want = kit_names();
    want.extend(names(&["00001_users", "00002_notes"]));
    assert_eq!(m.pending().await.unwrap(), want);
    m.up(&mut Vec::new()).await.unwrap();
    assert!(m.pending().await.unwrap().is_empty());
    let status = m.status().await.unwrap();
    assert_eq!(status.len(), want.len());
    assert!(status.iter().all(|s| s.applied));
    assert_eq!(status.last().map(|s| s.name.as_str()), Some("00002_notes"));
}

/// Two runners start `up` at once. The advisory lock makes one wait. Both succeed, and each
/// migration applies exactly once.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn pg_migrator_concurrent_up() {
    let Some(tdb) = test_db().await else { return };
    let mut list = Vec::new();
    // A slow first migration keeps the lock held while the second runner tries.
    list.extend(pair(
        1,
        "users",
        "SELECT pg_sleep(1.5); CREATE TABLE users (id bigint PRIMARY KEY);",
        "DROP TABLE users;",
    ));
    list.extend(pair(
        2,
        "notes",
        "CREATE TABLE notes (id bigint PRIMARY KEY);",
        "DROP TABLE notes;",
    ));
    let url = tdb.url().to_owned();
    let runner = |list: Vec<Migration>, url: String| async move {
        let m = migrations(list, &url);
        let mut out = Vec::new();
        let r = m.up(&mut out).await;
        (r, String::from_utf8(out).unwrap())
    };
    let (a, b) = tokio::join!(
        tokio::spawn(runner(list.clone(), url.clone())),
        tokio::spawn(runner(list, url))
    );
    let kit = kit_names();
    let total = kit.len() + 2;
    let mut all = kit.clone();
    all.extend(names(&["00001_users", "00002_notes"]));
    let full = format!("migrate: {total} pending\n{}", lines("applied", &all));
    let late = format!("migrate: {total} pending\nmigrate: another runner applied them\n");
    let mut applied = 0;
    for (i, joined) in [a, b].into_iter().enumerate() {
        let (r, out) = joined.unwrap();
        r.unwrap_or_else(|e| panic!("runner {i}: {e:#}\n{out}"));
        assert!(
            out == full || out == late || out == "migrate: no pending migrations\n",
            "runner {i}: unexpected output:\n{out}"
        );
        applied += out.matches("applied  ").count();
    }
    assert_eq!(applied, total);
    let n: i64 = sqlx::query_scalar("SELECT count(*) FROM _sqlx_migrations WHERE success")
        .fetch_one(tdb.pool())
        .await
        .unwrap();
    assert_eq!(n, 2);
}

/// The migration connections carry `lock_timeout`, `statement_timeout`, and the
/// application name `spinup-migrate`.
#[tokio::test]
async fn pg_migrator_timeouts() {
    let cases = [
        (None, "5s", "5min"),
        (
            Some((Duration::from_secs(2), Duration::from_secs(60))),
            "2s",
            "1min",
        ),
    ];
    for (opts, want_lock, want_statement) in cases {
        let Some(tdb) = test_db().await else { return };
        let mut list = Vec::new();
        list.extend(pair(
            1,
            "settings",
            "CREATE TABLE settings AS SELECT current_setting('lock_timeout') AS lock_timeout, current_setting('statement_timeout') AS statement_timeout, current_setting('application_name') AS app;",
            "DROP TABLE settings;",
        ));
        let mut m = migrations(list, tdb.url());
        if let Some((lock, statement)) = opts {
            m = m.lock_timeout(lock).statement_timeout(statement);
        }
        m.up(&mut Vec::new()).await.unwrap();
        let (lock, statement, app): (String, String, String) =
            sqlx::query_as("SELECT lock_timeout, statement_timeout, app FROM settings")
                .fetch_one(tdb.pool())
                .await
                .unwrap();
        assert_eq!(
            (lock.as_str(), statement.as_str(), app.as_str()),
            (want_lock, want_statement, "spinup-migrate")
        );
    }
}

#[tokio::test]
async fn pg_migrator_rejects_a_changed_migration() {
    let Some(tdb) = test_db().await else { return };
    migrations(two(), tdb.url())
        .up(&mut Vec::new())
        .await
        .unwrap();
    let mut changed = Vec::new();
    changed.extend(pair(
        1,
        "users",
        "CREATE TABLE users (id bigint PRIMARY KEY);",
        "DROP TABLE users;",
    ));
    changed.extend(three().split_off(2));
    let e = run_err(&migrations(changed, tdb.url()), &["up"]).await;
    assert!(
        e.contains("00001_users changed after it was applied") && e.contains("\nTry: "),
        "{e}"
    );
}

#[tokio::test]
async fn pg_migration_status_and_version() {
    let Some(tdb) = test_db().await else { return };
    let pool = tdb.pool().clone();
    let status = db::migration_status(pool.clone());
    let version = db::migration_version(pool.clone());
    assert!(status().await.unwrap().is_empty());
    assert_eq!(version().await.unwrap(), "0");
    migrations(two(), tdb.url())
        .up(&mut Vec::new())
        .await
        .unwrap();
    let list = status().await.unwrap();
    let got: Vec<(&str, bool)> = list.iter().map(|m| (m.name.as_str(), m.applied)).collect();
    assert_eq!(got, [("00001_users", true), ("00002_notes", true)]);
    assert_eq!(version().await.unwrap(), "2");
}

#[tokio::test]
async fn pg_readiness() {
    let Some(tdb) = test_db().await else { return };
    let check = db::readiness(tdb.pool().clone());
    check().await.unwrap();
}

#[tokio::test]
async fn readiness_fails_without_a_database() {
    let pool = sqlx::postgres::PgPoolOptions::new()
        .acquire_timeout(Duration::from_millis(300))
        .connect_lazy("postgres://nobody@127.0.0.1:1/none")
        .unwrap();
    let check = db::readiness(pool);
    let e = check().await.unwrap_err();
    assert!(format!("{e:#}").contains("db: ping the database"), "{e:#}");
}

// ---------------------------------------------------------------------------------------
// The problem mapping

#[test]
fn problem_from_row_not_found() {
    let p = db::problem_from(&sqlx::Error::RowNotFound, false);
    assert_eq!(p.status, 404);
    assert_eq!(p.detail.as_deref(), Some("The record does not exist."));
    let e = ApiError::from(sqlx::Error::PoolTimedOut);
    assert_eq!(e.problem().status, 500);
    assert_eq!(e.problem().detail, None);
}

#[tokio::test]
async fn pg_problem_from_database_errors() {
    let Some(tdb) = test_db().await else { return };
    let pool = tdb.pool();
    sqlx::raw_sql(
        "CREATE TABLE parents (id int PRIMARY KEY, code text UNIQUE, n int CHECK (n > 0));
         CREATE TABLE kids (id int PRIMARY KEY, parent_id int NOT NULL REFERENCES parents (id));
         INSERT INTO parents VALUES (1, 'a', 1);
         INSERT INTO kids VALUES (1, 1);",
    )
    .execute(pool)
    .await
    .unwrap();
    let cases: [(&'static str, bool, u16, &str); 7] = [
        (
            "INSERT INTO parents VALUES (2, 'a', 1)",
            false,
            409,
            "A record with the same unique value already exists.",
        ),
        (
            "DELETE FROM parents WHERE id = 1",
            true,
            409,
            "Other records refer to this record.",
        ),
        (
            "INSERT INTO kids VALUES (2, 99)",
            false,
            422,
            "A referenced record does not exist.",
        ),
        (
            "INSERT INTO kids (id) VALUES (3)",
            false,
            422,
            "A value breaks a rule of the database.",
        ),
        (
            "INSERT INTO parents VALUES (3, 'c', 0)",
            false,
            422,
            "A value breaks a rule of the database.",
        ),
        (
            "SELECT 'x'::int",
            false,
            422,
            "A value has an invalid format or is out of range.",
        ),
        ("SELECT * FROM missing_table", false, 500, ""),
    ];
    for (sql, delete, status, detail) in cases {
        let err = sqlx::query(sql).execute(pool).await.unwrap_err();
        let p = if delete {
            db::on_delete(err).problem().clone()
        } else {
            db::problem_from(&err, false)
        };
        assert_eq!(p.status, status, "{sql}");
        assert_eq!(p.detail.as_deref().unwrap_or(""), detail, "{sql}");
    }
}

// ---------------------------------------------------------------------------------------
// The testkit (Go testkit_test.go, the skip rule of server.go)

#[tokio::test]
async fn testkit_db_skips_without_database_url() {
    assert!(db_with(|_| None).await.is_none());
    let env = HashMap::from([("DATABASE_URL", ""), ("CI", "")]);
    assert!(
        db_with(|k| env.get(k).map(|v| (*v).to_owned()))
            .await
            .is_none()
    );
}

#[tokio::test]
#[should_panic(expected = "Try: export DATABASE_URL=")]
async fn testkit_db_fails_in_ci_without_database_url() {
    let _ = db_with(|k| (k == "CI").then(|| "true".to_owned())).await;
}

#[tokio::test]
async fn pg_testkit_db_is_empty_and_dropped() {
    let Some(outer) = test_db().await else { return };
    let inner_name = {
        let inner = test_db().await.unwrap();
        let name = inner.name().to_owned();
        assert!(
            name.starts_with("t_db_pg_testkit_db_is_empty_and_") && name.len() <= 63,
            "database name {name:?}"
        );
        let current: String = sqlx::query_scalar("SELECT current_database()")
            .fetch_one(inner.pool())
            .await
            .unwrap();
        assert_eq!(current, name);
        let tables: i64 =
            sqlx::query_scalar("SELECT count(*) FROM pg_tables WHERE schemaname = 'public'")
                .fetch_one(inner.pool())
                .await
                .unwrap();
        assert_eq!(tables, 0);
        assert_eq!(show(inner.pool(), "lock_timeout").await, "5s");
        assert!(inner.url().contains(&name), "the URL does not name {name}");
        name
    };
    let exists: bool =
        sqlx::query_scalar("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)")
            .bind(&inner_name)
            .fetch_one(outer.pool())
            .await
            .unwrap();
    assert!(!exists, "database {inner_name} still exists after the drop");
}

#[tokio::test]
async fn pg_testkit_long_test_name_fits_an_identifier() {
    let Some(tdb) = test_db().await else { return };
    let name = tdb.name();
    assert!(name.len() <= 63 && name.starts_with("t_"), "{name}");
}

fn seeded() -> Migrator {
    Migrator::with_migrations(
        pair(
            1,
            "todos",
            "CREATE TABLE todos (id bigint PRIMARY KEY, title text NOT NULL); INSERT INTO todos VALUES (1, 'seeded');",
            "DROP TABLE todos;",
        )
        .to_vec(),
    )
}

/// Parallel tests get migrated, independent clones of one template.
#[tokio::test(flavor = "multi_thread", worker_threads = 3)]
async fn pg_testkit_db_with_migrations_clones() {
    if std::env::var("DATABASE_URL").map_or(true, |u| u.is_empty()) {
        eprintln!("skip: DATABASE_URL is not set");
        return;
    }
    let app = seeded();
    let template = spinup_kit::testkit::template::template_name(&app);
    let one = |title: &'static str| {
        let app = seeded();
        async move {
            let tdb = spinup_kit::testkit::db::db_with_migrations(&app)
                .await
                .unwrap();
            sqlx::query("INSERT INTO todos VALUES (2, $1)")
                .bind(title)
                .execute(tdb.pool())
                .await
                .unwrap();
            let titles: Vec<String> = sqlx::query_scalar("SELECT title FROM todos ORDER BY id")
                .fetch_all(tdb.pool())
                .await
                .unwrap();
            assert_eq!(titles, ["seeded", title]);
            // The kit migrations are in the template too.
            let kit: i64 = sqlx::query_scalar(sqlx::AssertSqlSafe(format!(
                "SELECT count(*) FROM {KIT_MIGRATIONS_TABLE}"
            )))
            .fetch_one(tdb.pool())
            .await
            .unwrap();
            assert_eq!(kit, kit_names().len() as i64);
        }
    };
    let (a, b, c) = tokio::join!(
        tokio::spawn(one("first")),
        tokio::spawn(one("second")),
        tokio::spawn(one("third"))
    );
    a.unwrap();
    b.unwrap();
    c.unwrap();
    let Some(check) = test_db().await else { return };
    let is_template: bool =
        sqlx::query_scalar("SELECT datistemplate FROM pg_database WHERE datname = $1")
            .bind(&template)
            .fetch_one(check.pool())
            .await
            .unwrap();
    assert!(is_template, "{template} is not a template database");
}

#[tokio::test]
async fn testkit_send_and_decode_json() {
    #[derive(serde::Deserialize, Debug, PartialEq)]
    struct Echo {
        method: String,
        path: String,
        ct: String,
    }
    let app = Router::new().route(
        "/todos",
        any(|method: Method, uri: Uri, headers: HeaderMap| async move {
            let ct = headers
                .get("content-type")
                .and_then(|v| v.to_str().ok())
                .unwrap_or("")
                .to_owned();
            axum::Json(serde_json::json!({"method": method.as_str(), "path": uri.path(), "ct": ct}))
        }),
    );
    let cases = [
        (Req::get("/todos"), ("GET", "")),
        (
            Req::post("/todos").json(r#"{"title":"x"}"#),
            ("POST", "application/json"),
        ),
        (
            Req::put("/todos")
                .json("x")
                .header("Content-Type", "text/plain"),
            ("PUT", "text/plain"),
        ),
    ];
    for (req, (method, ct)) in cases {
        let answer = send(&app, req).await;
        assert_eq!(answer.status, StatusCode::OK);
        assert_eq!(
            decode_json::<Echo>(&answer),
            Echo {
                method: method.into(),
                path: "/todos".into(),
                ct: ct.into()
            }
        );
    }
    let missing = send(&app, Req::get("/nope")).await;
    assert_eq!(missing.status, StatusCode::NOT_FOUND);
}

#[tokio::test]
#[should_panic(expected = "testkit: decode JSON answer (status 200 OK)")]
async fn testkit_decode_json_fails_on_bad_json() {
    let app = Router::new().route("/", any(|| async { "not json" }));
    let answer = send(&app, Req::get("/")).await;
    let _: serde_json::Value = decode_json(&answer);
}
