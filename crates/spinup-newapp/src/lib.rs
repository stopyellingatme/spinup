//! `spinup new`: pack composition, questions, lock writing, the plugin seam, `git init`, and the kit replace (Go `internal/gen/newapp`).
//!
//! The module list is a shared file (ADR 0020 §10). Units own the modules, not this list.

pub mod git;
pub mod kit;
pub mod lockfile;
pub mod plugins;
pub mod questions;
pub mod select;
