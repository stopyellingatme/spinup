//! The register, login, logout, and session endpoints, as
//! `kit/auth/handlers.go`.

use axum::body::Body;
use axum::extract::State;
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router, middleware};
use serde::Deserialize;
use serde::de::DeserializeOwned;
use serde_json::json;
use tower_sessions::Session;

use super::password::{self, HashError, MAX_PASSWORD_LENGTH, MIN_PASSWORD_LENGTH, password_length};
use super::ratelimit::{PATH_LOGIN, PATH_REGISTER};
use super::{Auth, CurrentUser, HashBusy, User, internal_error, no_store, queries};
use crate::problem::{FieldError, Problem};

/// The limit of the JSON body of the auth endpoints.
pub const MAX_BODY: usize = 16 << 10;
/// The longest email address that fits in SMTP (RFC 5321).
pub const MAX_EMAIL_LENGTH: usize = 254;

#[derive(Debug, Default, Deserialize)]
#[serde(deny_unknown_fields, default)]
struct Credentials {
    email: String,
    password: String,
}

pub(super) fn routes(auth: Auth) -> Router {
    Router::new()
        .route(PATH_REGISTER, post(register))
        .route(PATH_LOGIN, post(login))
        .route("/api/auth/logout", post(logout))
        .route("/api/session", get(session_route))
        .layer(middleware::map_response(endpoint))
        .with_state(auth)
}

/// Every auth answer has `Cache-Control: no-store`.
pub(super) async fn endpoint(mut res: Response) -> Response {
    no_store(&mut res);
    res
}

fn bad(status: StatusCode, detail: &str) -> Response {
    Problem::new(status, detail).into_response()
}

/// Reads a JSON body with the rules of the Go `httpx.DecodeJSON`: at most
/// [`MAX_BODY`] bytes, one JSON value, and only known fields.
// An axum Response is 128 bytes. The error path answers the request at once,
// so boxing it buys nothing.
#[allow(clippy::result_large_err)]
pub(super) async fn decode_json<T: DeserializeOwned>(body: Body) -> Result<T, Response> {
    let bytes = axum::body::to_bytes(body, MAX_BODY).await.map_err(|_| {
        bad(
            StatusCode::PAYLOAD_TOO_LARGE,
            "The request body exceeds the size limit.",
        )
    })?;
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Err(bad(
            StatusCode::BAD_REQUEST,
            "The request body must contain one JSON value.",
        ));
    }
    let syntax = || {
        bad(
            StatusCode::BAD_REQUEST,
            "The request body must contain valid JSON with only known fields and correct value types.",
        )
    };
    let mut de = serde_json::Deserializer::from_slice(&bytes);
    let value = T::deserialize(&mut de).map_err(|e| {
        if e.is_eof() {
            bad(
                StatusCode::BAD_REQUEST,
                "The request body must contain one JSON value.",
            )
        } else {
            syntax()
        }
    })?;
    if de.end().is_err() {
        // A second value after the first: Go decodes it to tell the cases apart.
        let rest = serde_json::Deserializer::from_slice(&bytes)
            .into_iter::<serde_json::Value>()
            .nth(1);
        return Err(match rest {
            Some(Ok(_)) => bad(
                StatusCode::BAD_REQUEST,
                "The request body must contain only one JSON value.",
            ),
            _ => syntax(),
        });
    }
    Ok(value)
}

// An axum Response is 128 bytes. The error path answers the request at once,
// so boxing it buys nothing.
#[allow(clippy::result_large_err)]
async fn decode_credentials(headers: &HeaderMap, body: Body) -> Result<Credentials, Response> {
    let media = headers
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .split(';')
        .next()
        .unwrap_or("")
        .trim()
        .to_ascii_lowercase();
    if media != "application/json" {
        return Err(bad(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            "The request body must be application/json.",
        ));
    }
    decode_json(body).await
}

fn invalid(fields: Vec<FieldError>) -> Response {
    Problem::new(
        StatusCode::UNPROCESSABLE_ENTITY,
        "The request has invalid fields.",
    )
    .with_errors(fields)
    .into_response()
}

fn is_atext(c: char) -> bool {
    c.is_ascii_alphanumeric() || "!#$%&'*+-/=?^_`{|}~".contains(c) || !c.is_ascii()
}

fn dot_atom(s: &str) -> bool {
    !s.is_empty()
        && s.split('.')
            .all(|p| !p.is_empty() && p.chars().all(is_atext))
}

