//! The SQL of kit auth. The query macros check it against `.sqlx/`.

use std::time::Duration;

use sqlx::{PgPool, Postgres, Transaction};
use time::OffsetDateTime;

use super::User;

/// Returns the user with `id`.
pub async fn user_by_id(pool: &PgPool, id: i64) -> Result<Option<User>, sqlx::Error> {
    sqlx::query_as!(User, "SELECT id, email FROM users WHERE id = $1", id)
        .fetch_optional(pool)
        .await
}

/// Returns the user and the password hash for `email`. An account without a
/// password (OIDC only) is not found.
pub async fn credentials_by_email(
    pool: &PgPool,
    email: &str,
) -> Result<Option<(User, String)>, sqlx::Error> {
    let row = sqlx::query!(
        r#"SELECT id, email, password_hash AS "password_hash!" FROM users WHERE email = $1 AND password_hash IS NOT NULL"#,
        email
    )
    .fetch_optional(pool)
    .await?;
    Ok(row.map(|r| {
        (
            User {
                id: r.id,
                email: r.email,
            },
            r.password_hash,
        )
    }))
}

/// Inserts a user. `None` when the email is taken.
pub async fn create_user(
    pool: &PgPool,
    email: &str,
    hash: &str,
) -> Result<Option<User>, sqlx::Error> {
    sqlx::query_as!(
        User,
        "INSERT INTO users (email, password_hash) VALUES ($1, $2) ON CONFLICT (email) DO NOTHING RETURNING id, email",
        email,
        hash
    )
    .fetch_optional(pool)
    .await
}

/// Replaces the hash of a user, but only when it still equals `old`.
pub async fn update_password_hash(
    pool: &PgPool,
    id: i64,
    old: &str,
    hash: &str,
) -> Result<(), sqlx::Error> {
    sqlx::query!(
        "UPDATE users SET password_hash = $3, updated_at = now() WHERE id = $1 AND password_hash = $2",
        id,
        old,
        hash
    )
    .execute(pool)
    .await?;
    Ok(())
}

/// Starts a READ COMMITTED transaction. Each statement sees the rows that
/// the previous holder of an advisory lock committed.
pub async fn begin(pool: &PgPool) -> Result<Transaction<'static, Postgres>, sqlx::Error> {
    let mut tx = pool.begin().await?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL READ COMMITTED")
        .execute(&mut *tx)
        .await?;
    Ok(tx)
}

/// Takes the transaction-scoped advisory lock of `key`.
pub async fn advisory_lock(
    tx: &mut Transaction<'static, Postgres>,
    key: &str,
) -> Result<(), sqlx::Error> {
    // pg_advisory_xact_lock returns void, which the query macros cannot type.
    sqlx::query("SELECT pg_advisory_xact_lock(hashtext($1))")
        .bind(key)
        .execute(&mut **tx)
        .await?;
    Ok(())
}

/// Checks the lockout and records an attempt before password verification.
/// It returns the end of the lockout, or the ID of the recorded attempt.
/// The lock on `key` serializes reservations across all instances.
pub async fn reserve_attempt(
    pool: &PgPool,
    key: &str,
    now: OffsetDateTime,
    threshold: i64,
    window: Duration,
) -> Result<Result<i64, OffsetDateTime>, sqlx::Error> {
    let mut tx = begin(pool).await?;
    advisory_lock(&mut tx, key).await?;
    let times = sqlx::query_scalar!(
        "SELECT attempted_at FROM login_attempts WHERE email = $1 AND attempted_at > $2 ORDER BY attempted_at DESC LIMIT $3",
        key,
        now - window,
        threshold
    )
    .fetch_all(&mut *tx)
    .await?;
    if times.len() as i64 >= threshold {
        let oldest = *times.last().expect("threshold is at least 1");
        return Ok(Err(oldest + window));
    }
    let id = sqlx::query_scalar!(
        "INSERT INTO login_attempts (email, attempted_at) VALUES ($1, $2) RETURNING id",
        key,
        now
    )
    .fetch_one(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(Ok(id))
}

/// Deletes the attempt with `id`. A login that checked no password releases
/// its attempt, so a busy server never counts toward a lockout.
pub async fn release_attempt(pool: &PgPool, id: i64) -> Result<(), sqlx::Error> {
    sqlx::query!("DELETE FROM login_attempts WHERE id = $1", id)
        .execute(pool)
        .await?;
    Ok(())
}

/// Deletes the failed logins of `email`.
pub async fn clear_failures(pool: &PgPool, email: &str) -> Result<(), sqlx::Error> {
    sqlx::query!("DELETE FROM login_attempts WHERE email = $1", email)
        .execute(pool)
        .await?;
    Ok(())
}

/// Deletes the attempts before `before`.
pub async fn delete_old_attempts(pool: &PgPool, before: OffsetDateTime) -> Result<(), sqlx::Error> {
    sqlx::query!("DELETE FROM login_attempts WHERE attempted_at < $1", before)
        .execute(pool)
        .await?;
    Ok(())
}

/// Deletes every session of a user.
pub async fn delete_user_sessions(
    tx: &mut Transaction<'static, Postgres>,
    user_id: i64,
) -> Result<(), sqlx::Error> {
    sqlx::query!("DELETE FROM sessions WHERE user_id = $1", user_id)
        .execute(&mut **tx)
        .await?;
    Ok(())
}
