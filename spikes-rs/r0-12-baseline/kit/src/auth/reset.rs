//! Password reset (ADR 0013 §2), as `kit/auth/reset.go`.
//!
//! `POST /api/auth/reset/request` always answers 202. The request goes into
//! a bounded queue, and a background task looks up the account and sends the
//! mail, so the response time does not reveal accounts.
//! `POST /api/auth/reset/confirm` sets the password once, ends every session
//! of the user, and clears the lockout.

use std::sync::Arc;
use std::time::Duration;

use axum::body::Body;
use axum::extract::State;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use axum::{Router, middleware};
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD as B64URL;
use serde::Deserialize;
use sha2::{Digest, Sha256};
use tokio::sync::mpsc;
use tower_sessions::Session;

use super::handlers::{decode_json, endpoint, hash_failed};
use super::password::{self, MAX_PASSWORD_LENGTH, MIN_PASSWORD_LENGTH, password_length};
use super::ratelimit::{PATH_RESET_CONFIRM, PATH_RESET_REQUEST};
use super::store::SESSION_USER_KEY;
use super::{Auth, AuthError, internal_error, normalize_email, queries};
use crate::mail::{Mailer, Message};
use crate::problem::{FieldError, Problem};

/// The validity period of a reset token.
pub const RESET_LIFETIME: Duration = Duration::from_secs(3600);
/// The admitted reset requests across all accounts per lockout window.
const RESET_GLOBAL_THRESHOLD: i64 = 100;

/// The settings of the reset routes.
pub struct ResetOptions {
    /// The mailer.
    pub mailer: Arc<dyn Mailer>,
    /// The sender address (MAIL_FROM).
    pub from: String,
    /// The trusted public URL before the token, such as
    /// `https://app.example/reset`. It never comes from a request header.
    pub base_url: String,
}

#[derive(Clone)]
struct ResetState {
    auth: Auth,
    queue: mpsc::Sender<String>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields, default)]
struct RequestBody {
    email: String,
}

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields, default)]
struct ConfirmBody {
    token: String,
    password: String,
}

fn reset_message(o: &ResetOptions, to: &str, token: &str) -> Message {
    let link = format!("{}/{token}", o.base_url);
    Message {
        from: o.from.clone(),
        to: to.to_owned(),
        subject: "Reset your password".into(),
        text: format!("Reset your password: {link}\nThis link expires in one hour.\n"),
    }
}

impl Auth {
    /// The reset routes. It starts the delivery task. It fails when the URL
    /// or the sender is not valid.
    pub fn reset_routes(&self, mut o: ResetOptions) -> Result<Router, AuthError> {
        let url_ok = url::Url::parse(&o.base_url).is_ok_and(|u| {
            matches!(u.scheme(), "http" | "https")
                && u.host_str().is_some()
                && u.username().is_empty()
                && u.query().is_none()
                && u.fragment().is_none()
        });
        if !url_ok {
            return Err(AuthError(
                "auth: cannot enable password reset: the public reset URL is invalid\nTry: pass the public /reset URL in ResetOptions::base_url".into(),
            ));
        }
        o.base_url = o.base_url.trim_end_matches('/').to_owned();
        if reset_message(&o, "check@example.com", "token")
            .build()
            .is_err()
        {
            return Err(AuthError(
                "auth: cannot enable password reset: the sender is invalid\nTry: set MAIL_FROM to a valid sender address".into(),
            ));
        }
        let (tx, rx) = mpsc::channel(64);
        let task = tokio::spawn(deliver_loop(self.clone(), Arc::new(o), rx));
        self.track(task);
        Ok(Router::new()
            .route(PATH_RESET_REQUEST, post(request))
            .route(PATH_RESET_CONFIRM, post(confirm))
            .layer(middleware::map_response(endpoint))
            .with_state(ResetState {
                auth: self.clone(),
                queue: tx,
            }))
    }
}

async fn request(State(s): State<ResetState>, body: Body) -> Response {
    // Every request, also a malformed or throttled one, gets the same empty
    // answer. This handler does not read account data.
    if let Ok(input) = decode_json::<RequestBody>(body).await
        && let (email, true) = normalize_email(&input.email)
        && reserve_reset_request(&s.auth, &email).await
    {
        let _ = s.queue.try_send(email);
    }
    StatusCode::ACCEPTED.into_response()
}

/// Applies the email limit before the global limit, so repeated requests
/// for one email cannot use up the global budget.
async fn reserve_reset_request(auth: &Auth, email: &str) -> bool {
    let now = auth.now();
    let per_email = queries::reserve_attempt(
        auth.pool(),
        &format!("reset-email:{email}"),
        now,
        auth.0.threshold,
        auth.0.window,
    )
    .await;
    if !matches!(per_email, Ok(Ok(_))) {
        return false;
    }
    matches!(
        queries::reserve_attempt(
            auth.pool(),
            "reset-global",
            now,
            RESET_GLOBAL_THRESHOLD,
            auth.0.window
        )
        .await,
        Ok(Ok(_))
    )
}

