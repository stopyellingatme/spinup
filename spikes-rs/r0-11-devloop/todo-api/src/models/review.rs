//! The review model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Review {
    pub id: i64,
    pub rating: i32,
    #[schema(required = true)]
    pub body: Option<String>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ReviewInput {
    pub rating: i32,
    #[serde(default)]
    pub body: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ReviewList {
    pub items: Vec<Review>,
    pub total: i64,
}

/// A row of the reviews table.
pub struct ReviewRow {
    pub id: i64,
    pub rating: i32,
    pub body: Option<String>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<ReviewRow> for Review {
    type Error = ApiError;

    fn try_from(row: ReviewRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            rating: row.rating,
            body: row.body,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
