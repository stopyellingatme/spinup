//! Spike r0-06: the hand-written Rust todo API. It has the contract of the
//! Go `gen-resource` golden app: the same routes, operationIds, schema
//! names, status codes, pagination, and validation.

use axum::Router;
use sqlx::PgPool;
use sqlx::migrate::Migrator;
use utoipa::OpenApi;
use utoipa_axum::router::OpenApiRouter;

pub mod todos;

/// The app migrations in `migrations/`, embedded in the binary.
pub static MIGRATOR: Migrator = sqlx::migrate!();

/// The root of the OpenAPI document. The operations and their schemas come
/// from the `#[utoipa::path]` handlers.
#[derive(OpenApi)]
#[openapi(info(title = "todo", version = "0.1.0"), servers((url = "/")))]
pub struct ApiDoc;

/// The API router with its spec. Each resource nests under `/api`.
pub fn api() -> OpenApiRouter<PgPool> {
    OpenApiRouter::with_openapi(ApiDoc::openapi()).nest("/api", todos::routes())
}

/// The OpenAPI 3.1 document of the API.
pub fn openapi() -> utoipa::openapi::OpenApi {
    let mut doc = api().into_openapi();
    // utoipa fills these from the Cargo package. The Go spec has neither.
    doc.info.description = None;
    doc.info.license = None;
    kit::openapi::hoist_path_params(&mut doc);
    doc
}

/// The API routes with the pool as state.
pub fn router(pool: PgPool) -> Router {
    let (router, _) = api().split_for_parts();
    router.with_state(pool)
}
