//! The R1 commands, one file each (Go `internal/cli`).
//!
//! The module list is a shared file (ADR 0020 §10). Units R1.22 and R1.23 own the command
//! files, not this list. A command registers itself in the command slice of `root` (unit
//! R1.21), so no unit edits a central list.

pub mod check;
pub mod diff;
pub mod eject;
pub mod generate;
pub mod module;
pub mod new;
pub mod restore;
pub mod upgrade;
