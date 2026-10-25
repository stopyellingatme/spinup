//! The cargo phases of an upgrade step: `Cargo.toml` edits, the kit patch, `rust-version`,
//! and verify (unit R2.10).
//!
//! Stub from the R1 pre-seed. Unit R2.10 owns this module and replaces this stub. R1 has no
//! cargo phases: the `fix` slot and the other slots stay empty.

use crate::contract::Phases;

/// Returns the step phases. The state machine (unit R1.18) calls this function.
pub fn phases() -> Phases {
    Phases::default()
}
