//! The routes of the generated resources. spinup owns this file: `spinup g
//! resource` writes one line per resource, sorted.

use sqlx::PgPool;
use utoipa_axum::router::OpenApiRouter;

use crate::handlers;

/// Merges the routes of each resource. Each line is independent of the
/// others, so the layout does not depend on the number of resources.
pub fn routes() -> OpenApiRouter<PgPool> {
    let mut router = OpenApiRouter::new();
    router = router.merge(handlers::notes::routes());
    router = router.merge(handlers::todos::routes());
    router
}
