//! The invoice handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::invoice::{Invoice, InvoiceInput, InvoiceList, InvoiceRow};

/// The page parameters of `listInvoices`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of invoices to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of invoices to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_invoices, create_invoice))
        .routes(routes!(get_invoice, update_invoice, delete_invoice))
}

#[utoipa::path(
    get,
    path = "/invoices",
    operation_id = "listInvoices",
    summary = "List invoices",
    tag = "invoices",
    params(ListParams),
    responses(
        (status = 200, description = "A page of invoices in id order.", body = InvoiceList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_invoices(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<InvoiceList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        InvoiceRow,
        "SELECT id, number, amount, paid, due, created_at, updated_at
         FROM invoices ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM invoices"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Invoice::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(InvoiceList { items, total }))
}

#[utoipa::path(
    post,
    path = "/invoices",
    operation_id = "createInvoice",
    summary = "Create an invoice",
    tag = "invoices",
    request_body = InvoiceInput,
    responses(
        (status = 201, description = "The new invoice.", body = Invoice),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_invoice(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<InvoiceInput>,
) -> ApiResult<(StatusCode, Json<Invoice>)> {
    let row = sqlx::query_as!(
        InvoiceRow,
        "INSERT INTO invoices (number, amount, paid, due)
         VALUES ($1, $2, $3, $4)
         RETURNING id, number, amount, paid, due, created_at, updated_at",
        input.number,
        input.amount,
        input.paid,
        input.due
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/invoices/{id}",
    operation_id = "getInvoice",
    summary = "Get an invoice",
    tag = "invoices",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The invoice.", body = Invoice),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The invoice does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_invoice(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Invoice>> {
    let row = sqlx::query_as!(
        InvoiceRow,
        "SELECT id, number, amount, paid, due, created_at, updated_at
         FROM invoices WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/invoices/{id}",
    operation_id = "updateInvoice",
    summary = "Replace an invoice",
    tag = "invoices",
    params(("id" = i64, Path)),
    request_body = InvoiceInput,
    responses(
        (status = 200, description = "The updated invoice.", body = Invoice),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The invoice does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_invoice(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<InvoiceInput>,
) -> ApiResult<Json<Invoice>> {
    let row = sqlx::query_as!(
        InvoiceRow,
        "UPDATE invoices
         SET number = $2, amount = $3, paid = $4, due = $5, updated_at = now()
         WHERE id = $1
         RETURNING id, number, amount, paid, due, created_at, updated_at",
        id,
        input.number,
        input.amount,
        input.paid,
        input.due
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/invoices/{id}",
    operation_id = "deleteInvoice",
    summary = "Delete an invoice",
    tag = "invoices",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The invoice is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The invoice does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_invoice(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM invoices WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
