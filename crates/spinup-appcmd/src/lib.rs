//! App commands: `spinup routes` and `spinup test` (Go `internal/routes`, `internal/apptest`).
//! R3c adds `devloop`.
//!
//! The module list is a shared file (main loop only). Unit R2.17 owns the modules.

pub mod apptest;
pub mod routes;
