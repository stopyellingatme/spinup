//! Problem answers, request extractors, and the HTTP layers (Go `kit/httpx`).
//!
//! Handlers return [`ApiResult`]. An [`ApiError`] becomes an `application/problem+json`
//! answer (RFC 9457) with the Go texts. The extractors [`ValidJson`], [`Path`], and
//! [`Query`] turn each request error into such an answer too.
//!
//! ```
//! use axum::Router;
//! use axum::routing::post;
//! use serde::Deserialize;
//! use spinup_kit::httpx::{ApiResult, ValidJson};
//! use utoipa::ToSchema;
//!
//! #[derive(Deserialize, ToSchema)]
//! struct Input {
//!     title: String,
//! }
//!
//! async fn create(ValidJson(input): ValidJson<Input>) -> ApiResult<String> {
//!     Ok(input.title)
//! }
//!
//! let router: Router = Router::new().route("/items", post(create));
//! ```

mod extract;
mod layers;
mod problem;

pub use extract::{
    BAD_JSON, NOT_OBJECT, ONE_VALUE, ONLY_ONE_VALUE, Path, Query, TOO_LARGE, ValidJson, decode,
    page, required_fields,
};
pub use layers::{
    RequestId, SECURITY_HEADERS, X_REQUEST_ID, access_log, max_body, recover, request_id,
    security_headers, stack, valid_request_id,
};
pub use problem::{ApiError, ApiResult, FieldError, PROBLEM_JSON, Problem, status_text};
