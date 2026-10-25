//! The routes of the generated resources. spinup owns this file: `spinup g
//! resource` writes one line per resource, sorted.

use sqlx::PgPool;
use utoipa_axum::router::OpenApiRouter;

use crate::handlers;

/// Merges the routes of each resource. Each line is independent of the
/// others, so the layout does not depend on the number of resources.
pub fn routes() -> OpenApiRouter<PgPool> {
    let mut router = OpenApiRouter::new();
    router = router.merge(handlers::articles::routes());
    router = router.merge(handlers::categories::routes());
    router = router.merge(handlers::comments::routes());
    router = router.merge(handlers::coupons::routes());
    router = router.merge(handlers::customers::routes());
    router = router.merge(handlers::employees::routes());
    router = router.merge(handlers::events::routes());
    router = router.merge(handlers::invoices::routes());
    router = router.merge(handlers::meetings::routes());
    router = router.merge(handlers::notes::routes());
    router = router.merge(handlers::orders::routes());
    router = router.merge(handlers::products::routes());
    router = router.merge(handlers::projects::routes());
    router = router.merge(handlers::reviews::routes());
    router = router.merge(handlers::shipments::routes());
    router = router.merge(handlers::suppliers::routes());
    router = router.merge(handlers::tags::routes());
    router = router.merge(handlers::tickets::routes());
    router = router.merge(handlers::todos::routes());
    router = router.merge(handlers::warehouses::routes());
    router
}
