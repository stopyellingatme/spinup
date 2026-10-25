//! The todo worker runs the purge job against the app tables.
#![cfg(feature = "jobs")]

use std::time::{Duration, Instant};

use kit::jobs::{Client, Config, InsertOpts};
use sqlx::PgPool;
use todo_api::jobs::PurgeDoneTodos;

#[sqlx::test(migrations = false)]
async fn worker_runs_the_purge_job(pool: PgPool) {
    let mut out = Vec::new();
    kit::db::migrate_command(&pool, &todo_api::MIGRATOR, "up", &mut out)
        .await
        .unwrap();
    sqlx::raw_sql(
        "INSERT INTO todos (title, done, priority, updated_at) VALUES
            ('old done', true, 'low', now() - interval '40 days'),
            ('new done', true, 'low', now()),
            ('old open', false, 'low', now() - interval '40 days')",
    )
    .execute(&pool)
    .await
    .unwrap();
    let config = Config {
        poll_interval: Duration::from_millis(100),
        ..Config::default()
    };
    let worker = todo_api::jobs::worker(pool.clone(), config);
    let stop = std::sync::Arc::new(tokio::sync::Notify::new());
    let wait = stop.clone();
    let handle = tokio::spawn(worker.run(async move { wait.notified().await }));
    Client::new(pool.clone())
        .insert(
            &PurgeDoneTodos {
                older_than_days: 30,
            },
            &InsertOpts::default(),
        )
        .await
        .unwrap();
    let start = Instant::now();
    loop {
        let done: i64 =
            sqlx::query_scalar("SELECT count(*) FROM kit_job WHERE state = 'completed'")
                .fetch_one(&pool)
                .await
                .unwrap();
        if done == 1 {
            break;
        }
        assert!(
            start.elapsed() < Duration::from_secs(5),
            "the job did not run"
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    stop.notify_one();
    handle.await.unwrap().unwrap();
    let titles: Vec<String> = sqlx::query_scalar("SELECT title FROM todos ORDER BY title")
        .fetch_all(&pool)
        .await
        .unwrap();
    assert_eq!(titles, ["new done", "old open"]);
}
