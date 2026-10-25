//! `spinup.lock` schema 3: canonical JSON, normalized hash, status, tombstones, orphans, and atomic save (Go `internal/manifest`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod hash;
pub mod json;
pub mod lock;
pub mod save;
pub mod status;
