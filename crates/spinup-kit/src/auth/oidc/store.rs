//! The account link of OIDC sign-ins (Go `kit/auth/oidc_store.go`). All queries are
//! runtime queries (ADR 0021 §2).

use crate::auth::required::{Auth, User};
use crate::auth::store::{self, queries};

/// Resolves an identity before its email, so a provider cannot move an identity to
/// another account with a changed email claim (Go `oidcUser`). A new identity with the
/// email of an existing account takes over that account: the password and the sessions of
/// the account end. Everything runs in one transaction. The caller checks
/// `email_verified` first.
pub(super) async fn link(
    auth: &Auth,
    provider: &str,
    subject: &str,
    email: &str,
) -> Result<User, sqlx::Error> {
    let mut tx = queries::begin(auth.pool()).await?;
    // Serialize the callbacks of this identity, also callbacks with different emails.
    sqlx::query("SELECT pg_advisory_xact_lock(hashtextextended($1, 0))")
        .bind(format!("{provider}:{subject}"))
        .execute(&mut *tx)
        .await?;
    let known = sqlx::query_as::<_, User>(
        "SELECT u.id, u.email FROM users u JOIN user_identities i ON i.user_id = u.id WHERE i.provider = $1 AND i.subject = $2",
    )
    .bind(provider)
    .bind(subject)
    .fetch_optional(&mut *tx)
    .await?;
    if let Some(user) = known {
        tx.commit().await?;
        return Ok(user);
    }
    // The unique email key serializes different identities with one email. Lock an
    // existing account before its password and sessions end.
    let inserted = sqlx::query_as::<_, User>(
        "INSERT INTO users (email, password_hash) VALUES ($1, NULL) ON CONFLICT (email) DO NOTHING RETURNING id, email",
    )
    .bind(email)
    .fetch_optional(&mut *tx)
    .await?;
    let user = match inserted {
        Some(user) => user,
        None => {
            let user = sqlx::query_as::<_, User>(
                "SELECT id, email FROM users WHERE email = $1 FOR UPDATE",
            )
            .bind(email)
            .fetch_one(&mut *tx)
            .await?;
            sqlx::query("UPDATE users SET password_hash = NULL WHERE id = $1")
                .bind(user.id)
                .execute(&mut *tx)
                .await?;
            store::delete_user_sessions(&mut *tx, user.id).await?;
            user
        }
    };
    sqlx::query(
        "INSERT INTO user_identities (provider, subject, user_id, email) VALUES ($1, $2, $3, $4)",
    )
    .bind(provider)
    .bind(subject)
    .bind(user.id)
    .bind(email)
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(user)
}
