//! The rescuer and the cleaner. The leader runs them.

use std::time::Duration;

use super::db_error;
use super::worker::Inner;

/// The number of jobs that one rescue or clean statement changes at most.
const BATCH: i64 = 1000;

/// The number of clean statements in one clean run at most.
const CLEAN_BATCHES: usize = 10;

/// Moves the jobs that run longer than `rescue_after` back to `retryable`, with the
/// backoff of their attempt. A job after its last attempt goes to `discarded`, and a job
/// with a cancel request goes to `cancelled`. Returns the number of jobs.
pub(super) async fn rescue(inner: &Inner) -> anyhow::Result<u64> {
    // The backoff of attempts 1 to 100, so one statement can rescue jobs with different
    // attempt numbers. Later attempts use the delay of attempt 100.
    let delays: Vec<f64> = (1..=100)
        .map(|a| inner.config.backoff.delay(a).as_secs_f64())
        .collect();
    let last = delays[delays.len() - 1];
    let done = sqlx::query(
        "WITH stuck AS (
            SELECT id FROM kit_job
            WHERE state = 'running' AND attempted_at < now() - make_interval(secs => $1)
            ORDER BY id LIMIT $4
            FOR UPDATE SKIP LOCKED
        ), next AS (
            SELECT j.id, now() + make_interval(secs => coalesce(($2::float8[])[j.attempt], $3)) AS retry_at
            FROM kit_job j JOIN stuck USING (id)
        )
        UPDATE kit_job j SET
            state = CASE
                WHEN j.metadata ? 'cancel_attempted_at' THEN 'cancelled'
                WHEN j.attempt >= j.max_attempts THEN 'discarded'
                ELSE 'retryable' END::kit_job_state,
            finalized_at = CASE
                WHEN j.metadata ? 'cancel_attempted_at' OR j.attempt >= j.max_attempts THEN now() END,
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
    .bind(BATCH)
    .execute(&inner.pool)
    .await
    .map_err(|e| db_error("rescue stuck jobs", &e))?;
    Ok(done.rows_affected())
}

/// Deletes the finalized jobs that are older than their retention (River `JobCleaner`).
/// Returns the number of jobs.
pub(super) async fn clean(inner: &Inner) -> anyhow::Result<u64> {
    let cfg = &inner.config;
    let secs = |d: Option<Duration>| d.map(|d| d.as_secs_f64());
    let mut total = 0;
    for _ in 0..CLEAN_BATCHES {
        let done = sqlx::query(
            "WITH old AS (
                SELECT id FROM kit_job
                WHERE (state = 'completed' AND finalized_at < now() - make_interval(secs => $1::float8))
                    OR (state = 'cancelled' AND finalized_at < now() - make_interval(secs => $2::float8))
                    OR (state = 'discarded' AND finalized_at < now() - make_interval(secs => $3::float8))
                ORDER BY id LIMIT $4
                FOR UPDATE SKIP LOCKED
            )
            DELETE FROM kit_job WHERE id IN (SELECT id FROM old)",
        )
        .bind(secs(cfg.completed_retention))
        .bind(secs(cfg.cancelled_retention))
        .bind(secs(cfg.discarded_retention))
        .bind(BATCH)
        .execute(&inner.pool)
        .await
        .map_err(|e| db_error("delete old jobs", &e))?;
        let n = done.rows_affected();
        total += n;
        if n < BATCH as u64 {
            break;
        }
    }
    Ok(total)
}
