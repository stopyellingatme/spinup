//! The OpenAPI document. spinup owns this file.

use kit::db::PgPool;
use utoipa::OpenApi;
use utoipa_axum::router::OpenApiRouter;

/// The document root. Resource handlers supply the operations and schemas.
#[derive(OpenApi)]
#[openapi(info(title = "app", version = "0.1.0"), servers((url = "/")))]
pub struct ApiDoc;

/// Returns the generated routes and their document under `/api`.
pub fn api() -> OpenApiRouter<PgPool> {
    OpenApiRouter::with_openapi(ApiDoc::openapi()).nest("/api", crate::routes_gen::routes())
}

/// Returns the OpenAPI 3.1 document without Cargo metadata.
pub fn openapi() -> utoipa::openapi::OpenApi {
    let mut doc = api().into_openapi();
    doc.info.description = None;
    doc.info.license = None;
    kit::app::hoist_path_params(&mut doc);
    doc
}
