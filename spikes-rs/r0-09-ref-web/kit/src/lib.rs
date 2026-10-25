//! Spike r0-09: the Rust kit for the reference web app.
//!
//! R0.6 gave the app start, problem details, request IDs, the database pool,
//! and the `migrate` command. R0.7 added `auth` and `mail`. R0.8 added
//! `jobs` (feature `jobs`) and `telemetry` (feature `telemetry`). R0.9 joins
//! the three and adds `apidocs` (the Scalar API reference of the Go
//! `kit/apidocs`), so the SvelteKit web pack and its Playwright suite run
//! against this kit.

pub mod apidocs;
pub mod app;
pub mod auth;
pub mod config;
pub mod db;
pub mod extract;
#[cfg(feature = "jobs")]
pub mod jobs;
pub mod mail;
pub mod middleware;
pub mod openapi;
pub mod problem;
#[cfg(feature = "telemetry")]
pub mod telemetry;

pub use app::App;
pub use config::Config;
pub use problem::{ApiError, FieldError, Problem};

/// The result type of a handler.
pub type ApiResult<T> = Result<T, ApiError>;
