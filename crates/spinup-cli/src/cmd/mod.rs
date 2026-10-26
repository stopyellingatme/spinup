//! The commands, one file each (Go `internal/cli`).
//!
//! The module list is a shared file (ADR 0020 §10). Units R1.22, R1.23, R2.23, and R2.24 own
//! the command files, not this list. A command registers itself in the command slice of `root` (unit
//! R1.21), so no unit edits a central list.

pub mod build;
pub mod check;
pub mod db;
pub mod destroy;
pub mod diff;
pub mod eject;
pub mod generate;
pub mod module;
pub mod new;
pub mod restore;
pub mod routes;
pub mod test;
pub mod upgrade;
