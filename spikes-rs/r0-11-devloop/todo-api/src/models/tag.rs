//! The tag model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Tag {
    pub id: i64,
    pub name: String,
    #[schema(required = true)]
    pub color: Option<String>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct TagInput {
    pub name: String,
    #[serde(default)]
    pub color: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct TagList {
    pub items: Vec<Tag>,
    pub total: i64,
}

/// A row of the tags table.
pub struct TagRow {
    pub id: i64,
    pub name: String,
    pub color: Option<String>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<TagRow> for Tag {
    type Error = ApiError;

    fn try_from(row: TagRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            color: row.color,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
