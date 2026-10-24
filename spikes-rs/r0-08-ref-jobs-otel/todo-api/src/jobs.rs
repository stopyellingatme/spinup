//! The jobs of the todo app. A generator (`spinup g job`) would write this
//! file.

use std::time::Duration;

use kit::jobs::{Job, JobArgs, JobResult, Worker};
use serde::{Deserialize, Serialize};
use sqlx::PgPool;

/// Deletes the done todos that did not change for `older_than_days` days.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct PurgeDoneTodos {
    pub older_than_days: i32,
}

impl JobArgs for PurgeDoneTodos {
    const KIND: &'static str = "purge_done_todos";
}

async fn purge_done_todos(pool: PgPool, job: Job<PurgeDoneTodos>) -> JobResult {
    let deleted = sqlx::query(
        "DELETE FROM todos WHERE done AND updated_at < now() - make_interval(days => $1)",
    )
    .bind(job.args.older_than_days)
    .execute(&pool)
    .await?
    .rows_affected();
    tracing::info!(deleted, "purged done todos");
    Ok(())
}

/// The worker with every job of the app, and the hourly purge.
pub fn worker(pool: PgPool, config: kit::jobs::Config) -> Worker {
    let mut worker = Worker::new(pool.clone(), config);
    worker
        .register(move |job| purge_done_todos(pool.clone(), job))
        .periodic(
            Duration::from_secs(3600),
            &PurgeDoneTodos {
                older_than_days: 30,
            },
        );
    worker
}
