//! The project model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Project {
    pub id: i64,
    pub name: String,
    #[schema(required = true)]
    pub description: Option<String>,
    pub archived: bool,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ProjectInput {
    pub name: String,
    #[serde(default)]
    pub description: Option<String>,
    pub archived: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ProjectList {
    pub items: Vec<Project>,
    pub total: i64,
}

/// A row of the projects table.
pub struct ProjectRow {
    pub id: i64,
    pub name: String,
    pub description: Option<String>,
    pub archived: bool,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<ProjectRow> for Project {
    type Error = ApiError;

    fn try_from(row: ProjectRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            description: row.description,
            archived: row.archived,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
