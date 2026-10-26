//! The integration tests of spinup-kit (one binary, rust-plan.md §1). The module list is a
//! shared file. A test that needs Postgres has a name that starts with `pg_` (ADR 0021 §2).

mod apidocs;
mod app;
mod auth;
mod clientip;
mod config;
mod db;
mod deployx;
mod devtools;
mod httpx;
mod jobs;
mod logx;
mod mail;
mod oidc;
mod ratelimit;
mod reset;
mod secrets;
mod spa;
mod telemetry;
