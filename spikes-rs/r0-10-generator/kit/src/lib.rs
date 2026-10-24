//! Spike r0-06: the Rust kit for the reference API.
//!
//! It mirrors the Go `kit/app`, `kit/httpx`, and `kit/db` packages: the app
//! start with configuration from the environment, problem details (RFC 9457),
//! request IDs and request logging, `/healthz` and `/readyz`, graceful
//! shutdown, the database pool, and the `migrate` command.

pub mod app;
pub mod config;
pub mod db;
pub mod extract;
pub mod middleware;
pub mod openapi;
pub mod problem;

pub use app::App;
pub use config::Config;
pub use problem::{ApiError, FieldError, Problem};

/// The result type of a handler.
pub type ApiResult<T> = Result<T, ApiError>;
