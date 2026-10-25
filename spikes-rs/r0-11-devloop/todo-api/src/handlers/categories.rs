//! The category handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::category::{Category, CategoryInput, CategoryList, CategoryRow};

/// The page parameters of `listCategories`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of categories to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of categories to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_categories, create_category))
        .routes(routes!(get_category, update_category, delete_category))
}

#[utoipa::path(
    get,
    path = "/categories",
    operation_id = "listCategories",
    summary = "List categories",
    tag = "categories",
    params(ListParams),
    responses(
        (status = 200, description = "A page of categories in id order.", body = CategoryList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_categories(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<CategoryList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        CategoryRow,
        "SELECT id, name, position, created_at, updated_at
         FROM categories ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM categories"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Category::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(CategoryList { items, total }))
}

#[utoipa::path(
    post,
    path = "/categories",
    operation_id = "createCategory",
    summary = "Create a category",
    tag = "categories",
    request_body = CategoryInput,
    responses(
        (status = 201, description = "The new category.", body = Category),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_category(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<CategoryInput>,
) -> ApiResult<(StatusCode, Json<Category>)> {
    let row = sqlx::query_as!(
        CategoryRow,
        "INSERT INTO categories (name, position)
         VALUES ($1, $2)
         RETURNING id, name, position, created_at, updated_at",
        input.name,
        input.position
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/categories/{id}",
    operation_id = "getCategory",
    summary = "Get a category",
    tag = "categories",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The category.", body = Category),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The category does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_category(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Category>> {
    let row = sqlx::query_as!(
        CategoryRow,
        "SELECT id, name, position, created_at, updated_at
         FROM categories WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/categories/{id}",
    operation_id = "updateCategory",
    summary = "Replace a category",
    tag = "categories",
    params(("id" = i64, Path)),
    request_body = CategoryInput,
    responses(
        (status = 200, description = "The updated category.", body = Category),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The category does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_category(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<CategoryInput>,
) -> ApiResult<Json<Category>> {
    let row = sqlx::query_as!(
        CategoryRow,
        "UPDATE categories
         SET name = $2, position = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, name, position, created_at, updated_at",
        id,
        input.name,
        input.position
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/categories/{id}",
    operation_id = "deleteCategory",
    summary = "Delete a category",
    tag = "categories",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The category is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The category does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_category(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM categories WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
