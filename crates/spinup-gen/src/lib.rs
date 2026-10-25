//! The generator contract, registry, runner, and picker (Go `internal/gen`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod contract;
pub mod picker;
pub mod registry;
pub mod run;
