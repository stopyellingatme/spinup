//! spinup-kit: the runtime library of the apps that spinup generates (ADR 0021 §2).
//!
//! It replaces the Go packages kit/app, kit/config, kit/secrets, kit/httpx, kit/logx,
//! kit/deployx, kit/db, kit/telemetry, kit/apidocs, kit/devtools, kit/auth,
//! kit/auth/authtest, kit/mail, kit/jobs, and kit/testkit. The module list is a shared
//! file (main loop only).

pub mod app;
pub mod config;
pub mod deployx;
pub mod httpx;
pub mod logx;
pub mod secrets;

#[cfg(feature = "apidocs")]
pub mod apidocs;
#[cfg(feature = "auth")]
pub mod auth;
#[cfg(feature = "db")]
pub mod db;
#[cfg(feature = "devtools")]
pub mod devtools;
#[cfg(feature = "jobs")]
pub mod jobs;
#[cfg(feature = "mail")]
pub mod mail;
#[cfg(feature = "embedweb")]
pub mod spa;
#[cfg(feature = "telemetry")]
pub mod telemetry;
#[cfg(feature = "testkit")]
pub mod testkit;
