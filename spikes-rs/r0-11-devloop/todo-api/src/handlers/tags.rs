//! The tag handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::tag::{Tag, TagInput, TagList, TagRow};

/// The page parameters of `listTags`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of tags to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of tags to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_tags, create_tag))
        .routes(routes!(get_tag, update_tag, delete_tag))
}

#[utoipa::path(
    get,
    path = "/tags",
    operation_id = "listTags",
    summary = "List tags",
    tag = "tags",
    params(ListParams),
    responses(
        (status = 200, description = "A page of tags in id order.", body = TagList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_tags(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<TagList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        TagRow,
        "SELECT id, name, color, created_at, updated_at
         FROM tags ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM tags"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Tag::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(TagList { items, total }))
}

#[utoipa::path(
    post,
    path = "/tags",
    operation_id = "createTag",
    summary = "Create a tag",
    tag = "tags",
    request_body = TagInput,
    responses(
        (status = 201, description = "The new tag.", body = Tag),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_tag(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<TagInput>,
) -> ApiResult<(StatusCode, Json<Tag>)> {
    let row = sqlx::query_as!(
        TagRow,
        "INSERT INTO tags (name, color)
         VALUES ($1, $2)
         RETURNING id, name, color, created_at, updated_at",
        input.name,
        input.color
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/tags/{id}",
    operation_id = "getTag",
    summary = "Get a tag",
    tag = "tags",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The tag.", body = Tag),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The tag does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_tag(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Tag>> {
    let row = sqlx::query_as!(
        TagRow,
        "SELECT id, name, color, created_at, updated_at
         FROM tags WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/tags/{id}",
    operation_id = "updateTag",
    summary = "Replace a tag",
    tag = "tags",
    params(("id" = i64, Path)),
    request_body = TagInput,
    responses(
        (status = 200, description = "The updated tag.", body = Tag),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The tag does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_tag(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<TagInput>,
) -> ApiResult<Json<Tag>> {
    let row = sqlx::query_as!(
        TagRow,
        "UPDATE tags
         SET name = $2, color = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, name, color, created_at, updated_at",
        id,
        input.name,
        input.color
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/tags/{id}",
    operation_id = "deleteTag",
    summary = "Delete a tag",
    tag = "tags",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The tag is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The tag does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_tag(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM tags WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
