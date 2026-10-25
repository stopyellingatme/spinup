//! Problem details (RFC 9457), as `kit/httpx` in Go.

use axum::http::{HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use serde::{Deserialize, Serialize};
use utoipa::ToSchema;
use utoipa::openapi::schema::{ObjectBuilder, Type};

/// The media type of a problem response.
pub const PROBLEM_JSON: &str = "application/problem+json";

// One validation failure of one input field.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct FieldError {
    // The invalid input field.
    pub field: String,
    // What is wrong with the field.
    pub message: String,
}

impl FieldError {
    // Returns a field error.
    pub fn new(field: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            field: field.into(),
            message: message.into(),
        }
    }
}

// An HTTP error as problem details. The JSON fields are the fields of the Go
// `httpx.Problem`, plus `request_id`, which the Go `WriteProblem` adds.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize, ToSchema)]
pub struct Problem {
    // A URI reference that identifies the problem type.
    #[serde(rename = "type")]
    pub type_: String,
    // A short summary of the problem type.
    pub title: String,
    // The HTTP status code.
    #[schema(schema_with = plain_integer)]
    pub status: u16,
    // An explanation of this occurrence.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[schema(nullable = false)]
    pub detail: Option<String>,
    // A URI reference that identifies this occurrence.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[schema(nullable = false)]
    pub instance: Option<String>,
    // The request ID of this occurrence.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[schema(nullable = false)]
    pub request_id: Option<String>,
    // The validation failures of the input fields.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    #[schema(inline)]
    pub errors: Vec<FieldError>,
}

/// The Go spec has `type: integer` with no format for `status`.
fn plain_integer() -> ObjectBuilder {
    ObjectBuilder::new().schema_type(Type::Integer)
}

/// The status text of Go's `http.StatusText` for the codes that the kit uses.
fn status_text(status: StatusCode) -> &'static str {
    match status.as_u16() {
        413 => "Request Entity Too Large",
        _ => status.canonical_reason().unwrap_or(""),
    }
}

impl Problem {
    /// Returns a problem with a status and a detail text.
    pub fn new(status: StatusCode, detail: impl Into<String>) -> Self {
        Self {
            status: status.as_u16(),
            detail: Some(detail.into()),
            ..Self::default()
        }
    }

    /// Returns a problem with the status only. The title is the status text.
    pub fn status(status: StatusCode) -> Self {
        Self {
            status: status.as_u16(),
            ..Self::default()
        }
    }

    /// Adds field errors.
    pub fn with_errors(mut self, errors: Vec<FieldError>) -> Self {
        self.errors = errors;
        self
    }

    /// Sets the defaults of the Go `WriteProblem`: status 500, type
    /// `about:blank`, and the status text as the title.
    pub fn normalized(mut self) -> Self {
        if self.status == 0 {
            self.status = 500;
        }
        if self.type_.is_empty() {
            self.type_ = "about:blank".into();
        }
        if self.title.is_empty() {
            let status =
                StatusCode::from_u16(self.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
            self.title = status_text(status).into();
        }
        self
    }

    /// The HTTP status of the problem.
    pub fn status_code(&self) -> StatusCode {
        StatusCode::from_u16(self.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR)
    }
}

impl IntoResponse for Problem {
    fn into_response(self) -> Response {
        let p = self.normalized();
        // The payload holds only strings and numbers, so encoding cannot fail.
        let mut body = serde_json::to_vec(&p).unwrap_or_default();
        body.push(b'\n');
        let mut res = (p.status_code(), body).into_response();
        res.headers_mut()
            .insert(header::CONTENT_TYPE, HeaderValue::from_static(PROBLEM_JSON));
        // The request ID middleware reads the problem back and adds the ID.
        res.extensions_mut().insert(p);
        res
    }
}

/// An error that a handler returns. It becomes a problem response.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ApiError(pub Box<Problem>);

impl ApiError {
    /// Returns an error with a status and a detail text.
    pub fn new(status: StatusCode, detail: impl Into<String>) -> Self {
        Self::from(Problem::new(status, detail))
    }

    /// The 404 error of an operation on a row that does not exist.
    pub fn not_found() -> Self {
        Self::new(StatusCode::NOT_FOUND, "The record does not exist.")
    }

    /// A 422 error with field errors.
    pub fn invalid(errors: Vec<FieldError>) -> Self {
        Self::from(
            Problem::new(
                StatusCode::UNPROCESSABLE_ENTITY,
                "The request has invalid values.",
            )
            .with_errors(errors),
        )
    }

    /// A 401 error: the operation needs a signed-in user.
    pub fn unauthorized() -> Self {
        Self::new(StatusCode::UNAUTHORIZED, "A signed-in user is required.")
    }
}

impl From<Problem> for ApiError {
    fn from(p: Problem) -> Self {
        Self(Box::new(p))
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (*self.0).into_response()
    }
}

impl std::fmt::Display for ApiError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let p = (*self.0).clone().normalized();
        match &p.detail {
            Some(d) => write!(f, "{}: {d}", p.title),
            None => write!(f, "{}", p.title),
        }
    }
}

impl std::error::Error for ApiError {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults_follow_go_write_problem() {
        let p = Problem::status(StatusCode::NOT_FOUND).normalized();
        assert_eq!(p.type_, "about:blank");
        assert_eq!(p.title, "Not Found");
        let json = serde_json::to_value(&p).unwrap();
        assert_eq!(
            json,
            serde_json::json!({"type": "about:blank", "title": "Not Found", "status": 404})
        );
        assert_eq!(
            Problem::status(StatusCode::PAYLOAD_TOO_LARGE)
                .normalized()
                .title,
            "Request Entity Too Large"
        );
    }

    #[test]
    fn response_has_problem_media_type() {
        let res = ApiError::invalid(vec![FieldError::new("title", "is required")]).into_response();
        assert_eq!(res.status(), StatusCode::UNPROCESSABLE_ENTITY);
        assert_eq!(res.headers()[header::CONTENT_TYPE], PROBLEM_JSON);
    }
}
