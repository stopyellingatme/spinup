//! The upgrade engine: planner, chain, state machine, worktree, and module add (Go `internal/upgrade`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod cargo;
pub mod chain;
pub mod contract;
pub mod lock;
pub mod module;
pub mod plan;
pub mod run;
pub mod source;
pub mod state;
pub mod ui;
pub mod worktree;
