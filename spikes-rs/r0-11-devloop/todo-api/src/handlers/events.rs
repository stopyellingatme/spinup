//! The event handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::event::{Event, EventInput, EventList, EventRow};

/// The page parameters of `listEvents`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of events to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of events to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_events, create_event))
        .routes(routes!(get_event, update_event, delete_event))
}

#[utoipa::path(
    get,
    path = "/events",
    operation_id = "listEvents",
    summary = "List events",
    tag = "events",
    params(ListParams),
    responses(
        (status = 200, description = "A page of events in id order.", body = EventList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_events(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<EventList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        EventRow,
        "SELECT id, title, starts_at, ends_at, created_at, updated_at
         FROM events ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM events"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Event::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(EventList { items, total }))
}

#[utoipa::path(
    post,
    path = "/events",
    operation_id = "createEvent",
    summary = "Create an event",
    tag = "events",
    request_body = EventInput,
    responses(
        (status = 201, description = "The new event.", body = Event),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_event(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<EventInput>,
) -> ApiResult<(StatusCode, Json<Event>)> {
    let row = sqlx::query_as!(
        EventRow,
        "INSERT INTO events (title, starts_at, ends_at)
         VALUES ($1, $2, $3)
         RETURNING id, title, starts_at, ends_at, created_at, updated_at",
        input.title,
        input.starts_at,
        input.ends_at
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/events/{id}",
    operation_id = "getEvent",
    summary = "Get an event",
    tag = "events",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The event.", body = Event),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The event does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_event(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Event>> {
    let row = sqlx::query_as!(
        EventRow,
        "SELECT id, title, starts_at, ends_at, created_at, updated_at
         FROM events WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/events/{id}",
    operation_id = "updateEvent",
    summary = "Replace an event",
    tag = "events",
    params(("id" = i64, Path)),
    request_body = EventInput,
    responses(
        (status = 200, description = "The updated event.", body = Event),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The event does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_event(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<EventInput>,
) -> ApiResult<Json<Event>> {
    let row = sqlx::query_as!(
        EventRow,
        "UPDATE events
         SET title = $2, starts_at = $3, ends_at = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, title, starts_at, ends_at, created_at, updated_at",
        id,
        input.title,
        input.starts_at,
        input.ends_at
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/events/{id}",
    operation_id = "deleteEvent",
    summary = "Delete an event",
    tag = "events",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The event is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The event does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_event(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM events WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