/// Trims and lower-cases `s`. The flag is false unless the result is a bare
/// `local@domain` address (dot-atom on both sides, as Go's `net/mail`
/// accepts and prints back unchanged).
pub fn normalize_email(s: &str) -> (String, bool) {
    let email = s.trim().to_lowercase();
    if email.is_empty() || email.len() > MAX_EMAIL_LENGTH {
        return (email, false);
    }
    let ok = match email.split_once('@') {
        Some((local, domain)) => dot_atom(local) && dot_atom(domain),
        None => false,
    };
    (email, ok)
}

/// The 503 answer of a full hash queue. The request did no password work,
/// so the middleware gives its rate limit tokens back.
pub(super) fn busy() -> Response {
    let mut res = Problem::new(
        StatusCode::SERVICE_UNAVAILABLE,
        "The server is busy. Try again in a moment.",
    )
    .into_response();
    res.headers_mut()
        .insert(header::RETRY_AFTER, HeaderValue::from_static("1"));
    res.extensions_mut().insert(HashBusy);
    res
}

pub(super) fn hash_failed(what: &str, err: HashError) -> Response {
    match err {
        HashError::Busy => busy(),
        HashError::Failed => internal_error(what, &"the hash task failed"),
    }
}

fn user_json(status: StatusCode, user: &User) -> Response {
    (status, Json(json!({ "user": user }))).into_response()
}

async fn register(
    State(auth): State<Auth>,
    session: Session,
    headers: HeaderMap,
    body: Body,
) -> Response {
    let input = match decode_credentials(&headers, body).await {
        Ok(c) => c,
        Err(r) => return r,
    };
    let (email, email_ok) = normalize_email(&input.email);
    let mut fields = Vec::new();
    if !email_ok {
        fields.push(FieldError::new("email", "Enter a valid email address."));
    }
    let n = password_length(&input.password);
    if n < MIN_PASSWORD_LENGTH {
        fields.push(FieldError::new(
            "password",
            format!("Use at least {MIN_PASSWORD_LENGTH} characters."),
        ));
    } else if n > MAX_PASSWORD_LENGTH {
        fields.push(FieldError::new(
            "password",
            format!("Use at most {MAX_PASSWORD_LENGTH} characters."),
        ));
    }
    if !fields.is_empty() {
        return invalid(fields);
    }
    let params = auth.0.params;
    let pw = input.password;
    let hash = match auth
        .slots()
        .run(move || password::hash_password(&params, &pw))
        .await
    {
        Ok(h) => h,
        Err(e) => return hash_failed("hash a password", e),
    };
    let user = match queries::create_user(auth.pool(), &email, &hash).await {
        Ok(Some(u)) => u,
        Ok(None) => {
            return Problem::new(
                StatusCode::CONFLICT,
                "An account with this email address already exists.",
            )
            .with_errors(vec![FieldError::new(
                "email",
                "This email address is already registered.",
            )])
            .into_response();
        }
        Err(e) => return internal_error("create a user", &e),
    };
    if let Err(e) = auth.start_session(&session, &user).await {
        return internal_error("start a session", &e);
    }
    user_json(StatusCode::CREATED, &user)
}

