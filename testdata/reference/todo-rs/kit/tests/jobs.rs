//! Tests of the kit job queue against PostgreSQL. Each test gets its own
//! database from `#[sqlx::test]` and runs the kit migrations itself.

use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, Instant};

use kit::jobs::{Backoff, Client, Config, InsertOpts, Job, JobArgs, Worker};
use serde::{Deserialize, Serialize};
use sqlx::PgPool;
use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use tokio::sync::Notify;
use tokio::task::JoinHandle;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
struct Email {
    to: String,
}

impl JobArgs for Email {
    const KIND: &'static str = "email";
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct Chaos {
    n: i64,
}

impl JobArgs for Chaos {
    const KIND: &'static str = "chaos";
}

async fn setup(opts: &PgConnectOptions, max: u32) -> PgPool {
    let pool = PgPoolOptions::new()
        .max_connections(max)
        .connect_with(opts.clone())
        .await
        .unwrap();
    kit::db::kit_migrator().run(&pool).await.unwrap();
    pool
}

fn worker_pool(opts: &PgConnectOptions, max: u32) -> PgPool {
    PgPoolOptions::new()
        .max_connections(max)
        .connect_lazy_with(opts.clone())
}

/// A fast test configuration. The backoff is 100 ms × attempt², without
/// jitter, so the test can check each retry time.
fn test_config(id: &str) -> Config {
    Config {
        id: id.into(),
        concurrency: 8,
        poll_interval: Duration::from_millis(200),
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

fn spawn(worker: Worker) -> (JoinHandle<anyhow::Result<()>>, Arc<Notify>) {
    let stop = Arc::new(Notify::new());
    let wait = stop.clone();
    let handle = tokio::spawn(worker.run(async move { wait.notified().await }));
    (handle, stop)
}

#[sqlx::test(migrations = false)]
async fn unique_by_args(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let client = Client::new(pool.clone());
    let unique = InsertOpts {
        unique_by_args: true,
        ..InsertOpts::default()
    };
    let a = Email { to: "a@x".into() };
    let first = client.insert(&a, &unique).await.unwrap();
    assert!(!first.duplicate);
    let again = client.insert(&a, &unique).await.unwrap();
    assert_eq!(again.id, first.id);
    assert!(again.duplicate);
    // The canonical jsonb text ignores key order and white space.
    let raw = serde_json::json!({"to": "a@x"});
    let mut conn = pool.acquire().await.unwrap();
    let same: (i64,) = sqlx::query_as(
        "SELECT count(*) FROM kit_job WHERE unique_key = sha256(convert_to('email:' || $1::jsonb::text, 'UTF8'))",
    )
    .bind(sqlx::types::Json(&raw))
    .fetch_one(&mut *conn)
    .await
    .unwrap();
    assert_eq!(same.0, 1);
    // Other args, or no uniqueness, make a new job.
    let b = client
        .insert(&Email { to: "b@x".into() }, &unique)
        .await
        .unwrap();
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
    // An insert in a transaction is visible only after the commit.
    let mut tx = pool.begin().await.unwrap();
    let in_tx = kit::jobs::insert_tx(
        &mut tx,
        &Email { to: "tx@x".into() },
        &InsertOpts::default(),
    )
    .await
    .unwrap();
    let seen: i64 = sqlx::query_scalar("SELECT count(*) FROM kit_job WHERE id = $1")
        .bind(in_tx.id)
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(seen, 0);
    tx.commit().await.unwrap();
}

#[sqlx::test(migrations = false)]
async fn notify_wakes_the_worker_without_a_poll(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let mut worker = Worker::new(
        worker_pool(&opts, 6),
        Config {
            // Polls never come during the test: only NOTIFY can wake it.
            poll_interval: Duration::from_secs(600),
            ..test_config("notify")
        },
    );
    worker.register(|_: Job<Email>| async { Ok(()) });
    let (handle, stop) = spawn(worker);
    // Let the worker fetch once and LISTEN.
    tokio::time::sleep(Duration::from_millis(500)).await;
    let client = Client::new(pool.clone());
    let mut latencies = Vec::new();
    for i in 0..5 {
        client
            .insert(
                &Email {
                    to: format!("{i}@x"),
                },
                &InsertOpts::default(),
            )
            .await
            .unwrap();
        let took = wait_for(
            &pool,
            "SELECT count(*) FROM kit_job WHERE state = 'completed'",
            i + 1,
            Duration::from_secs(5),
        )
        .await;
        latencies.push(took);
    }
    stop.notify_one();
    handle.await.unwrap().unwrap();
    println!("NOTIFY insert-to-completed latencies: {latencies:?}");
}

#[sqlx::test(migrations = false)]
async fn poll_fallback_without_listen(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let mut worker = Worker::new(
        worker_pool(&opts, 6),
        Config {
            listen: false,
            poll_interval: Duration::from_millis(100),
            ..test_config("poll")
        },
    );
    worker.register(|_: Job<Email>| async { Ok(()) });
    let (handle, stop) = spawn(worker);
    // A job that waits 300 ms gets no NOTIFY at its time.
    let client = Client::new(pool.clone());
    let opts_later = InsertOpts {
        run_in: Some(Duration::from_millis(300)),
        ..InsertOpts::default()
    };
    client
        .insert(&Email { to: "now@x".into() }, &InsertOpts::default())
        .await
        .unwrap();
    client
        .insert(
            &Email {
                to: "later@x".into(),
            },
            &opts_later,
        )
        .await
        .unwrap();
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job WHERE state = 'completed'",
        2,
        Duration::from_secs(3),
    )
    .await;
    stop.notify_one();
    handle.await.unwrap().unwrap();
}

#[sqlx::test(migrations = false)]
async fn retries_follow_the_backoff_then_discard(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("retry"));
    worker.register(|job: Job<Email>| async move {
        anyhow::bail!("smtp down (attempt {})", job.attempt)
    });
    let (handle, stop) = spawn(worker);
    let id = Client::new(pool.clone())
        .insert(
            &Email { to: "x@x".into() },
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
    stop.notify_one();
    handle.await.unwrap().unwrap();
    // Each failed attempt has its error, and the retry time is the failure
    // time plus the backoff of the attempt (100 ms × attempt²).
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
    // The gaps between the failures are at least the backoff.
    let gaps: Vec<(f64,)> = sqlx::query_as(
        "SELECT extract(epoch FROM (e->>'at')::timestamptz
            - lag((e->>'retry_at')::timestamptz) OVER (ORDER BY (e->>'attempt')::int))::float8
        FROM kit_job, unnest(errors) e WHERE id = $1 ORDER BY (e->>'attempt')::int OFFSET 1",
    )
    .bind(id)
    .fetch_all(&pool)
    .await
    .unwrap();
    for (gap,) in gaps {
        assert!(
            gap >= 0.0,
            "an attempt started {gap} s before its retry time"
        );
    }
}

#[sqlx::test(migrations = false)]
async fn rescuer_retries_a_stuck_job(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    // A job that a dead worker left in running an hour ago.
    let id: i64 = sqlx::query_scalar(
        "INSERT INTO kit_job (kind, args, max_attempts, state, attempt, attempted_at, attempted_by)
        VALUES ('email', '{\"to\": \"x@x\"}', 5, 'running', 1, now() - interval '1 hour', '{dead}')
        RETURNING id",
    )
    .fetch_one(&pool)
    .await
    .unwrap();
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("rescuer"));
    worker.register(|_: Job<Email>| async { Ok(()) });
    let (handle, stop) = spawn(worker);
    wait_for(
        &pool,
        "SELECT count(*) FROM kit_job WHERE state = 'completed'",
        1,
        Duration::from_secs(5),
    )
    .await;
    stop.notify_one();
    handle.await.unwrap().unwrap();
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

#[sqlx::test(migrations = false)]
async fn periodic_jobs_come_from_one_leader(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let runs = Arc::new(AtomicUsize::new(0));
    let mut handles = Vec::new();
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
            .periodic(
                Duration::from_millis(250),
                &Email {
                    to: "digest@x".into(),
                },
            );
        handles.push(spawn(worker));
    }
    tokio::time::sleep(Duration::from_millis(2000)).await;
    for (_, stop) in &handles {
        stop.notify_one();
    }
    for (handle, _) in handles {
        handle.await.unwrap().unwrap();
    }
    // Three workers for 2 s with a 250 ms period: about 7 jobs from the one
    // leader. Three leaders would insert about 21.
    let inserted: i64 = sqlx::query_scalar("SELECT count(*) FROM kit_job")
        .fetch_one(&pool)
        .await
        .unwrap();
    let by: Vec<(String,)> =
        sqlx::query_as("SELECT DISTINCT attempted_by[1] FROM kit_job WHERE attempt > 0")
            .fetch_all(&pool)
            .await
            .unwrap();
    println!(
        "periodic: {inserted} jobs, {} runs, workers {by:?}",
        runs.load(Ordering::SeqCst)
    );
    assert!((4..=9).contains(&inserted), "{inserted} periodic jobs");
    let leaders: i64 = sqlx::query_scalar("SELECT count(*) FROM kit_leader")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(leaders, 0, "each worker gives up the lead when it stops");
}

