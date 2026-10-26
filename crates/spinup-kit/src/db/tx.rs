//! Transactions (Go `kit/db/tx.go`).

use std::future::Future;
use std::pin::Pin;

use sqlx::{Acquire, PgConnection, PgPool};

/// Runs `f` in a transaction (Go `db.InTx`). It commits when `f` returns `Ok`. It rolls
/// back when `f` returns `Err`, panics, or is cancelled (the future is dropped): sqlx rolls
/// back a transaction that is dropped before its commit.
///
/// `f` gets the connection of the transaction. Pass `&mut *conn` to a query, or call
/// `conn.begin()` for a savepoint.
///
/// ```no_run
/// # async fn demo(pool: spinup_kit::db::PgPool) -> anyhow::Result<()> {
/// let id: i64 = spinup_kit::db::in_tx(&pool, |conn| {
///     Box::pin(async move {
///         let id: i64 = sqlx::query_scalar("INSERT INTO lists (name) VALUES ($1) RETURNING id")
///             .bind("home")
///             .fetch_one(&mut *conn)
///             .await?;
///         sqlx::query("INSERT INTO items (list_id) VALUES ($1)")
///             .bind(id)
///             .execute(&mut *conn)
///             .await?;
///         Ok::<_, anyhow::Error>(id)
///     })
/// })
/// .await?;
/// # Ok(())
/// # }
/// ```
///
/// # Errors
///
/// The error of `f`, or the error of the begin or the commit (through `From<sqlx::Error>`).
pub async fn in_tx<T, E, F>(pool: &PgPool, f: F) -> Result<T, E>
where
    F: for<'c> FnOnce(
        &'c mut PgConnection,
    ) -> Pin<Box<dyn Future<Output = Result<T, E>> + Send + 'c>>,
    E: From<sqlx::Error>,
{
    let mut tx = pool.begin().await?;
    let result = {
        let conn = tx.acquire().await?;
        f(conn).await
    };
    match result {
        Ok(value) => {
            tx.commit().await?;
            Ok(value)
        }
        Err(err) => {
            // The error of f wins. A failed rollback closes the connection, so the pool
            // never gets a connection back in a transaction.
            if let Err(rb) = tx.rollback().await {
                tracing::warn!(error = %rb, "db: roll back transaction failed");
            }
            Err(err)
        }
    }
}
