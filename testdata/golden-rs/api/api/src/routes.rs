//! The application routes.

use axum::Router;
use kit::db::PgPool;

/// Registers the application routes.
pub fn routes() -> Router<PgPool> {
    // Add application routes here. Generators never edit this file.
    Router::new()
}
