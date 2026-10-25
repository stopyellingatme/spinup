//! The supplier handlers. `spinup g resource` wrote this file. You may edit
//! it.

use axum::Json;
use axum::extract::State;
use axum::http::StatusCode;
use kit::extract::{self, Path, Query, page};
use kit::{ApiError, ApiResult, Problem};
use serde::Deserialize;
use sqlx::PgPool;
use utoipa::IntoParams;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;

use crate::models::supplier::{Supplier, SupplierInput, SupplierList, SupplierRow};

/// The page parameters of `listSuppliers`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of suppliers to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of suppliers to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_suppliers, create_supplier))
        .routes(routes!(get_supplier, update_supplier, delete_supplier))
}

#[utoipa::path(
    get,
    path = "/suppliers",
    operation_id = "listSuppliers",
    summary = "List suppliers",
    tag = "suppliers",
    params(ListParams),
    responses(
        (status = 200, description = "A page of suppliers in id order.", body = SupplierList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_suppliers(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<SupplierList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        SupplierRow,
        "SELECT id, name, country, created_at, updated_at
         FROM suppliers ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM suppliers"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Supplier::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(SupplierList { items, total }))
}

#[utoipa::path(
    post,
    path = "/suppliers",
    operation_id = "createSupplier",
    summary = "Create a supplier",
    tag = "suppliers",
    request_body = SupplierInput,
    responses(
        (status = 201, description = "The new supplier.", body = Supplier),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_supplier(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<SupplierInput>,
) -> ApiResult<(StatusCode, Json<Supplier>)> {
    let row = sqlx::query_as!(
        SupplierRow,
        "INSERT INTO suppliers (name, country)
         VALUES ($1, $2)
         RETURNING id, name, country, created_at, updated_at",
        input.name,
        input.country
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/suppliers/{id}",
    operation_id = "getSupplier",
    summary = "Get a supplier",
    tag = "suppliers",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The supplier.", body = Supplier),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The supplier does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_supplier(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Supplier>> {
    let row = sqlx::query_as!(
        SupplierRow,
        "SELECT id, name, country, created_at, updated_at
         FROM suppliers WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/suppliers/{id}",
    operation_id = "updateSupplier",
    summary = "Replace a supplier",
    tag = "suppliers",
    params(("id" = i64, Path)),
    request_body = SupplierInput,
    responses(
        (status = 200, description = "The updated supplier.", body = Supplier),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The supplier does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_supplier(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<SupplierInput>,
) -> ApiResult<Json<Supplier>> {
    let row = sqlx::query_as!(
        SupplierRow,
        "UPDATE suppliers
         SET name = $2, country = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, name, country, created_at, updated_at",
        id,
        input.name,
        input.country
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/suppliers/{id}",
    operation_id = "deleteSupplier",
    summary = "Delete a supplier",
    tag = "suppliers",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The supplier is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The supplier does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_supplier(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM suppliers WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
