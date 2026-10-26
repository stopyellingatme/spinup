//! Problem details (RFC 9457), as Go `httpx.Problem`, `httpx.Error`, and `WriteProblem`.

use axum::http::{HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use serde::{Deserialize, Serialize};
use utoipa::ToSchema;
use utoipa::openapi::schema::{ObjectBuilder, Type};

/// The media type of a problem answer.
pub const PROBLEM_JSON: &str = "application/problem+json";

// The schema types use `//` comments: utoipa turns `///` comments into spec descriptions,
// and the Go spec has none (r0-06-ref-api.md).

// One validation failure of one input field (Go `httpx.FieldError`).
#[allow(missing_docs, reason = "a `///` comment becomes a spec description")]
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct FieldError {
    // The invalid input field.
    pub field: String,
    // What is wrong with the field.
    pub message: String,
}

impl FieldError {
    /// Returns a field error.
    pub fn new(field: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            field: field.into(),
            message: message.into(),
        }
    }
}

// An HTTP error as problem details (Go `httpx.Problem`). The JSON fields are the Go fields,
// in the Go order, plus `request_id`, which the Go `WriteProblem` adds at the end. The
// request ID layer fills `request_id`.
#[allow(missing_docs, reason = "a `///` comment becomes a spec description")]
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
    // An explanation of this occurrence. It holds no internal error text.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[schema(nullable = false)]
    pub detail: Option<String>,
    // A URI reference that identifies this occurrence.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[schema(nullable = false)]
    pub instance: Option<String>,
    // The validation failures of the input fields.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    #[schema(inline)]
    pub errors: Vec<FieldError>,
    // The request ID of this occurrence.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    #[schema(nullable = false)]
    pub request_id: Option<String>,
}

/// The Go spec has `type: integer` with no format for `status`.
fn plain_integer() -> ObjectBuilder {
    ObjectBuilder::new().schema_type(Type::Integer)
}

/// The status text of Go `http.StatusText` for a status.
pub fn status_text(status: StatusCode) -> &'static str {
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

    /// Sets the field errors.
    #[must_use]
    pub fn with_errors(mut self, errors: Vec<FieldError>) -> Self {
        self.errors = errors;
        self
    }

    /// Sets the defaults of the Go `WriteProblem`: status 500, type `about:blank`, and the
    /// status text as the title.
    #[must_use]
    pub fn normalized(mut self) -> Self {
        if self.status == 0 {
            self.status = 500;
        }
        if self.type_.is_empty() {
            self.type_ = "about:blank".into();
        }
        if self.title.is_empty() {
            self.title = status_text(self.status_code()).into();
        }
        self
    }

    /// The HTTP status of the problem. A status that is not valid gives 500.
    pub fn status_code(&self) -> StatusCode {
        StatusCode::from_u16(self.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR)
    }

    /// The JSON body of the answer: the problem and one final newline, as Go writes it.
    pub fn body(&self) -> Vec<u8> {
        // The payload holds only strings and numbers, so encoding cannot fail.
        let mut body = serde_json::to_vec(self).unwrap_or_default();
        body.push(b'\n');
        body
    }
}

impl IntoResponse for Problem {
    /// Writes `application/problem+json`. The response extensions keep the problem, so the
    /// recover layer can add the request ID to the body.
    fn into_response(self) -> Response {
        let p = self.normalized();
        let mut res = (p.status_code(), p.body()).into_response();
        res.headers_mut()
            .insert(header::CONTENT_TYPE, HeaderValue::from_static(PROBLEM_JSON));
        res.extensions_mut().insert(p);
        res
    }
}

/// An error that a handler returns (Go `httpx.Error`). It becomes a problem answer. The
/// problem is boxed, so `Result<T, ApiError>` stays small (clippy `result_large_err`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ApiError(pub Box<Problem>);

/// The result type of a handler.
pub type ApiResult<T> = Result<T, ApiError>;

impl ApiError {
    /// Returns an error with a status and a detail text.
    pub fn new(status: StatusCode, detail: impl Into<String>) -> Self {
        Self::from(Problem::new(status, detail))
    }

    /// Returns an error with the status only.
    pub fn status(status: StatusCode) -> Self {
        Self::from(Problem::status(status))
    }

    /// The 400 error of a request that the server cannot read.
    pub fn bad_request(detail: impl Into<String>) -> Self {
        Self::new(StatusCode::BAD_REQUEST, detail)
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

    /// The 401 error: the operation needs a signed-in user.
    pub fn unauthorized() -> Self {
        Self::new(StatusCode::UNAUTHORIZED, "A signed-in user is required.")
    }

    /// The 500 error. It holds no internal error text.
    pub fn internal() -> Self {
        Self::status(StatusCode::INTERNAL_SERVER_ERROR)
    }

    /// The problem of the error.
    pub fn problem(&self) -> &Problem {
        &self.0
    }
}

impl From<Problem> for ApiError {
    fn from(p: Problem) -> Self {
        Self(Box::new(p))
    }
}

impl From<anyhow::Error> for ApiError {
    /// Keeps an `ApiError` inside the chain (Go `httpx.ProblemFrom`). Every other error
    /// becomes a 500 problem without its text. The error text goes to the log.
    fn from(err: anyhow::Error) -> Self {
        match err.downcast::<ApiError>() {
            Ok(api) => api,
            Err(err) => {
                tracing::error!(error = %format!("{err:#}"), "HTTP handler failed");
                Self::internal()
            }
        }
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (*self.0).into_response()
    }
}

impl std::fmt::Display for ApiError {
    /// The title and the detail, without internal error text (Go `Error.Error`).
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let p = (*self.0).clone().normalized();
        match &p.detail {
            Some(d) => write!(f, "{}: {d}", p.title),
            None => write!(f, "{}", p.title),
        }
    }
}

impl std::error::Error for ApiError {}
