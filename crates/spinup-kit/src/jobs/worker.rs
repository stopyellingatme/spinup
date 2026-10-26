//! The worker (Go `kit/jobs/worker.go`): the fetch loop, the handlers, the outcome of each
//! attempt, the listener, and the app command `worker`.

use std::collections::HashMap;
use std::fmt;
use std::future::Future;
use std::panic::AssertUnwindSafe;
use std::pin::Pin;
use std::sync::Arc;
use std::time::{Duration, Instant};

use futures::FutureExt as _;
use serde_json::Value;
use sqlx::postgres::PgListener;
use sqlx::types::Json;
use sqlx::{PgConnection, PgPool};
use tokio::sync::{Notify, Semaphore, mpsc};
use tokio::task::{AbortHandle, JoinError, JoinSet};
use tracing::Instrument as _;

use super::client::check_queue;
use super::leader;
use super::metrics::{self, JobMetrics};
use super::periodic::PeriodicJob;
use super::{Args, CONTROL_CHANNEL, DEFAULT_QUEUE, Job, JobResult, NOTIFY_CHANNEL, db_error};
use crate::app::{Command, CommandInput};
use crate::config::Base;

/// The wait before the next attempt, from the number of the failed attempt.
#[derive(Clone)]
pub struct Backoff(Arc<dyn Fn(i16) -> Duration + Send + Sync>);

impl fmt::Debug for Backoff {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Backoff")
    }
}

impl Backoff {
    /// Returns a backoff from a function of the attempt number.
    pub fn new(f: impl Fn(i16) -> Duration + Send + Sync + 'static) -> Self {
        Self(Arc::new(f))
    }

    /// The River default: attempt⁴ seconds, plus or minus 10 % jitter, at most 24 hours.
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

impl Default for Backoff {
    fn default() -> Self {
        Self::river()
    }
}

/// The configuration of a worker. [`Config::default`] has the production values.
#[derive(Debug, Clone)]
pub struct Config {
    /// The worker ID in `attempted_by` and `kit_leader`. The default is `worker-<uuid>`.
    pub id: String,
    /// The queue that the worker takes jobs from (default [`DEFAULT_QUEUE`]).
    pub queue: String,
    /// The number of jobs that run at the same time (default 10, Go
    /// `DefaultQueueWorkers`). The pool needs at least `concurrency + 2` connections.
    pub concurrency: usize,
    /// The fetch interval when no notification comes (default 1 s).
    pub poll_interval: Duration,
    /// The shortest time between a fetch and a fetch that a notification causes (default
    /// 100 ms). It keeps a burst of inserts from causing a burst of fetches.
    pub fetch_cooldown: Duration,
    /// Listen for `NOTIFY kit_insert` and the control messages (default true). Without it,
    /// the worker only polls.
    pub listen: bool,
    /// The rescuer moves a job that runs longer than this back to `retryable` (default
    /// 1 h, the River default).
    pub rescue_after: Duration,
    /// How often the worker tries to be the leader and, as the leader, runs the rescuer,
    /// the cleaner, and the periodic jobs (default 1 s).
    pub maintenance_interval: Duration,
    /// The time the leader keeps the lead without a new election (default 10 s).
    pub leader_ttl: Duration,
    /// The wait before each retry (default [`Backoff::river`]).
    pub backoff: Backoff,
    /// How long a stop waits for the running jobs (default 10 s). Then the worker cancels
    /// them, and they retry later.
    pub stop_timeout: Duration,
    /// How often the leader deletes old finalized jobs (default 30 s).
    pub clean_interval: Duration,
    /// The time a completed job stays in the table (default 24 h). `None` keeps it.
    pub completed_retention: Option<Duration>,
    /// The time a cancelled job stays in the table (default 24 h). `None` keeps it.
    pub cancelled_retention: Option<Duration>,
    /// The time a discarded job stays in the table (default 7 days). `None` keeps it.
    pub discarded_retention: Option<Duration>,
}

impl Default for Config {
    fn default() -> Self {
        const HOUR: Duration = Duration::from_secs(3600);
        Self {
            id: format!("worker-{}", uuid::Uuid::new_v4().simple()),
            queue: DEFAULT_QUEUE.into(),
            concurrency: 10,
            poll_interval: Duration::from_secs(1),
            fetch_cooldown: Duration::from_millis(100),
            listen: true,
            rescue_after: HOUR,
            maintenance_interval: Duration::from_secs(1),
            leader_ttl: Duration::from_secs(10),
            backoff: Backoff::river(),
            stop_timeout: Duration::from_secs(10),
            clean_interval: Duration::from_secs(30),
            completed_retention: Some(24 * HOUR),
            cancelled_retention: Some(24 * HOUR),
            discarded_retention: Some(7 * 24 * HOUR),
        }
    }
}

impl Config {
    /// Returns the default configuration with the stop timeout of the app
    /// (`SHUTDOWN_TIMEOUT`, Go `cfg.ShutdownTimeout` of `cmd/worker`).
    pub fn from_base(base: &Base) -> Self {
        Self {
            stop_timeout: base.shutdown_timeout,
            ..Self::default()
        }
    }

