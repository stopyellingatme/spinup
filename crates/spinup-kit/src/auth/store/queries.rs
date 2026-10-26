//! The user and login-attempt SQL of kit auth (Go `kit/auth/store.go`). The password
//! reset and OIDC modules use the same functions.

use std::time::Duration;

use sqlx::{PgPool, Postgres, Transaction};
use time::OffsetDateTime;

use crate::auth::required::User;

/// Returns the user with `id`.
///
/// # Errors
///
/// A database error.
pub async fn user_by_id(pool: &PgPool, id: i64) -> Result<Option<User>, sqlx::Error> {
    sqlx::query_as::<_, User>("SELECT id, email FROM users WHERE id = $1")
        .bind(id)
        .fetch_optional(pool)
        .await
}

/// Returns the user and the password hash for `email`. An account without a password
/// (OIDC only) is not found, so the password login gives the same generic error.
///
/// # Errors
///
/// A database error.
pub async fn credentials_by_email(
    pool: &PgPool,
    email: &str,
) -> Result<Option<(User, String)>, sqlx::Error> {
    let row: Option<(i64, String, String)> = sqlx::query_as(
        "SELECT id, email, password_hash FROM users WHERE email = $1 AND password_hash IS NOT NULL",
    )
    .bind(email)
    .fetch_optional(pool)
    .await?;
    Ok(row.map(|(id, email, hash)| (User { id, email }, hash)))
}

/// Inserts a user. `None` when the email is taken.
///
/// # Errors
///
/// A database error.
pub async fn create_user(
    pool: &PgPool,
    email: &str,
    hash: &str,
) -> Result<Option<User>, sqlx::Error> {
    sqlx::query_as::<_, User>(
        "INSERT INTO users (email, password_hash) VALUES ($1, $2) ON CONFLICT (email) DO NOTHING RETURNING id, email",
    )
    .bind(email)
    .bind(hash)
    .fetch_optional(pool)
    .await
}

/// Replaces the hash of a user, but only when it still equals `old`.
///
/// # Errors
///
/// A database error.
pub async fn update_password_hash(
    pool: &PgPool,
    id: i64,
    old: &str,
    hash: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "UPDATE users SET password_hash = $3, updated_at = now() WHERE id = $1 AND password_hash = $2",
    )
    .bind(id)
    .bind(old)
    .bind(hash)
    .execute(pool)
    .await?;
    Ok(())
}

/// Starts a READ COMMITTED transaction. Each statement sees the rows that the previous
/// holder of an advisory lock committed.
///
/// # Errors
///
/// A database error.
pub async fn begin(pool: &PgPool) -> Result<Transaction<'static, Postgres>, sqlx::Error> {
    let mut tx = pool.begin().await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL READ COMMITTED")
        .execute(&mut *tx)
        .await?;
    Ok(tx)
}

/// Takes the transaction-scoped advisory lock of `key`.
///
/// # Errors
///
/// A database error.
pub async fn advisory_lock(
    tx: &mut Transaction<'static, Postgres>,
    key: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query("SELECT pg_advisory_xact_lock(hashtext($1))")
        .bind(key)
        .execute(&mut **tx)
        .await?;
    Ok(())
}

/// The result of [`reserve_attempt`].
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Reservation {
    /// The attempt is recorded with this ID.
    Reserved(i64),
    /// The key is locked until this time. No attempt is recorded.
    LockedUntil(OffsetDateTime),
}

/// Checks the lockout of `key` and records an attempt before the password check. The
/// advisory lock on `key` serializes the reservations of all instances that share the
/// database.
///
/// # Errors
///
/// A database error.
pub async fn reserve_attempt(
    pool: &PgPool,
    key: &str,
    now: OffsetDateTime,
    threshold: i64,
    window: Duration,
) -> Result<Reservation, sqlx::Error> {
    let mut tx = begin(pool).await?;
    advisory_lock(&mut tx, key).await?;
    let times: Vec<OffsetDateTime> = sqlx::query_scalar(
        "SELECT attempted_at FROM login_attempts WHERE email = $1 AND attempted_at > $2 ORDER BY attempted_at DESC LIMIT $3",
    )
    .bind(key)
    .bind(now - window)
    .bind(threshold)
    .fetch_all(&mut *tx)
    .await?;
    if i64::try_from(times.len()).unwrap_or(i64::MAX) >= threshold
        && let Some(oldest) = times.last()
    {
        return Ok(Reservation::LockedUntil(*oldest + window));
    }
    let id: i64 = sqlx::query_scalar(
        "INSERT INTO login_attempts (email, attempted_at) VALUES ($1, $2) RETURNING id",
    )
    .bind(key)
    .bind(now)
    .fetch_one(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(Reservation::Reserved(id))
}

/// Deletes the attempt with `id`. A login that checked no password releases its attempt,
/// so a busy server never counts toward a lockout.
///
/// # Errors
///
/// A database error.
pub async fn release_attempt(pool: &PgPool, id: i64) -> Result<(), sqlx::Error> {
    sqlx::query("DELETE FROM login_attempts WHERE id = $1")
        .bind(id)
        .execute(pool)
        .await?;
    Ok(())
}

/// Deletes the failed logins of `email`.
///
/// # Errors
///
/// A database error.
pub async fn clear_failures(pool: &PgPool, email: &str) -> Result<(), sqlx::Error> {
    sqlx::query("DELETE FROM login_attempts WHERE email = $1")
        .bind(email)
        .execute(pool)
        .await?;
    Ok(())
}

/// Deletes the attempts before `before`.
///
/// # Errors
///
/// A database error.
pub async fn delete_old_attempts(pool: &PgPool, before: OffsetDateTime) -> Result<(), sqlx::Error> {
    sqlx::query("DELETE FROM login_attempts WHERE attempted_at < $1")
        .bind(before)
        .execute(pool)
        .await?;
    Ok(())
}
