//! The article model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Article {
    pub id: i64,
    pub title: String,
    pub body: String,
    pub published: bool,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ArticleInput {
    pub title: String,
    pub body: String,
    pub published: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ArticleList {
    pub items: Vec<Article>,
    pub total: i64,
}

/// A row of the articles table.
pub struct ArticleRow {
    pub id: i64,
    pub title: String,
    pub body: String,
    pub published: bool,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<ArticleRow> for Article {
    type Error = ApiError;

    fn try_from(row: ArticleRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            title: row.title,
            body: row.body,
            published: row.published,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
