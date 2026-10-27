//! The client: inserts, cancels, and queue pauses (Go `kit/jobs/jobs.go`).

use std::time::Duration;

use serde_json::Value;
use sqlx::types::Json;
use sqlx::{PgConnection, PgPool};
use time::OffsetDateTime;

use super::{
    Args, CONTROL_CHANNEL, DEFAULT_MAX_ATTEMPTS, DEFAULT_QUEUE, JobState, NOTIFY_CHANNEL, db_error,
    metrics,
};

/// The insert. The state list is the predicate of the partial unique index
/// `kit_job_unique_key`: a unique job blocks an insert with the same key while it waits or
/// runs. A unique job that a concurrent transaction inserts is not in the snapshot of the
/// second SELECT, so the caller tries again.
const INSERT_SQL: &str = "WITH ins AS (
    INSERT INTO kit_job (kind, args, queue, priority, max_attempts, scheduled_at, state, unique_key, metadata)
    VALUES ($1::text, $2::jsonb, $3::text, $4::int2, $5::int2,
            coalesce($6::timestamptz, now()),
            CASE WHEN $6::timestamptz > now() THEN 'scheduled' ELSE 'available' END::kit_job_state,
            CASE WHEN $7::bool THEN sha256(convert_to($1::text || ':' || $2::jsonb::text, 'UTF8')) END,
            $8::jsonb)
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

/// The options of one insert (Go `InsertOption`). The default inserts on
/// [`DEFAULT_QUEUE`] with priority 1, now, and [`DEFAULT_MAX_ATTEMPTS`] attempts.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct InsertOpts {
    /// The queue (Go `Queue`). `None` selects [`DEFAULT_QUEUE`].
    pub queue: Option<String>,
    /// The priority (Go `Priority`), from 1 (the highest, the default) to 4.
    pub priority: Option<i16>,
    /// Run the job at this time or later, instead of now (Go `RunAt`).
    pub run_at: Option<OffsetDateTime>,
    /// The number of attempts before the job is discarded (Go `MaxAttempts`).
    pub max_attempts: Option<i16>,
    /// Skip the insert when a job of the same kind with the same args waits or runs (Go
    /// `UniqueByArgs`). The insert then returns the ID of that job.
    pub unique_by_args: bool,
}

impl InsertOpts {
    /// Returns options that run the job after `delay`.
    pub fn run_in(delay: Duration) -> Self {
        Self {
            run_at: Some(OffsetDateTime::now_utc() + delay),
            ..Self::default()
        }
    }

    /// Checks the values, so a bad value fails with a clear text and not with a database
    /// constraint.
    pub(super) fn check(&self, kind: &str) -> anyhow::Result<()> {
        let what = format!("jobs: insert a {kind:?} job");
        if let Some(p) = self.priority
            && !(1..=4).contains(&p)
        {
            anyhow::bail!(
                "{what}: the priority {p} is out of range (want 1 to 4)\nTry: set InsertOpts.priority to 1, 2, 3, or 4"
            );
        }
        if let Some(n) = self.max_attempts
            && n < 1
        {
            anyhow::bail!(
                "{what}: max_attempts is {n} (want 1 or more)\nTry: set InsertOpts.max_attempts to 1 or more"
            );
        }
        if let Some(q) = &self.queue {
            check_queue(q).map_err(|why| anyhow::anyhow!("{what}: {why}"))?;
        }
        Ok(())
    }
}

/// Checks a queue name: 1 to 127 characters. The error is the reason and a `Try:` line.
pub(super) fn check_queue(queue: &str) -> Result<(), String> {
    let n = queue.chars().count();
    if n == 0 || n >= 128 {
        return Err(format!(
            "the queue name {queue:?} has {n} characters (want 1 to 127)\nTry: use a short queue name, such as \"default\""
        ));
    }
    Ok(())
}

/// The result of an insert.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Inserted {
    /// The ID of the new job, or of the unique job that exists.
    pub id: i64,
    /// True when a unique job with the same args exists, so the insert added no job.
    pub duplicate: bool,
}

/// Inserts jobs, cancels jobs, and pauses queues (Go `jobs.Client`). Use it in the API
/// process. It does not work jobs. It is cheap to clone.
#[derive(Debug, Clone)]
pub struct Client {
    pool: PgPool,
}

