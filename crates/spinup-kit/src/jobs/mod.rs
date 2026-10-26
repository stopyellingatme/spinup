//! The job queue and the worker (Go `kit/jobs`). The kit has its own queue on sqlx. It
//! replaces River and keeps the River schema.
//!
//! The kit migration `00001_job_queue` makes the tables: `kit_job`, `kit_leader`,
//! `kit_queue`, and a NOTIFY trigger. `api migrate up` (or `spinup db migrate up`) applies
//! it before the app migrations.
//!
//! # Use
//!
//! An app inserts jobs with a [`Client`] and works them in a separate process with a
//! [`Worker`]. A job is an [`Args`] value: a JSON struct with a stable `KIND`.
//! [`Worker::register`] adds the handler of one kind. [`worker_command`] makes the app
//! command `worker`.
//!
//! ```no_run
//! use std::time::Duration;
//!
//! use serde::{Deserialize, Serialize};
//! use spinup_kit::db::PgPool;
//! use spinup_kit::jobs::{self, Args, Client, Config, InsertOpts, Job, Worker};
//!
//! #[derive(Debug, Serialize, Deserialize)]
//! struct SendDigest {
//!     user_id: i64,
//! }
//!
//! impl Args for SendDigest {
//!     const KIND: &'static str = "send_digest";
//! }
//!
//! # async fn demo(pool: PgPool) -> anyhow::Result<()> {
//! // The API process inserts a job.
//! Client::new(pool.clone())
//!     .insert(&SendDigest { user_id: 7 }, &InsertOpts::default())
//!     .await?;
//!
//! // The worker process works it.
//! let mut worker = Worker::new(pool, Config::default());
//! worker
//!     .register(|job: Job<SendDigest>| async move {
//!         tracing::info!(user_id = job.args.user_id, "digest sent");
//!         Ok(())
//!     })
//!     .periodic(Duration::from_secs(3600), &SendDigest { user_id: 0 });
//! worker.run(spinup_kit::app::shutdown_signal()).await
//! # }
//! ```
//!
//! # Delivery
//!
//! Delivery is at least once. A job runs again when its worker dies after the handler made
//! its change, but before the worker recorded the completion. Make each handler
//! idempotent. A handler that changes only the database can call [`complete_tx`] in its own
//! transaction: then the change and the completion commit together, and the change occurs
//! once. A job never goes to `completed` twice: each change of a running job checks
//! `state = 'running'` and the attempt number.
//!
//! # How the queue works
//!
//! - A worker fetches jobs with `FOR UPDATE SKIP LOCKED`, so many workers can share one
//!   queue.
//! - An insert sends `NOTIFY kit_insert`. A worker listens and also polls, so a lost
//!   notification costs at most one poll interval. [`Config::fetch_cooldown`] limits the
//!   fetches that notifications cause.
//! - A failed attempt goes to `retryable`, with `scheduled_at` from the [`Backoff`]. After
//!   `max_attempts`, the job goes to `discarded`.
//! - A handler can return [`snooze`] (run again later, the attempt does not count) or
//!   [`cancel`] (stop the job for good).
//! - The leader (one worker, elected through `kit_leader`) runs the rescuer, the cleaner,
//!   and the periodic jobs. The rescuer moves a job that stays `running` longer than
//!   [`Config::rescue_after`] back to `retryable`. The cleaner deletes old finalized jobs.
//! - A unique job ([`InsertOpts::unique_by_args`]) has a `unique_key`: the sha256 of the
//!   kind and the canonical `jsonb` text of the args. A partial unique index allows one
//!   waiting or running job for each key.
//! - [`Client::pause`] stops the fetches of a queue. [`Client::resume`] starts them again.
//!   [`Client::cancel`] cancels a waiting job at once, and a running job through its worker.
//!
//! # Telemetry
//!
//! With the feature `telemetry`, an insert stores the `traceparent` of the current span in
//! the job metadata. The span of the job (`job <kind>`) links to that trace. Each attempt
//! records the histogram [`JOB_DURATION`] (`kit.job.duration`, seconds) with the labels
//! `job.kind`, `job.queue`, and `job.result` (`completed`, `retryable`, `discarded`,
//! `cancelled`, or `snoozed`). Prometheus shows it as `kit_job_duration_seconds`.

mod client;
mod leader;
mod metrics;
mod periodic;
mod rescuer;
mod worker;

use std::fmt;
use std::str::FromStr;

use serde::Serialize;
use serde::de::DeserializeOwned;

pub use client::{Client, InsertOpts, Inserted};
pub use metrics::{JOB_DURATION, JOB_DURATION_BOUNDS};
pub use worker::{
    Backoff, Cancel, Config, Snooze, Worker, cancel, complete_tx, snooze, worker_command,
};

