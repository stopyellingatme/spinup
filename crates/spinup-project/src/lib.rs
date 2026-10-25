//! The `spinup.toml` model, root lookup, `confirm_protected`, and the web tool helpers (Go `internal/project`, `internal/webtool`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod config;
pub mod confirm;
pub mod root;
pub mod webtool;
