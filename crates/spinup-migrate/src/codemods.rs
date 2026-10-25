//! The codemods, one module each (Go `internal/migrate/0001_*.go`).
//!
//! Each module registers its codemod in [`crate::contract::MIGRATIONS`].

pub mod web_svmigrate;
