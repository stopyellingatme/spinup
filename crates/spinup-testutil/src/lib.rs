//! Golden helpers for the Rust golden tests: the `.modes` format, `repo_root`, and the file filters of `testdata/golden/rust-filter.toml` (Go `internal/testutil`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod filter;
pub mod golden;
pub mod modes;
pub mod repo;
