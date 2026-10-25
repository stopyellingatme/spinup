//! Drift: `diff`, `check`, `eject`, and `restore` (Go `internal/drift`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod check;
pub mod diff;
pub mod eject;
pub mod restore;
