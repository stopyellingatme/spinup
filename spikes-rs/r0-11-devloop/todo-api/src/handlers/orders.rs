//! The order handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::order::{Order, OrderInput, OrderList, OrderRow};

/// The page parameters of `listOrders`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of orders to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of orders to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_orders, create_order))
        .routes(routes!(get_order, update_order, delete_order))
}

#[utoipa::path(
    get,
    path = "/orders",
    operation_id = "listOrders",
    summary = "List orders",
    tag = "orders",
    params(ListParams),
    responses(
        (status = 200, description = "A page of orders in id order.", body = OrderList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_orders(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<OrderList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        OrderRow,
        "SELECT id, reference, total, status, created_at, updated_at
         FROM orders ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM orders"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Order::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(OrderList { items, total }))
}

#[utoipa::path(
    post,
    path = "/orders",
    operation_id = "createOrder",
    summary = "Create an order",
    tag = "orders",
    request_body = OrderInput,
    responses(
        (status = 201, description = "The new order.", body = Order),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_order(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<OrderInput>,
) -> ApiResult<(StatusCode, Json<Order>)> {
    let row = sqlx::query_as!(
        OrderRow,
        "INSERT INTO orders (reference, total, status)
         VALUES ($1, $2, $3)
         RETURNING id, reference, total, status, created_at, updated_at",
        input.reference,
        input.total,
        input.status.as_str()
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/orders/{id}",
    operation_id = "getOrder",
    summary = "Get an order",
    tag = "orders",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The order.", body = Order),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The order does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_order(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Order>> {
    let row = sqlx::query_as!(
        OrderRow,
        "SELECT id, reference, total, status, created_at, updated_at
         FROM orders WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/orders/{id}",
    operation_id = "updateOrder",
    summary = "Replace an order",
    tag = "orders",
    params(("id" = i64, Path)),
    request_body = OrderInput,
    responses(
        (status = 200, description = "The updated order.", body = Order),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The order does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_order(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<OrderInput>,
) -> ApiResult<Json<Order>> {
    let row = sqlx::query_as!(
        OrderRow,
        "UPDATE orders
         SET reference = $2, total = $3, status = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, reference, total, status, created_at, updated_at",
        id,
        input.reference,
        input.total,
        input.status.as_str()
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/orders/{id}",
    operation_id = "deleteOrder",
    summary = "Delete an order",
    tag = "orders",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The order is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The order does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_order(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM orders WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
