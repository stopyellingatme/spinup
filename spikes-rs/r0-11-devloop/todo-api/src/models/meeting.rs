//! The meeting model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Meeting {
    pub id: i64,
    pub topic: String,
    #[serde(with = "time::serde::rfc3339")]
    pub at: OffsetDateTime,
    #[schema(required = true)]
    pub notes: Option<String>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct MeetingInput {
    pub topic: String,
    #[serde(with = "time::serde::rfc3339")]
    pub at: OffsetDateTime,
    #[serde(default)]
    pub notes: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct MeetingList {
    pub items: Vec<Meeting>,
    pub total: i64,
}

/// A row of the meetings table.
pub struct MeetingRow {
    pub id: i64,
    pub topic: String,
    pub at: OffsetDateTime,
    pub notes: Option<String>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<MeetingRow> for Meeting {
    type Error = ApiError;

    fn try_from(row: MeetingRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            topic: row.topic,
            at: row.at,
            notes: row.notes,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