async fn deliver_loop(auth: Auth, o: Arc<ResetOptions>, mut rx: mpsc::Receiver<String>) {
    let mut stop = auth.stopped();
    loop {
        // A stop that came before the task started is already in the value.
        if *stop.borrow_and_update() {
            return;
        }
        tokio::select! {
            _ = stop.changed() => return,
            next = rx.recv() => {
                let Some(email) = next else { return };
                let run = tokio::time::timeout(Duration::from_secs(30), deliver(&auth, &o, &email)).await;
                if !matches!(run, Ok(Ok(()))) {
                    // Never log the email, the token, or the SMTP error text.
                    tracing::error!(try_ = "check the database and mail configuration", "auth: password reset delivery failed");
                }
            }
        }
    }
}

async fn deliver(auth: &Auth, o: &ResetOptions, email: &str) -> anyhow::Result<()> {
    let Some(id) = sqlx::query_scalar!("SELECT id FROM users WHERE email = $1", email)
        .fetch_optional(auth.pool())
        .await?
    else {
        return Ok(());
    };
    let mut raw = [0u8; 32];
    aws_lc_rs::rand::fill(&mut raw).map_err(|_| anyhow::anyhow!("random source"))?;
    let token = B64URL.encode(raw);
    let digest = Sha256::digest(raw).to_vec();
    sqlx::query!(
        "INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
        digest,
        id,
        auth.now() + RESET_LIFETIME
    )
    .execute(auth.pool())
    .await?;
    o.mailer.send(&reset_message(o, email, &token)).await
}

fn reset_invalid() -> Response {
    Problem::new(
        StatusCode::UNPROCESSABLE_ENTITY,
        "The reset link is invalid or expired. Request a new link.",
    )
    .into_response()
}

async fn confirm(State(s): State<ResetState>, session: Session, body: Body) -> Response {
    let Ok(input) = decode_json::<ConfirmBody>(body).await else {
        return Problem::new(
            StatusCode::BAD_REQUEST,
            "Enter a reset token and a new password.",
        )
        .into_response();
    };
    let n = password_length(&input.password);
    if !(MIN_PASSWORD_LENGTH..=MAX_PASSWORD_LENGTH).contains(&n) {
        return Problem::new(
            StatusCode::UNPROCESSABLE_ENTITY,
            "The request has invalid fields.",
        )
        .with_errors(vec![FieldError::new(
            "password",
            format!("Use between {MIN_PASSWORD_LENGTH} and {MAX_PASSWORD_LENGTH} characters."),
        )])
        .into_response();
    }
    let raw = match B64URL.decode(&input.token) {
        Ok(r) if r.len() == 32 => r,
        _ => return reset_invalid(),
    };
    let digest = Sha256::digest(&raw).to_vec();
    let auth = &s.auth;
    let params = auth.0.params;
    let pw = input.password;
    let hash = match auth
        .slots()
        .run(move || password::hash_password(&params, &pw))
        .await
    {
        Ok(h) => h,
        Err(e) => return hash_failed("hash the reset password", e),
    };
    let id = match consume_reset(auth, &digest, &hash).await {
        Ok(Some(id)) => id,
        Ok(None) => return reset_invalid(),
        Err(e) => return internal_error("confirm the password reset", &e),
    };
    // The store row of the current session is already gone. End the
    // session in this response too, so the cookie is removed.
    if let Ok(Some(current)) = session.get::<i64>(SESSION_USER_KEY).await
        && current == id
        && let Err(e) = session.flush().await
    {
        return internal_error("end the reset session", &e);
    }
    StatusCode::NO_CONTENT.into_response()
}

/// Sets the password, consumes all reset tokens of the user, deletes the
/// sessions, and clears the lockout, in one transaction.
async fn consume_reset(auth: &Auth, digest: &[u8], hash: &str) -> Result<Option<i64>, sqlx::Error> {
    let mut tx = queries::begin(auth.pool()).await?;
    // Lock the user before the token. Different tokens of one user serialize here.
    let Some(user) = sqlx::query!(
        "SELECT u.id, u.email FROM users u JOIN password_resets p ON p.user_id = u.id WHERE p.token_hash = $1 FOR UPDATE OF u",
        digest
    )
    .fetch_optional(&mut *tx)
    .await?
    else {
        return Ok(None);
    };
    let token = sqlx::query!(
        "SELECT expires_at, used_at FROM password_resets WHERE token_hash = $1 FOR UPDATE",
        digest
    )
    .fetch_one(&mut *tx)
    .await?;
    let now = auth.now();
    if token.used_at.is_some() || now >= token.expires_at {
        return Ok(None);
    }
    sqlx::query!(
        "UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1",
        user.id,
        hash,
        now
    )
    .execute(&mut *tx)
    .await?;
    sqlx::query!(
        "UPDATE password_resets SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL",
        user.id,
        now
    )
    .execute(&mut *tx)
    .await?;
    queries::delete_user_sessions(&mut tx, user.id).await?;
    sqlx::query!("DELETE FROM login_attempts WHERE email = $1", user.email)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;
    Ok(Some(user.id))
}
