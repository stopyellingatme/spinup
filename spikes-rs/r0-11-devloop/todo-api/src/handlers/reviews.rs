//! The review handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::review::{Review, ReviewInput, ReviewList, ReviewRow};

/// The page parameters of `listReviews`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of reviews to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of reviews to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_reviews, create_review))
        .routes(routes!(get_review, update_review, delete_review))
}

#[utoipa::path(
    get,
    path = "/reviews",
    operation_id = "listReviews",
    summary = "List reviews",
    tag = "reviews",
    params(ListParams),
    responses(
        (status = 200, description = "A page of reviews in id order.", body = ReviewList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_reviews(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<ReviewList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        ReviewRow,
        "SELECT id, rating, body, created_at, updated_at
         FROM reviews ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM reviews"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Review::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(ReviewList { items, total }))
}

#[utoipa::path(
    post,
    path = "/reviews",
    operation_id = "createReview",
    summary = "Create a review",
    tag = "reviews",
    request_body = ReviewInput,
    responses(
        (status = 201, description = "The new review.", body = Review),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_review(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<ReviewInput>,
) -> ApiResult<(StatusCode, Json<Review>)> {
    let row = sqlx::query_as!(
        ReviewRow,
        "INSERT INTO reviews (rating, body)
         VALUES ($1, $2)
         RETURNING id, rating, body, created_at, updated_at",
        input.rating,
        input.body
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/reviews/{id}",
    operation_id = "getReview",
    summary = "Get a review",
    tag = "reviews",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The review.", body = Review),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The review does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_review(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Review>> {
    let row = sqlx::query_as!(
        ReviewRow,
        "SELECT id, rating, body, created_at, updated_at
         FROM reviews WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/reviews/{id}",
    operation_id = "updateReview",
    summary = "Replace a review",
    tag = "reviews",
    params(("id" = i64, Path)),
    request_body = ReviewInput,
    responses(
        (status = 200, description = "The updated review.", body = Review),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The review does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_review(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<ReviewInput>,
) -> ApiResult<Json<Review>> {
    let row = sqlx::query_as!(
        ReviewRow,
        "UPDATE reviews
         SET rating = $2, body = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, rating, body, created_at, updated_at",
        id,
        input.rating,
        input.body
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/reviews/{id}",
    operation_id = "deleteReview",
    summary = "Delete a review",
    tag = "reviews",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The review is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The review does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_review(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM reviews WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
