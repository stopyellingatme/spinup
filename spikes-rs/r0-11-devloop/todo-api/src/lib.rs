//! Spike r0-11: the R0.12 full-kit app (the auth stack of R0.7, and the job
//! queue and telemetry of R0.8) with 20 resources from the R0.10 generator.
//! The app template writes this file. `gen resource` writes `handlers/`,
//! `models/`, `routes_gen.rs`, and `migrations/`.

use axum::Router;
use kit::Config;
use kit::auth::reset::ResetOptions;
use kit::auth::{Auth, AuthError, Options};
use sqlx::PgPool;
use sqlx::migrate::Migrator;
use utoipa::OpenApi;
use utoipa_axum::router::OpenApiRouter;

pub mod handlers;
#[cfg(feature = "jobs")]
pub mod jobs;
pub mod models;
mod routes_gen;

/// The app migrations in `migrations/`, embedded in the binary.
pub static MIGRATOR: Migrator = sqlx::migrate!();

/// The root of the OpenAPI document. The operations and their schemas come
/// from the `#[utoipa::path]` handlers.
#[derive(OpenApi)]
#[openapi(info(title = "todo", version = "0.1.0"), servers((url = "/")))]
pub struct ApiDoc;

/// The API router with its spec. The resources nest under `/api`.
pub fn api() -> OpenApiRouter<PgPool> {
    OpenApiRouter::with_openapi(ApiDoc::openapi()).nest("/api", routes_gen::routes())
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

/// The auth settings from the configuration and the environment: the
/// cookie is Secure unless `SPINUP_ENV=dev`, and `PUBLIC_ORIGIN` (the web
/// app) may send unsafe requests. `AUTH_DEFER_DUMMY_HASH=1` computes the
/// startup dummy hash after the start (R0.11 measures both).
pub fn auth_options(config: &Config) -> Options {
    let mut o = Options {
        secure: config.env != "dev",
        ..Options::default()
    };
    o.defer_dummy_hash = (o.lookup)("AUTH_DEFER_DUMMY_HASH").as_deref() == Some("1");
    if let Some(origin) = (o.lookup)("PUBLIC_ORIGIN").filter(|s| !s.is_empty()) {
        o.origins.push(origin);
    }
    o
}

/// The API router with auth: the todo routes, the auth routes, the OIDC
/// routes, and (with a mailer) the reset routes, inside the auth layer.
pub fn router_with_auth(
    pool: PgPool,
    auth: &Auth,
    reset: Option<ResetOptions>,
) -> Result<Router, AuthError> {
    let mut r = router(pool).merge(auth.routes()).merge(auth.oidc_routes());
    if let Some(o) = reset {
        r = r.merge(auth.reset_routes(o)?);
    }
    Ok(auth.layer(r))
}
