//! The session store and the SQL of kit auth (Go `kit/auth/store.go` and the scs
//! `pgxstore`).
//!
//! The table `sessions` keeps the hex SHA-256 of each cookie token, never the token
//! (ADR 0008 §3). The `user_id` column copies the user of the session data, so the
//! password reset and the OIDC link end the sessions of one user with one indexed
//! statement. The column `data` holds the JSON form of [`Record`]. All queries are
//! runtime queries (ADR 0021 §2).

pub mod queries;
mod session;

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use sqlx::{PgExecutor, PgPool};
use time::OffsetDateTime;

pub use session::{Session, SessionChange};

/// The session key that holds the user ID (an `i64`).
pub const SESSION_USER_KEY: &str = "auth.user_id";

/// The number of random bytes in a session token. The token is their unpadded base64url
/// text, as the scs token of the Go kit.
pub const TOKEN_BYTES: usize = 32;

/// The stored data of one session.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Record {
    /// The end of the absolute lifetime. A login starts a new lifetime.
    #[serde(with = "time::serde::rfc3339")]
    pub deadline: OffsetDateTime,
    /// The session values, such as [`SESSION_USER_KEY`].
    pub values: serde_json::Map<String, serde_json::Value>,
}

impl Record {
    /// The user ID of the session, from [`SESSION_USER_KEY`].
    pub fn user_id(&self) -> Option<i64> {
        self.values
            .get(SESSION_USER_KEY)
            .and_then(serde_json::Value::as_i64)
    }
}

/// Returns a new random session token.
///
/// # Panics
///
/// When the system random source fails, which a supported OS never does.
pub fn new_token() -> String {
    use base64::Engine;
    let mut bytes = [0u8; TOKEN_BYTES];
    if aws_lc_rs::rand::fill(&mut bytes).is_err() {
        panic!("auth: the system random source failed");
    }
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(bytes)
}

/// The stored key of a session token: the hex SHA-256 of the cookie value.
pub fn token_hash(token: &str) -> String {
    hex::encode(Sha256::digest(token.as_bytes()))
}

/// Returns the record of `token` when the session exists and has not expired.
///
/// # Errors
///
/// A database error, or a row whose data does not decode.
pub async fn load(pool: &PgPool, token: &str) -> Result<Option<Record>, sqlx::Error> {
    let data: Option<serde_json::Value> =
        sqlx::query_scalar("SELECT data FROM sessions WHERE token = $1 AND expiry > now()")
            .bind(token_hash(token))
            .fetch_optional(pool)
            .await?;
    data.map(|d| serde_json::from_value(d).map_err(|e| sqlx::Error::Decode(Box::new(e))))
        .transpose()
}

/// Writes the session of `token` with the expiry `expiry`.
///
/// # Errors
///
/// A database error, such as a user ID that no longer exists.
pub async fn save(
    pool: &PgPool,
    token: &str,
    record: &Record,
    expiry: OffsetDateTime,
) -> Result<(), sqlx::Error> {
    let data = serde_json::to_value(record).map_err(|e| sqlx::Error::Encode(Box::new(e)))?;
    sqlx::query(
        "INSERT INTO sessions (token, user_id, data, expiry) VALUES ($1, $2, $3, $4) \
         ON CONFLICT (token) DO UPDATE SET user_id = EXCLUDED.user_id, data = EXCLUDED.data, expiry = EXCLUDED.expiry",
    )
    .bind(token_hash(token))
    .bind(record.user_id())
    .bind(data)
    .bind(expiry)
    .execute(pool)
    .await?;
    Ok(())
}

/// Deletes the session of `token`. A missing session is not an error.
///
/// # Errors
///
/// A database error.
pub async fn delete(pool: &PgPool, token: &str) -> Result<(), sqlx::Error> {
    sqlx::query("DELETE FROM sessions WHERE token = $1")
        .bind(token_hash(token))
        .execute(pool)
        .await?;
    Ok(())
}

/// Deletes the expired sessions and returns the number of rows.
///
/// # Errors
///
/// A database error.
pub async fn delete_expired(pool: &PgPool) -> Result<u64, sqlx::Error> {
    Ok(sqlx::query("DELETE FROM sessions WHERE expiry < now()")
        .execute(pool)
        .await?
        .rows_affected())
}

/// Deletes every session of a user. The password reset and the OIDC link call it inside
/// their transaction.
///
/// # Errors
///
/// A database error.
pub async fn delete_user_sessions<'e>(
    executor: impl PgExecutor<'e>,
    user_id: i64,
) -> Result<u64, sqlx::Error> {
    Ok(sqlx::query("DELETE FROM sessions WHERE user_id = $1")
        .bind(user_id)
        .execute(executor)
        .await?
        .rows_affected())
}
