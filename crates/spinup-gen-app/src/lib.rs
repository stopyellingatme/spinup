//! The API generators: `resource`, `model`, `migration`, `handler`, `job`, `mailer`,
//! `openapi`, and the `destroy` library (Go `internal/gen/{resource,model,migration,handler,
//! job,mailer,openapi,destroy}`).
//!
//! The module list is a shared file (main loop only).

pub mod destroy;
pub mod handler;
pub mod job;
pub mod mailer;
pub mod migration;
pub mod model;
pub mod openapi;
pub mod resource_core;
pub mod resource_rs;
