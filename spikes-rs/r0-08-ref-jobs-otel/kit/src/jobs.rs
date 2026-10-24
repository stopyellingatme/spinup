//! The own job queue on sqlx. It replaces River (Go `kit/jobs`).
//!
//! The schema follows River: one `kit_job` table, a `kit_leader` table, and
//! a NOTIFY trigger (`kit/migrations/0001_job_queue.up.sql`).
//!
//! - A worker fetches jobs with `FOR UPDATE SKIP LOCKED`, so many workers can
//!   share one queue.
//! - An insert sends `NOTIFY kit_insert`. A worker listens and also polls, so
//!   a lost notification costs at most one poll interval.
//! - A failed attempt goes to `retryable` with `scheduled_at` set by the
//!   [`Backoff`]. After `max_attempts`, the job goes to `discarded`.
//! - The leader (one worker, elected through `kit_leader`) runs the rescuer
//!   and inserts the periodic jobs. The rescuer moves a job that stays in
//!   `running` longer than `rescue_after` back to `retryable`.
//! - A unique job (`unique_by_args`) has a `unique_key`: the sha256 of the
//!   kind and the canonical `jsonb` text of the args. A partial unique index
//!   allows one waiting or running job for each key.
//!
//! Delivery is at least once. A job runs again when its worker dies after the
//! handler made its change but before the worker marked the job completed.
//! The handler must be idempotent, or make its change and the completion in
//! one transaction. A job never goes to `completed` twice: each state change
//! of a running job checks `state = 'running'` and the attempt number.

use std::collections::HashMap;
use std::future::Future;
use std::panic::AssertUnwindSafe;
use std::pin::Pin;
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::Context as _;
use futures::FutureExt;
use serde::Serialize;
use serde::de::DeserializeOwned;
use sqlx::postgres::PgListener;
use sqlx::types::Json;
use sqlx::{PgConnection, PgPool};
use tokio::sync::{Notify, Semaphore};
use tokio::task::JoinSet;
use tracing::Instrument;

/// The queue of a job when the insert names no queue.
pub const DEFAULT_QUEUE: &str = "default";
/// The channel of the insert trigger.
pub const NOTIFY_CHANNEL: &str = "kit_insert";
/// The attempts of a job when the insert gives no number (River's default).
pub const DEFAULT_MAX_ATTEMPTS: i16 = 25;

/// The insert. The state list is the predicate of the partial unique index
/// `kit_job_unique_key`: a unique job blocks an insert with the same key while
/// it waits or runs. A unique job that a concurrent transaction inserts is not
/// in the snapshot of the second SELECT, so the caller tries again.
const INSERT_SQL: &str = "WITH ins AS (
    INSERT INTO kit_job (kind, args, queue, priority, max_attempts, scheduled_at, state, unique_key)
    VALUES ($1::text, $2::jsonb, $3::text, $4::int2, $5::int2,
            now() + make_interval(secs => $6::float8),
            CASE WHEN $6::float8 > 0 THEN 'scheduled' ELSE 'available' END::kit_job_state,
            CASE WHEN $7::bool THEN sha256(convert_to($1::text || ':' || $2::jsonb::text, 'UTF8')) END)
    ON CONFLICT (unique_key)
        WHERE unique_key IS NOT NULL AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
    DO NOTHING
    RETURNING id
)
SELECT id, false AS duplicate FROM ins
UNION ALL
SELECT id, true FROM kit_job
WHERE $7::bool AND NOT EXISTS (SELECT 1 FROM ins)
    AND unique_key = sha256(convert_to($1::text || ':' || $2::jsonb::text, 'UTF8'))
    AND state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
LIMIT 1";

/// The arguments of one job kind. The queue stores them as JSON. `KIND` is a
/// stable name: do not change it after jobs of the kind exist.
pub trait JobArgs: Serialize + DeserializeOwned + Send + Sync + 'static {
    /// The job kind, such as `send_digest`.
    const KIND: &'static str;
}

