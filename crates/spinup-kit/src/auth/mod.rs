//! Sessions, passwords, the rate limit, password reset, and OIDC (Go kit/auth).
//! The module list is a shared file (main loop only). R2.5 puts `Auth`, `Options`,
//! `AuthUser`, and `User` in `required`. R2.6 puts `AuthError` in `error`.

pub mod clientip;
pub mod error;
pub mod password;
pub mod ratelimit;
pub mod required;
pub mod routes;
pub mod store;

#[cfg(feature = "oidc")]
pub mod oidc;
#[cfg(feature = "mail")]
pub mod reset;

pub use required::*;
