//! The web generators: the resource `pages` part, `nav.gen.ts`, `page`, `component`,
//! `client`, `dashboard`, and `alert` (Go `internal/gen/{page,component,client,dashboard,alert}`
//! and the pages and navigation of `internal/gen/resource`).
//!
//! The module list is a shared file (main loop only). Unit R2.12 owns the modules.

pub mod alert;
pub mod client;
pub mod component;
pub mod dashboard;
pub mod nav;
pub mod page;
pub mod pages;
