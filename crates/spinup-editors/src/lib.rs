//! Comment-keeping editors for YAML (tree-sitter source spans), TOML, and JSON (Go `internal/codemod`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod json;
pub mod toml;
pub mod yaml;
