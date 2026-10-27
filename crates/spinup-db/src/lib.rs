//! The database commands of the CLI: migrations through sqlx-cli, migration names, the
//! migration lint, the compose operations, and `db prepare` (Go `internal/db`,
//! `internal/db/ops`, `internal/db/lint`; ADR 0021 §6).
//!
//! The module list is a shared file (main loop only). Unit R2.9 owns the modules.

pub mod lint;
pub mod migrate;
pub mod names;
pub mod ops;
pub mod prepare;
