//! The todo handlers.

use axum::Json;
use axum::extract::State;
use axum::http::StatusCode;
use kit::auth::AuthUser;
use kit::db::PgPool;
use kit::httpx::{ApiResult, Path, Problem, Query, ValidJson, page};
use serde::Deserialize;
use utoipa::IntoParams;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;

use crate::models::todo::{self, Todo, TodoInput, TodoList};

/// The page parameters. Invalid values produce a 422 response.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of todos to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of todos to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// Returns the operations of this resource.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_todos, create_todo))
        .routes(routes!(get_todo, update_todo, delete_todo))
}

#[utoipa::path(
    get,
    path = "/todos",
    operation_id = "listTodos",
    summary = "List todos",
    tag = "todos",
    params(ListParams),
    responses(
        (status = 200, description = "A page of todos in id order.", body = TodoList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 401, description = "A signed-in user is required.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_todos(
    State(db): State<PgPool>,
    AuthUser(user): AuthUser,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<TodoList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    Ok(Json(todo::list(&db, user.id, limit, offset).await?))
}

#[utoipa::path(
    post,
    path = "/todos",
    operation_id = "createTodo",
    summary = "Create a todo",
    tag = "todos",
    request_body = TodoInput,
    responses(
        (status = 201, description = "The new todo.", body = Todo),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 401, description = "A signed-in user is required.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_todo(
    State(db): State<PgPool>,
    AuthUser(user): AuthUser,
    ValidJson(input): ValidJson<TodoInput>,
) -> ApiResult<(StatusCode, Json<Todo>)> {
    let item = todo::create(&db, user.id, input).await?;
    Ok((StatusCode::CREATED, Json(item)))
}

#[utoipa::path(
    get,
    path = "/todos/{id}",
    operation_id = "getTodo",
    summary = "Get a todo",
    tag = "todos",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The todo.", body = Todo),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 401, description = "A signed-in user is required.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The todo does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_todo(
    State(db): State<PgPool>,
    AuthUser(user): AuthUser,
    Path(id): Path<i64>,
) -> ApiResult<Json<Todo>> {
    Ok(Json(todo::get(&db, user.id, id).await?))
}

#[utoipa::path(
    put,
    path = "/todos/{id}",
    operation_id = "updateTodo",
    summary = "Replace a todo",
    tag = "todos",
    params(("id" = i64, Path)),
    request_body = TodoInput,
    responses(
        (status = 200, description = "The updated todo.", body = Todo),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 401, description = "A signed-in user is required.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The todo does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_todo(
    State(db): State<PgPool>,
    AuthUser(user): AuthUser,
    Path(id): Path<i64>,
    ValidJson(input): ValidJson<TodoInput>,
) -> ApiResult<Json<Todo>> {
    Ok(Json(todo::update(&db, user.id, id, input).await?))
}

#[utoipa::path(
    delete,
    path = "/todos/{id}",
    operation_id = "deleteTodo",
    summary = "Delete a todo",
    tag = "todos",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The todo is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 401, description = "A signed-in user is required.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The todo does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_todo(
    State(db): State<PgPool>,
    AuthUser(user): AuthUser,
    Path(id): Path<i64>,
) -> ApiResult<StatusCode> {
    todo::delete(&db, user.id, id).await?;
    Ok(StatusCode::NO_CONTENT)
}

#[cfg(test)]
#[path = "todos_tests.rs"]
mod tests;
