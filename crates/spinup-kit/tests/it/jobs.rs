//! The tests of the kit job queue (Go `kit/jobs/jobs_test.go`, and the R0.8 spike tests
//! with the chaos test). A test that needs Postgres starts with `pg_`. It gets its own
//! database from `#[sqlx::test]` and runs the kit migrations itself.
//!
//! The chaos test runs real worker processes: it starts this test binary again with the
//! ignored test `jobs::chaos_worker_child`, and kills the process with SIGKILL.

use std::io::Read as _;
use std::process::{Child, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, Instant};

use opentelemetry::KeyValue;
use opentelemetry::trace::SpanKind;
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::logs::SdkLoggerProvider;
use opentelemetry_sdk::metrics::data::{AggregatedMetrics, MetricData};
use opentelemetry_sdk::metrics::{InMemoryMetricExporter, PeriodicReader, SdkMeterProvider};
use opentelemetry_sdk::trace::{InMemorySpanExporter, SdkTracerProvider};
use serde::{Deserialize, Serialize};
use spinup_kit::app::CommandInput;
use spinup_kit::jobs::{
    self, Args, Backoff, Client, Config, InsertOpts, Job, JobState, Worker, worker_command,
};
use spinup_kit::telemetry::{Providers, Telemetry};
use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use sqlx::{ConnectOptions as _, PgPool};
use time::OffsetDateTime;
use tokio::sync::{Notify, mpsc};
use tokio::task::JoinHandle;
use tracing::Instrument as _;
use tracing_subscriber::layer::SubscriberExt as _;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
struct Email {
    to: String,
}

impl Args for Email {
    const KIND: &'static str = "email";
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct Chaos {
    n: i64,
}

impl Args for Chaos {
    const KIND: &'static str = "chaos";
}

fn email(to: &str) -> Email {
    Email { to: to.into() }
}

/// A pool on the test database with the kit migrations.
async fn setup(opts: &PgConnectOptions, max: u32) -> PgPool {
    let pool = PgPoolOptions::new()
        .max_connections(max)
        .connect_with(opts.clone())
        .await
        .unwrap();
    spinup_kit::db::kit_migrator().run(&pool).await.unwrap();
    pool
}

/// The pool of one worker.
fn worker_pool(opts: &PgConnectOptions, max: u32) -> PgPool {
    PgPoolOptions::new()
        .max_connections(max)
        .connect_lazy_with(opts.clone())
}

/// A pool that never connects, for the tests without a database.
fn no_db() -> PgPool {
    PgPoolOptions::new()
        .connect_lazy("postgres://nobody@127.0.0.1:1/none")
        .unwrap()
}

/// A fast test configuration. The backoff is 100 ms × attempt², without jitter, so a test
/// can check each retry time.
fn test_config(id: &str) -> Config {
    Config {
        id: id.into(),
        concurrency: 8,
        poll_interval: Duration::from_millis(200),
        fetch_cooldown: Duration::from_millis(5),
        rescue_after: Duration::from_secs(1),
        maintenance_interval: Duration::from_millis(100),
        leader_ttl: Duration::from_secs(1),
        backoff: Backoff::new(|a| Duration::from_millis(100 * (a as u64).pow(2))),
        stop_timeout: Duration::from_secs(2),
        ..Config::default()
    }
}

async fn state_counts(pool: &PgPool) -> Vec<(String, i64)> {
    sqlx::query_as("SELECT state::text, count(*) FROM kit_job GROUP BY 1 ORDER BY 1")
        .fetch_all(pool)
        .await
        .unwrap()
}

async fn state_of(pool: &PgPool, id: i64) -> String {
    sqlx::query_scalar("SELECT state::text FROM kit_job WHERE id = $1")
        .bind(id)
        .fetch_one(pool)
        .await
        .unwrap()
}

/// Waits until `sql` (a count) gives `want`. Returns the wait.
async fn wait_for(pool: &PgPool, sql: &'static str, want: i64, limit: Duration) -> Duration {
    let start = Instant::now();
    loop {
        let n: i64 = sqlx::query_scalar(sql).fetch_one(pool).await.unwrap();
        if n == want {
            return start.elapsed();
        }
        assert!(
            start.elapsed() < limit,
            "{sql} is {n}, want {want} within {limit:?}; states: {:?}",
            state_counts(pool).await
        );
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
}

const COMPLETED: &str = "SELECT count(*) FROM kit_job WHERE state = 'completed'";

/// Runs `worker` on a task. Notify the returned stop signal to stop it.
fn spawn(worker: Worker) -> (JoinHandle<anyhow::Result<()>>, Arc<Notify>) {
    let stop = Arc::new(Notify::new());
    let wait = stop.clone();
    let handle = tokio::spawn(worker.run(async move { wait.notified().await }));
    (handle, stop)
}

async fn stop(handle: JoinHandle<anyhow::Result<()>>, stop: &Notify) {
    stop.notify_one();
    handle.await.unwrap().unwrap();
}

/// A worker whose `email` handler always succeeds.
fn ok_worker(opts: &PgConnectOptions, config: Config) -> Worker {
    let mut worker = Worker::new(worker_pool(opts, 6), config);
    worker.register(|_: Job<Email>| async { Ok(()) });
    worker
}

// ---------------------------------------------------------------------------------------
// Without a database
// ---------------------------------------------------------------------------------------

/// Go `TestWorkerRules`: a duplicate kind and a zero interval are programming errors, a
/// bad configuration fails at the start, and a worker with no job does nothing.
#[tokio::test]
async fn jobs_worker_rules() {
    let mut w = Worker::new(no_db(), Config::default());
    w.register(|_: Job<Email>| async { Ok(()) });
    assert_eq!(w.kinds(), ["email"]);
    let dup = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        w.register(|_: Job<Email>| async { Ok(()) });
    }));
    assert!(dup.is_err(), "a duplicate kind must panic");
    let zero = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        w.periodic(Duration::ZERO, &email("x"));
    }));
    assert!(zero.is_err(), "a zero interval must panic");

    let mut bad = Worker::new(
        no_db(),
        Config {
            concurrency: 0,
            ..Config::default()
        },
    );
    bad.register(|_: Job<Email>| async { Ok(()) });
    let err = bad.run(async {}).await.unwrap_err().to_string();
    assert!(err.contains("the concurrency is 0"), "{err}");
    assert!(err.contains("\nTry: "), "{err}");

    // A worker with no job runs until the shutdown, so a new app's worker can run.
    Worker::new(no_db(), Config::default())
        .run(async {})
        .await
        .unwrap();
}

