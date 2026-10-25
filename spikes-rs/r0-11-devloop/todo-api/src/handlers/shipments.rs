//! The shipment handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::shipment::{Shipment, ShipmentInput, ShipmentList, ShipmentRow};

/// The page parameters of `listShipments`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of shipments to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of shipments to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_shipments, create_shipment))
        .routes(routes!(get_shipment, update_shipment, delete_shipment))
}

#[utoipa::path(
    get,
    path = "/shipments",
    operation_id = "listShipments",
    summary = "List shipments",
    tag = "shipments",
    params(ListParams),
    responses(
        (status = 200, description = "A page of shipments in id order.", body = ShipmentList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_shipments(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<ShipmentList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        ShipmentRow,
        "SELECT id, tracking, shipped_at, created_at, updated_at
         FROM shipments ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM shipments"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Shipment::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(ShipmentList { items, total }))
}

#[utoipa::path(
    post,
    path = "/shipments",
    operation_id = "createShipment",
    summary = "Create a shipment",
    tag = "shipments",
    request_body = ShipmentInput,
    responses(
        (status = 201, description = "The new shipment.", body = Shipment),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_shipment(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<ShipmentInput>,
) -> ApiResult<(StatusCode, Json<Shipment>)> {
    let row = sqlx::query_as!(
        ShipmentRow,
        "INSERT INTO shipments (tracking, shipped_at)
         VALUES ($1, $2)
         RETURNING id, tracking, shipped_at, created_at, updated_at",
        input.tracking,
        input.shipped_at
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/shipments/{id}",
    operation_id = "getShipment",
    summary = "Get a shipment",
    tag = "shipments",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The shipment.", body = Shipment),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The shipment does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_shipment(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Shipment>> {
    let row = sqlx::query_as!(
        ShipmentRow,
        "SELECT id, tracking, shipped_at, created_at, updated_at
         FROM shipments WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/shipments/{id}",
    operation_id = "updateShipment",
    summary = "Replace a shipment",
    tag = "shipments",
    params(("id" = i64, Path)),
    request_body = ShipmentInput,
    responses(
        (status = 200, description = "The updated shipment.", body = Shipment),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The shipment does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_shipment(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<ShipmentInput>,
) -> ApiResult<Json<Shipment>> {
    let row = sqlx::query_as!(
        ShipmentRow,
        "UPDATE shipments
         SET tracking = $2, shipped_at = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, tracking, shipped_at, created_at, updated_at",
        id,
        input.tracking,
        input.shipped_at
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/shipments/{id}",
    operation_id = "deleteShipment",
    summary = "Delete a shipment",
    tag = "shipments",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The shipment is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The shipment does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_shipment(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM shipments WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
