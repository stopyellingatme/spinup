//! The category model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Category {
    pub id: i64,
    pub name: String,
    pub position: i32,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct CategoryInput {
    pub name: String,
    pub position: i32,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct CategoryList {
    pub items: Vec<Category>,
    pub total: i64,
}

/// A row of the categories table.
pub struct CategoryRow {
    pub id: i64,
    pub name: String,
    pub position: i32,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<CategoryRow> for Category {
    type Error = ApiError;

    fn try_from(row: CategoryRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            position: row.position,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
