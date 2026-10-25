//! The write plan, `clean_path`, apply with backups and rollback, patience diff, and binary detection (Go `internal/vfs`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod apply;
pub mod binary;
pub mod diff;
pub mod path;
pub mod plan;
pub mod tree;
