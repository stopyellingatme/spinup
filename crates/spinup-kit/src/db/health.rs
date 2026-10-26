//! The pool closures of `/readyz`, the dev tools, and `deployx`.

use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use anyhow::Context as _;
use sqlx::PgPool;

use super::migrate::migration_name;
use crate::app::{Migration, MigrationStatusFn};

/// A boxed future that can move between threads.
type BoxFuture<T> = Pin<Box<dyn Future<Output = T> + Send + 'static>>;

/// The migrations table of the app (the sqlx default).
const APP_TABLE: &str = "_sqlx_migrations";

/// Returns the readiness check of the pool for `App::readiness("db", ..)` (Go
/// `WithReadiness("db", pool.Ping)`). It runs `SELECT 1`.
pub fn readiness(
    pool: PgPool,
) -> impl Fn() -> BoxFuture<anyhow::Result<()>> + Send + Sync + 'static {
    move || {
        let pool = pool.clone();
        Box::pin(async move {
            sqlx::query("SELECT 1")
                .execute(&pool)
                .await
                .context("db: ping the database")?;
            Ok(())
        })
    }
}

/// Returns the applied app migrations, in version order (Go `WithMigrationStatus`), for the
/// dev tools. The list comes from the table `_sqlx_migrations`. It is empty when the table
/// does not exist.
pub fn migration_status(pool: PgPool) -> MigrationStatusFn {
    Arc::new(move || {
        let pool = pool.clone();
        Box::pin(async move {
            if !app_table_exists(&pool).await? {
                return Ok(Vec::new());
            }
            let rows: Vec<(i64, String)> = sqlx::query_as::<_, (i64, String)>(
                "SELECT version, description FROM _sqlx_migrations WHERE success ORDER BY version",
            )
            .fetch_all(&pool)
            .await
            .context("db: read the app migrations")?;
            Ok(rows
                .into_iter()
                .map(|(v, d)| Migration {
                    name: migration_name(v, &d),
                    applied: true,
                })
                .collect())
        })
    })
}

/// Returns the highest applied app migration version as text, `0` when none is applied,
/// for `deployx::Options::migration_version`.
pub fn migration_version(
    pool: PgPool,
) -> impl Fn() -> BoxFuture<anyhow::Result<String>> + Send + Sync + 'static {
    move || {
        let pool = pool.clone();
        Box::pin(async move {
            if !app_table_exists(&pool).await? {
                return Ok("0".to_owned());
            }
            let v: Option<i64> =
                sqlx::query_scalar("SELECT max(version) FROM _sqlx_migrations WHERE success")
                    .fetch_one(&pool)
                    .await
                    .context("db: read the app migration version")?;
            Ok(v.unwrap_or(0).to_string())
        })
    }
}

async fn app_table_exists(pool: &PgPool) -> anyhow::Result<bool> {
    sqlx::query_scalar("SELECT to_regclass($1) IS NOT NULL")
        .bind(APP_TABLE)
        .fetch_one(pool)
        .await
        .context("db: read the app migrations")
}
