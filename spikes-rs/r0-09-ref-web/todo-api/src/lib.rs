//! Spike r0-09: the Rust reference API behind the SvelteKit web pack. It
//! joins R0.6 (the API), R0.7 (auth), and R0.8 (jobs and OTel). The todo
//! routes have the contract of the Go app of `testdata/e2e/web/run.sh`:
//! `spinup new --modules ci,auth`, then `spinup g resource todo title:string
//! done:bool 'due:date?'`. The auth routes have the contract of ADR 0008 §3.

use axum::Router;
use kit::Config;
use kit::auth::reset::ResetOptions;
use kit::auth::{Auth, AuthError, Options};
use sqlx::PgPool;
use sqlx::migrate::Migrator;
use utoipa::OpenApi;
use utoipa_axum::router::OpenApiRouter;

#[cfg(feature = "jobs")]
pub mod jobs;
pub mod todos;

/// The app migrations in `migrations/`, embedded in the binary.
pub static MIGRATOR: Migrator = sqlx::migrate!();

/// The root of the OpenAPI document. The operations and their schemas come
/// from the `#[utoipa::path]` handlers.
#[derive(OpenApi)]
#[openapi(info(title = "demo", version = "0.1.0"), servers((url = "/")))]
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

/// The auth settings from the configuration and the environment: the
/// cookie is Secure unless `SPINUP_ENV=dev`, and `PUBLIC_ORIGIN` (the web
/// app) may send unsafe requests.
pub fn auth_options(config: &Config) -> Options {
    let mut o = Options {
        secure: config.env != "dev",
        ..Options::default()
    };
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

/// The API reference routes (`/api/docs`, `/api/openapi.yaml`). As in the Go
/// kit, the app mounts them in development or with `SPINUP_API_DOCS=true`.
pub fn api_docs() -> anyhow::Result<Router> {
    Ok(kit::apidocs::routes(
        openapi().to_yaml()?,
        "demo API reference",
    ))
}
