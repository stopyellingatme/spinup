//! The doctor check registry, `composelint`, and `plainsecrets` (Go `internal/doctor`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod composelint;
pub mod plainsecrets;
pub mod registry;
