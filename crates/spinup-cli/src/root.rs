//! The root command: global flags, command groups, the usage-error mapping to exit 2, the
//! signal handler, completions, and the update notice.
//!
//! Stub from the R1 pre-seed. Unit R1.21 owns this file and replaces this stub.
//!
//! Seam (ADR 0020 §5): `crates/spinup/src/main.rs` calls [`execute`]. R1.21 keeps its path
//! and signature. R1.21 also defines the linkme slice of commands here. Each file in
//! `cmd/` registers its command into that slice.

use std::ffi::OsString;
use std::process::ExitCode;

/// Runs the CLI with the process arguments (the program name first) and returns the exit
/// code (Go `cli.Execute`).
pub fn execute(args: Vec<OsString>) -> ExitCode {
    let _ = args;
    eprintln!(
        "Error: the Rust spinup CLI has no commands yet\n  Try: use the Go build (make install)"
    );
    ExitCode::from(1)
}
