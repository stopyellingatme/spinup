//! Codemods: the registry, `migratetest`, the kit notes parser, and codemod 0001 (Go `internal/migrate`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod codemods;
pub mod contract;
pub mod notes;
pub mod registry;
pub mod testkit;
