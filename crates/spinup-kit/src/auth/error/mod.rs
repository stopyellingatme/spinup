//! Auth configuration failures and safe HTTP answers.

use crate::httpx::Problem;
use axum::http::{HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};

/// An auth failure. Its text states the cause and ends with a `Try:` line.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthError(pub String);

impl std::fmt::Display for AuthError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for AuthError {}

impl IntoResponse for AuthError {
    /// Logs the failure and returns a 500 answer without internal details.
    fn into_response(self) -> Response {
        tracing::error!(error = %self, "auth: request failed");
        let mut response = Problem::status(StatusCode::INTERNAL_SERVER_ERROR).into_response();
        response
            .headers_mut()
            .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
        response
    }
}