#[sqlx::test(migrations = false)]
async fn stop_waits_for_running_jobs(_: PgPoolOptions, opts: PgConnectOptions) {
    let pool = setup(&opts, 4).await;
    let started = Arc::new(Notify::new());
    let mut worker = Worker::new(worker_pool(&opts, 6), test_config("stop"));
    let signal = started.clone();
    worker.register(move |_: Job<Email>| {
        let signal = signal.clone();
        async move {
            signal.notify_one();
            tokio::time::sleep(Duration::from_millis(300)).await;
            Ok(())
        }
    });
    let (handle, stop) = spawn(worker);
    Client::new(pool.clone())
        .insert(&Email { to: "x@x".into() }, &InsertOpts::default())
        .await
        .unwrap();
    started.notified().await;
    stop.notify_one();
    handle.await.unwrap().unwrap();
    let states = state_counts(&pool).await;
    assert_eq!(states, [("completed".to_owned(), 1)]);
}

/// The chaos test of the R0.8 row: 10 000 jobs, two workers, and 50 kills.
/// A kill aborts the worker task. That drops the fetch loop, the running
/// jobs, the listener, the leader loop, and the pool of that worker, as a
/// crash does. A new worker with a new ID and a new pool replaces it.
///
/// The handler is idempotent by design: it records its effect with an
/// upsert that counts the runs. A second run of a job is allowed only when a
/// kill hit the first run (the rescuer then has an entry in the job errors).
/// Each job with an `n` that divides by 10 fails its first attempt.
#[sqlx::test(migrations = false)]
async fn chaos_kill_the_worker_50_times(_: PgPoolOptions, opts: PgConnectOptions) {
    chaos(opts, 50).await;
}