impl Client {
    /// Returns a client on `pool`. It does not connect until a method runs. The kit
    /// migrations must have run.
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Adds a job (Go `Client.Insert`).
    ///
    /// # Errors
    ///
    /// A bad option, an args value that does not encode, or a database error. Each error
    /// has a `Try:` line. A missing table asks for the kit migrations.
    pub async fn insert<A: Args>(&self, args: &A, opts: &InsertOpts) -> anyhow::Result<Inserted> {
        opts.check(A::KIND)?;
        let mut conn = self
            .pool
            .acquire()
            .await
            .map_err(|e| db_error(&format!("insert a {:?} job", A::KIND), &e))?;
        self.insert_tx(&mut conn, args, opts).await
    }

    /// Adds a job on `conn` (Go `Client.InsertTx`). Pass the connection of a transaction
    /// (`&mut *tx`): workers see the job only after the commit, and a rollback removes it.
    ///
    /// # Errors
    ///
    /// The errors of [`Client::insert`].
    pub async fn insert_tx<A: Args>(
        &self,
        conn: &mut PgConnection,
        args: &A,
        opts: &InsertOpts,
    ) -> anyhow::Result<Inserted> {
        let value = serde_json::to_value(args).map_err(|e| {
            anyhow::anyhow!(
                "jobs: encode the args of a {:?} job: {e}\nTry: give the args type plain serde fields",
                A::KIND
            )
        })?;
        insert_raw(conn, A::KIND, &value, opts).await
    }

    /// Cancels the job `id` (River `JobCancel`). It returns the state after the cancel, or
    /// `None` when no job has the ID:
    ///
    /// - A waiting job (`available`, `scheduled`, `retryable`, `pending`) goes to
    ///   `cancelled` at once.
    /// - A running job stays `running`. Its worker gets a notification, stops the handler,
    ///   and records `cancelled`. When the notification is lost, the job goes to `cancelled`
    ///   at its next error or rescue, and to `completed` when the handler succeeds.
    /// - A final job does not change.
    ///
    /// # Errors
    ///
    /// A database error with a `Try:` line.
    pub async fn cancel(&self, id: i64) -> anyhow::Result<Option<JobState>> {
        let what = format!("cancel the job {id}");
        let err = |e: sqlx::Error| db_error(&what, &e);
        let mut tx = self.pool.begin().await.map_err(err)?;
        let row: Option<(String, String)> =
            sqlx::query_as("SELECT state::text, queue FROM kit_job WHERE id = $1 FOR UPDATE")
                .bind(id)
                .fetch_optional(&mut *tx)
                .await
                .map_err(err)?;
        let Some((state, queue)) = row else {
            return Ok(None);
        };
        let state: JobState = state.parse()?;
        let after = match state {
            s if s.is_final() => return Ok(Some(s)),
            JobState::Running => {
                sqlx::query(
                    "UPDATE kit_job SET metadata = metadata || jsonb_build_object('cancel_attempted_at', now())
                    WHERE id = $1",
                )
                .bind(id)
                .execute(&mut *tx)
                .await
                .map_err(err)?;
                let payload = serde_json::json!({"action": "cancel", "job_id": id, "queue": queue});
                sqlx::query("SELECT pg_notify($1, $2)")
                    .bind(CONTROL_CHANNEL)
                    .bind(payload.to_string())
                    .execute(&mut *tx)
                    .await
                    .map_err(err)?;
                JobState::Running
            }
            _ => {
                sqlx::query(
                    "UPDATE kit_job SET state = 'cancelled', finalized_at = now(),
                        metadata = metadata || jsonb_build_object('cancel_attempted_at', now())
                    WHERE id = $1",
                )
                .bind(id)
                .execute(&mut *tx)
                .await
                .map_err(err)?;
                JobState::Cancelled
            }
        };
        tx.commit().await.map_err(err)?;
        Ok(Some(after))
    }

