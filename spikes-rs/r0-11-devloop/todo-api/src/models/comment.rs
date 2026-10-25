//! The comment model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Comment {
    pub id: i64,
    pub body: String,
    pub author: String,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct CommentInput {
    pub body: String,
    pub author: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct CommentList {
    pub items: Vec<Comment>,
    pub total: i64,
}

/// A row of the comments table.
pub struct CommentRow {
    pub id: i64,
    pub body: String,
    pub author: String,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<CommentRow> for Comment {
    type Error = ApiError;

    fn try_from(row: CommentRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            body: row.body,
            author: row.author,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