/// The same run without kills, for the throughput baseline:
/// `cargo test --test jobs -- --ignored --nocapture`.
#[sqlx::test(migrations = false)]
#[ignore = "a measurement, not a check"]
async fn chaos_baseline_without_kills(_: PgPoolOptions, opts: PgConnectOptions) {
    chaos(opts, 0).await;
}

async fn chaos(opts: PgConnectOptions, kills_wanted: i64) {
    const JOBS: i64 = 10_000;

    let admin = setup(&opts, 4).await;
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
    .bind(JOBS)
    .execute(&admin)
    .await
    .unwrap();

    let new_worker = |slot: i64, generation: i64| {
        let pool = worker_pool(&opts, 10);
        let effects = pool.clone();
        let mut worker = Worker::new(pool, test_config(&format!("w{slot}-{generation}")));
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
        tokio::spawn(worker.run(std::future::pending::<()>()))
    };

    let start = Instant::now();
    let mut workers = [new_worker(0, 0), new_worker(1, 0)];
    let mut kills = 0;
    let mut kill_at = Vec::new();
    while kills < kills_wanted {
        let done: i64 =
            sqlx::query_scalar("SELECT count(*) FROM kit_job WHERE state = 'completed'")
                .fetch_one(&admin)
                .await
                .unwrap();
        assert!(
            done < JOBS,
            "the run ended after {kills} kills, before the 50th kill"
        );
        if done >= (kills + 1) * JOBS / (kills_wanted + 2) {
            let slot = (kills % 2) as usize;
            workers[slot].abort();
            let _ = (&mut workers[slot]).await;
            workers[slot] = new_worker(slot as i64, kills + 1);
            kills += 1;
            kill_at.push(done);
        } else {
            tokio::time::sleep(Duration::from_millis(2)).await;
        }
    }
    let after_kills = start.elapsed();
    wait_for(
        &admin,
        "SELECT count(*) FROM kit_job WHERE state = 'completed'",
        JOBS,
        Duration::from_secs(300),
    )
    .await;
    let elapsed = start.elapsed();
    for w in &workers {
        w.abort();
    }

    // No job is lost: every job is completed, and every job made its effect.
    assert_eq!(state_counts(&admin).await, [("completed".to_owned(), JOBS)]);
    let (effects, duplicates, extra_runs): (i64, i64, i64) = sqlx::query_as(
        "SELECT count(*), count(*) FILTER (WHERE runs > 1), coalesce(sum(runs - 1), 0)::int8 FROM chaos_done",
    )
    .fetch_one(&admin)
    .await
    .unwrap();
    assert_eq!(effects, JOBS);
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
    // Each job with an n that divides by 10 failed its first attempt: by
    // the planned failure, or by a kill (then the rescuer has the entry).
    let (planned, first_failed): (i64, i64) = sqlx::query_as(
        "SELECT count(DISTINCT j.id) FILTER (WHERE e->>'error' = 'planned failure'),
            count(DISTINCT j.id)
        FROM kit_job j, unnest(j.errors) e
        WHERE (j.args->>'n')::int % 10 = 0 AND (e->>'attempt')::int = 1",
    )
    .fetch_one(&admin)
    .await
    .unwrap();
    assert_eq!(first_failed, JOBS / 10);
    assert!(planned > JOBS / 20, "only {planned} planned failures");
    // Retry follows the backoff: each recorded retry time is the failure
    // time plus 100 ms × attempt², and the next attempt starts after it.
    let (errors, rescued, bad_delay, checked, early): (i64, i64, i64, i64, i64) = sqlx::query_as(
        "SELECT count(*),
            count(*) FILTER (WHERE e->>'error' LIKE 'rescued%'),
            count(*) FILTER (WHERE abs(extract(epoch FROM (e->>'retry_at')::timestamptz
                - (e->>'at')::timestamptz) - 0.1 * ((e->>'attempt')::int ^ 2)) > 0.001),
            count(a.started_at),
            count(*) FILTER (WHERE a.started_at < (e->>'retry_at')::timestamptz)
        FROM kit_job j CROSS JOIN LATERAL unnest(j.errors) e
        LEFT JOIN chaos_attempt a ON a.job_id = j.id AND a.attempt = (e->>'attempt')::int + 1",
    )
    .fetch_one(&admin)
    .await
    .unwrap();
    assert_eq!(bad_delay, 0, "a retry time does not follow the backoff");
    assert_eq!(early, 0, "an attempt started before its retry time");
    let (max_attempt,): (i16,) = sqlx::query_as("SELECT max(attempt) FROM kit_job")
        .fetch_one(&admin)
        .await
        .unwrap();
    println!(
        "CHAOS jobs={JOBS} kills={kills} elapsed={:.1}s kills_done_after={:.1}s \
        errors={errors} rescued={rescued} planned_failures={planned} retries_checked={checked} \
        duplicate_effects={duplicates} extra_runs={extra_runs} max_attempt={max_attempt} \
        kill_progress_first={:?} kill_progress_last={:?}",
        elapsed.as_secs_f64(),
        after_kills.as_secs_f64(),
        kill_at.first(),
        kill_at.last()
    );
}
