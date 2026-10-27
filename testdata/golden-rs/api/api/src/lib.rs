//! The API of todo.

use axum::Router;
use kit::db::PgPool;
use sqlx::migrate::Migrator;

pub mod handlers;
pub mod models;
pub mod openapi;
pub mod routes;
pub mod routes_gen;

/// The app migrations, embedded from `migrations/`.
pub static MIGRATOR: Migrator = sqlx::migrate!();

/// Returns the API routes with the pool as state.
pub fn router(pool: PgPool) -> Router {
    let (generated, _) = openapi::api().split_for_parts();
    generated.merge(routes::routes()).with_state(pool)
}
