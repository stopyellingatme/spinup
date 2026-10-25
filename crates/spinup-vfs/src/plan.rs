//! The write plan: the files that a render or a generator wants on disk.
//!
//! Stub from the R1 pre-seed. Unit R1.3 owns this module and replaces this stub.
//!
//! Seam (ADR 0020 §1): the contract files name [`Plan`]. R1.3 keeps the path
//! `spinup_vfs::plan::Plan` and the derives `Debug`, `Clone`, and `Default`.

/// The files to write, each with its class. Only the apply step writes them to disk.
#[derive(Debug, Clone, Default)]
pub struct Plan {}
