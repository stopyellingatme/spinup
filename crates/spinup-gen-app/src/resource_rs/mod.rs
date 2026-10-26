//! The Rust API files of a resource: the handlers, the model types and queries, and the
//! handler tests (Go `planAPI`, `internal/gen/resource/resource.go:298-350`).
//!
//! Stub from the R2 pre-seed. Unit R2.14 owns this module. It keeps the signature of
//! [`plan_api`], because R2.13 calls it (ADR 0021 §1).

use spinup_gen::part::Spec;
use spinup_vfs::plan::Plan;

/// Plans the Rust API files of `spec`. `tests` is false with `--no-tests`.
///
/// # Errors
/// Returns an error with a `Try:` line when a template fails. The stub returns an empty plan.
pub fn plan_api(spec: &Spec<'_>, tests: bool) -> anyhow::Result<Plan> {
    let _ = (spec, tests);
    Ok(Plan::default())
}
