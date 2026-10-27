//! The API of app.

use axum::Router;
use kit::db::PgPool;
use sqlx::migrate::Migrator;

pub mod handlers;
#[cfg(feature = "jobs")]
pub mod jobs;
#[cfg(feature = "mail")]
pub mod mail;
pub mod models;
pub mod openapi;
#[cfg(all(feature = "auth", feature = "mail"))]
pub mod reset;
pub mod routes;
pub mod routes_gen;

/// The app migrations, embedded from `migrations/`.
pub static MIGRATOR: Migrator = sqlx::migrate!();

/// Returns the API routes with the pool as state.
pub fn router(pool: PgPool) -> Router {
    let (generated, _) = openapi::api().split_for_parts();
    generated.merge(routes::routes()).with_state(pool)
}
