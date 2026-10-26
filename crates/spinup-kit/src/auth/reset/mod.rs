//! Password reset (Go `kit/auth/reset.go`, ADR 0013 §2).
//!
//! ```text
//! POST /api/auth/reset/request  {"email"}             -> 202 (always, empty body)
//! POST /api/auth/reset/confirm  {"token","password"}  -> 204, 400, 422, 429, 503
//! ```
//!
//! - A request always answers 202 with an empty body, also for a malformed, unknown, or
//!   throttled email. It reads no account data. Admitted emails go into a bounded queue.
//!   A background task looks up the account, stores the SHA-256 of a new one-use token,
//!   and sends the link, so the response time does not reveal accounts.
//! - One email gets at most the lockout threshold of requests in one lockout window. All
//!   emails together get at most 100. The per-client rate limit of [`Auth::layer`] also
//!   applies.
//! - A confirmation sets the password once, ends every session of the user, and clears
//!   the lockout of the email, in one transaction. A token lives one hour.
//! - The link is the trusted [`ResetOptions::base_url`], a `/`, and the token. It never
//!   comes from a request header.
//!
//! ```no_run
//! # async fn demo(pool: spinup_kit::db::PgPool) -> anyhow::Result<()> {
//! use std::sync::Arc;
//! use spinup_kit::auth::reset::{self, ResetOptions};
//! use spinup_kit::auth::{Auth, Options};
//! use spinup_kit::mail;
//!
//! let auth = Auth::new(pool, Options::default())?;
//! let mailer = mail::new(&spinup_kit::config::load()?)?;
//! let resets = reset::routes(&auth, ResetOptions {
//!     mailer: Some(mailer),
//!     from: "noreply@app.example".into(),
//!     base_url: "https://app.example/reset".into(),
//!     templates: None,
//! })?;
//! let api = auth.layer(auth.routes().merge(resets));
//! # let _ = api;
//! # Ok(())
//! # }
//! ```

mod store;

use std::sync::Arc;
use std::time::Duration;

use axum::Router;
use axum::body::Body;
use axum::extract::State;
use axum::http::StatusCode;
use axum::middleware;
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD as B64URL;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use tokio::sync::mpsc;

use crate::auth::error::AuthError;
use crate::auth::password::{self, MAX_PASSWORD_LENGTH, MIN_PASSWORD_LENGTH, password_length};
use crate::auth::ratelimit::{PATH_RESET_CONFIRM, PATH_RESET_REQUEST};
use crate::auth::required::{Auth, Session, internal_error};
use crate::auth::routes::{decode_json, endpoint, hash_failed, normalize_email};
use crate::httpx::{FieldError, Problem};
use crate::mail::{Mailer, Message, Templates, render};

/// The validity period of a reset token (Go `ResetLifetime`).
pub const RESET_LIFETIME: Duration = Duration::from_secs(3600);
/// The name of the reset templates: `password_reset.{subject,txt,html}.tmpl`.
pub const TEMPLATE_NAME: &str = "password_reset";

/// The size of the delivery queue. A request that finds it full sends no mail.
const QUEUE: usize = 64;
/// The limit of one delivery.
const DELIVERY_LIMIT: Duration = Duration::from_secs(30);

/// The settings of [`routes`] (Go `ResetOption`). `Default` has no mailer, which leaves
/// the routes out.
#[derive(Clone, Default)]
pub struct ResetOptions {
    /// The mailer (Go `WithMailer`). `None` leaves the routes out.
    pub mailer: Option<Arc<dyn Mailer>>,
    /// The sender address, from `MAIL_FROM` (Go `WithResetFrom`).
    pub from: String,
    /// The trusted public URL before the token, such as `https://app.example/reset` (Go
    /// `WithResetURL`). It never comes from a request header.
    pub base_url: String,
    /// The `password_reset.{subject,txt,html}.tmpl` templates (Go `WithResetTemplates`).
    /// Each gets a [`ResetData`]. Without them, the kit sends plain text.
    pub templates: Option<Templates>,
}

impl std::fmt::Debug for ResetOptions {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ResetOptions")
            .field("mailer", &self.mailer.is_some())
            .field("from", &self.from)
            .field("base_url", &self.base_url)
            .field("templates", &self.templates)
            .finish()
    }
}

/// The data of the reset templates (Go `ResetData`). It never holds a password. A
/// template writes the link as `{{ url }}`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct ResetData {
    /// The trusted public reset link with the one-use token.
    pub url: String,
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

fn cannot_enable(what: &str, try_: &str) -> AuthError {
    AuthError(format!(
        "auth: cannot enable password reset: {what}\nTry: {try_}"
    ))
}

/// The message with the link for `token` to `to` (Go `resetMessage`).
fn message(o: &ResetOptions, to: &str, token: &str) -> anyhow::Result<Message> {
    let url = format!("{}/{token}", o.base_url);
    match &o.templates {
        Some(t) => render(t, TEMPLATE_NAME, &o.from, to, &ResetData { url }),
        None => Ok(Message {
            from: o.from.clone(),
            to: to.to_owned(),
            subject: "Reset your password".into(),
            text: format!("Reset your password: {url}\nThis link expires in one hour."),
            html: String::new(),
        }),
    }
}

fn base_url_ok(raw: &str) -> bool {
    url::Url::parse(raw).is_ok_and(|u| {
        matches!(u.scheme(), "http" | "https")
            && u.host_str().is_some_and(|h| !h.is_empty())
            && u.username().is_empty()
            && u.password().is_none()
            && u.query().is_none()
            && u.fragment().is_none()
    })
}

