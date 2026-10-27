//! Test helpers for apps and for the kit (Go kit/testkit, kit/auth/authtest). Tests that
//! need Postgres read DATABASE_URL and fail with a Try: line without it.
//! The module list is a shared file (main loop only).

pub mod db;
pub mod http;
pub mod template;

#[cfg(feature = "auth")]
pub mod auth;
