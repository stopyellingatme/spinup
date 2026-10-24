//! Spike r0-07: the Rust kit for the reference API, with auth.
//!
//! R0.6 gave the app start, problem details, request IDs, the database pool,
//! and the `migrate` command. R0.7 adds `auth` (sessions, passwords, lockout,
//! the cross-origin check, the rate limit, password reset, and OIDC) and
//! `mail` (lettre), as the Go `kit/auth` and `kit/mail` packages.

pub mod app;
pub mod auth;
pub mod config;
pub mod db;
pub mod extract;
pub mod mail;
pub mod middleware;
pub mod openapi;
pub mod problem;

pub use app::App;
pub use config::Config;
pub use problem::{ApiError, FieldError, Problem};

/// The result type of a handler.
pub type ApiResult<T> = Result<T, ApiError>;
