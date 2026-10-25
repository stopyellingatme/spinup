//! The product handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::product::{Product, ProductInput, ProductList, ProductRow};

/// The page parameters of `listProducts`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of products to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of products to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_products, create_product))
        .routes(routes!(get_product, update_product, delete_product))
}

#[utoipa::path(
    get,
    path = "/products",
    operation_id = "listProducts",
    summary = "List products",
    tag = "products",
    params(ListParams),
    responses(
        (status = 200, description = "A page of products in id order.", body = ProductList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_products(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<ProductList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        ProductRow,
        "SELECT id, name, price, stock, created_at, updated_at
         FROM products ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM products"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Product::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(ProductList { items, total }))
}

#[utoipa::path(
    post,
    path = "/products",
    operation_id = "createProduct",
    summary = "Create a product",
    tag = "products",
    request_body = ProductInput,
    responses(
        (status = 201, description = "The new product.", body = Product),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_product(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<ProductInput>,
) -> ApiResult<(StatusCode, Json<Product>)> {
    let row = sqlx::query_as!(
        ProductRow,
        "INSERT INTO products (name, price, stock)
         VALUES ($1, $2, $3)
         RETURNING id, name, price, stock, created_at, updated_at",
        input.name,
        input.price,
        input.stock
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/products/{id}",
    operation_id = "getProduct",
    summary = "Get a product",
    tag = "products",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The product.", body = Product),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The product does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_product(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Product>> {
    let row = sqlx::query_as!(
        ProductRow,
        "SELECT id, name, price, stock, created_at, updated_at
         FROM products WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/products/{id}",
    operation_id = "updateProduct",
    summary = "Replace a product",
    tag = "products",
    params(("id" = i64, Path)),
    request_body = ProductInput,
    responses(
        (status = 200, description = "The updated product.", body = Product),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The product does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_product(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<ProductInput>,
) -> ApiResult<Json<Product>> {
    let row = sqlx::query_as!(
        ProductRow,
        "UPDATE products
         SET name = $2, price = $3, stock = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, name, price, stock, created_at, updated_at",
        id,
        input.name,
        input.price,
        input.stock
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/products/{id}",
    operation_id = "deleteProduct",
    summary = "Delete a product",
    tag = "products",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The product is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The product does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_product(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM products WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
