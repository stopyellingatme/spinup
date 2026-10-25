//! The minijinja environment, the template functions, answers validation, and the modules catalog (Go `internal/render`, `internal/modules`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod answers;
pub mod env;
pub mod functions;
pub mod modules;
pub mod render;
