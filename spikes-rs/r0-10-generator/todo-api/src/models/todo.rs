//! The todo model. `spinup g resource` wrote this file. You may edit it.

use axum::http::StatusCode;
use kit::{ApiError, Problem};
use serde::{Deserialize, Serialize};
use time::{Date, OffsetDateTime};
use utoipa::ToSchema;

// The priority of a todo. Plain comments on schema
// types keep the spec free of descriptions, as the Go spec.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub enum Priority {
    Low,
    Normal,
    High,
}

impl Priority {
    /// The stored text of the value.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Low => "low",
            Self::Normal => "normal",
            Self::High => "high",
        }
    }

    /// Parses the stored text. Other text is a 500: the CHECK constraint
    /// allows only these values.
    pub fn parse(s: &str) -> Result<Self, ApiError> {
        match s {
            "low" => Ok(Self::Low),
            "normal" => Ok(Self::Normal),
            "high" => Ok(Self::High),
            _ => Err(ApiError::from(Problem::status(
                StatusCode::INTERNAL_SERVER_ERROR,
            ))),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Todo {
    pub id: i64,
    pub title: String,
    pub done: bool,
    #[schema(required = true)]
    pub due: Option<Date>,
    #[schema(inline)]
    pub priority: Priority,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct TodoInput {
    pub title: String,
    pub done: bool,
    #[serde(default)]
    pub due: Option<Date>,
    #[schema(inline)]
    pub priority: Priority,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct TodoList {
    pub items: Vec<Todo>,
    pub total: i64,
}

/// A row of the todos table.
pub struct TodoRow {
    pub id: i64,
    pub title: String,
    pub done: bool,
    pub due: Option<Date>,
    pub priority: String,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<TodoRow> for Todo {
    type Error = ApiError;

    fn try_from(row: TodoRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            title: row.title,
            done: row.done,
            due: row.due,
            priority: Priority::parse(&row.priority)?,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
