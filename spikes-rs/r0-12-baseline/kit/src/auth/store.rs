//! The session store: a sqlx `SessionStore` for tower-sessions that keeps a
//! SHA-256 hash of each session ID, never the ID (ADR 0008 §3).
//!
//! The table has a `user_id` column, filled from the session data. The
//! password reset and the OIDC link delete the sessions of a user with one
//! indexed statement. The Go kit decodes every row for this.

use async_trait::async_trait;
use sha2::{Digest, Sha256};
use sqlx::PgPool;
use time::OffsetDateTime;
use tower_sessions::session::{Id, Record};
use tower_sessions::session_store::{self, SessionStore};

/// The session key that holds the user ID (`i64`).
pub const SESSION_USER_KEY: &str = "auth.user_id";

/// A tower-sessions store on the `sessions` table.
#[derive(Debug, Clone)]
pub struct PgSessionStore {
    pool: PgPool,
}

impl PgSessionStore {
    /// Returns a store on `pool`.
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

/// The stored key of a session ID: the hex SHA-256 of the cookie value.
pub fn token_hash(id: &Id) -> String {
    hex::encode(Sha256::digest(id.to_string().as_bytes()))
}

fn backend(e: sqlx::Error) -> session_store::Error {
    session_store::Error::Backend(e.to_string())
}

fn user_of(record: &Record) -> Option<i64> {
    record.data.get(SESSION_USER_KEY).and_then(|v| v.as_i64())
}

fn encode(record: &Record) -> Result<serde_json::Value, session_store::Error> {
    serde_json::to_value(&record.data).map_err(|e| session_store::Error::Encode(e.to_string()))
}

#[async_trait]
impl SessionStore for PgSessionStore {
    async fn create(&self, record: &mut Record) -> session_store::Result<()> {
        let data = encode(record)?;
        loop {
            let done = sqlx::query!(
                "INSERT INTO sessions (token, user_id, data, expiry) VALUES ($1, $2, $3, $4) ON CONFLICT (token) DO NOTHING",
                token_hash(&record.id),
                user_of(record),
                data,
                record.expiry_date,
            )
            .execute(&self.pool)
            .await
            .map_err(backend)?;
            if done.rows_affected() == 1 {
                return Ok(());
            }
            // A 128-bit collision: draw another ID.
            record.id = Id::default();
        }
    }

    async fn save(&self, record: &Record) -> session_store::Result<()> {
        sqlx::query!(
            "INSERT INTO sessions (token, user_id, data, expiry) VALUES ($1, $2, $3, $4) ON CONFLICT (token) DO UPDATE SET user_id = EXCLUDED.user_id, data = EXCLUDED.data, expiry = EXCLUDED.expiry",
            token_hash(&record.id),
            user_of(record),
            encode(record)?,
            record.expiry_date,
        )
        .execute(&self.pool)
        .await
        .map_err(backend)?;
        Ok(())
    }

    async fn load(&self, id: &Id) -> session_store::Result<Option<Record>> {
        let row = sqlx::query!(
            "SELECT data, expiry FROM sessions WHERE token = $1 AND expiry > now()",
            token_hash(id),
        )
        .fetch_optional(&self.pool)
        .await
        .map_err(backend)?;
        let Some(row) = row else { return Ok(None) };
        let data = serde_json::from_value(row.data)
            .map_err(|e| session_store::Error::Decode(e.to_string()))?;
        Ok(Some(Record {
            id: *id,
            data,
            expiry_date: row.expiry,
        }))
    }

    async fn delete(&self, id: &Id) -> session_store::Result<()> {
        sqlx::query!("DELETE FROM sessions WHERE token = $1", token_hash(id))
            .execute(&self.pool)
            .await
            .map_err(backend)?;
        Ok(())
    }
}

/// Deletes the expired sessions. It returns the number of rows.
pub async fn delete_expired(pool: &PgPool, now: OffsetDateTime) -> Result<u64, sqlx::Error> {
    Ok(sqlx::query!("DELETE FROM sessions WHERE expiry < $1", now)
        .execute(pool)
        .await?
        .rows_affected())
}
