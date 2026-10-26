//! The integration test binary of `spinup-packs` (one binary for each crate, rust-plan.md §1).
//!
//! The module list is a shared file. A unit adds only the files that its row names.

mod golden_api;
mod golden_api_openapi;
mod golden_auth;
mod golden_backups;
mod golden_base;
mod golden_ci;
mod golden_deploy;
mod golden_jobs;
mod golden_mail;
mod golden_monitoring;
mod golden_sample;
mod golden_secrets;
mod golden_tofu;
mod golden_web;
mod golden_web_embed;
mod golden_web_variants;
