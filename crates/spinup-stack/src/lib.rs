//! The stack versions, schema 2 (additive): loader, strict decode, and helpers (Go `internal/stackversions`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod load;
pub mod pins;
pub mod stack;
