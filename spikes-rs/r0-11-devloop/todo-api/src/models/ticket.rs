//! The ticket model. `spinup g resource` wrote this file. You may edit it.

use axum::http::StatusCode;
use kit::{ApiError, Problem};
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

// The severity of a ticket. Plain comments on schema
// types keep the spec free of descriptions, as the Go spec.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub enum Severity {
    Low,
    High,
}

impl Severity {
    /// The stored text of the value.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Low => "low",
            Self::High => "high",
        }
    }

    /// Parses the stored text. Other text is a 500: the CHECK constraint
    /// allows only these values.
    pub fn parse(s: &str) -> Result<Self, ApiError> {
        match s {
            "low" => Ok(Self::Low),
            "high" => Ok(Self::High),
            _ => Err(ApiError::from(Problem::status(
                StatusCode::INTERNAL_SERVER_ERROR,
            ))),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Ticket {
    pub id: i64,
    pub subject: String,
    #[schema(inline)]
    pub severity: Severity,
    pub open: bool,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct TicketInput {
    pub subject: String,
    #[schema(inline)]
    pub severity: Severity,
    pub open: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct TicketList {
    pub items: Vec<Ticket>,
    pub total: i64,
}

/// A row of the tickets table.
pub struct TicketRow {
    pub id: i64,
    pub subject: String,
    pub severity: String,
    pub open: bool,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<TicketRow> for Ticket {
    type Error = ApiError;

    fn try_from(row: TicketRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            subject: row.subject,
            severity: Severity::parse(&row.severity)?,
            open: row.open,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
