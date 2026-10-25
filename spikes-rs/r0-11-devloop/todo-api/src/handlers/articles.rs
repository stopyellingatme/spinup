//! The article handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::article::{Article, ArticleInput, ArticleList, ArticleRow};

/// The page parameters of `listArticles`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of articles to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of articles to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_articles, create_article))
        .routes(routes!(get_article, update_article, delete_article))
}

#[utoipa::path(
    get,
    path = "/articles",
    operation_id = "listArticles",
    summary = "List articles",
    tag = "articles",
    params(ListParams),
    responses(
        (status = 200, description = "A page of articles in id order.", body = ArticleList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_articles(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<ArticleList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        ArticleRow,
        "SELECT id, title, body, published, created_at, updated_at
         FROM articles ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM articles"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Article::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(ArticleList { items, total }))
}

#[utoipa::path(
    post,
    path = "/articles",
    operation_id = "createArticle",
    summary = "Create an article",
    tag = "articles",
    request_body = ArticleInput,
    responses(
        (status = 201, description = "The new article.", body = Article),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_article(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<ArticleInput>,
) -> ApiResult<(StatusCode, Json<Article>)> {
    let row = sqlx::query_as!(
        ArticleRow,
        "INSERT INTO articles (title, body, published)
         VALUES ($1, $2, $3)
         RETURNING id, title, body, published, created_at, updated_at",
        input.title,
        input.body,
        input.published
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/articles/{id}",
    operation_id = "getArticle",
    summary = "Get an article",
    tag = "articles",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The article.", body = Article),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The article does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_article(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Article>> {
    let row = sqlx::query_as!(
        ArticleRow,
        "SELECT id, title, body, published, created_at, updated_at
         FROM articles WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/articles/{id}",
    operation_id = "updateArticle",
    summary = "Replace an article",
    tag = "articles",
    params(("id" = i64, Path)),
    request_body = ArticleInput,
    responses(
        (status = 200, description = "The updated article.", body = Article),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The article does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_article(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<ArticleInput>,
) -> ApiResult<Json<Article>> {
    let row = sqlx::query_as!(
        ArticleRow,
        "UPDATE articles
         SET title = $2, body = $3, published = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, title, body, published, created_at, updated_at",
        id,
        input.title,
        input.body,
        input.published
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/articles/{id}",
    operation_id = "deleteArticle",
    summary = "Delete an article",
    tag = "articles",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The article is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The article does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_article(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM articles WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
