//! The customer handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::customer::{Customer, CustomerInput, CustomerList, CustomerRow};

/// The page parameters of `listCustomers`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of customers to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of customers to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_customers, create_customer))
        .routes(routes!(get_customer, update_customer, delete_customer))
}

#[utoipa::path(
    get,
    path = "/customers",
    operation_id = "listCustomers",
    summary = "List customers",
    tag = "customers",
    params(ListParams),
    responses(
        (status = 200, description = "A page of customers in id order.", body = CustomerList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_customers(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<CustomerList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        CustomerRow,
        "SELECT id, name, email, phone, created_at, updated_at
         FROM customers ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM customers"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Customer::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(CustomerList { items, total }))
}

#[utoipa::path(
    post,
    path = "/customers",
    operation_id = "createCustomer",
    summary = "Create a customer",
    tag = "customers",
    request_body = CustomerInput,
    responses(
        (status = 201, description = "The new customer.", body = Customer),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_customer(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<CustomerInput>,
) -> ApiResult<(StatusCode, Json<Customer>)> {
    let row = sqlx::query_as!(
        CustomerRow,
        "INSERT INTO customers (name, email, phone)
         VALUES ($1, $2, $3)
         RETURNING id, name, email, phone, created_at, updated_at",
        input.name,
        input.email,
        input.phone
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/customers/{id}",
    operation_id = "getCustomer",
    summary = "Get a customer",
    tag = "customers",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The customer.", body = Customer),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The customer does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_customer(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Customer>> {
    let row = sqlx::query_as!(
        CustomerRow,
        "SELECT id, name, email, phone, created_at, updated_at
         FROM customers WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/customers/{id}",
    operation_id = "updateCustomer",
    summary = "Replace a customer",
    tag = "customers",
    params(("id" = i64, Path)),
    request_body = CustomerInput,
    responses(
        (status = 200, description = "The updated customer.", body = Customer),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The customer does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_customer(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<CustomerInput>,
) -> ApiResult<Json<Customer>> {
    let row = sqlx::query_as!(
        CustomerRow,
        "UPDATE customers
         SET name = $2, email = $3, phone = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, name, email, phone, created_at, updated_at",
        id,
        input.name,
        input.email,
        input.phone
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/customers/{id}",
    operation_id = "deleteCustomer",
    summary = "Delete a customer",
    tag = "customers",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The customer is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The customer does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_customer(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM customers WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
