//! The phases of an upgrade step: codemod migrations, the kit patch, `Cargo.toml` edits,
//! `rust-version`, and verify (unit R2.10, ADR 0021 §8).
//!
//! Stub from the R2 pre-seed. Unit R2.10 owns this module. It keeps the signatures of
//! [`phases`] and [`kept_manifest`].

use crate::contract::Phases;

/// Returns the step phases. The state machine (unit R1.18) calls this function.
pub fn phases() -> Phases {
    Phases::default()
}

/// Returns the reason why the planner keeps a dependency manifest out of the text merge,
/// or `None`. The dependency phase applies the template changes to these files. The plan
/// keeps the new render of a kept manifest for that phase.
///
/// The stub keeps the Go module files of R1. R2.10 adds `api/Cargo.toml` and `api/Cargo.lock`.
pub fn kept_manifest(path: &str) -> Option<&'static str> {
    match path {
        "api/go.mod" | "api/go.sum" => Some(crate::plan::GO_MOD_KEEP_REASON),
        _ => None,
    }
}
