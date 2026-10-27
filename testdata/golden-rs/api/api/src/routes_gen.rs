//! The routes of generated resources. spinup owns this file.

use kit::db::PgPool;
use utoipa_axum::router::OpenApiRouter;

/// Merges the routes of each resource in name order.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
}
