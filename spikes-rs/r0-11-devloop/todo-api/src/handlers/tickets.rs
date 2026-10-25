//! The ticket handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::ticket::{Ticket, TicketInput, TicketList, TicketRow};

/// The page parameters of `listTickets`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of tickets to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of tickets to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_tickets, create_ticket))
        .routes(routes!(get_ticket, update_ticket, delete_ticket))
}

#[utoipa::path(
    get,
    path = "/tickets",
    operation_id = "listTickets",
    summary = "List tickets",
    tag = "tickets",
    params(ListParams),
    responses(
        (status = 200, description = "A page of tickets in id order.", body = TicketList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_tickets(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<TicketList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        TicketRow,
        "SELECT id, subject, severity, open, created_at, updated_at
         FROM tickets ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM tickets"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Ticket::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(TicketList { items, total }))
}

#[utoipa::path(
    post,
    path = "/tickets",
    operation_id = "createTicket",
    summary = "Create a ticket",
    tag = "tickets",
    request_body = TicketInput,
    responses(
        (status = 201, description = "The new ticket.", body = Ticket),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_ticket(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<TicketInput>,
) -> ApiResult<(StatusCode, Json<Ticket>)> {
    let row = sqlx::query_as!(
        TicketRow,
        "INSERT INTO tickets (subject, severity, open)
         VALUES ($1, $2, $3)
         RETURNING id, subject, severity, open, created_at, updated_at",
        input.subject,
        input.severity.as_str(),
        input.open
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/tickets/{id}",
    operation_id = "getTicket",
    summary = "Get a ticket",
    tag = "tickets",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The ticket.", body = Ticket),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The ticket does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_ticket(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Ticket>> {
    let row = sqlx::query_as!(
        TicketRow,
        "SELECT id, subject, severity, open, created_at, updated_at
         FROM tickets WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/tickets/{id}",
    operation_id = "updateTicket",
    summary = "Replace a ticket",
    tag = "tickets",
    params(("id" = i64, Path)),
    request_body = TicketInput,
    responses(
        (status = 200, description = "The updated ticket.", body = Ticket),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The ticket does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_ticket(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<TicketInput>,
) -> ApiResult<Json<Ticket>> {
    let row = sqlx::query_as!(
        TicketRow,
        "UPDATE tickets
         SET subject = $2, severity = $3, open = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, subject, severity, open, created_at, updated_at",
        id,
        input.subject,
        input.severity.as_str(),
        input.open
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/tickets/{id}",
    operation_id = "deleteTicket",
    summary = "Delete a ticket",
    tag = "tickets",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The ticket is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The ticket does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_ticket(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM tickets WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