    /// Pauses `queue` (River `QueuePause`): workers fetch no new job from it. Running jobs
    /// go on. A second pause changes nothing.
    ///
    /// # Errors
    ///
    /// A bad queue name, or a database error. Each error has a `Try:` line.
    pub async fn pause(&self, queue: &str) -> anyhow::Result<()> {
        check_queue(queue).map_err(|why| anyhow::anyhow!("jobs: pause a queue: {why}"))?;
        sqlx::query(
            "INSERT INTO kit_queue (name, paused_at) VALUES ($1, now())
            ON CONFLICT (name) DO UPDATE
                SET paused_at = coalesce(kit_queue.paused_at, now()), updated_at = now()",
        )
        .bind(queue)
        .execute(&self.pool)
        .await
        .map_err(|e| db_error(&format!("pause the queue {queue:?}"), &e))?;
        Ok(())
    }

    /// Resumes `queue` (River `QueueResume`) and wakes its workers. A queue that is not
    /// paused does not change.
    ///
    /// # Errors
    ///
    /// A bad queue name, or a database error. Each error has a `Try:` line.
    pub async fn resume(&self, queue: &str) -> anyhow::Result<()> {
        check_queue(queue).map_err(|why| anyhow::anyhow!("jobs: resume a queue: {why}"))?;
        let what = format!("resume the queue {queue:?}");
        let mut tx = self.pool.begin().await.map_err(|e| db_error(&what, &e))?;
        sqlx::query("UPDATE kit_queue SET paused_at = NULL, updated_at = now() WHERE name = $1")
            .bind(queue)
            .execute(&mut *tx)
            .await
            .map_err(|e| db_error(&what, &e))?;
        sqlx::query("SELECT pg_notify($1, $2)")
            .bind(NOTIFY_CHANNEL)
            .bind(serde_json::json!({ "queue": queue }).to_string())
            .execute(&mut *tx)
            .await
            .map_err(|e| db_error(&what, &e))?;
        tx.commit().await.map_err(|e| db_error(&what, &e))?;
        Ok(())
    }
}

/// Inserts a job with encoded args. The worker uses it for the periodic jobs.
pub(super) async fn insert_raw(
    conn: &mut PgConnection,
    kind: &str,
    args: &Value,
    opts: &InsertOpts,
) -> anyhow::Result<Inserted> {
    opts.check(kind)?;
    let what = format!("insert a {kind:?} job");
    let metadata = metrics::trace_metadata();
    for _ in 0..5 {
        let row: Option<(i64, bool)> = sqlx::query_as(INSERT_SQL)
            .bind(kind)
            .bind(Json(args))
            .bind(opts.queue.as_deref().unwrap_or(DEFAULT_QUEUE))
            .bind(opts.priority.unwrap_or(1))
            .bind(opts.max_attempts.unwrap_or(DEFAULT_MAX_ATTEMPTS))
            .bind(opts.run_at)
            .bind(opts.unique_by_args)
            .bind(Json(&metadata))
            .fetch_optional(&mut *conn)
            .await
            .map_err(|e| db_error(&what, &e))?;
        if let Some((id, duplicate)) = row {
            return Ok(Inserted { id, duplicate });
        }
    }
    anyhow::bail!(
        "jobs: {what}: the unique job changed its state during the insert five times\nTry: insert the job again"
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn jobs_insert_opts_check() {
        assert!(InsertOpts::default().check("email").is_ok());
        let cases = [
            (
                InsertOpts {
                    priority: Some(5),
                    ..InsertOpts::default()
                },
                "priority 5 is out of range",
            ),
            (
                InsertOpts {
                    max_attempts: Some(0),
                    ..InsertOpts::default()
                },
                "max_attempts is 0",
            ),
            (
                InsertOpts {
                    queue: Some(String::new()),
                    ..InsertOpts::default()
                },
                "has 0 characters",
            ),
            (
                InsertOpts {
                    queue: Some("q".repeat(128)),
                    ..InsertOpts::default()
                },
                "has 128 characters",
            ),
        ];
        for (opts, want) in cases {
            let err = opts.check("email").unwrap_err().to_string();
            assert!(err.contains(want), "{err}");
            assert!(err.contains("insert a \"email\" job"), "{err}");
            assert!(err.contains("\nTry: "), "{err}");
        }
    }

    #[test]
    fn jobs_insert_opts_run_in() {
        let before = OffsetDateTime::now_utc();
        let opts = InsertOpts::run_in(Duration::from_secs(60));
        let at = opts.run_at.unwrap();
        assert!(at >= before + Duration::from_secs(60));
        assert!(at <= OffsetDateTime::now_utc() + Duration::from_secs(60));
    }
}
