//! The coupon handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::coupon::{Coupon, CouponInput, CouponList, CouponRow};

/// The page parameters of `listCoupons`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of coupons to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of coupons to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_coupons, create_coupon))
        .routes(routes!(get_coupon, update_coupon, delete_coupon))
}

#[utoipa::path(
    get,
    path = "/coupons",
    operation_id = "listCoupons",
    summary = "List coupons",
    tag = "coupons",
    params(ListParams),
    responses(
        (status = 200, description = "A page of coupons in id order.", body = CouponList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_coupons(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<CouponList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        CouponRow,
        "SELECT id, code, discount, expires, created_at, updated_at
         FROM coupons ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM coupons"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Coupon::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(CouponList { items, total }))
}

#[utoipa::path(
    post,
    path = "/coupons",
    operation_id = "createCoupon",
    summary = "Create a coupon",
    tag = "coupons",
    request_body = CouponInput,
    responses(
        (status = 201, description = "The new coupon.", body = Coupon),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_coupon(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<CouponInput>,
) -> ApiResult<(StatusCode, Json<Coupon>)> {
    let row = sqlx::query_as!(
        CouponRow,
        "INSERT INTO coupons (code, discount, expires)
         VALUES ($1, $2, $3)
         RETURNING id, code, discount, expires, created_at, updated_at",
        input.code,
        input.discount,
        input.expires
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/coupons/{id}",
    operation_id = "getCoupon",
    summary = "Get a coupon",
    tag = "coupons",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The coupon.", body = Coupon),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The coupon does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_coupon(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Coupon>> {
    let row = sqlx::query_as!(
        CouponRow,
        "SELECT id, code, discount, expires, created_at, updated_at
         FROM coupons WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/coupons/{id}",
    operation_id = "updateCoupon",
    summary = "Replace a coupon",
    tag = "coupons",
    params(("id" = i64, Path)),
    request_body = CouponInput,
    responses(
        (status = 200, description = "The updated coupon.", body = Coupon),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The coupon does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_coupon(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<CouponInput>,
) -> ApiResult<Json<Coupon>> {
    let row = sqlx::query_as!(
        CouponRow,
        "UPDATE coupons
         SET code = $2, discount = $3, expires = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, code, discount, expires, created_at, updated_at",
        id,
        input.code,
        input.discount,
        input.expires
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/coupons/{id}",
    operation_id = "deleteCoupon",
    summary = "Delete a coupon",
    tag = "coupons",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The coupon is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The coupon does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_coupon(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM coupons WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
