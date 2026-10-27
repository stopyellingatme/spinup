//! The todo model and its queries.

use kit::db::PgPool;
use kit::httpx::{ApiError, ApiResult};
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

time::serde::format_description!(date_format, Date, "[year]-[month]-[day]");

// Doc macros preserve Rust documentation without adding schema descriptions.
#[doc = concat!("One todo row.")]
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
#[schema(as = Todo)]
pub struct Todo {
    #[doc = concat!("The primary key.")]
    pub id: i64,
    #[doc = concat!("The value of title.")]
    pub title: String,
    #[doc = concat!("The value of done.")]
    pub done: bool,
    #[schema(required = true)]
    #[serde(with = "date_format::option")]
    #[doc = concat!("The value of due.")]
    pub due: Option<time::Date>,
    #[serde(with = "time::serde::rfc3339")]
    #[doc = concat!("The creation time.")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    #[doc = concat!("The last update time.")]
    pub updated_at: OffsetDateTime,
}

#[doc = concat!("The writable fields of a todo.")]
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
#[schema(as = TodoInput)]
pub struct TodoInput {
    #[doc = concat!("The value of title.")]
    pub title: String,
    #[doc = concat!("The value of done.")]
    pub done: bool,
    #[serde(default)]
    #[serde(with = "date_format::option")]
    #[doc = concat!("The value of due.")]
    pub due: Option<time::Date>,
}

#[doc = concat!("A page of todos.")]
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
#[schema(as = TodoList)]
pub struct TodoList {
    #[doc = concat!("The rows of this page.")]
    pub items: Vec<Todo>,
    #[doc = concat!("The total number of rows.")]
    pub total: i64,
}

struct TodoRow {
    id: i64,
    title: String,
    done: bool,
    due: Option<time::Date>,
    created_at: OffsetDateTime,
    updated_at: OffsetDateTime,
}

impl TryFrom<TodoRow> for Todo {
    type Error = ApiError;

    fn try_from(row: TodoRow) -> ApiResult<Self> {
        Ok(Self {
            id: row.id,
            title: row.title,
            done: row.done,
            due: row.due,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}

/// Returns a page in ID order and the total count.
///
/// # Errors
/// Returns a database or row conversion error.
pub async fn list(db: &PgPool, user_id: i64, limit: i64, offset: i64) -> ApiResult<TodoList> {
    let rows = sqlx::query_as!(
        TodoRow,
        r#"SELECT id, title, done, due, created_at, updated_at
         FROM todos WHERE user_id = $1 ORDER BY id LIMIT $2 OFFSET $3"#,
        user_id,
        limit,
        offset
    )
    .fetch_all(db)
    .await?;
    let total = sqlx::query_scalar!(
        r#"SELECT count(*) AS "count!" FROM todos WHERE user_id = $1"#,
        user_id
    )
    .fetch_one(db)
    .await?;
    let items = rows
        .into_iter()
        .map(Todo::try_from)
        .collect::<Result<_, _>>()?;
    Ok(TodoList { items, total })
}

/// Creates a row from validated input.
///
/// # Errors
/// Returns a validation, database, or row conversion error.
pub async fn create(db: &PgPool, user_id: i64, input: TodoInput) -> ApiResult<Todo> {
    let row = sqlx::query_as!(
        TodoRow,
        r#"INSERT INTO todos (title, done, due, user_id)
         VALUES ($1, $2, $3, $4)
         RETURNING id, title, done, due, created_at, updated_at"#,
        input.title,
        input.done,
        input.due,
        user_id
    )
    .fetch_one(db)
    .await?;
    row.try_into()
}

/// Returns one row by ID.
///
/// # Errors
/// Returns a database or row conversion error, including a missing row.
pub async fn get(db: &PgPool, user_id: i64, id: i64) -> ApiResult<Todo> {
    let row = sqlx::query_as!(
        TodoRow,
        r#"SELECT id, title, done, due, created_at, updated_at
         FROM todos WHERE id = $1 AND user_id = $2"#,
        id,
        user_id
    )
    .fetch_one(db)
    .await?;
    row.try_into()
}

/// Replaces the writable fields of a row.
///
/// # Errors
/// Returns a validation, database, or row conversion error.
pub async fn update(db: &PgPool, user_id: i64, id: i64, input: TodoInput) -> ApiResult<Todo> {
    let row = sqlx::query_as!(
        TodoRow,
        r#"UPDATE todos
         SET title = $2, done = $3, due = $4, updated_at = now()
         WHERE id = $1 AND user_id = $5
         RETURNING id, title, done, due, created_at, updated_at"#,
        id,
        input.title,
        input.done,
        input.due,
        user_id
    )
    .fetch_one(db)
    .await?;
    row.try_into()
}

/// Deletes one row by ID.
///
/// # Errors
/// Returns a database error or a missing row error.
pub async fn delete(db: &PgPool, user_id: i64, id: i64) -> ApiResult<()> {
    let done = sqlx::query!(
        r#"DELETE FROM todos WHERE id = $1 AND user_id = $2"#,
        id,
        user_id
    )
    .execute(db)
    .await
    .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(())
}
