//! Error model, exit codes, `Try:` handling, JSON errors, printer, and the `Prompter` trait (Go `internal/ui`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod error;
pub mod exit;
pub mod printer;
pub mod prompt;