/// The `worker` command takes no arguments, passes the error of `register` on, and stops
/// on the shutdown future.
#[tokio::test]
async fn jobs_worker_command_rules() {
    let cmd = worker_command(no_db(), Config::default(), |_| Ok(()));
    let err = cmd
        .run(CommandInput::new(vec!["now".into()], async {}))
        .await
        .unwrap_err()
        .to_string();
    assert!(err.contains("unexpected arguments [\"now\"]"), "{err}");
    assert!(err.contains("\nTry: run the worker command"), "{err}");

    let cmd = worker_command(no_db(), Config::default(), |_| {
        anyhow::bail!("jobs: register the digest job: no mailer\nTry: set SMTP_URL")
    });
    let err = cmd
        .run(CommandInput::new(Vec::new(), async {}))
        .await
        .unwrap_err()
        .to_string();
    assert_eq!(
        err,
        "jobs: register the digest job: no mailer\nTry: set SMTP_URL"
    );

    let cmd = worker_command(no_db(), Config::default(), |_| Ok(()));
    cmd.run(CommandInput::new(Vec::new(), async {}))
        .await
        .unwrap();
}

/// A bad insert option or queue name fails before the database.
#[tokio::test]
async fn jobs_insert_rejects_bad_options() {
    let client = Client::new(no_db());
    let opts = InsertOpts {
        priority: Some(9),
        ..InsertOpts::default()
    };
    let err = client
        .insert(&email("x"), &opts)
        .await
        .unwrap_err()
        .to_string();
    assert!(
        err.starts_with("jobs: insert a \"email\" job: the priority 9 is out of range"),
        "{err}"
    );
    assert!(err.contains("\nTry: "), "{err}");
    for err in [
        client.pause("").await.unwrap_err().to_string(),
        client.resume("").await.unwrap_err().to_string(),
    ] {
        assert!(err.contains("has 0 characters"), "{err}");
        assert!(err.contains("\nTry: "), "{err}");
    }
}

// ---------------------------------------------------------------------------------------
// The migration (Go TestMigrationHookAndCommand)
// ---------------------------------------------------------------------------------------

/// The kit migration makes the queue tables, and its down migration removes them.
#[sqlx::test(migrations = false)]
async fn pg_jobs_kit_migration(pool: PgPool) {
    let m = spinup_kit::db::kit_migrator();
    let names: Vec<String> = m
        .iter()
        .map(|m| spinup_kit::db::migration_name(m.version, &m.description))
        .collect();
    assert!(names.contains(&"00001_job_queue".to_string()), "{names:?}");
    m.run(&pool).await.unwrap();
    let tables = |pool: PgPool| async move {
        let n: i64 = sqlx::query_scalar(
            "SELECT count(*) FROM pg_tables
            WHERE schemaname = 'public' AND tablename IN ('kit_job', 'kit_leader', 'kit_queue')",
        )
        .fetch_one(&pool)
        .await
        .unwrap();
        n
    };
    assert_eq!(tables(pool.clone()).await, 3);
    m.undo(&pool, 0).await.unwrap();
    assert_eq!(tables(pool.clone()).await, 0);
    m.run(&pool).await.unwrap();
    assert_eq!(tables(pool.clone()).await, 3);
}

/// Go `TestInsertWithoutMigrations`: the error asks for the migrations.
#[sqlx::test(migrations = false)]
async fn pg_jobs_insert_without_migrations(pool: PgPool) {
    let err = Client::new(pool)
        .insert(&email("x"), &InsertOpts::default())
        .await
        .unwrap_err()
        .to_string();
    assert!(err.starts_with("jobs: insert a \"email\" job: "), "{err}");
    assert!(err.contains("\nTry: run the migrations"), "{err}");
}

// ---------------------------------------------------------------------------------------
// Inserts
// ---------------------------------------------------------------------------------------

