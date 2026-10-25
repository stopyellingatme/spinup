//! The command tree of the spinup CLI. It holds wiring only (Go `internal/cli`, `internal/docsite`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod cache;
pub mod cmd;
pub mod docs;
pub mod registry;
pub mod root;
pub mod version;
