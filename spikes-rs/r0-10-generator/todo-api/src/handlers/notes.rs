//! The note handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::note::{Note, NoteInput, NoteList, NoteRow};

/// The page parameters of `listNotes`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of notes to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of notes to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_notes, create_note))
        .routes(routes!(get_note, update_note, delete_note))
}

#[utoipa::path(
    get,
    path = "/notes",
    operation_id = "listNotes",
    summary = "List notes",
    tag = "notes",
    params(ListParams),
    responses(
        (status = 200, description = "A page of notes in id order.", body = NoteList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_notes(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<NoteList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        NoteRow,
        "SELECT id, title, body, created_at, updated_at
         FROM notes ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM notes"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Note::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(NoteList { items, total }))
}

#[utoipa::path(
    post,
    path = "/notes",
    operation_id = "createNote",
    summary = "Create a note",
    tag = "notes",
    request_body = NoteInput,
    responses(
        (status = 201, description = "The new note.", body = Note),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_note(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<NoteInput>,
) -> ApiResult<(StatusCode, Json<Note>)> {
    let row = sqlx::query_as!(
        NoteRow,
        "INSERT INTO notes (title, body)
         VALUES ($1, $2)
         RETURNING id, title, body, created_at, updated_at",
        input.title,
        input.body
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/notes/{id}",
    operation_id = "getNote",
    summary = "Get a note",
    tag = "notes",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The note.", body = Note),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The note does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_note(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Note>> {
    let row = sqlx::query_as!(
        NoteRow,
        "SELECT id, title, body, created_at, updated_at
         FROM notes WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/notes/{id}",
    operation_id = "updateNote",
    summary = "Replace a note",
    tag = "notes",
    params(("id" = i64, Path)),
    request_body = NoteInput,
    responses(
        (status = 200, description = "The updated note.", body = Note),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The note does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_note(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<NoteInput>,
) -> ApiResult<Json<Note>> {
    let row = sqlx::query_as!(
        NoteRow,
        "UPDATE notes
         SET title = $2, body = $3, updated_at = now()
         WHERE id = $1
         RETURNING id, title, body, created_at, updated_at",
        id,
        input.title,
        input.body
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/notes/{id}",
    operation_id = "deleteNote",
    summary = "Delete a note",
    tag = "notes",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The note is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The note does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_note(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM notes WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