/// The queue of a job when the insert names no queue (Go `DefaultQueue`).
pub const DEFAULT_QUEUE: &str = "default";
/// The channel of the insert trigger. Its payload is `{"queue": "<name>"}`.
pub const NOTIFY_CHANNEL: &str = "kit_insert";
/// The channel of the control messages, such as the cancel of a running job.
pub const CONTROL_CHANNEL: &str = "kit_control";
/// The attempts of a job when the insert gives no number (the River default).
pub const DEFAULT_MAX_ATTEMPTS: i16 = 25;

/// The arguments of one job kind (Go `jobs.Args`). The queue stores them as JSON. `KIND`
/// is a stable name, such as `send_digest`. Do not change it after jobs of the kind exist.
pub trait Args: Serialize + DeserializeOwned + Send + Sync + 'static {
    /// The job kind.
    const KIND: &'static str;
}

/// One job that a handler works (Go `jobs.Job`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Job<T> {
    /// The job ID.
    pub id: i64,
    /// The decoded arguments.
    pub args: T,
    /// The attempt number. The first attempt is 1.
    pub attempt: i16,
}

/// The result of a handler. An error makes the job retry after the backoff, unless it is
/// a [`Snooze`] or a [`Cancel`].
pub type JobResult = anyhow::Result<()>;

/// The state of a job (the enum `kit_job_state`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum JobState {
    /// The job waits for a worker.
    Available,
    /// A cancel stopped the job. It is final.
    Cancelled,
    /// A handler completed the job. It is final.
    Completed,
    /// The job failed its last attempt. It is final.
    Discarded,
    /// The job waits for an outside event. The kit does not use it yet.
    Pending,
    /// The job failed and waits for its retry time.
    Retryable,
    /// A worker runs the job.
    Running,
    /// The job waits for its run time.
    Scheduled,
}

impl JobState {
    /// The name of the state in the database, such as `available`.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Available => "available",
            Self::Cancelled => "cancelled",
            Self::Completed => "completed",
            Self::Discarded => "discarded",
            Self::Pending => "pending",
            Self::Retryable => "retryable",
            Self::Running => "running",
            Self::Scheduled => "scheduled",
        }
    }

    /// Reports whether the state is final: `cancelled`, `completed`, or `discarded`.
    pub fn is_final(self) -> bool {
        matches!(self, Self::Cancelled | Self::Completed | Self::Discarded)
    }
}

impl fmt::Display for JobState {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl FromStr for JobState {
    type Err = anyhow::Error;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        Ok(match s {
            "available" => Self::Available,
            "cancelled" => Self::Cancelled,
            "completed" => Self::Completed,
            "discarded" => Self::Discarded,
            "pending" => Self::Pending,
            "retryable" => Self::Retryable,
            "running" => Self::Running,
            "scheduled" => Self::Scheduled,
            _ => anyhow::bail!(
                "jobs: {s:?} is not a job state\nTry: run the kit migrations: api migrate up"
            ),
        })
    }
}

/// A database error with a `Try:` line. A missing table (SQLSTATE 42P01) means that the
/// kit migrations did not run.
pub(crate) fn db_error(what: &str, err: &sqlx::Error) -> anyhow::Error {
    let undefined_table =
        matches!(err, sqlx::Error::Database(db) if db.code().as_deref() == Some("42P01"));
    let try_line = if undefined_table {
        "run the migrations: api migrate up, or spinup db migrate up"
    } else {
        "check DATABASE_URL and that PostgreSQL runs"
    };
    anyhow::anyhow!("jobs: {what}: {err}\nTry: {try_line}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn jobs_state_names_round_trip() {
        for s in [
            JobState::Available,
            JobState::Cancelled,
            JobState::Completed,
            JobState::Discarded,
            JobState::Pending,
            JobState::Retryable,
            JobState::Running,
            JobState::Scheduled,
        ] {
            assert_eq!(s.as_str().parse::<JobState>().unwrap(), s);
            assert_eq!(s.to_string(), s.as_str());
        }
        let err = "lost".parse::<JobState>().unwrap_err().to_string();
        assert!(err.contains("\"lost\"") && err.contains("\nTry: "), "{err}");
        assert!(JobState::Completed.is_final() && !JobState::Running.is_final());
    }

    #[test]
    fn jobs_db_error_has_a_try_line() {
        let err = db_error("fetch jobs", &sqlx::Error::PoolTimedOut).to_string();
        assert!(err.starts_with("jobs: fetch jobs: "), "{err}");
        assert!(
            err.ends_with("\nTry: check DATABASE_URL and that PostgreSQL runs"),
            "{err}"
        );
    }
}
