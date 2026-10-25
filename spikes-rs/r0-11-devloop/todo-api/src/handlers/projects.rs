//! The project handlers. `spinup g resource` wrote this file. You may edit
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

use crate::models::project::{Project, ProjectInput, ProjectList, ProjectRow};

/// The page parameters of `listProjects`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of projects to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of projects to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_projects, create_project))
        .routes(routes!(get_project, update_project, delete_project))
}

#[utoipa::path(
    get,
    path = "/projects",
    operation_id = "listProjects",
    summary = "List projects",
    tag = "projects",
    params(ListParams),
    responses(
        (status = 200, description = "A page of projects in id order.", body = ProjectList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_projects(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<ProjectList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        ProjectRow,
        "SELECT id, name, description, archived, created_at, updated_at
         FROM projects ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM projects"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Project::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(ProjectList { items, total }))
}

#[utoipa::path(
    post,
    path = "/projects",
    operation_id = "createProject",
    summary = "Create a project",
    tag = "projects",
    request_body = ProjectInput,
    responses(
        (status = 201, description = "The new project.", body = Project),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_project(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<ProjectInput>,
) -> ApiResult<(StatusCode, Json<Project>)> {
    let row = sqlx::query_as!(
        ProjectRow,
        "INSERT INTO projects (name, description, archived)
         VALUES ($1, $2, $3)
         RETURNING id, name, description, archived, created_at, updated_at",
        input.name,
        input.description,
        input.archived
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/projects/{id}",
    operation_id = "getProject",
    summary = "Get a project",
    tag = "projects",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The project.", body = Project),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The project does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_project(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Project>> {
    let row = sqlx::query_as!(
        ProjectRow,
        "SELECT id, name, description, archived, created_at, updated_at
         FROM projects WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/projects/{id}",
    operation_id = "updateProject",
    summary = "Replace a project",
    tag = "projects",
    params(("id" = i64, Path)),
    request_body = ProjectInput,
    responses(
        (status = 200, description = "The updated project.", body = Project),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The project does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_project(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<ProjectInput>,
) -> ApiResult<Json<Project>> {
    let row = sqlx::query_as!(
        ProjectRow,
        "UPDATE projects
         SET name = $2, description = $3, archived = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, name, description, archived, created_at, updated_at",
        id,
        input.name,
        input.description,
        input.archived
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/projects/{id}",
    operation_id = "deleteProject",
    summary = "Delete a project",
    tag = "projects",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The project is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The project does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_project(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM projects WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
