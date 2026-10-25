//! The comment handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::comment::{Comment, CommentInput, CommentList, CommentRow};

/// The page parameters of `listComments`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of comments to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of comments to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_comments, create_comment))
        .routes(routes!(get_comment, update_comment, delete_comment))
}

#[utoipa::path(
    get,
    path = "/comments",
    operation_id = "listComments",
    summary = "List comments",
    tag = "comments",
    params(ListParams),
    responses(
        (status = 200, description = "A page of comments in id order.", body = CommentList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_comments(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<CommentList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        CommentRow,
        "SELECT id, body, author, created_at, updated_at
         FROM comments ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM comments"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Comment::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(CommentList { items, total }))
}

#[utoipa::path(
    post,
    path = "/comments",
    operation_id = "createComment",
    summary = "Create a comment",
    tag = "comments",
    request_body = CommentInput,
    responses(
        (status = 201, description = "The new comment.", body = Comment),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_comment(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<CommentInput>,
) -> ApiResult<(StatusCode, Json<Comment>)> {
    let row = sqlx::query_as!(
        CommentRow,
        "INSERT INTO comments (body, author)
         VALUES ($1, $2)
         RETURNING id, body, author, created_at, updated_at",
        input.body,
        input.author
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/comments/{id}",
    operation_id = "getComment",
    summary = "Get a comment",
    tag = "comments",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The comment.", body = Comment),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The comment does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_comment(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Comment>> {
    let row = sqlx::query_as!(
        CommentRow,
        "SELECT id, body, author, created_at, updated_at
         FROM comments WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/comments/{id}",
    operation_id = "updateComment",
    summary = "Replace a comment",
    tag = "comments",
    params(("id" = i64, Path)),
    request_body = CommentInput,
    responses(
        (status = 200, description = "The updated comment.", body = Comment),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The comment does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_comment(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<CommentInput>,
) -> ApiResult<Json<Comment>> {
    let row = sqlx::query_as!(
        CommentRow,
        "UPDATE comments
         SET body = $2, author = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, body, author, created_at, updated_at",
        id,
        input.body,
        input.author
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/comments/{id}",
    operation_id = "deleteComment",
    summary = "Delete a comment",
    tag = "comments",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The comment is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The comment does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_comment(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM comments WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