/// One job that a handler runs.
#[derive(Debug, Clone)]
pub struct Job<A> {
    /// The job ID.
    pub id: i64,
    /// The decoded arguments.
    pub args: A,
    /// The attempt number. The first attempt is 1.
    pub attempt: i16,
}

/// The result of a handler. An error makes the job retry after the backoff.
pub type JobResult = anyhow::Result<()>;

/// Options of one insert.
#[derive(Debug, Clone, Default)]
pub struct InsertOpts {
    /// The queue. `None` selects [`DEFAULT_QUEUE`].
    pub queue: Option<String>,
    /// The priority, from 1 (the highest, the default) to 4.
    pub priority: Option<i16>,
    /// Run the job after this delay instead of now.
    pub run_in: Option<Duration>,
    /// The number of attempts before the job is discarded.
    pub max_attempts: Option<i16>,
    /// Skip the insert when a job of the same kind with the same args
    /// waits or runs. The insert then returns the ID of that job.
    pub unique_by_args: bool,
}

/// The result of an insert.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Inserted {
    /// The ID of the new job, or of the unique job that exists.
    pub id: i64,
    /// True when a unique job with the same args exists, so the insert did
    /// not add a job.
    pub duplicate: bool,
}

/// Inserts jobs. Use it in the API process. It is cheap to clone.
#[derive(Debug, Clone)]
pub struct Client {
    pool: PgPool,
}

impl Client {
    /// Returns a client on `pool`.
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Adds a job.
    pub async fn insert<A: JobArgs>(
        &self,
        args: &A,
        opts: &InsertOpts,
    ) -> anyhow::Result<Inserted> {
        let mut conn = self
            .pool
            .acquire()
            .await
            .map_err(|e| db_error(&format!("insert a {:?} job", A::KIND), &e))?;
        insert_tx(&mut conn, args, opts).await
    }
}

/// Adds a job on `conn`. With a transaction (`&mut *tx`), workers see the
/// job only after the commit.
pub async fn insert_tx<A: JobArgs>(
    conn: &mut PgConnection,
    args: &A,
    opts: &InsertOpts,
) -> anyhow::Result<Inserted> {
    let value = serde_json::to_value(args)
        .with_context(|| format!("jobs: encode the args of a {:?} job", A::KIND))?;
    insert_raw(conn, A::KIND, &value, opts).await
}

async fn insert_raw(
    conn: &mut PgConnection,
    kind: &str,
    args: &serde_json::Value,
    opts: &InsertOpts,
) -> anyhow::Result<Inserted> {
    let what = format!("insert a {kind:?} job");
    for _ in 0..5 {
        let row: Option<(i64, bool)> = sqlx::query_as(INSERT_SQL)
            .bind(kind)
            .bind(Json(args))
            .bind(opts.queue.as_deref().unwrap_or(DEFAULT_QUEUE))
            .bind(opts.priority.unwrap_or(1))
            .bind(opts.max_attempts.unwrap_or(DEFAULT_MAX_ATTEMPTS))
            .bind(opts.run_in.unwrap_or_default().as_secs_f64())
            .bind(opts.unique_by_args)
            .fetch_optional(&mut *conn)
            .await
            .map_err(|e| db_error(&what, &e))?;
        if let Some((id, duplicate)) = row {
            return Ok(Inserted { id, duplicate });
        }
    }
    anyhow::bail!(
        "jobs: {what}: the unique job changed state during the insert five times\nTry: insert the job again"
    )
}

/// The wait before the next attempt, from the number of the failed attempt.
#[derive(Clone)]
pub struct Backoff(Arc<dyn Fn(i16) -> Duration + Send + Sync>);

impl std::fmt::Debug for Backoff {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Backoff")
    }
}

impl Backoff {
    /// A backoff from a function of the attempt number.
    pub fn new(f: impl Fn(i16) -> Duration + Send + Sync + 'static) -> Self {
        Self(Arc::new(f))
    }

