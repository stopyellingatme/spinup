//! R0.5 format experiments. The Go implementation remains the oracle.
pub mod archive;
pub mod diff;
pub use r0_05_paths as paths;
mod lock;
pub use lock::canonical_lock;
