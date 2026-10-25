//! Spike r0-12: the full Rust kit of the reference API, for the size and
//! start baseline.
//!
//! R0.6 gave the app start, problem details, request IDs, the database pool,
//! and the `migrate` command. R0.7 added `auth` and `mail`. R0.8 added the
//! job queue (feature `jobs`) and OTLP telemetry (feature `telemetry`). This
//! crate joins the three spikes without other changes.

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
