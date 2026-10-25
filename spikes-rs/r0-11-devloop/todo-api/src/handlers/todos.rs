//! The todo handlers. `spinup g resource` wrote this file. You may edit
//! it.

use axum::Json;
use axum::extract::State;
use axum::http::StatusCode;
use kit::extract::{self, Path, Query, page};
use kit::{ApiError, ApiResult, Problem};
use serde::{Deserialize, Serialize};
use sqlx::PgPool;
use utoipa::{IntoParams, ToSchema};
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;

use crate::models::todo::{Todo, TodoInput, TodoList, TodoRow};

/// The page parameters of `listTodos`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
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

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_todos, create_todo))
        .routes(routes!(get_todo, update_todo, delete_todo))
        .routes(routes!(todo_greeting))
}

/// The greeting of the todo list. This handler is a hand edit of the
/// scaffold file: R0.11 edits its body, its signature, and its
/// `#[utoipa::path]` in the dev loop measurements.
#[derive(Debug, Serialize, ToSchema)]
pub struct Greeting {
    /// The message.
    pub message: String,
    /// The number of open todos.
    pub open: i64,
}

#[utoipa::path(
    get,
    path = "/todos/greeting",
    operation_id = "todoGreeting",
    summary = "Greet the todo list",
    tag = "todos",
    responses(
        (status = 200, description = "The greeting.", body = Greeting),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn todo_greeting(State(db): State<PgPool>) -> ApiResult<Json<Greeting>> {
    let open = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM todos WHERE NOT done"#)
        .fetch_one(&db)
        .await?;
    let message = format!("{open} open todos (mark-0000)"); // r0-11: edit marker
    Ok(Json(Greeting { message, open }))
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
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_todos(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<TodoList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        TodoRow,
        "SELECT id, title, done, due, priority, created_at, updated_at
         FROM todos ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM todos"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Todo::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(TodoList { items, total }))
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
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_todo(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<TodoInput>,
) -> ApiResult<(StatusCode, Json<Todo>)> {
    let row = sqlx::query_as!(
        TodoRow,
        "INSERT INTO todos (title, done, due, priority)
         VALUES ($1, $2, $3, $4)
         RETURNING id, title, done, due, priority, created_at, updated_at",
        input.title,
        input.done,
        input.due,
        input.priority.as_str()
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
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
        (status = 404, description = "The todo does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_todo(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Todo>> {
    let row = sqlx::query_as!(
        TodoRow,
        "SELECT id, title, done, due, priority, created_at, updated_at
         FROM todos WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
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
        (status = 404, description = "The todo does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_todo(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<TodoInput>,
) -> ApiResult<Json<Todo>> {
    let row = sqlx::query_as!(
        TodoRow,
        "UPDATE todos
         SET title = $2, done = $3, due = $4, priority = $5, updated_at = now()
         WHERE id = $1
         RETURNING id, title, done, due, priority, created_at, updated_at",
        id,
        input.title,
        input.done,
        input.due,
        input.priority.as_str()
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
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
        (status = 404, description = "The todo does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_todo(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM todos WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