/// Go `TestInsertOptions`: each option reaches the job row.
#[sqlx::test(migrations = false)]
async fn pg_jobs_insert_options(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let client = Client::new(pool.clone());
    let at = time::macros::datetime!(2030-01-02 03:04:05 UTC);
    let id = client
        .insert(
            &email("opts"),
            &InsertOpts {
                queue: Some("mail".into()),
                priority: Some(3),
                run_at: Some(at),
                max_attempts: Some(4),
                unique_by_args: false,
            },
        )
        .await
        .unwrap()
        .id;
    let row: (String, i16, i16, OffsetDateTime, String) = sqlx::query_as(
        "SELECT queue, priority, max_attempts, scheduled_at, state::text FROM kit_job WHERE id = $1",
    )
    .bind(id)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(row, ("mail".into(), 3, 4, at, "scheduled".into()));
    // The defaults.
    let id = client
        .insert(&email("plain"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    let row: (String, i16, i16, String, serde_json::Value) = sqlx::query_as(
        "SELECT queue, priority, max_attempts, state::text, args FROM kit_job WHERE id = $1",
    )
    .bind(id)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(
        row,
        (
            "default".into(),
            1,
            25,
            "available".into(),
            serde_json::json!({"to": "plain"})
        )
    );
}

/// Unique jobs (Go `UniqueByArgs`, and the R0.8 spike test).
#[sqlx::test(migrations = false)]
async fn pg_jobs_unique_by_args(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let client = Client::new(pool.clone());
    let unique = InsertOpts {
        unique_by_args: true,
        ..InsertOpts::default()
    };
    let a = email("a@x");
    let first = client.insert(&a, &unique).await.unwrap();
    assert!(!first.duplicate);
    let again = client.insert(&a, &unique).await.unwrap();
    assert_eq!(again.id, first.id);
    assert!(again.duplicate);
    // The canonical jsonb text ignores key order and white space.
    let raw = serde_json::json!({"to": "a@x"});
    let same: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM kit_job WHERE unique_key = sha256(convert_to('email:' || $1::jsonb::text, 'UTF8'))",
    )
    .bind(sqlx::types::Json(&raw))
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(same, 1);
    // Other args, or no uniqueness, make a new job.
    let b = client.insert(&email("b@x"), &unique).await.unwrap();
    assert_ne!(b.id, first.id);
    let plain = client.insert(&a, &InsertOpts::default()).await.unwrap();
    assert!(!plain.duplicate);
    // A completed unique job does not block a new insert.
    sqlx::query("UPDATE kit_job SET state = 'completed', finalized_at = now() WHERE id = $1")
        .bind(first.id)
        .execute(&pool)
        .await
        .unwrap();
    let after = client.insert(&a, &unique).await.unwrap();
    assert!(!after.duplicate);
    assert_ne!(after.id, first.id);
}

/// Go `TestInsertTx`: a rolled-back insert leaves no job, a committed insert stays.
#[sqlx::test(migrations = false)]
async fn pg_jobs_insert_tx(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let client = Client::new(pool.clone());
    let mut tx = pool.begin().await.unwrap();
    client
        .insert_tx(&mut tx, &email("gone"), &InsertOpts::default())
        .await
        .unwrap();
    tx.rollback().await.unwrap();

    let mut tx = pool.begin().await.unwrap();
    let kept = client
        .insert_tx(&mut tx, &email("kept"), &InsertOpts::default())
        .await
        .unwrap();
    // Workers do not see the job before the commit.
    let seen: i64 = sqlx::query_scalar("SELECT count(*) FROM kit_job")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(seen, 0);
    tx.commit().await.unwrap();
    let rows: Vec<(i64, String)> = sqlx::query_as("SELECT id, args->>'to' FROM kit_job")
        .fetch_all(&pool)
        .await
        .unwrap();
    assert_eq!(rows, [(kept.id, "kept".to_string())]);
}

// ---------------------------------------------------------------------------------------
// The worker
// ---------------------------------------------------------------------------------------

/// Go `TestInsertAndWork`: the handler gets the ID, the args, and attempt 1.
#[sqlx::test(migrations = false)]
async fn pg_jobs_insert_and_work(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let (tx, mut rx) = mpsc::unbounded_channel();
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("work"));
    worker.register(move |job: Job<Email>| {
        let tx = tx.clone();
        async move {
            tx.send(job).unwrap();
            Ok(())
        }
    });
    assert_eq!(worker.kinds(), ["email"]);
    let (handle, signal) = spawn(worker);
    let id = Client::new(pool.clone())
        .insert(&email("ada"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    let job = tokio::time::timeout(Duration::from_secs(10), rx.recv())
        .await
        .expect("the worker runs the job")
        .unwrap();
    assert_eq!(
        job,
        Job {
            id,
            args: email("ada"),
            attempt: 1
        }
    );
    wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    stop(handle, &signal).await;
    let by: Vec<String> = sqlx::query_scalar("SELECT attempted_by FROM kit_job WHERE id = $1")
        .bind(id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(by, ["work"]);
}

/// An insert wakes the worker through NOTIFY, without a poll.
#[sqlx::test(migrations = false)]
async fn pg_jobs_notify_wakes_the_worker_without_a_poll(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let worker = ok_worker(
        &opts,
        Config {
            // Polls never come during the test: only NOTIFY can wake the worker.
            poll_interval: Duration::from_secs(600),
            ..test_config("notify")
        },
    );
    let (handle, signal) = spawn(worker);
    // Let the worker fetch once and LISTEN.
    tokio::time::sleep(Duration::from_millis(500)).await;
    let client = Client::new(pool.clone());
    for i in 0..5 {
        client
            .insert(&email(&format!("{i}@x")), &InsertOpts::default())
            .await
            .unwrap();
        wait_for(&pool, COMPLETED, i + 1, Duration::from_secs(5)).await;
    }
    stop(handle, &signal).await;
}

/// Without LISTEN, the poll finds new jobs and scheduled jobs.
#[sqlx::test(migrations = false)]
async fn pg_jobs_poll_fallback_without_listen(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let worker = ok_worker(
        &opts,
        Config {
            listen: false,
            poll_interval: Duration::from_millis(100),
            ..test_config("poll")
        },
    );
    let (handle, signal) = spawn(worker);
    let client = Client::new(pool.clone());
    client
        .insert(&email("now@x"), &InsertOpts::default())
        .await
        .unwrap();
    // A job that waits 300 ms gets no NOTIFY at its time.
    client
        .insert(
            &email("later@x"),
            &InsertOpts::run_in(Duration::from_millis(300)),
        )
        .await
        .unwrap();
    wait_for(&pool, COMPLETED, 2, Duration::from_secs(3)).await;
    stop(handle, &signal).await;
}

/// A failed attempt retries after the backoff. After the last attempt the job is
/// discarded.
#[sqlx::test(migrations = false)]
async fn pg_jobs_retries_follow_the_backoff_then_discard(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("retry"));
    worker.register(|job: Job<Email>| async move {
        anyhow::bail!("smtp down (attempt {})", job.attempt)
    });
    let (handle, signal) = spawn(worker);
    let id = Client::new(pool.clone())
        .insert(
            &email("x@x"),
            &InsertOpts {
                max_attempts: Some(4),
                ..InsertOpts::default()
            },
        )
        .await
        .unwrap()
        .id;
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job WHERE state = 'discarded'",
        1,
        Duration::from_secs(10),
    )
    .await;
    stop(handle, &signal).await;
    // Each failed attempt has its error, and the retry time is the failure time plus the
    // backoff of the attempt (100 ms × attempt²).
    let rows: Vec<(i32, f64, String)> = sqlx::query_as(
        "SELECT (e->>'attempt')::int,
            extract(epoch FROM (e->>'retry_at')::timestamptz - (e->>'at')::timestamptz)::float8,
            e->>'error'
        FROM kit_job, unnest(errors) e WHERE id = $1 ORDER BY 1",
    )
    .bind(id)
    .fetch_all(&pool)
    .await
    .unwrap();
    assert_eq!(rows.len(), 4);
    for (i, (attempt, delay, error)) in rows.iter().enumerate() {
        assert_eq!(*attempt as usize, i + 1);
        let want = 0.1 * f64::from(attempt * attempt);
        assert!(
            (delay - want).abs() < 0.001,
            "attempt {attempt}: {delay} s, want {want} s"
        );
        assert_eq!(error, &format!("smtp down (attempt {attempt})"));
    }
    // The next attempt starts at its retry time or later.
    let gaps: Vec<f64> = sqlx::query_scalar(
        "SELECT extract(epoch FROM (e->>'at')::timestamptz
            - lag((e->>'retry_at')::timestamptz) OVER (ORDER BY (e->>'attempt')::int))::float8
        FROM kit_job, unnest(errors) e WHERE id = $1 ORDER BY (e->>'attempt')::int OFFSET 1",
    )
    .bind(id)
    .fetch_all(&pool)
    .await
    .unwrap();
    for gap in gaps {
        assert!(
            gap >= 0.0,
            "an attempt started {gap} s before its retry time"
        );
    }
}

