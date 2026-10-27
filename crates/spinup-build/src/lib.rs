//! `spinup build`: the local release build of the API and the kit build context
//! (Go `internal/build`). R3a adds `image`.
//!
//! The module list is a shared file (main loop only). Unit R2.16 owns the modules.

pub mod kit;
pub mod local;