    fn check(&self) -> anyhow::Result<()> {
        const WHAT: &str = "jobs: start the worker";
        if self.concurrency == 0 {
            anyhow::bail!(
                "{WHAT}: the concurrency is 0 (want 1 or more)\nTry: set Config.concurrency to 1 or more"
            );
        }
        check_queue(&self.queue).map_err(|why| anyhow::anyhow!("{WHAT}: {why}"))?;
        for (name, d) in [
            ("poll_interval", self.poll_interval),
            ("maintenance_interval", self.maintenance_interval),
            ("leader_ttl", self.leader_ttl),
            ("clean_interval", self.clean_interval),
        ] {
            if d.is_zero() {
                anyhow::bail!(
                    "{WHAT}: Config.{name} is zero (want a positive duration)\nTry: start from Config::default()"
                );
            }
        }
        if self.id.is_empty() || self.id.chars().count() >= 128 {
            anyhow::bail!(
                "{WHAT}: the worker ID {:?} has {} characters (want 1 to 127)\nTry: start from Config::default()",
                self.id,
                self.id.chars().count()
            );
        }
        Ok(())
    }
}

/// A handler error that runs the job again after a delay (River `JobSnooze`). The attempt
/// does not count against `max_attempts`. Return it with [`snooze`].
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Snooze(pub Duration);

impl fmt::Display for Snooze {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "jobs: snooze the job for {:?}", self.0)
    }
}

impl std::error::Error for Snooze {}

/// Returns the handler error that runs the job again after `delay`, without a used attempt
/// (River `JobSnooze`).
///
/// ```no_run
/// # use spinup_kit::jobs::{self, Job, JobResult};
/// # async fn handler(job: Job<u8>, ready: bool) -> JobResult {
/// if !ready {
///     return Err(jobs::snooze(std::time::Duration::from_secs(30)));
/// }
/// # Ok(())
/// # }
/// ```
pub fn snooze(delay: Duration) -> anyhow::Error {
    anyhow::Error::new(Snooze(delay))
}

/// A handler error that stops the job for good (River `JobCancel`). The job goes to
/// `cancelled` and never runs again. Return it with [`cancel`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Cancel(pub String);

impl fmt::Display for Cancel {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "jobs: cancel the job: {}", self.0)
    }
}

impl std::error::Error for Cancel {}

/// Returns the handler error that cancels the job for good, with `reason` in the job
/// errors (River `JobCancel`).
pub fn cancel(reason: impl fmt::Display) -> anyhow::Error {
    anyhow::Error::new(Cancel(reason.to_string()))
}

/// Marks `job` as completed on `conn` (River `JobCompleteTx`). Call it in the transaction
/// of the handler: the change of the handler and the completion then commit together, so
/// the change occurs once, also when the worker dies.
///
/// ```no_run
/// # use spinup_kit::jobs::{self, Job, JobResult};
/// # async fn handler(pool: spinup_kit::db::PgPool, job: Job<i64>) -> JobResult {
/// let mut tx = pool.begin().await?;
/// sqlx::query("UPDATE accounts SET credit = credit + 1 WHERE id = $1")
///     .bind(job.args)
///     .execute(&mut *tx)
///     .await?;
/// jobs::complete_tx(&mut tx, &job).await?;
/// tx.commit().await?;
/// Ok(())
/// # }
/// ```
///
/// # Errors
///
/// The job does not run this attempt any more (a rescue or a cancel changed it), or a
/// database error. Return the error from the handler, so the transaction rolls back.
pub async fn complete_tx<A>(conn: &mut PgConnection, job: &Job<A>) -> anyhow::Result<()> {
    let what = format!("complete the job {} in the transaction", job.id);
    let done = sqlx::query(
        "UPDATE kit_job SET state = 'completed', finalized_at = now()
        WHERE id = $1 AND attempt = $2 AND state = 'running'",
    )
    .bind(job.id)
    .bind(job.attempt)
    .execute(conn)
    .await
    .map_err(|e| db_error(&what, &e))?;
    if done.rows_affected() == 0 {
        anyhow::bail!(
            "jobs: {what}: the job does not run attempt {} any more (a rescue or a cancel changed it)\nTry: return this error from the handler, so the transaction rolls back",
            job.attempt
        );
    }
    Ok(())
}