    /// River's default: attempt⁴ seconds, plus or minus 10 % jitter, at
    /// most 24 hours.
    pub fn river() -> Self {
        Self::new(|attempt| {
            let base = f64::from(attempt.max(1)).powi(4);
            let jitter = base * 0.1 * (fastrand::f64() * 2.0 - 1.0);
            Duration::from_secs_f64((base + jitter).min(86_400.0))
        })
    }

    /// The wait after the failed attempt `attempt`.
    pub fn delay(&self, attempt: i16) -> Duration {
        (self.0)(attempt)
    }
}

/// The configuration of a worker.
#[derive(Debug, Clone)]
pub struct Config {
    /// The worker ID in `attempted_by` and `kit_leader`.
    pub id: String,
    /// The queue that the worker takes jobs from.
    pub queue: String,
    /// The number of jobs that run at the same time.
    pub concurrency: usize,
    /// The fetch interval when no notification comes.
    pub poll_interval: Duration,
    /// Listen for `NOTIFY kit_insert`. Without it, the worker only polls.
    pub listen: bool,
    /// The rescuer moves a job that runs longer than this back to retryable.
    pub rescue_after: Duration,
    /// How often the worker tries to be the leader and, as the leader, runs
    /// the rescuer and the periodic jobs.
    pub maintenance_interval: Duration,
    /// The time the leader keeps the lead without a new election.
    pub leader_ttl: Duration,
    /// The wait before each retry.
    pub backoff: Backoff,
    /// How long a stop waits for the running jobs.
    pub stop_timeout: Duration,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            id: format!("worker-{}", uuid::Uuid::new_v4().simple()),
            queue: DEFAULT_QUEUE.into(),
            concurrency: 10,
            poll_interval: Duration::from_secs(1),
            listen: true,
            rescue_after: Duration::from_secs(3600),
            maintenance_interval: Duration::from_secs(1),
            leader_ttl: Duration::from_secs(10),
            backoff: Backoff::river(),
            stop_timeout: Duration::from_secs(10),
        }
    }
}

type BoxFuture = Pin<Box<dyn Future<Output = JobResult> + Send>>;
type Handler = Arc<dyn Fn(i64, i16, serde_json::Value) -> BoxFuture + Send + Sync>;

struct PeriodicJob {
    every: Duration,
    kind: &'static str,
    args: serde_json::Value,
}

/// Works the jobs of one queue. Register the handlers and the periodic jobs,
/// then call [`Worker::run`].
pub struct Worker {
    pool: PgPool,
    config: Config,
    handlers: HashMap<&'static str, Handler>,
    periodic: Vec<PeriodicJob>,
}

struct Inner {
    pool: PgPool,
    config: Config,
    handlers: HashMap<&'static str, Handler>,
    periodic: Vec<PeriodicJob>,
}

#[derive(sqlx::FromRow)]
struct Fetched {
    id: i64,
    kind: String,
    args: Json<serde_json::Value>,
    attempt: i16,
}

impl Worker {
    /// Returns a worker on `pool`. The pool needs at least
    /// `concurrency + 2` connections.
    pub fn new(pool: PgPool, config: Config) -> Self {
        Self {
            pool,
            config,
            handlers: HashMap::new(),
            periodic: Vec::new(),
        }
    }

    /// Adds `handler` as the function that runs jobs of the kind `A`.
    ///
    /// # Panics
    ///
    /// When the kind already has a handler: that is a programming error.
    pub fn register<A, F, Fut>(&mut self, handler: F) -> &mut Self
    where
        A: JobArgs,
        F: Fn(Job<A>) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = JobResult> + Send + 'static,
    {
        let handler = Arc::new(handler);
        let erased: Handler = Arc::new(move |id, attempt, value| {
            let handler = handler.clone();
            Box::pin(async move {
                let args: A = serde_json::from_value(value)
                    .with_context(|| format!("jobs: decode the args of a {:?} job", A::KIND))?;
                handler(Job { id, args, attempt }).await
            })
        });
        assert!(
            self.handlers.insert(A::KIND, erased).is_none(),
            "jobs: the job kind {:?} is registered twice",
            A::KIND
        );
        self
    }

