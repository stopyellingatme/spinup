//! The only crate that runs subprocesses: the `Runner` contract, `OsRunner`, process groups, cancel, redaction, and `Fake` (Go `internal/exec`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod fake;
pub mod lookup;
pub mod os_runner;
pub mod process;
pub mod redact;
pub mod runner;
