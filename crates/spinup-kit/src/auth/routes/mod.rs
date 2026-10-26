//! The register, login, logout, and session routes (Go `kit/auth/handlers.go`).
//!
//! ```text
//! POST /api/auth/register  {"email","password"} -> 201 {"user"}, 409, 422, 429, 503
//! POST /api/auth/login     {"email","password"} -> 200 {"user"}, 401, 422, 429, 503
//! POST /api/auth/logout                         -> 204
//! GET  /api/session                             -> 200 {"user"}, 401
//! ```
//!
//! Errors are problem details. A 422 problem lists field errors for `email` and
//! `password`. Register and login set the session cookie with a new token. When every
//! hash slot is busy, they answer 503 with Retry-After. A 429 from login is the lockout
//! of one email or the rate limit of one client. Every answer has
//! `Cache-Control: no-store`.

use axum::body::Body;
use axum::extract::State;
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router, middleware};
use serde::Deserialize;
use serde::de::DeserializeOwned;
use serde_json::json;

use crate::auth::password::{
    self, HashError, MAX_PASSWORD_LENGTH, MIN_PASSWORD_LENGTH, password_length,
};
use crate::auth::ratelimit::{HashBusy, PATH_LOGIN, PATH_REGISTER};
use crate::auth::required::{Auth, Session, User, internal_error, no_store, unauthorized};
use crate::auth::store::queries::{self, Reservation};
use crate::httpx::{BAD_JSON, FieldError, ONE_VALUE, ONLY_ONE_VALUE, Problem, TOO_LARGE};

/// The path of the logout route.
pub const PATH_LOGOUT: &str = "/api/auth/logout";
/// The path of the session route.
pub const PATH_SESSION: &str = "/api/session";
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

/// The auth routes of `auth`, with the state applied. They need [`Auth::layer`].
pub fn routes(auth: Auth) -> Router {
    Router::new()
        .route(PATH_REGISTER, post(register))
        .route(PATH_LOGIN, post(login))
        .route(PATH_LOGOUT, post(logout))
        .route(PATH_SESSION, get(session_route))
        .layer(middleware::map_response(endpoint))
        .with_state(auth)
}

/// Sets `Cache-Control: no-store` on an answer. The auth routes map every answer with it.
pub async fn endpoint(mut res: Response) -> Response {
    no_store(&mut res);
    res
}

fn bad(status: StatusCode, detail: &str) -> Response {
    Problem::new(status, detail).into_response()
}

/// Reads a JSON body with the rules of the Go `httpx.DecodeJSON`: at most [`MAX_BODY`]
/// bytes, one JSON value, only known fields, and correct value types (all 400, except
/// 413 for a large body).
///
/// # Errors
///
/// The problem answer.
// An axum Response is 128 bytes. The error path answers the request at once, so boxing
// it buys nothing.
#[allow(clippy::result_large_err)]
pub async fn decode_json<T: DeserializeOwned>(body: Body) -> Result<T, Response> {
    let bytes = axum::body::to_bytes(body, MAX_BODY)
        .await
        .map_err(|_| bad(StatusCode::PAYLOAD_TOO_LARGE, TOO_LARGE))?;
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Err(bad(StatusCode::BAD_REQUEST, ONE_VALUE));
    }
    let mut de = serde_json::Deserializer::from_slice(&bytes);
    let value = T::deserialize(&mut de).map_err(|e| {
        if e.is_eof() {
            bad(StatusCode::BAD_REQUEST, ONE_VALUE)
        } else {
            bad(StatusCode::BAD_REQUEST, BAD_JSON)
        }
    })?;
    if de.end().is_err() {
        // A second value after the first: Go decodes it to tell the cases apart.
        let rest = serde_json::Deserializer::from_slice(&bytes)
            .into_iter::<serde_json::Value>()
            .nth(1);
        return Err(match rest {
            Some(Ok(_)) => bad(StatusCode::BAD_REQUEST, ONLY_ONE_VALUE),
            _ => bad(StatusCode::BAD_REQUEST, BAD_JSON),
        });
    }
    Ok(value)
}