/// Returns the password reset routes with the state applied, and starts the delivery
/// task (Go `Auth.ResetRoutes`). Without a mailer it returns an empty router. The routes
/// need [`Auth::layer`]. [`Auth::close`] stops the delivery task.
///
/// # Errors
///
/// An [`AuthError`] with a `Try:` line for an invalid base URL, an invalid sender, or
/// templates that do not render.
pub fn routes(auth: &Auth, options: ResetOptions) -> Result<Router, AuthError> {
    let mut o = options;
    let Some(mailer) = o.mailer.clone() else {
        return Ok(Router::new());
    };
    if !base_url_ok(&o.base_url) {
        return Err(cannot_enable(
            "the public reset URL is invalid",
            "pass the public /reset URL in ResetOptions::base_url",
        ));
    }
    o.base_url = o.base_url.trim_end_matches('/').to_owned();
    if o.from.parse::<lettre::message::Mailbox>().is_err() || o.from.contains(['\r', '\n']) {
        return Err(cannot_enable(
            "the sender is invalid",
            "set MAIL_FROM to a valid sender address",
        ));
    }
    if let Err(e) = message(&o, "check@example.com", "token") {
        return Err(cannot_enable(
            &e.to_string(),
            "check MAIL_FROM and the reset templates",
        ));
    }
    let (tx, rx) = mpsc::channel(QUEUE);
    auth.spawn(deliver_loop(auth.clone(), Arc::new(o), mailer, rx));
    Ok(Router::new()
        .route(PATH_RESET_REQUEST, post(request))
        .route(PATH_RESET_CONFIRM, post(confirm))
        .layer(middleware::map_response(endpoint))
        .with_state(ResetState {
            auth: auth.clone(),
            queue: tx,
        }))
}

// The session extractor answers 500 without the auth layer, as Go `endpoint` does.
async fn request(State(s): State<ResetState>, _session: Session, body: Body) -> Response {
    // Every request, also a malformed or throttled one, gets the same empty answer. This
    // handler reads no account data.
    if let Ok(input) = decode_json::<RequestBody>(body).await
        && let (email, true) = normalize_email(&input.email)
        && store::reserve_request(&s.auth, &email).await
    {
        let _ = s.queue.try_send(email);
    }
    StatusCode::ACCEPTED.into_response()
}

async fn deliver_loop(
    auth: Auth,
    o: Arc<ResetOptions>,
    mailer: Arc<dyn Mailer>,
    mut rx: mpsc::Receiver<String>,
) {
    let mut stop = auth.stopped();
    loop {
        // A stop that came before the task started is already in the value.
        if *stop.borrow_and_update() {
            return;
        }
        let email = tokio::select! {
            _ = stop.changed() => return,
            next = rx.recv() => match next {
                Some(email) => email,
                None => return,
            },
        };
        // Close cancels a delivery that runs, as the Go context does.
        let run = tokio::time::timeout(DELIVERY_LIMIT, deliver(&auth, &o, &*mailer, &email));
        let ok = tokio::select! {
            _ = stop.changed() => return,
            r = run => matches!(r, Ok(Ok(()))),
        };
        if !ok {
            // Never log the email, the token, or the SMTP error text.
            tracing::error!(
                "try" = "check the database and mail configuration",
                "auth: password reset delivery failed"
            );
        }
    }
}

async fn deliver(
    auth: &Auth,
    o: &ResetOptions,
    mailer: &dyn Mailer,
    email: &str,
) -> anyhow::Result<()> {
    let Some(id) = store::user_id(auth, email).await? else {
        return Ok(());
    };
    let mut raw = [0u8; 32];
    aws_lc_rs::rand::fill(&mut raw)
        .map_err(|_| anyhow::anyhow!("auth: the system random source failed"))?;
    let token = B64URL.encode(raw);
    let digest = Sha256::digest(raw);
    store::insert(auth, &digest[..], id, auth.now() + RESET_LIFETIME).await?;
    mailer.send(&message(o, email, &token)?).await
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
    let raw = match B64URL.decode(input.token.as_bytes()) {
        Ok(r) if r.len() == 32 => r,
        _ => return reset_invalid(),
    };
    let digest = Sha256::digest(&raw);
    let auth = &s.auth;
    // Bound the hash with the slots and the parameters of the registration.
    let params = auth.params();
    let pw = input.password;
    let hash = match auth
        .slots()
        .run(move || password::hash_password(&params, &pw))
        .await
    {
        Ok(h) => h,
        Err(e) => return hash_failed("hash the reset password", e),
    };
    let id = match store::consume(auth, &digest[..], &hash).await {
        Ok(Some(id)) => id,
        Ok(None) => return reset_invalid(),
        Err(e) => return internal_error("confirm the password reset", &e),
    };
    // The store row of the current session is already gone. End the session in this
    // answer too, so the cookie is removed.
    if session.user_id() == Some(id) {
        session.destroy();
    }
    StatusCode::NO_CONTENT.into_response()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reset_base_url_rules() {
        for (url, ok) in [
            ("https://app.example/reset", true),
            ("http://localhost:5173/reset/", true),
            ("ftp://app.example/reset", false),
            ("https://app.example/reset?x=1", false),
            ("https://app.example/reset#x", false),
            ("https://user@app.example/reset", false),
            ("/reset", false),
        ] {
            assert_eq!(base_url_ok(url), ok, "{url}");
        }
    }

    #[test]
    fn reset_plain_message() {
        let o = ResetOptions {
            from: "app@example.com".into(),
            base_url: "https://app.example/reset".into(),
            ..ResetOptions::default()
        };
        let m = message(&o, "you@example.com", "abc").unwrap();
        assert_eq!(
            m.text,
            "Reset your password: https://app.example/reset/abc\nThis link expires in one hour."
        );
        assert_eq!(m.subject, "Reset your password");
    }
}