type BoxFuture = Pin<Box<dyn Future<Output = JobResult> + Send>>;
type Handler = Arc<dyn Fn(i64, i16, Value) -> BoxFuture + Send + Sync>;

/// Works the jobs of one queue (Go `jobs.Worker`). Register the handlers and the periodic
/// jobs, then call [`Worker::run`]. Run more workers (or processes) to share the load.
pub struct Worker {
    pool: PgPool,
    config: Config,
    handlers: HashMap<&'static str, Handler>,
    periodic: Vec<PeriodicJob>,
}

impl fmt::Debug for Worker {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Worker")
            .field("config", &self.config)
            .field("kinds", &self.kinds())
            .field("periodic", &self.periodic.len())
            .finish_non_exhaustive()
    }
}

/// The state that the fetch loop, the jobs, and the leader share.
pub(super) struct Inner {
    pub(super) pool: PgPool,
    pub(super) config: Config,
    pub(super) handlers: HashMap<&'static str, Handler>,
    pub(super) periodic: Vec<PeriodicJob>,
    metrics: JobMetrics,
}

#[derive(sqlx::FromRow)]
struct Fetched {
    id: i64,
    kind: String,
    args: Json<Value>,
    attempt: i16,
    metadata: Json<Value>,
}

/// A job that a task of the fetch loop runs.
struct Running {
    id: i64,
    attempt: i16,
    kind: String,
    abort: AbortHandle,
    started: Instant,
    /// A client asked to cancel the job.
    cancel: bool,
}

/// The outcome of one attempt, as the job state after it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Outcome {
    Completed,
    Retryable,
    Discarded,
    Cancelled,
    Snoozed,
}

impl Outcome {
    fn as_str(self) -> &'static str {
        match self {
            Self::Completed => "completed",
            Self::Retryable => "retryable",
            Self::Discarded => "discarded",
            Self::Cancelled => "cancelled",
            Self::Snoozed => "snoozed",
        }
    }

    fn from_state(state: &str) -> Self {
        match state {
            "discarded" => Self::Discarded,
            "cancelled" => Self::Cancelled,
            "completed" => Self::Completed,
            _ => Self::Retryable,
        }
    }
}

impl Worker {
    /// Returns a worker on `pool` (Go `NewWorker`). It does not connect until
    /// [`Worker::run`]. The pool needs at least `concurrency + 2` connections.
    pub fn new(pool: PgPool, config: Config) -> Self {
        Self {
            pool,
            config,
            handlers: HashMap::new(),
            periodic: Vec::new(),
        }
    }

