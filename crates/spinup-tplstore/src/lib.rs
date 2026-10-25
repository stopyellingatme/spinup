//! Template versions: archives, signatures, trusted keys, cache, limits, and `cache warm` (Go `internal/templates/store`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod archive;
pub mod cache;
pub mod keys;
pub mod source;
pub mod store;
pub mod warm;