    /// Inserts a unique job with `args` every `every` while this worker is
    /// the leader. More workers do not insert more jobs.
    ///
    /// # Panics
    ///
    /// When `every` is zero or the args do not encode.
    pub fn periodic<A: JobArgs>(&mut self, every: Duration, args: &A) -> &mut Self {
        assert!(
            !every.is_zero(),
            "jobs: a periodic job needs an interval above zero"
        );
        let args = serde_json::to_value(args).expect("jobs: the periodic job args encode as JSON");
        self.periodic.push(PeriodicJob {
            every,
            kind: A::KIND,
            args,
        });
        self
    }

    /// The registered job kinds, sorted.
    pub fn kinds(&self) -> Vec<&'static str> {
        let mut kinds: Vec<_> = self.handlers.keys().copied().collect();
        kinds.sort_unstable();
        kinds
    }

    /// Works jobs until `shutdown` completes. Then it stops fetching, waits
    /// for the running jobs at most `stop_timeout`, cancels the others, and
    /// gives up the lead. A cancelled job stays `running` until the rescuer
    /// moves it.
    ///
    /// Dropping the future (a crash, or an aborted task) also cancels every
    /// running job: the jobs and the background tasks live in `JoinSet`s that
    /// this future owns.
    pub async fn run(self, shutdown: impl Future<Output = ()> + Send) -> anyhow::Result<()> {
        let inner = Arc::new(Inner {
            pool: self.pool,
            config: self.config,
            handlers: self.handlers,
            periodic: self.periodic,
        });
        let cfg = &inner.config;
        anyhow::ensure!(
            cfg.concurrency > 0,
            "jobs: the worker concurrency is 0\nTry: set Config.concurrency to 1 or more"
        );
        tracing::info!(worker = %cfg.id, queue = %cfg.queue, kinds = ?inner.handlers.keys().collect::<Vec<_>>(), "jobs: worker started");
        let wake = Arc::new(Notify::new());
        let mut background = JoinSet::new();
        if cfg.listen {
            background.spawn(listen(inner.clone(), wake.clone()));
        }
        background.spawn(maintain(inner.clone()));

        let slots = Arc::new(Semaphore::new(cfg.concurrency));
        let mut running = JoinSet::new();
        tokio::pin!(shutdown);
        loop {
            while running.try_join_next().is_some() {}
            let free = slots.available_permits();
            let mut backlog = false;
            if free > 0 {
                match fetch(&inner, free).await {
                    Ok(jobs) => {
                        backlog = jobs.len() == free;
                        for job in jobs {
                            let permit = slots.clone().try_acquire_owned().expect("a free slot");
                            let inner = inner.clone();
                            running.spawn(async move {
                                execute(&inner, job).await;
                                drop(permit);
                            });
                        }
                    }
                    Err(e) => tracing::error!(error = %e, "jobs: fetch failed"),
                }
            } else {
                backlog = true;
            }
            tokio::select! {
                () = &mut shutdown => break,
                () = wake.notified() => {}
                () = tokio::time::sleep(cfg.poll_interval) => {}
                Some(_) = running.join_next(), if backlog && !running.is_empty() => {}
            }
        }

        let drained = tokio::time::timeout(cfg.stop_timeout, async {
            while running.join_next().await.is_some() {}
        })
        .await;
        running.abort_all();
        background.abort_all();
        // Wait for the cancelled tasks, so no election runs after the resign.
        while background.join_next().await.is_some() {}
        while running.join_next().await.is_some() {}
        let resign = sqlx::query("DELETE FROM kit_leader WHERE leader_id = $1")
            .bind(&cfg.id)
            .execute(&inner.pool)
            .await;
        if let Err(e) = resign {
            tracing::warn!(error = %e, "jobs: cannot give up the lead");
        }
        tracing::info!(worker = %cfg.id, "jobs: worker stopped");
        if drained.is_err() {
            anyhow::bail!(
                "jobs: stop the worker: running jobs did not finish in {:?} and were cancelled\nTry: raise the stop timeout, or make long jobs end sooner",
                cfg.stop_timeout
            );
        }
        Ok(())
    }
}

