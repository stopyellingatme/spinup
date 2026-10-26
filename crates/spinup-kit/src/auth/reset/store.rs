//! The SQL of the password reset (Go `kit/auth/reset_store.go`). All queries are runtime
//! queries (ADR 0021 §2).

use time::OffsetDateTime;

use crate::auth::required::Auth;
use crate::auth::store::{self, queries};

/// The admitted reset requests across all accounts in one lockout window.
pub(super) const RESET_GLOBAL_THRESHOLD: i64 = 100;

/// Applies the limit of one email before the global limit, so repeated requests for one
/// email cannot use up the global budget. Both limits reuse the login attempt table with
/// the lockout window. A database error refuses the request.
pub(super) async fn reserve_request(auth: &Auth, email: &str) -> bool {
    let now = auth.now();
    let (threshold, window) = auth.lockout();
    let per_email = queries::reserve_attempt(
        auth.pool(),
        &format!("reset-email:{email}"),
        now,
        threshold,
        window,
    )
    .await;
    if !matches!(per_email, Ok(queries::Reservation::Reserved(_))) {
        return false;
    }
    matches!(
        queries::reserve_attempt(
            auth.pool(),
            "reset-global",
            now,
            RESET_GLOBAL_THRESHOLD,
            window
        )
        .await,
        Ok(queries::Reservation::Reserved(_))
    )
}

/// Returns the ID of the user with `email`.
pub(super) async fn user_id(auth: &Auth, email: &str) -> Result<Option<i64>, sqlx::Error> {
    sqlx::query_scalar("SELECT id FROM users WHERE email = $1")
        .bind(email)
        .fetch_optional(auth.pool())
        .await
}

/// Stores the SHA-256 `digest` of a new token for `user_id`.
pub(super) async fn insert(
    auth: &Auth,
    digest: &[u8],
    user_id: i64,
    expires: OffsetDateTime,
) -> Result<(), sqlx::Error> {
    sqlx::query(
        "INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
    )
    .bind(digest)
    .bind(user_id)
    .bind(expires)
    .execute(auth.pool())
    .await?;
    Ok(())
}

/// Sets the password, consumes every reset token of the user, deletes the sessions of
/// the user, and clears the lockout, in one transaction. `None` for an unknown, used, or
/// expired token. A rollback keeps the token.
pub(super) async fn consume(
    auth: &Auth,
    digest: &[u8],
    hash: &str,
) -> Result<Option<i64>, sqlx::Error> {
    let mut tx = queries::begin(auth.pool()).await?;
    // Lock the user before the token. Different tokens of one user serialize here.
    let user: Option<(i64, String)> = sqlx::query_as(
        "SELECT u.id, u.email FROM users u JOIN password_resets p ON p.user_id = u.id WHERE p.token_hash = $1 FOR UPDATE OF u",
    )
    .bind(digest)
    .fetch_optional(&mut *tx)
    .await?;
    let Some((id, email)) = user else {
        return Ok(None);
    };
    let (expires, used): (OffsetDateTime, Option<OffsetDateTime>) = sqlx::query_as(
        "SELECT expires_at, used_at FROM password_resets WHERE token_hash = $1 FOR UPDATE",
    )
    .bind(digest)
    .fetch_one(&mut *tx)
    .await?;
    let now = auth.now();
    if used.is_some() || now >= expires {
        return Ok(None);
    }
    sqlx::query("UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1")
        .bind(id)
        .bind(hash)
        .bind(now)
        .execute(&mut *tx)
        .await?;
    sqlx::query("UPDATE password_resets SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL")
        .bind(id)
        .bind(now)
        .execute(&mut *tx)
        .await?;
    store::delete_user_sessions(&mut *tx, id).await?;
    sqlx::query("DELETE FROM login_attempts WHERE email = $1")
        .bind(&email)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;
    Ok(Some(id))
}