async fn login(
    State(auth): State<Auth>,
    session: Session,
    headers: HeaderMap,
    body: Body,
) -> Response {
    let input = match decode_credentials(&headers, body).await {
        Ok(c) => c,
        Err(r) => return r,
    };
    let (email, email_ok) = normalize_email(&input.email);
    let mut fields = Vec::new();
    if !email_ok {
        fields.push(FieldError::new("email", "Enter a valid email address."));
    }
    match password_length(&input.password) {
        0 => fields.push(FieldError::new("password", "Enter your password.")),
        n if n > MAX_PASSWORD_LENGTH => fields.push(FieldError::new(
            "password",
            format!("Use at most {MAX_PASSWORD_LENGTH} characters."),
        )),
        _ => {}
    }
    if !fields.is_empty() {
        return invalid(fields);
    }
    // Take a place in the hash queue before any database work. A burst of
    // logins that the queue refuses never waits for a pool connection.
    let Ok(place) = auth.slots().admit() else {
        return busy();
    };
    let now = auth.now();
    let attempt =
        match queries::reserve_attempt(auth.pool(), &email, now, auth.0.threshold, auth.0.window)
            .await
        {
            Ok(Ok(id)) => id,
            Ok(Err(until)) => {
                let wait = ((until - now).as_seconds_f64().ceil() as i64).max(1);
                let mut res = Problem::new(
                    StatusCode::TOO_MANY_REQUESTS,
                    "Too many failed logins for this email address. Try again later.",
                )
                .into_response();
                res.headers_mut()
                    .insert(header::RETRY_AFTER, HeaderValue::from(wait));
                return res;
            }
            Err(e) => return internal_error("reserve a login attempt", &e),
        };
    let found = match queries::credentials_by_email(auth.pool(), &email).await {
        Ok(f) => f,
        Err(e) => return internal_error("read a user", &e),
    };
    // Spend the same work for an unknown email, so the time does not reveal accounts.
    let stored = found
        .as_ref()
        .map_or_else(|| auth.0.dummy.clone(), |(_, h)| h.clone());
    let (pw, check) = (input.password.clone(), stored.clone());
    let verified = auth
        .slots()
        .run_admitted(&place, move || password::verify_password(&check, &pw))
        .await;
    let (matched, used) = match verified {
        Err(e) => {
            // No password was checked, so the attempt must not count. The
            // release runs to the end even when the client goes away.
            let pool = auth.pool().clone();
            tokio::spawn(async move {
                if let Err(e) = queries::release_attempt(&pool, attempt).await {
                    tracing::warn!(error = %e, "auth: cannot release a login attempt");
                }
            });
            return hash_failed("verify a password", e);
        }
        Ok(Err(e)) => return internal_error("verify a password", &e),
        Ok(Ok(v)) => v,
    };
    let user = match found {
        Some((user, _)) if matched => user,
        _ => {
            return Problem::new(
                StatusCode::UNAUTHORIZED,
                "The email address or password is incorrect.",
            )
            .into_response();
        }
    };
    if let Err(e) = queries::clear_failures(auth.pool(), &email).await {
        return internal_error("clear failed logins", &e);
    }
    if used != auth.0.params {
        // Rehash with the current parameters, in the place this login holds.
        let (params, pw) = (auth.0.params, input.password.clone());
        let fresh = auth
            .slots()
            .run_admitted(&place, move || password::hash_password(&params, &pw))
            .await;
        let result = match fresh {
            Ok(h) => queries::update_password_hash(auth.pool(), user.id, &stored, &h)
                .await
                .map_err(|e| e.to_string()),
            Err(e) => Err(format!("{e:?}")),
        };
        if let Err(e) = result {
            tracing::warn!(error = %e, "auth: cannot rehash a password with the current parameters");
        }
    }
    drop(place);
    if let Err(e) = auth.start_session(&session, &user).await {
        return internal_error("start a session", &e);
    }
    user_json(StatusCode::OK, &user)
}

async fn logout(session: Session) -> Response {
    // flush deletes the session row and the cookie, also for a session
    // without a user.
    if let Err(e) = session.flush().await {
        return internal_error("destroy a session", &e);
    }
    StatusCode::NO_CONTENT.into_response()
}

async fn session_route(user: Result<CurrentUser, Response>) -> Response {
    match user {
        Ok(CurrentUser(u)) => user_json(StatusCode::OK, &u),
        Err(r) => r,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // TestNormalizeEmail of password_test.go, unchanged.
    #[test]
    fn normalize_email_table() {
        let long = format!("{}@{}.com", "a".repeat(64), "b".repeat(186));
        for (input, want, ok) in [
            ("alice@example.com", "alice@example.com", true),
            ("  Alice@Example.COM\t", "alice@example.com", true),
            ("a.b+tag@sub.example.org", "a.b+tag@sub.example.org", true),
            ("", "", false),
            ("   ", "", false),
            ("alice", "alice", false),
            ("alice@", "alice@", false),
            ("@example.com", "@example.com", false),
            (
                "Alice <alice@example.com>",
                "alice <alice@example.com>",
                false,
            ),
            ("<alice@example.com>", "<alice@example.com>", false),
            ("a@b@example.com", "a@b@example.com", false),
            (r#""a b"@example.com"#, r#""a b"@example.com"#, false),
            (
                "alice@example.com, bob@example.com",
                "alice@example.com, bob@example.com",
                false,
            ),
            (long.as_str(), "", false),
        ] {
            let (got, got_ok) = normalize_email(input);
            assert_eq!(got_ok, ok, "{input:?}");
            if !want.is_empty() {
                assert_eq!(got, want, "{input:?}");
            }
        }
    }
}
