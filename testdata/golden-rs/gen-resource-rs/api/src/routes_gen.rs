//! The routes of generated resources. spinup owns this file.

use kit::db::PgPool;
use utoipa_axum::router::OpenApiRouter;

use crate::handlers;

/// Merges the routes of each resource in name order.
pub fn routes() -> OpenApiRouter<PgPool> {
    let mut router = OpenApiRouter::new();
    router = router.merge(handlers::todos::routes());
    router
}
