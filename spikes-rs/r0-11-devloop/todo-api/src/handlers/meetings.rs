//! The meeting handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::meeting::{Meeting, MeetingInput, MeetingList, MeetingRow};

/// The page parameters of `listMeetings`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of meetings to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of meetings to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_meetings, create_meeting))
        .routes(routes!(get_meeting, update_meeting, delete_meeting))
}

#[utoipa::path(
    get,
    path = "/meetings",
    operation_id = "listMeetings",
    summary = "List meetings",
    tag = "meetings",
    params(ListParams),
    responses(
        (status = 200, description = "A page of meetings in id order.", body = MeetingList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_meetings(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<MeetingList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        MeetingRow,
        "SELECT id, topic, at, notes, created_at, updated_at
         FROM meetings ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM meetings"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Meeting::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(MeetingList { items, total }))
}

#[utoipa::path(
    post,
    path = "/meetings",
    operation_id = "createMeeting",
    summary = "Create a meeting",
    tag = "meetings",
    request_body = MeetingInput,
    responses(
        (status = 201, description = "The new meeting.", body = Meeting),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_meeting(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<MeetingInput>,
) -> ApiResult<(StatusCode, Json<Meeting>)> {
    let row = sqlx::query_as!(
        MeetingRow,
        "INSERT INTO meetings (topic, at, notes)
         VALUES ($1, $2, $3)
         RETURNING id, topic, at, notes, created_at, updated_at",
        input.topic,
        input.at,
        input.notes
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/meetings/{id}",
    operation_id = "getMeeting",
    summary = "Get a meeting",
    tag = "meetings",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The meeting.", body = Meeting),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The meeting does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_meeting(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Meeting>> {
    let row = sqlx::query_as!(
        MeetingRow,
        "SELECT id, topic, at, notes, created_at, updated_at
         FROM meetings WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/meetings/{id}",
    operation_id = "updateMeeting",
    summary = "Replace a meeting",
    tag = "meetings",
    params(("id" = i64, Path)),
    request_body = MeetingInput,
    responses(
        (status = 200, description = "The updated meeting.", body = Meeting),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The meeting does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_meeting(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<MeetingInput>,
) -> ApiResult<Json<Meeting>> {
    let row = sqlx::query_as!(
        MeetingRow,
        "UPDATE meetings
         SET topic = $2, at = $3, notes = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, topic, at, notes, created_at, updated_at",
        id,
        input.topic,
        input.at,
        input.notes
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/meetings/{id}",
    operation_id = "deleteMeeting",
    summary = "Delete a meeting",
    tag = "meetings",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The meeting is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The meeting does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_meeting(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM meetings WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