/// The rescuer retries a job that a dead worker left in `running`.
#[sqlx::test(migrations = false)]
async fn pg_jobs_rescuer_retries_a_stuck_job(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let id: i64 = sqlx::query_scalar(
        "INSERT INTO kit_job (kind, args, max_attempts, state, attempt, attempted_at, attempted_by)
        VALUES ('email', '{\"to\": \"x@x\"}', 5, 'running', 1, now() - interval '1 hour', '{dead}')
        RETURNING id",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let (handle, signal) = spawn(ok_worker(&opts, test_config("rescuer")));
    wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    stop(handle, &signal).await;
    let (attempt, by, error): (i16, Vec<String>, String) = sqlx::query_as(
        "SELECT attempt, attempted_by, errors[1]->>'error' FROM kit_job WHERE id = $1",
    )
    .bind(id)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(attempt, 2);
    assert_eq!(by, ["dead", "rescuer"]);
    assert!(error.starts_with("rescued"), "{error}");
}

/// Go `TestPeriodic`, with three workers: only the leader inserts the periodic job.
#[sqlx::test(migrations = false)]
async fn pg_jobs_periodic_jobs_come_from_one_leader(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let runs = Arc::new(AtomicUsize::new(0));
    let mut workers = Vec::new();
    for name in ["p1", "p2", "p3"] {
        let mut worker = Worker::new(worker_pool(&opts, 5), test_config(name));
        let runs = runs.clone();
        worker
            .register(move |_: Job<Email>| {
                let runs = runs.clone();
                async move {
                    runs.fetch_add(1, Ordering::SeqCst);
                    Ok(())
                }
            })
            .periodic(Duration::from_millis(250), &email("digest@x"));
        workers.push(spawn(worker));
    }
    tokio::time::sleep(Duration::from_millis(2000)).await;
    for (handle, signal) in workers {
        stop(handle, &signal).await;
    }
    // Three workers for 2 s with a 250 ms period: about 7 jobs from the one leader. Three
    // leaders would insert about 21.
    let inserted: i64 = sqlx::query_scalar("SELECT count(*) FROM kit_job")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert!((4..=9).contains(&inserted), "{inserted} periodic jobs");
    assert!(runs.load(Ordering::SeqCst) >= 2, "the periodic job ran");
    let leaders: i64 = sqlx::query_scalar("SELECT count(*) FROM kit_leader")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(leaders, 0, "each worker gives up the lead when it stops");
}

/// Go `TestGracefulStop`: the stop waits for the running job.
#[sqlx::test(migrations = false)]
async fn pg_jobs_stop_waits_for_running_jobs(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let started = Arc::new(Notify::new());
    let release = Arc::new(Notify::new());
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("stop"));
    let (s, r) = (started.clone(), release.clone());
    worker.register(move |_: Job<Email>| {
        let (s, r) = (s.clone(), r.clone());
        async move {
            s.notify_one();
            r.notified().await;
            Ok(())
        }
    });
    let (handle, signal) = spawn(worker);
    Client::new(pool.clone())
        .insert(&email("x@x"), &InsertOpts::default())
        .await
        .unwrap();
    started.notified().await;
    signal.notify_one();
    tokio::time::sleep(Duration::from_millis(200)).await;
    assert!(
        !handle.is_finished(),
        "the stop returned before the job ended"
    );
    release.notify_one();
    handle.await.unwrap().unwrap();
    assert_eq!(state_counts(&pool).await, [("completed".to_owned(), 1)]);
}

