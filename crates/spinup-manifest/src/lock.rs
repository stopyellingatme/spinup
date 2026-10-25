//! The `spinup.lock` model, schema 3 (ADR 0020 §2).
//!
//! Stub from the R1 pre-seed. Unit R1.4 owns this module and replaces this stub.
//!
//! Seam (ADR 0020 §1): the contract files name [`Lock`] and [`Class`]. R1.4 keeps the paths
//! `spinup_manifest::lock::{Lock, Class}`, the derives `Debug` and `Clone` on both, and the
//! three `Class` variants with their lock names.

use serde::{Deserialize, Serialize};

/// The schema that the Rust CLI writes. It rejects schema 1 and 2 (Go-era) locks.
pub const SCHEMA_VERSION: u32 = 3;

/// How an upgrade treats a generated file.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Class {
    /// The CLI maintains the file.
    Managed,
    /// An upgrade merges the file with the user's changes.
    Scaffold,
    /// The CLI writes the file once and never compares it again.
    Seed,
}

/// The inputs and the file state that reproduce and upgrade an app.
#[derive(Debug, Clone, Default)]
pub struct Lock {}
