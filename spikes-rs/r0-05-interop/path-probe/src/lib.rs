//! Path policy probe without the archive's C compiler dependency.
#[path = "../../src/paths.rs"]
mod implementation;
pub use implementation::*;