/// Go `TestStopTimeoutCancelsJobs`: after the stop timeout, the worker cancels the job,
/// the job retries later, and the stop returns an error with a `Try:` line.
#[sqlx::test(migrations = false)]
async fn pg_jobs_stop_timeout_cancels_jobs(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let started = Arc::new(Notify::new());
    let mut worker = Worker::new(
        worker_pool(&opts, 6),
        Config {
            stop_timeout: Duration::from_millis(100),
            ..test_config("timeout")
        },
    );
    let s = started.clone();
    worker.register(move |_: Job<Email>| {
        let s = s.clone();
        async move {
            s.notify_one();
            std::future::pending::<()>().await;
            Ok(())
        }
    });
    let (handle, signal) = spawn(worker);
    let id = Client::new(pool.clone())
        .insert(&email("x@x"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    started.notified().await;
    signal.notify_one();
    let err = handle.await.unwrap().unwrap_err().to_string();
    assert!(err.contains("did not finish in 100ms"), "{err}");
    assert!(err.contains("\nTry: raise the stop timeout"), "{err}");
    let (state, error): (String, String) =
        sqlx::query_as("SELECT state::text, errors[1]->>'error' FROM kit_job WHERE id = $1")
            .bind(id)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(state, "retryable");
    assert_eq!(error, "jobs: the worker stopped before the job finished");
}

/// A panic in a handler is a failed attempt.
#[sqlx::test(migrations = false)]
async fn pg_jobs_handler_panic_is_an_error(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("panic"));
    worker.register(|job: Job<Email>| async move {
        assert!(job.attempt > 1, "first attempt fails");
        Ok(())
    });
    let (handle, signal) = spawn(worker);
    let id = Client::new(pool.clone())
        .insert(&email("x@x"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    stop(handle, &signal).await;
    let error: String = sqlx::query_scalar("SELECT errors[1]->>'error' FROM kit_job WHERE id = $1")
        .bind(id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(error, "jobs: the handler panicked: first attempt fails");
}

// ---------------------------------------------------------------------------------------
// complete_tx, snooze, cancel, pause
// ---------------------------------------------------------------------------------------

/// `complete_tx` commits the change of the handler and the completion together.
#[sqlx::test(migrations = false)]
async fn pg_jobs_complete_tx(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    sqlx::raw_sql("CREATE TABLE effects (job_id bigint NOT NULL, note text NOT NULL)")
        .execute(&pool)
        .await
        .unwrap();
    let effects = pool.clone();
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("complete"));
    worker.register(move |job: Job<Email>| {
        let pool = effects.clone();
        async move {
            let mut tx = pool.begin().await?;
            sqlx::query("INSERT INTO effects (job_id, note) VALUES ($1, $2)")
                .bind(job.id)
                .bind(&job.args.to)
                .execute(&mut *tx)
                .await?;
            jobs::complete_tx(&mut tx, &job).await?;
            tx.commit().await?;
            // An error after the commit does not undo the completion.
            if job.args.to == "late-error" {
                anyhow::bail!("an error after the commit");
            }
            Ok(())
        }
    });
    let (handle, signal) = spawn(worker);
    let client = Client::new(pool.clone());
    let a = client
        .insert(&email("ok"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    let b = client
        .insert(&email("late-error"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    wait_for(&pool, COMPLETED, 2, Duration::from_secs(5)).await;
    // Let the worker record the outcome of the late error.
    tokio::time::sleep(Duration::from_millis(300)).await;
    stop(handle, &signal).await;
    let rows: Vec<(i64, String, i16, i32)> = sqlx::query_as(
        "SELECT id, state::text, attempt, cardinality(errors) FROM kit_job ORDER BY id",
    )
    .fetch_all(&pool)
    .await
    .unwrap();
    assert_eq!(
        rows,
        [(a, "completed".into(), 1, 0), (b, "completed".into(), 1, 0)]
    );
    let effects: i64 = sqlx::query_scalar("SELECT count(*) FROM effects")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(effects, 2);

    // A job that does not run this attempt cannot complete.
    let mut conn = pool.acquire().await.unwrap();
    let job = Job {
        id: a,
        args: email("ok"),
        attempt: 1,
    };
    let err = jobs::complete_tx(&mut conn, &job)
        .await
        .unwrap_err()
        .to_string();
    assert!(err.contains("does not run attempt 1"), "{err}");
    assert!(err.contains("\nTry: "), "{err}");
}

/// A snoozed job runs again after the delay. The snooze does not use an attempt.
#[sqlx::test(migrations = false)]
async fn pg_jobs_snooze(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("snooze"));
    worker.register(|job: Job<Email>| async move {
        if job.attempt == 1 {
            return Err(jobs::snooze(Duration::from_millis(300)));
        }
        Ok(())
    });
    let (handle, signal) = spawn(worker);
    let id = Client::new(pool.clone())
        .insert(&email("x@x"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    let took = wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    stop(handle, &signal).await;
    assert!(took >= Duration::from_millis(300), "{took:?}");
    let row: (i16, i16, i32) = sqlx::query_as(
        "SELECT attempt, max_attempts, cardinality(errors) FROM kit_job WHERE id = $1",
    )
    .bind(id)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(row, (2, 26, 0));
}

/// A handler that returns `cancel` stops the job for good.
#[sqlx::test(migrations = false)]
async fn pg_jobs_cancel_from_the_handler(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let runs = Arc::new(AtomicUsize::new(0));
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("hcancel"));
    let r = runs.clone();
    worker.register(move |_: Job<Email>| {
        r.fetch_add(1, Ordering::SeqCst);
        async { Err(jobs::cancel("the address is invalid")) }
    });
    let (handle, signal) = spawn(worker);
    let id = Client::new(pool.clone())
        .insert(&email("x@x"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job WHERE state = 'cancelled' AND finalized_at IS NOT NULL",
        1,
        Duration::from_secs(5),
    )
    .await;
    tokio::time::sleep(Duration::from_millis(500)).await;
    stop(handle, &signal).await;
    assert_eq!(runs.load(Ordering::SeqCst), 1);
    let error: String = sqlx::query_scalar("SELECT errors[1]->>'error' FROM kit_job WHERE id = $1")
        .bind(id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(error, "jobs: cancel the job: the address is invalid");
}

/// `Client::cancel`: a waiting job at once, a running job through its worker, a final job
/// not at all.
#[sqlx::test(migrations = false)]
async fn pg_jobs_client_cancel(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let client = Client::new(pool.clone());
    let other = InsertOpts {
        queue: Some("other".into()),
        ..InsertOpts::default()
    };
    let waiting = client.insert(&email("w"), &other).await.unwrap().id;
    assert_eq!(
        client.cancel(waiting).await.unwrap(),
        Some(JobState::Cancelled)
    );
    assert_eq!(state_of(&pool, waiting).await, "cancelled");
    assert_eq!(
        client.cancel(waiting).await.unwrap(),
        Some(JobState::Cancelled)
    );
    assert_eq!(client.cancel(999_999).await.unwrap(), None);

    let started = Arc::new(Notify::new());
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("ccancel"));
    let s = started.clone();
    worker.register(move |job: Job<Email>| {
        let s = s.clone();
        async move {
            if job.args.to == "slow" {
                s.notify_one();
                std::future::pending::<()>().await;
            }
            Ok(())
        }
    });
    let (handle, signal) = spawn(worker);
    let done = client
        .insert(&email("fast"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    assert_eq!(
        client.cancel(done).await.unwrap(),
        Some(JobState::Completed)
    );
    let slow = client
        .insert(&email("slow"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    started.notified().await;
    assert_eq!(client.cancel(slow).await.unwrap(), Some(JobState::Running));
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job WHERE state = 'cancelled' AND queue = 'default'",
        1,
        Duration::from_secs(5),
    )
    .await;
    // No job runs now, so the stop is quick and clean.
    stop(handle, &signal).await;
    let error: String = sqlx::query_scalar("SELECT errors[1]->>'error' FROM kit_job WHERE id = $1")
        .bind(slow)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(error, "cancelled: a client cancelled the running job");
}

/// A cancel whose notification is lost: the job goes to `cancelled` at its next error.
#[sqlx::test(migrations = false)]
async fn pg_jobs_cancel_without_listen(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let started = Arc::new(Notify::new());
    let release = Arc::new(Notify::new());
    let mut worker = Worker::new(
        worker_pool(&opts, 6),
        Config {
            listen: false,
            poll_interval: Duration::from_millis(50),
            ..test_config("nolisten")
        },
    );
    let (s, r) = (started.clone(), release.clone());
    worker.register(move |_: Job<Email>| {
        let (s, r) = (s.clone(), r.clone());
        async move {
            s.notify_one();
            r.notified().await;
            anyhow::bail!("the mail server is down")
        }
    });
    let (handle, signal) = spawn(worker);
    let client = Client::new(pool.clone());
    let id = client
        .insert(&email("x"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    started.notified().await;
    assert_eq!(client.cancel(id).await.unwrap(), Some(JobState::Running));
    release.notify_one();
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job WHERE state = 'cancelled'",
        1,
        Duration::from_secs(5),
    )
    .await;
    stop(handle, &signal).await;
}

/// Go has no pause. River `QueuePause` and `QueueResume`: a paused queue gives no job, and
/// a resume wakes the worker.
#[sqlx::test(migrations = false)]
async fn pg_jobs_pause_and_resume(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let client = Client::new(pool.clone());
    client.pause("default").await.unwrap();
    client.pause("default").await.unwrap();
    client.resume("never-paused").await.unwrap();
    let (handle, signal) = spawn(ok_worker(
        &opts,
        Config {
            poll_interval: Duration::from_secs(600),
            ..test_config("pause")
        },
    ));
    let id = client
        .insert(&email("x"), &InsertOpts::default())
        .await
        .unwrap()
        .id;
    tokio::time::sleep(Duration::from_millis(700)).await;
    assert_eq!(state_of(&pool, id).await, "available");
    client.resume("default").await.unwrap();
    // The resume wakes the worker through NOTIFY: the poll interval is 600 s.
    wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    stop(handle, &signal).await;
}

/// The cleaner deletes finalized jobs after their retention. `None` keeps them.
#[sqlx::test(migrations = false)]
async fn pg_jobs_cleaner(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    sqlx::raw_sql(
        "INSERT INTO kit_job (kind, max_attempts, state, finalized_at, args) VALUES
            ('email', 1, 'completed', now() - interval '2 hours', '{\"to\": \"old-completed\"}'),
            ('email', 1, 'completed', now(), '{\"to\": \"new-completed\"}'),
            ('email', 1, 'cancelled', now() - interval '2 hours', '{\"to\": \"old-cancelled\"}'),
            ('email', 1, 'discarded', now() - interval '2 hours', '{\"to\": \"old-discarded\"}'),
            ('email', 1, 'discarded', now() - interval '30 minutes', '{\"to\": \"new-discarded\"}');
        INSERT INTO kit_job (kind, max_attempts, args, scheduled_at)
            VALUES ('email', 1, '{\"to\": \"waiting\"}', now() + interval '1 day');",
    )
    .execute(&pool)
    .await
    .unwrap();
    let hour = Some(Duration::from_secs(3600));
    let config = Config {
        clean_interval: Duration::from_millis(100),
        completed_retention: hour,
        cancelled_retention: hour,
        discarded_retention: None,
        ..test_config("cleaner")
    };
    let (handle, signal) = spawn(ok_worker(&opts, config));
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job",
        4,
        Duration::from_secs(5),
    )
    .await;
    stop(handle, &signal).await;
    let left: Vec<String> = sqlx::query_scalar("SELECT args->>'to' FROM kit_job ORDER BY id")
        .fetch_all(&pool)
        .await
        .unwrap();
    assert_eq!(
        left,
        ["new-completed", "old-discarded", "new-discarded", "waiting"]
    );

    let config = Config {
        clean_interval: Duration::from_millis(100),
        discarded_retention: hour,
        ..test_config("cleaner2")
    };
    let (handle, signal) = spawn(ok_worker(&opts, config));
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job",
        3,
        Duration::from_secs(5),
    )
    .await;
    stop(handle, &signal).await;
}

/// The `worker` command works jobs until the shutdown future completes (Go `cmd/worker`).
#[sqlx::test(migrations = false)]
async fn pg_jobs_worker_command(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let runs = Arc::new(AtomicUsize::new(0));
    let r = runs.clone();
    let cmd = worker_command(worker_pool(&opts, 6), test_config("cmd"), move |w| {
        w.register(move |_: Job<Email>| {
            let r = r.clone();
            async move {
                r.fetch_add(1, Ordering::SeqCst);
                Ok(())
            }
        });
        Ok(())
    });
    let signal = Arc::new(Notify::new());
    let wait = signal.clone();
    let handle = tokio::spawn(cmd.run(CommandInput::new(Vec::new(), async move {
        wait.notified().await
    })));
    Client::new(pool.clone())
        .insert(&email("x"), &InsertOpts::default())
        .await
        .unwrap();
    wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    stop(handle, &signal).await;
    assert_eq!(runs.load(Ordering::SeqCst), 1);
}

// ---------------------------------------------------------------------------------------
// Telemetry
// ---------------------------------------------------------------------------------------

fn attr(attrs: &[KeyValue], key: &str) -> Option<String> {
    attrs
        .iter()
        .find(|kv| kv.key.as_str() == key)
        .map(|kv| kv.value.as_str().into_owned())
}

/// The job span links to the trace of the insert, and each attempt records
/// `kit.job.duration` with the kind, the queue, and the result. The test sets the global
/// subscriber and providers, so it runs only under nextest (one process per test).
#[sqlx::test(migrations = false)]
async fn pg_jobs_trace_link_and_metrics(_: PgPoolOptions, opts: PgConnectOptions) {
    let resource = Resource::builder_empty()
        .with_service_name("test-worker")
        .build();
    let spans = InMemorySpanExporter::default();
    let metrics = InMemoryMetricExporter::default();
    let meter = SdkMeterProvider::builder()
        .with_resource(resource.clone())
        .with_reader(PeriodicReader::builder(metrics.clone()).build())
        .build();
    let telemetry = Telemetry::install(Providers {
        tracer: SdkTracerProvider::builder()
            .with_resource(resource.clone())
            .with_simple_exporter(spans.clone())
            .build(),
        meter: meter.clone(),
        logger: SdkLoggerProvider::builder().with_resource(resource).build(),
    });
    let subscriber = tracing_subscriber::registry().with(telemetry.layer(tracing::Level::INFO));
    tracing::subscriber::set_global_default(subscriber)
        .expect("the only global subscriber of this test process");

    let pool = setup(&opts, 4).await;
    let client = Client::new(pool.clone());
    let id = client
        .insert(&email("traced"), &InsertOpts::default())
        .instrument(tracing::info_span!("request"))
        .await
        .unwrap()
        .id;
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("traced"));
    worker.register(|job: Job<Email>| async move {
        if job.attempt == 1 {
            anyhow::bail!("first attempt fails");
        }
        tracing::info!("sent");
        Ok(())
    });
    let (handle, signal) = spawn(worker);
    wait_for(&pool, COMPLETED, 1, Duration::from_secs(5)).await;
    stop(handle, &signal).await;

    let finished = spans.get_finished_spans().unwrap();
    let request = finished
        .iter()
        .find(|s| s.name == "request")
        .expect("the request span");
    let traceparent: String =
        sqlx::query_scalar("SELECT metadata->>'traceparent' FROM kit_job WHERE id = $1")
            .bind(id)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(
        traceparent,
        format!(
            "00-{}-{}-01",
            request.span_context.trace_id(),
            request.span_context.span_id()
        )
    );
    let jobs: Vec<_> = finished.iter().filter(|s| s.name == "job email").collect();
    assert_eq!(jobs.len(), 2, "{finished:?}");
    for job in jobs {
        assert_eq!(job.span_kind, SpanKind::Consumer);
        assert_ne!(
            job.span_context.trace_id(),
            request.span_context.trace_id(),
            "the job span starts its own trace"
        );
        // The link is the remote context from the metadata: the same trace and span IDs.
        let linked: Vec<_> = job
            .links
            .iter()
            .map(|l| (l.span_context.trace_id(), l.span_context.span_id()))
            .collect();
        assert_eq!(
            linked,
            [(
                request.span_context.trace_id(),
                request.span_context.span_id()
            )]
        );
    }

    meter.force_flush().unwrap();
    let rm = metrics
        .get_finished_metrics()
        .unwrap()
        .pop()
        .expect("an export");
    let duration = rm
        .scope_metrics()
        .flat_map(|s| s.metrics())
        .find(|m| m.name() == jobs::JOB_DURATION)
        .expect("the job duration histogram");
    assert_eq!(duration.unit(), "s");
    let AggregatedMetrics::F64(MetricData::Histogram(h)) = duration.data() else {
        panic!("not an f64 histogram: {:?}", duration.data())
    };
    let mut points: Vec<String> = h
        .data_points()
        .map(|p| {
            let attrs: Vec<KeyValue> = p.attributes().cloned().collect();
            let label = |k| attr(&attrs, k).unwrap_or_default();
            format!(
                "{} {} {} {}",
                label("job.kind"),
                label("job.queue"),
                label("job.result"),
                p.count()
            )
        })
        .collect();
    points.sort();
    assert_eq!(
        points,
        ["email default completed 1", "email default retryable 1"]
    );
}

// ---------------------------------------------------------------------------------------
// The chaos test (R0.8, again with real processes)
// ---------------------------------------------------------------------------------------

const CHAOS_URL: &str = "SPINUP_JOBS_CHAOS_URL";
const CHAOS_ID: &str = "SPINUP_JOBS_CHAOS_ID";
const CHAOS_JOBS: i64 = 10_000;

/// The worker of a chaos child process. Its handler is idempotent by design: it records
/// its effect with an upsert that counts the runs. Each job with an `n` that divides by 10
/// fails its first attempt.
fn chaos_worker(pool: PgPool, id: &str) -> Worker {
    let effects = pool.clone();
    let mut worker = Worker::new(pool, test_config(id));
    worker.register(move |job: Job<Chaos>| {
        let pool = effects.clone();
        async move {
            sqlx::query(
                "INSERT INTO chaos_attempt (job_id, attempt) VALUES ($1, $2) ON CONFLICT DO NOTHING",
            )
            .bind(job.id)
            .bind(i32::from(job.attempt))
            .execute(&pool)
            .await?;
            tokio::time::sleep(Duration::from_millis((job.id % 4) as u64)).await;
            if job.args.n % 10 == 0 && job.attempt == 1 {
                anyhow::bail!("planned failure");
            }
            sqlx::query(
                "INSERT INTO chaos_done (job_id, runs) VALUES ($1, 1)
                ON CONFLICT (job_id) DO UPDATE SET runs = chaos_done.runs + 1",
            )
            .bind(job.id)
            .execute(&pool)
            .await?;
            Ok(())
        }
    });
    worker
}

/// The worker process of the chaos test. The parent test starts this test binary with
/// this test and the database URL. Without the URL, it does nothing. The process ends
/// when its stdin closes, so a dead parent leaves no worker behind.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "the chaos test runs it as a worker child process"]
async fn chaos_worker_child() {
    let (Ok(url), Ok(id)) = (std::env::var(CHAOS_URL), std::env::var(CHAOS_ID)) else {
        return;
    };
    std::thread::spawn(|| {
        let mut buf = [0u8; 64];
        while std::io::stdin().read(&mut buf).is_ok_and(|n| n > 0) {}
        std::process::exit(0);
    });
    let opts: PgConnectOptions = url.parse().unwrap();
    let worker = chaos_worker(worker_pool(&opts, 10), &id);
    worker.run(std::future::pending::<()>()).await.unwrap();
}

/// The running chaos worker processes. Dropping it kills them, also when a check fails.
struct Children(Vec<Child>);

impl Drop for Children {
    fn drop(&mut self) {
        for child in &mut self.0 {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

/// Starts a chaos worker process: this test binary, with only the ignored test
/// `jobs::chaos_worker_child`.
#[allow(
    clippy::disallowed_methods,
    clippy::disallowed_types,
    reason = "the chaos test kills real worker processes, so it runs this test binary again as a child"
)]
fn chaos_child(url: &str, id: &str) -> Child {
    let exe = std::env::current_exe().expect("the path of the test binary");
    std::process::Command::new(exe)
        .args([
            "jobs::chaos_worker_child",
            "--exact",
            "--ignored",
            "--nocapture",
            "--test-threads=1",
        ])
        .env(CHAOS_URL, url)
        .env(CHAOS_ID, id)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("start a chaos worker process")
}

/// The chaos test of the R0.8 row, with real processes: 10 000 jobs, two worker processes,
/// and 50 kills with SIGKILL. A kill stops the fetch loop, the running jobs, the listener,
/// the leader loop, and the connections of that worker at once, as a crash does. A new
/// process with a new worker ID replaces it.
///
/// No job is lost, and no job completes twice. A second run of a job is allowed only when
/// a kill hit the first run (the rescuer then has an entry in the job errors).
#[sqlx::test(migrations = false)]
async fn pg_jobs_chaos_kill_the_worker_50_times(_: PgPoolOptions, opts: PgConnectOptions) {
    const KILLS: i64 = 50;
    let admin = setup(&opts, 4).await;
    let url = opts.to_url_lossy().to_string();
    sqlx::raw_sql(
        "CREATE TABLE chaos_done (job_id bigint PRIMARY KEY, runs int NOT NULL);
        CREATE TABLE chaos_attempt (
            job_id bigint NOT NULL, attempt int NOT NULL,
            started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
            PRIMARY KEY (job_id, attempt));",
    )
    .execute(&admin)
    .await
    .unwrap();
    sqlx::query(
        "INSERT INTO kit_job (kind, args, max_attempts)
        SELECT 'chaos', jsonb_build_object('n', g), 25 FROM generate_series(1, $1) g",
    )
    .bind(CHAOS_JOBS)
    .execute(&admin)
    .await
    .unwrap();

    let start = Instant::now();
    let mut children = Children(vec![chaos_child(&url, "w0-0"), chaos_child(&url, "w1-0")]);
    let mut kills = 0;
    while kills < KILLS {
        let done: i64 = sqlx::query_scalar(COMPLETED)
            .fetch_one(&admin)
            .await
            .unwrap();
        assert!(
            done < CHAOS_JOBS,
            "the run ended after {kills} kills, before the 50th kill"
        );
        if done >= (kills + 1) * CHAOS_JOBS / (KILLS + 2) {
            let slot = (kills % 2) as usize;
            let child = &mut children.0[slot];
            child.kill().expect("kill a worker process");
            child.wait().expect("reap a worker process");
            children.0[slot] = chaos_child(&url, &format!("w{slot}-{}", kills + 1));
            kills += 1;
        } else {
            tokio::time::sleep(Duration::from_millis(2)).await;
        }
    }
    wait_for(&admin, COMPLETED, CHAOS_JOBS, Duration::from_secs(200)).await;
    let elapsed = start.elapsed();
    drop(children);

    // No job is lost: every job is completed, and every job made its effect.
    assert_eq!(
        state_counts(&admin).await,
        [("completed".to_owned(), CHAOS_JOBS)]
    );
    let (effects, duplicates): (i64, i64) =
        sqlx::query_as("SELECT count(*), count(*) FILTER (WHERE runs > 1) FROM chaos_done")
            .fetch_one(&admin)
            .await
            .unwrap();
    assert_eq!(effects, CHAOS_JOBS);
    // A second run happens only after a kill: the job has a rescue entry.
    let unexplained: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM chaos_done d JOIN kit_job j ON j.id = d.job_id
        WHERE d.runs > 1 AND NOT EXISTS (
            SELECT 1 FROM unnest(j.errors) e WHERE e->>'error' LIKE 'rescued%')",
    )
    .fetch_one(&admin)
    .await
    .unwrap();
    assert_eq!(unexplained, 0, "a job ran twice without a kill");
    // Each job with an n that divides by 10 failed its first attempt: by the planned
    // failure, or by a kill (then the rescuer has the entry).
    let (planned, first_failed): (i64, i64) = sqlx::query_as(
        "SELECT count(DISTINCT j.id) FILTER (WHERE e->>'error' = 'planned failure'),
            count(DISTINCT j.id)
        FROM kit_job j, unnest(j.errors) e
        WHERE (j.args->>'n')::int % 10 = 0 AND (e->>'attempt')::int = 1",
    )
    .fetch_one(&admin)
    .await
    .unwrap();
    assert_eq!(first_failed, CHAOS_JOBS / 10);
    assert!(planned > CHAOS_JOBS / 20, "only {planned} planned failures");
    // Retry follows the backoff: each recorded retry time is the failure time plus
    // 100 ms × attempt², and the next attempt starts after it.
    let (rescued, bad_delay, early): (i64, i64, i64) = sqlx::query_as(
        "SELECT count(*) FILTER (WHERE e->>'error' LIKE 'rescued%'),
            count(*) FILTER (WHERE abs(extract(epoch FROM (e->>'retry_at')::timestamptz
                - (e->>'at')::timestamptz) - 0.1 * ((e->>'attempt')::int ^ 2)) > 0.001),
            count(*) FILTER (WHERE a.started_at < (e->>'retry_at')::timestamptz)
        FROM kit_job j CROSS JOIN LATERAL unnest(j.errors) e
        LEFT JOIN chaos_attempt a ON a.job_id = j.id AND a.attempt = (e->>'attempt')::int + 1",
    )
    .fetch_one(&admin)
    .await
    .unwrap();
    assert_eq!(bad_delay, 0, "a retry time does not follow the backoff");
    assert_eq!(early, 0, "an attempt started before its retry time");
    assert!(rescued > 0, "the kills left no job to rescue");
    println!(
        "CHAOS jobs={CHAOS_JOBS} kills={kills} elapsed={:.1}s rescued={rescued} \
        planned_failures={planned} duplicate_effects={duplicates}",
        elapsed.as_secs_f64()
    );
}