    /// Adds `handler` as the function that works jobs of the kind `A` (Go `Register`).
    ///
    /// # Panics
    ///
    /// When the kind already has a handler: that is a programming error.
    pub fn register<A, F, Fut>(&mut self, handler: F) -> &mut Self
    where
        A: Args,
        F: Fn(Job<A>) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = JobResult> + Send + 'static,
    {
        let handler = Arc::new(handler);
        let erased: Handler = Arc::new(move |id, attempt, value| {
            let handler = handler.clone();
            Box::pin(async move {
                let args: A = serde_json::from_value(value).map_err(|e| {
                    anyhow::anyhow!(
                        "jobs: decode the args of a {:?} job: {e}\nTry: keep the args type compatible with the jobs that wait",
                        A::KIND
                    )
                })?;
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

    /// Inserts a unique job with `args` every `every` while this worker is the leader (Go
    /// `Worker.Periodic`). More workers do not insert more jobs. The first insert comes one
    /// period after the worker becomes the leader.
    ///
    /// # Panics
    ///
    /// When `every` is zero or the args do not encode as JSON: both are programming
    /// errors.
    pub fn periodic<A: Args>(&mut self, every: Duration, args: &A) -> &mut Self {
        assert!(
            !every.is_zero(),
            "jobs: the periodic job {:?} needs an interval above zero",
            A::KIND
        );
        let args = serde_json::to_value(args).unwrap_or_else(|e| {
            panic!(
                "jobs: the args of the periodic job {:?} do not encode as JSON: {e}",
                A::KIND
            )
        });
        self.periodic.push(PeriodicJob {
            every,
            kind: A::KIND,
            args,
        });
        self
    }

    /// The registered job kinds, sorted (Go `Worker.Kinds`).
    pub fn kinds(&self) -> Vec<&'static str> {
        let mut kinds: Vec<_> = self.handlers.keys().copied().collect();
        kinds.sort_unstable();
        kinds
    }

    /// Works jobs until `shutdown` completes (Go `Start` and `Stop`). Then it stops the
    /// fetches, waits for the running jobs at most `stop_timeout`, cancels the others (they
    /// retry later), and gives up the lead.
    ///
    /// A worker with no registered job logs a warning and waits for `shutdown`, so the
    /// worker of a new app can run.
    ///
    /// Dropping the future (a crash, or an aborted task) also stops every running job of
    /// this worker. The rescuer then retries them after `rescue_after`.
    ///
    /// # Errors
    ///
    /// A bad configuration, or running jobs that did not finish in `stop_timeout`. Each
    /// error has a `Try:` line.
    pub async fn run(self, shutdown: impl Future<Output = ()> + Send) -> anyhow::Result<()> {
        self.config.check()?;
        if self.handlers.is_empty() {
            tracing::warn!(
                "jobs: no job is registered, the worker does nothing. Try: spinup g job <name>"
            );
            shutdown.await;
            return Ok(());
        }
        let kinds = self.kinds();
        let inner = Arc::new(Inner {
            pool: self.pool,
            config: self.config,
            handlers: self.handlers,
            periodic: self.periodic,
            metrics: JobMetrics::new(),
        });
        let cfg = &inner.config;
        tracing::info!(worker = %cfg.id, queue = %cfg.queue, ?kinds, periodic = inner.periodic.len(), "jobs: worker started");
        let wake = Arc::new(Notify::new());
        let (cancel_tx, mut cancel_rx) = mpsc::unbounded_channel::<i64>();
        let mut background = JoinSet::new();
        if cfg.listen {
            background.spawn(listen(inner.clone(), wake.clone(), cancel_tx.clone()));
        }
        background.spawn(leader::maintain(inner.clone()));

        let slots = Arc::new(Semaphore::new(cfg.concurrency));
        let mut running: JoinSet<()> = JoinSet::new();
        let mut jobs: HashMap<tokio::task::Id, Running> = HashMap::new();
        let mut last_fetch: Option<Instant> = None;
        let mut notified = false;
        tokio::pin!(shutdown);
        'work: loop {
            while let Some(res) = running.try_join_next_with_id() {
                reap(&inner, &mut jobs, res).await;
            }
            if notified && let Some(last) = last_fetch {
                tokio::select! {
                    () = &mut shutdown => break 'work,
                    () = tokio::time::sleep_until((last + cfg.fetch_cooldown).into()) => {}
                }
            }
            notified = false;
            let free = slots.available_permits();
            let mut backlog = true;
            if free > 0 {
                last_fetch = Some(Instant::now());
                match fetch(&inner, free).await {
                    Ok(fetched) => {
                        backlog = fetched.len() == free;
                        for job in fetched {
                            let Ok(permit) = slots.clone().try_acquire_owned() else {
                                break;
                            };
                            let (id, attempt, kind) = (job.id, job.attempt, job.kind.clone());
                            let task_inner = inner.clone();
                            let abort = running.spawn(async move {
                                execute(&task_inner, job).await;
                                drop(permit);
                            });
                            jobs.insert(
                                abort.id(),
                                Running {
                                    id,
                                    attempt,
                                    kind,
                                    abort,
                                    started: Instant::now(),
                                    cancel: false,
                                },
                            );
                        }
                    }
                    Err(e) => {
                        backlog = false;
                        tracing::error!(error = %e, "jobs: fetch failed");
                    }
                }
            }
            tokio::select! {
                () = &mut shutdown => break,
                () = wake.notified() => notified = true,
                () = tokio::time::sleep(cfg.poll_interval) => {}
                Some(id) = cancel_rx.recv() => cancel_running(&mut jobs, id),
                Some(res) = running.join_next_with_id(), if backlog && !running.is_empty() => {
                    reap(&inner, &mut jobs, res).await;
                }
            }
        }
        drop(cancel_tx);

        let drained = tokio::time::timeout(cfg.stop_timeout, async {
            while let Some(res) = running.join_next_with_id().await {
                reap(&inner, &mut jobs, res).await;
            }
        })
        .await;
        running.abort_all();
        while let Some(res) = running.join_next_with_id().await {
            reap(&inner, &mut jobs, res).await;
        }
        background.abort_all();
        // Wait for the cancelled tasks, so no election runs after the resign.
        while background.join_next().await.is_some() {}
        leader::resign(&inner).await;
        tracing::info!(worker = %cfg.id, "jobs: worker stopped");
        if drained.is_err() {
            anyhow::bail!(
                "jobs: stop the worker: running jobs did not finish in {:?} and were cancelled (they retry later)\nTry: raise the stop timeout (SHUTDOWN_TIMEOUT), or make long jobs end sooner",
                cfg.stop_timeout
            );
        }
        Ok(())
    }
}

/// Returns the app command `worker` (Go `cmd/worker`). The app registers it with
/// `App::command("worker", ..)`. The command calls `register` with a new [`Worker`] on
/// `pool`, then works jobs until SIGINT or SIGTERM. It takes no arguments.
///
/// ```no_run
/// use spinup_kit::app::App;
/// use spinup_kit::config::Base;
/// use spinup_kit::db;
/// use spinup_kit::jobs::{self, Config};
///
/// # fn demo(config: Base) -> anyhow::Result<App> {
/// let pool = db::pool(&config)?;
/// Ok(App::new(&config).command(
///     "worker",
///     jobs::worker_command(pool, Config::from_base(&config), |worker| {
///         // crate::jobs::register(worker, &pool)
///         let _ = worker;
///         Ok(())
///     }),
/// ))
/// # }
/// ```
pub fn worker_command(
    pool: PgPool,
    config: Config,
    register: impl FnOnce(&mut Worker) -> anyhow::Result<()> + Send + 'static,
) -> Command {
    Command::new(move |CommandInput { args, shutdown }| async move {
        if !args.is_empty() {
            anyhow::bail!(
                "worker: unexpected arguments {args:?} (the command takes none)\nTry: run the worker command without arguments"
            );
        }
        let mut worker = Worker::new(pool, config);
        register(&mut worker)?;
        worker.run(shutdown).await
    })
}

async fn fetch(inner: &Inner, limit: usize) -> anyhow::Result<Vec<Fetched>> {
    let jobs = sqlx::query_as::<_, Fetched>(
        "WITH next AS (
            SELECT id FROM kit_job
            WHERE state IN ('available', 'retryable', 'scheduled')
                AND queue = $1 AND scheduled_at <= now()
                AND NOT EXISTS (
                    SELECT 1 FROM kit_queue q WHERE q.name = $1 AND q.paused_at IS NOT NULL)
            ORDER BY priority, scheduled_at, id
            LIMIT $2
            FOR UPDATE SKIP LOCKED
        )
        UPDATE kit_job j
        SET state = 'running', attempt = j.attempt + 1, attempted_at = now(),
            attempted_by = array_append(j.attempted_by, $3)
        FROM next WHERE j.id = next.id
        RETURNING j.id, j.kind, j.args, j.attempt, j.metadata",
    )
    .bind(&inner.config.queue)
    .bind(i64::try_from(limit).unwrap_or(i64::MAX))
    .bind(&inner.config.id)
    .fetch_all(&inner.pool)
    .await
    .map_err(|e| db_error("fetch jobs", &e))?;
    Ok(jobs)
}

/// Runs one attempt and records its outcome.
async fn execute(inner: &Inner, job: Fetched) {
    let span = tracing::info_span!(
        parent: None,
        "job",
        otel.name = %format!("job {}", job.kind),
        otel.kind = "consumer",
        job.kind = %job.kind,
        job.id = job.id,
        job.attempt = job.attempt,
        job.queue = %inner.config.queue,
    );
    let log_span = metrics::link(&span, &job.metadata.0);
    let started = Instant::now();
    let result = match inner.handlers.get(job.kind.as_str()) {
        None => Err(anyhow::anyhow!(
            "jobs: no handler for the kind {:?}\nTry: register the handler in the worker of the queue {:?}",
            job.kind,
            inner.config.queue
        )),
        Some(handler) => {
            let run = AssertUnwindSafe(handler(job.id, job.attempt, job.args.0)).catch_unwind();
            match run
                .instrument(log_span.clone())
                .instrument(span.clone())
                .await
            {
                Ok(result) => result,
                Err(panic) => Err(anyhow::anyhow!(
                    "jobs: the handler panicked: {}",
                    panic_text(&*panic)
                )),
            }
        }
    };
    let took = started.elapsed();
    let recorded = record(inner, job.id, job.attempt, &result).await;
    let _span = span.enter();
    let _log = log_span.enter();
    report(inner, &job.kind, &result, recorded, took);
}

/// Records the outcome of an attempt. Each update checks that the job still runs this
/// attempt. `Ok(None)` means that the job changed its state while it ran (a rescue).
async fn record(
    inner: &Inner,
    id: i64,
    attempt: i16,
    result: &JobResult,
) -> Result<Option<Outcome>, sqlx::Error> {
    let err = match result {
        Ok(()) => {
            // A handler that called complete_tx already completed the job.
            let done = sqlx::query(
                "UPDATE kit_job SET state = 'completed', finalized_at = coalesce(finalized_at, now())
                WHERE id = $1 AND attempt = $2 AND state IN ('running', 'completed')",
            )
            .bind(id)
            .bind(attempt)
            .execute(&inner.pool)
            .await?;
            return Ok((done.rows_affected() > 0).then_some(Outcome::Completed));
        }
        Err(err) => err,
    };
    if let Some(Snooze(delay)) = err.downcast_ref::<Snooze>() {
        let done = sqlx::query(
            "UPDATE kit_job SET
                state = CASE WHEN $3::float8 > 0 THEN 'scheduled' ELSE 'available' END::kit_job_state,
                scheduled_at = now() + make_interval(secs => $3::float8),
                max_attempts = least(max_attempts + 1, 32767)
            WHERE id = $1 AND state = 'running' AND attempt = $2",
        )
        .bind(id)
        .bind(attempt)
        .bind(delay.as_secs_f64())
        .execute(&inner.pool)
        .await?;
        return Ok((done.rows_affected() > 0).then_some(Outcome::Snoozed));
    }
    if err.downcast_ref::<Cancel>().is_some() {
        return finish_cancelled(inner, id, attempt, &format!("{err:#}")).await;
    }
    fail(inner, id, attempt, &format!("{err:#}")).await
}

/// Records a failed attempt: `retryable` with the backoff, `discarded` after the last
/// attempt, or `cancelled` when a client asked to cancel the job.
async fn fail(
    inner: &Inner,
    id: i64,
    attempt: i16,
    error: &str,
) -> Result<Option<Outcome>, sqlx::Error> {
    let state: Option<String> = sqlx::query_scalar(
        "UPDATE kit_job SET
            state = CASE
                WHEN metadata ? 'cancel_attempted_at' THEN 'cancelled'
                WHEN attempt >= max_attempts THEN 'discarded'
                ELSE 'retryable' END::kit_job_state,
            finalized_at = CASE
                WHEN metadata ? 'cancel_attempted_at' OR attempt >= max_attempts THEN now() END,
            scheduled_at = now() + make_interval(secs => $3),
            errors = array_append(errors, jsonb_build_object(
                'attempt', attempt, 'at', now(), 'error', $4::text,
                'retry_at', now() + make_interval(secs => $3)))
        WHERE id = $1 AND state = 'running' AND attempt = $2
        RETURNING state::text",
    )
    .bind(id)
    .bind(attempt)
    .bind(inner.config.backoff.delay(attempt).as_secs_f64())
    .bind(error)
    .fetch_optional(&inner.pool)
    .await?;
    Ok(state.as_deref().map(Outcome::from_state))
}

async fn finish_cancelled(
    inner: &Inner,
    id: i64,
    attempt: i16,
    error: &str,
) -> Result<Option<Outcome>, sqlx::Error> {
    let done = sqlx::query(
        "UPDATE kit_job SET state = 'cancelled', finalized_at = now(),
            errors = array_append(errors, jsonb_build_object(
                'attempt', attempt, 'at', now(), 'error', $3::text))
        WHERE id = $1 AND state = 'running' AND attempt = $2",
    )
    .bind(id)
    .bind(attempt)
    .bind(error)
    .execute(&inner.pool)
    .await?;
    Ok((done.rows_affected() > 0).then_some(Outcome::Cancelled))
}

/// Writes the log line and the metric of an attempt.
fn report(
    inner: &Inner,
    kind: &str,
    result: &JobResult,
    recorded: Result<Option<Outcome>, sqlx::Error>,
    took: Duration,
) {
    let elapsed_ms = u64::try_from(took.as_millis()).unwrap_or(u64::MAX);
    match recorded {
        Err(e) => {
            // The job stays running. The rescuer retries it later.
            tracing::error!(error = %e, "jobs: cannot record the job result");
        }
        Ok(None) => tracing::warn!("jobs: the job changed its state while it ran (rescued?)"),
        Ok(Some(outcome)) => {
            inner
                .metrics
                .record(kind, &inner.config.queue, outcome.as_str(), took);
            match (outcome, result) {
                (Outcome::Completed, _) => tracing::debug!(elapsed_ms, "jobs: completed"),
                (_, Err(err)) => tracing::warn!(
                    error = %format!("{err:#}"),
                    result = outcome.as_str(),
                    elapsed_ms,
                    "jobs: attempt ended"
                ),
                (_, Ok(())) => {}
            }
        }
    }
}

/// Removes a finished task from `jobs`. A task that a cancel or a stop aborted did not
/// record its outcome, so this records it.
async fn reap(
    inner: &Inner,
    jobs: &mut HashMap<tokio::task::Id, Running>,
    res: Result<(tokio::task::Id, ()), JoinError>,
) {
    let (task, why) = match res {
        Ok((task, ())) => {
            jobs.remove(&task);
            return;
        }
        Err(e) if e.is_panic() => (e.id(), "jobs: the job task panicked"),
        Err(e) => (e.id(), "jobs: the worker stopped before the job finished"),
    };
    let Some(job) = jobs.remove(&task) else {
        return;
    };
    let span = tracing::info_span!(
        parent: None,
        "job",
        job.kind = %job.kind,
        job.id = job.id,
        job.attempt = job.attempt,
    );
    let took = job.started.elapsed();
    let recorded = if job.cancel {
        finish_cancelled(
            inner,
            job.id,
            job.attempt,
            "cancelled: a client cancelled the running job",
        )
        .instrument(span.clone())
        .await
    } else {
        fail(inner, job.id, job.attempt, why)
            .instrument(span.clone())
            .await
    };
    let _span = span.enter();
    let result = if job.cancel {
        Err(cancel("a client cancelled the running job"))
    } else {
        Err(anyhow::anyhow!(why))
    };
    report(inner, &job.kind, &result, recorded, took);
}

/// Stops the task of the running job `id`, after a cancel request from a client.
fn cancel_running(jobs: &mut HashMap<tokio::task::Id, Running>, id: i64) {
    if let Some(job) = jobs.values_mut().find(|j| j.id == id) {
        job.cancel = true;
        job.abort.abort();
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

/// Wakes the fetch loop on each insert notification for this queue, and passes the cancel
/// requests on. After a lost connection it wakes the loop too, because notifications can
/// be lost.
async fn listen(inner: Arc<Inner>, wake: Arc<Notify>, cancel: mpsc::UnboundedSender<i64>) {
    loop {
        match PgListener::connect_with(&inner.pool).await {
            Ok(mut listener) => {
                if let Err(e) = listener.listen_all([NOTIFY_CHANNEL, CONTROL_CHANNEL]).await {
                    tracing::warn!(error = %e, "jobs: LISTEN failed, the worker polls");
                } else {
                    wake.notify_one();
                    loop {
                        match listener.try_recv().await {
                            Ok(Some(n)) if n.channel() == CONTROL_CHANNEL => {
                                if let Some(id) = cancel_request(n.payload(), &inner.config.queue) {
                                    let _ = cancel.send(id);
                                }
                            }
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
                tracing::warn!(error = %e, "jobs: cannot connect the listener, the worker polls");
            }
        }
        tokio::time::sleep(inner.config.poll_interval.max(Duration::from_millis(100))).await;
    }
}

/// The queue of an insert notification, such as `{"queue": "default"}`.
fn queue_of(payload: &str) -> Option<String> {
    let v: Value = serde_json::from_str(payload).ok()?;
    v.get("queue")?.as_str().map(str::to_owned)
}

/// The job ID of a cancel request for `queue`, such as
/// `{"action": "cancel", "job_id": 7, "queue": "default"}`.
fn cancel_request(payload: &str, queue: &str) -> Option<i64> {
    let v: Value = serde_json::from_str(payload).ok()?;
    if v.get("action")?.as_str()? != "cancel" {
        return None;
    }
    if let Some(q) = v.get("queue").and_then(Value::as_str)
        && q != queue
    {
        return None;
    }
    v.get("job_id")?.as_i64()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn jobs_river_backoff_grows_with_the_attempt() {
        let b = Backoff::river();
        for (attempt, secs) in [(1, 1.0), (2, 16.0), (3, 81.0), (10, 10_000.0)] {
            let d = b.delay(attempt).as_secs_f64();
            assert!(
                (secs * 0.9..=secs * 1.1).contains(&d),
                "attempt {attempt}: {d}"
            );
        }
        assert!(b.delay(100).as_secs_f64() <= 86_400.0);
        assert!(b.delay(0).as_secs_f64() <= 1.1);
    }

    #[test]
    fn jobs_notification_payloads() {
        assert_eq!(
            queue_of(r#"{"queue" : "default"}"#).as_deref(),
            Some("default")
        );
        assert_eq!(queue_of("nope"), None);
        let req = r#"{"action": "cancel", "job_id": 7, "queue": "default"}"#;
        assert_eq!(cancel_request(req, "default"), Some(7));
        assert_eq!(cancel_request(req, "mail"), None);
        assert_eq!(
            cancel_request(r#"{"action": "cancel", "job_id": 8}"#, "mail"),
            Some(8)
        );
        assert_eq!(
            cancel_request(r#"{"action": "pause", "job_id": 7}"#, "default"),
            None
        );
        assert_eq!(cancel_request("{}", "default"), None);
    }

    #[test]
    fn jobs_config_check() {
        assert!(Config::default().check().is_ok());
        let bad = [
            (
                Config {
                    concurrency: 0,
                    ..Config::default()
                },
                "concurrency is 0",
            ),
            (
                Config {
                    queue: String::new(),
                    ..Config::default()
                },
                "has 0 characters",
            ),
            (
                Config {
                    maintenance_interval: Duration::ZERO,
                    ..Config::default()
                },
                "Config.maintenance_interval is zero",
            ),
            (
                Config {
                    id: String::new(),
                    ..Config::default()
                },
                "the worker ID",
            ),
        ];
        for (cfg, want) in bad {
            let err = cfg.check().unwrap_err().to_string();
            assert!(err.starts_with("jobs: start the worker: "), "{err}");
            assert!(err.contains(want), "{err}");
            assert!(err.contains("\nTry: "), "{err}");
        }
    }

    #[test]
    fn jobs_config_defaults() {
        let c = Config::default();
        assert_eq!(c.queue, DEFAULT_QUEUE);
        assert_eq!(c.concurrency, 10);
        assert_eq!(c.rescue_after, Duration::from_secs(3600));
        assert_eq!(c.leader_ttl, Duration::from_secs(10));
        assert_eq!(c.poll_interval, Duration::from_secs(1));
        assert_eq!(c.maintenance_interval, Duration::from_secs(1));
        assert!(c.id.starts_with("worker-"));
        assert_ne!(c.id, Config::default().id);
        let base = Base {
            shutdown_timeout: Duration::from_secs(42),
            ..Base::default()
        };
        assert_eq!(
            Config::from_base(&base).stop_timeout,
            Duration::from_secs(42)
        );
    }

    #[test]
    fn jobs_handler_errors() {
        let s = snooze(Duration::from_secs(3));
        assert_eq!(
            s.downcast_ref::<Snooze>(),
            Some(&Snooze(Duration::from_secs(3)))
        );
        let c = cancel("bad input").context("send the mail");
        assert_eq!(
            c.downcast_ref::<Cancel>(),
            Some(&Cancel("bad input".into()))
        );
        assert_eq!(
            format!("{c:#}"),
            "send the mail: jobs: cancel the job: bad input"
        );
    }

    #[test]
    fn jobs_outcome_names() {
        assert_eq!(Outcome::from_state("discarded"), Outcome::Discarded);
        assert_eq!(Outcome::from_state("cancelled"), Outcome::Cancelled);
        assert_eq!(Outcome::from_state("retryable"), Outcome::Retryable);
        assert_eq!(Outcome::Snoozed.as_str(), "snoozed");
    }
}