/// Checks that the media type is `application/json`, as Go `mime.ParseMediaType`.
#[allow(clippy::result_large_err)]
fn require_json(headers: &HeaderMap) -> Result<(), Response> {
    let media = headers
        .get(header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .split(';')
        .next()
        .unwrap_or("")
        .trim()
        .to_ascii_lowercase();
    if media == "application/json" {
        Ok(())
    } else {
        Err(bad(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            "The request body must be application/json.",
        ))
    }
}

#[allow(clippy::result_large_err)]
async fn decode_credentials(headers: &HeaderMap, body: Body) -> Result<Credentials, Response> {
    require_json(headers)?;
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

/// Trims and lower-cases `s` (Go `normalizeEmail`). The flag is false unless the result
/// is a bare `local@domain` address of at most [`MAX_EMAIL_LENGTH`] bytes (dot-atom on
/// both sides, which Go `net/mail` accepts and prints back unchanged).
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

/// The 503 answer of a full hash queue. The request did no password work, so the rate
/// limit layer gives its tokens back ([`HashBusy`]).
pub fn busy() -> Response {
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

/// The answer to an error of the hash queue: 503 for [`HashError::Busy`], else 500.
pub fn hash_failed(what: &str, err: HashError) -> Response {
    match err {
        HashError::Busy => busy(),
        HashError::Failed => internal_error(what, &err),
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
    let params = auth.params();
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
    auth.start_session(&session, &user);
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
    // Take a place in the hash queue before any database work. A burst of logins that
    // the queue refuses never waits for, or holds, a pool connection.
    let Ok(place) = auth.slots().admit() else {
        return busy();
    };
    let now = auth.now();
    let (threshold, window) = auth.lockout();
    let attempt = match queries::reserve_attempt(auth.pool(), &email, now, threshold, window).await
    {
        Ok(Reservation::Reserved(id)) => id,
        Ok(Reservation::LockedUntil(until)) => {
            // The wait is a small count of seconds.
            #[allow(clippy::cast_possible_truncation)]
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
    let stored = match &found {
        Some((_, h)) => h.clone(),
        None => auth.dummy_hash().await,
    };
    let (pw, check) = (input.password.clone(), stored.clone());
    let verified = auth
        .slots()
        .run_admitted(&place, move || password::verify_password(&check, &pw))
        .await;
    let (matched, used) = match verified {
        Err(e) => {
            // No password was checked, so the attempt must not count. The release runs
            // to the end also when the client goes away.
            let pool = auth.pool().clone();
            let release =
                tokio::spawn(async move { queries::release_attempt(&pool, attempt).await });
            if let Ok(Err(e)) = release.await {
                tracing::warn!(error = %e, "auth: cannot release a login attempt");
            }
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
    if used != auth.params() {
        // Rehash with the current parameters, in the place that this login holds. A
        // failure only logs: the old hash still works.
        let (params, pw) = (auth.params(), input.password.clone());
        let fresh = auth
            .slots()
            .run_admitted(&place, move || password::hash_password(&params, &pw))
            .await;
        let result = match fresh {
            Ok(h) => queries::update_password_hash(auth.pool(), user.id, &stored, &h)
                .await
                .map_err(|e| e.to_string()),
            Err(e) => Err(e.to_string()),
        };
        if let Err(e) = result {
            tracing::warn!(error = %e, "auth: cannot rehash a password with the current parameters");
        }
    }
    drop(place);
    auth.start_session(&session, &user);
    user_json(StatusCode::OK, &user)
}

async fn logout(session: Session) -> Response {
    // The commit deletes the session row and expires the cookie, also for a session
    // without a user.
    session.destroy();
    StatusCode::NO_CONTENT.into_response()
}

// The session extractor answers 500 without the auth layer, as Go does.
async fn session_route(_session: Session, user: Option<axum::Extension<User>>) -> Response {
    match user {
        Some(axum::Extension(u)) => user_json(StatusCode::OK, &u),
        None => unauthorized(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::required::problem;

    #[test]
    fn auth_routes_problem_helpers() {
        let r = problem(StatusCode::FORBIDDEN, "x");
        assert_eq!(r.headers()[header::CACHE_CONTROL], "no-store");
        let b = busy();
        assert_eq!(b.status(), StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(b.headers()[header::RETRY_AFTER], "1");
        assert!(b.extensions().get::<HashBusy>().is_some());
        assert_eq!(
            hash_failed("x", HashError::Failed).status(),
            StatusCode::INTERNAL_SERVER_ERROR
        );
    }
}