async fn fetch(inner: &Inner, limit: usize) -> anyhow::Result<Vec<Fetched>> {
    let jobs = sqlx::query_as::<_, Fetched>(
        "WITH next AS (
            SELECT id FROM kit_job
            WHERE state IN ('available', 'retryable', 'scheduled')
                AND queue = $1 AND scheduled_at <= now()
            ORDER BY priority, scheduled_at, id
            LIMIT $2
            FOR UPDATE SKIP LOCKED
        )
        UPDATE kit_job j
        SET state = 'running', attempt = j.attempt + 1, attempted_at = now(),
            attempted_by = array_append(j.attempted_by, $3)
        FROM next WHERE j.id = next.id
        RETURNING j.id, j.kind, j.args, j.attempt",
    )
    .bind(&inner.config.queue)
    .bind(i64::try_from(limit).unwrap_or(i64::MAX))
    .bind(&inner.config.id)
    .fetch_all(&inner.pool)
    .await
    .map_err(|e| db_error("fetch jobs", &e))?;
    Ok(jobs)
}

async fn execute(inner: &Inner, job: Fetched) {
    let span = tracing::info_span!(
        "job",
        otel.name = %format!("job {}", job.kind),
        job.kind = %job.kind,
        job.id = job.id,
        job.attempt = job.attempt,
    );
    let started = Instant::now();
    let result = match inner.handlers.get(job.kind.as_str()) {
        None => Err(anyhow::anyhow!(
            "jobs: no handler for the kind {:?}\nTry: register the handler in the worker",
            job.kind
        )),
        Some(handler) => {
            let run = AssertUnwindSafe(handler(job.id, job.attempt, job.args.0)).catch_unwind();
            match run.instrument(span.clone()).await {
                Ok(result) => result,
                Err(panic) => Err(anyhow::anyhow!(
                    "jobs: the handler panicked: {}",
                    panic_text(&panic)
                )),
            }
        }
    };
    let elapsed = started.elapsed();
    let update = match &result {
        Ok(()) => sqlx::query(
            "UPDATE kit_job SET state = 'completed', finalized_at = now()
            WHERE id = $1 AND state = 'running' AND attempt = $2",
        )
        .bind(job.id)
        .bind(job.attempt)
        .execute(&inner.pool)
        .await
        .map(|r| r.rows_affected()),
        Err(err) => sqlx::query(
            "UPDATE kit_job SET
                state = CASE WHEN attempt >= max_attempts THEN 'discarded' ELSE 'retryable' END::kit_job_state,
                finalized_at = CASE WHEN attempt >= max_attempts THEN now() END,
                scheduled_at = now() + make_interval(secs => $3),
                errors = array_append(errors, jsonb_build_object(
                    'attempt', attempt, 'at', now(), 'error', $4::text,
                    'retry_at', now() + make_interval(secs => $3)))
            WHERE id = $1 AND state = 'running' AND attempt = $2",
        )
        .bind(job.id)
        .bind(job.attempt)
        .bind(inner.config.backoff.delay(job.attempt).as_secs_f64())
        .bind(format!("{err:#}"))
        .execute(&inner.pool)
        .await
        .map(|r| r.rows_affected()),
    };
    let _enter = span.enter();
    match (&result, update) {
        (_, Err(e)) => {
            // The job stays running. The rescuer retries it later.
            tracing::error!(error = %e, "jobs: cannot record the job result");
        }
        (_, Ok(0)) => tracing::warn!("jobs: the job changed state while it ran (rescued?)"),
        (Ok(()), Ok(_)) => {
            tracing::debug!(elapsed_ms = elapsed.as_millis() as u64, "jobs: completed")
        }
        (Err(err), Ok(_)) => tracing::warn!(error = %err, "jobs: attempt failed"),
    }
}

