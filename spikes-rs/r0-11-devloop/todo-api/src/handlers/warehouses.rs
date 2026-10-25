//! The warehouse handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::warehouse::{Warehouse, WarehouseInput, WarehouseList, WarehouseRow};

/// The page parameters of `listWarehouses`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of warehouses to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of warehouses to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_warehouses, create_warehouse))
        .routes(routes!(get_warehouse, update_warehouse, delete_warehouse))
}

#[utoipa::path(
    get,
    path = "/warehouses",
    operation_id = "listWarehouses",
    summary = "List warehouses",
    tag = "warehouses",
    params(ListParams),
    responses(
        (status = 200, description = "A page of warehouses in id order.", body = WarehouseList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_warehouses(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<WarehouseList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        WarehouseRow,
        "SELECT id, name, capacity, created_at, updated_at
         FROM warehouses ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM warehouses"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Warehouse::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(WarehouseList { items, total }))
}

#[utoipa::path(
    post,
    path = "/warehouses",
    operation_id = "createWarehouse",
    summary = "Create a warehouse",
    tag = "warehouses",
    request_body = WarehouseInput,
    responses(
        (status = 201, description = "The new warehouse.", body = Warehouse),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_warehouse(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<WarehouseInput>,
) -> ApiResult<(StatusCode, Json<Warehouse>)> {
    let row = sqlx::query_as!(
        WarehouseRow,
        "INSERT INTO warehouses (name, capacity)
         VALUES ($1, $2)
         RETURNING id, name, capacity, created_at, updated_at",
        input.name,
        input.capacity
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/warehouses/{id}",
    operation_id = "getWarehouse",
    summary = "Get a warehouse",
    tag = "warehouses",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The warehouse.", body = Warehouse),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The warehouse does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_warehouse(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
) -> ApiResult<Json<Warehouse>> {
    let row = sqlx::query_as!(
        WarehouseRow,
        "SELECT id, name, capacity, created_at, updated_at
         FROM warehouses WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/warehouses/{id}",
    operation_id = "updateWarehouse",
    summary = "Replace a warehouse",
    tag = "warehouses",
    params(("id" = i64, Path)),
    request_body = WarehouseInput,
    responses(
        (status = 200, description = "The updated warehouse.", body = Warehouse),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The warehouse does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_warehouse(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<WarehouseInput>,
) -> ApiResult<Json<Warehouse>> {
    let row = sqlx::query_as!(
        WarehouseRow,
        "UPDATE warehouses
         SET name = $2, capacity = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, name, capacity, created_at, updated_at",
        id,
        input.name,
        input.capacity
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/warehouses/{id}",
    operation_id = "deleteWarehouse",
    summary = "Delete a warehouse",
    tag = "warehouses",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The warehouse is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The warehouse does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_warehouse(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM warehouses WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
