//! The pack loader: `pack.toml`, `pack.d`, the overlay merge, `when`, embedded packs, validation, and the engine marker (Go `internal/templates`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod embed;
pub mod engine;
pub mod loader;
pub mod overlay;
pub mod validate;
pub mod when;