fn panic_text(panic: &(dyn std::any::Any + Send)) -> String {
    if let Some(s) = panic.downcast_ref::<&str>() {
        (*s).to_owned()
    } else if let Some(s) = panic.downcast_ref::<String>() {
        s.clone()
    } else {
        "unknown panic".to_owned()
    }
}

/// Wakes the fetch loop on each notification for this queue. After a lost
/// connection it wakes the loop too, because notifications can be lost.
async fn listen(inner: Arc<Inner>, wake: Arc<Notify>) {
    loop {
        match PgListener::connect_with(&inner.pool).await {
            Ok(mut listener) => {
                if let Err(e) = listener.listen(NOTIFY_CHANNEL).await {
                    tracing::warn!(error = %e, "jobs: LISTEN failed, the worker polls");
                } else {
                    wake.notify_one();
                    loop {
                        match listener.try_recv().await {
                            Ok(Some(n)) => {
                                if queue_of(n.payload()).is_none_or(|q| q == inner.config.queue) {
                                    wake.notify_one();
                                }
                            }
                            // The connection is lost. try_recv connects again.
                            Ok(None) => wake.notify_one(),
                            Err(e) => {
                                tracing::warn!(error = %e, "jobs: the listener failed, the worker polls");
                                break;
                            }
                        }
                    }
                }
            }
            Err(e) => {
                tracing::warn!(error = %e, "jobs: cannot connect the listener, the worker polls")
            }
        }
        tokio::time::sleep(inner.config.poll_interval.max(Duration::from_millis(100))).await;
    }
}

fn queue_of(payload: &str) -> Option<String> {
    let v: serde_json::Value = serde_json::from_str(payload).ok()?;
    v.get("queue")?.as_str().map(str::to_owned)
}

/// The leader loop: elect, rescue, and insert periodic jobs.
async fn maintain(inner: Arc<Inner>) {
    let cfg = &inner.config;
    let mut leader = false;
    let mut due: Vec<Instant> = Vec::new();
    let mut tick = tokio::time::interval(cfg.maintenance_interval);
    tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    loop {
        tick.tick().await;
        let elected = match elect(&inner).await {
            Ok(elected) => elected,
            Err(e) => {
                tracing::warn!(error = %e, "jobs: leader election failed");
                false
            }
        };
        if elected && !leader {
            tracing::info!(worker = %cfg.id, "jobs: this worker is the leader");
            let now = Instant::now();
            due = inner.periodic.iter().map(|p| now + p.every).collect();
        }
        leader = elected;
        if !leader {
            continue;
        }
        match rescue(&inner).await {
            Ok(0) => {}
            Ok(n) => tracing::warn!(jobs = n, "jobs: rescued stuck jobs"),
            Err(e) => tracing::warn!(error = %e, "jobs: the rescuer failed"),
        }
        let now = Instant::now();
        for (p, next) in inner.periodic.iter().zip(due.iter_mut()) {
            if *next > now {
                continue;
            }
            while *next <= now {
                *next += p.every;
            }
            let opts = InsertOpts {
                queue: Some(cfg.queue.clone()),
                unique_by_args: true,
                ..InsertOpts::default()
            };
            let inserted = match inner.pool.acquire().await {
                Ok(mut conn) => insert_raw(&mut conn, p.kind, &p.args, &opts).await,
                Err(e) => Err(db_error("insert a periodic job", &e)),
            };
            if let Err(e) = inserted {
                tracing::warn!(error = %e, kind = p.kind, "jobs: cannot insert a periodic job");
            }
        }
    }
}

