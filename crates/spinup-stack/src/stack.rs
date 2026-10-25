//! The stack versions model, schema 2 (ADR 0020 §3).
//!
//! Stub from the R1 pre-seed. Unit R1.5 owns this module and replaces this stub.
//!
//! Seam (ADR 0020 §1): the contract files name [`Stack`]. R1.5 keeps the path
//! `spinup_stack::stack::Stack` and the derives `Debug` and `Clone`.

/// The pinned stack versions that ship with one template version.
#[derive(Debug, Clone, Default)]
pub struct Stack {}
