//! The three-way merge through `git merge-file` (Go `internal/merge`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod contract;
pub mod git;