/// Takes or keeps the lead. Returns true when this worker is the leader.
async fn elect(inner: &Inner) -> anyhow::Result<bool> {
    let row: Option<(String,)> = sqlx::query_as(
        "INSERT INTO kit_leader (leader_id, elected_at, expires_at)
        VALUES ($1, now(), now() + make_interval(secs => $2))
        ON CONFLICT (name) DO UPDATE SET
            leader_id = EXCLUDED.leader_id,
            elected_at = CASE WHEN kit_leader.leader_id = EXCLUDED.leader_id
                THEN kit_leader.elected_at ELSE now() END,
            expires_at = EXCLUDED.expires_at
        WHERE kit_leader.leader_id = EXCLUDED.leader_id OR kit_leader.expires_at < now()
        RETURNING leader_id",
    )
    .bind(&inner.config.id)
    .bind(inner.config.leader_ttl.as_secs_f64())
    .fetch_optional(&inner.pool)
    .await
    .map_err(|e| db_error("elect the leader", &e))?;
    Ok(row.is_some())
}

/// Moves jobs that run longer than `rescue_after` back to retryable (or to
/// discarded after the last attempt). Returns the number of jobs.
async fn rescue(inner: &Inner) -> anyhow::Result<u64> {
    // The backoff of attempts 1 to 100, so one statement can rescue jobs
    // with different attempt numbers.
    let delays: Vec<f64> = (1..=100)
        .map(|a| inner.config.backoff.delay(a).as_secs_f64())
        .collect();
    let last = delays[delays.len() - 1];
    let done = sqlx::query(
        "WITH stuck AS (
            SELECT id FROM kit_job
            WHERE state = 'running' AND attempted_at < now() - make_interval(secs => $1)
            ORDER BY id LIMIT 1000
            FOR UPDATE SKIP LOCKED
        ), next AS (
            SELECT j.id, now() + make_interval(secs => coalesce(($2::float8[])[j.attempt], $3)) AS retry_at
            FROM kit_job j JOIN stuck USING (id)
        )
        UPDATE kit_job j SET
            state = CASE WHEN j.attempt >= j.max_attempts THEN 'discarded' ELSE 'retryable' END::kit_job_state,
            finalized_at = CASE WHEN j.attempt >= j.max_attempts THEN now() END,
            scheduled_at = next.retry_at,
            errors = array_append(j.errors, jsonb_build_object(
                'attempt', j.attempt, 'at', now(),
                'error', 'rescued: the job ran longer than the rescue time',
                'retry_at', next.retry_at))
        FROM next WHERE j.id = next.id",
    )
    .bind(inner.config.rescue_after.as_secs_f64())
    .bind(&delays)
    .bind(last)
    .execute(&inner.pool)
    .await
    .map_err(|e| db_error("rescue stuck jobs", &e))?;
    Ok(done.rows_affected())
}

/// A database error with a `Try:` line. A missing table means the kit
/// migrations did not run.
fn db_error(what: &str, err: &sqlx::Error) -> anyhow::Error {
    let undefined_table =
        matches!(err, sqlx::Error::Database(db) if db.code().as_deref() == Some("42P01"));
    let try_line = if undefined_table {
        "run the migrations: <app> migrate up"
    } else {
        "check DATABASE_URL and that PostgreSQL runs"
    };
    anyhow::anyhow!("jobs: {what}: {err}\nTry: {try_line}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn river_backoff_grows_with_the_attempt() {
        let b = Backoff::river();
        for (attempt, secs) in [(1, 1.0), (2, 16.0), (3, 81.0), (10, 10_000.0)] {
            let d = b.delay(attempt).as_secs_f64();
            assert!(
                (secs * 0.9..=secs * 1.1).contains(&d),
                "attempt {attempt}: {d}"
            );
        }
        assert!(b.delay(100).as_secs_f64() <= 86_400.0);
    }

    #[test]
    fn queue_of_reads_the_trigger_payload() {
        assert_eq!(
            queue_of(r#"{"queue" : "default"}"#).as_deref(),
            Some("default")
        );
        assert_eq!(queue_of("nope"), None);
    }
}
