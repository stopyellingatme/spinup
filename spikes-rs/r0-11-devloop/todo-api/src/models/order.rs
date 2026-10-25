//! The order model. `spinup g resource` wrote this file. You may edit it.

use axum::http::StatusCode;
use kit::{ApiError, Problem};
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

// The status of an order. Plain comments on schema
// types keep the spec free of descriptions, as the Go spec.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
#[serde(rename_all = "snake_case")]
pub enum Status {
    New,
    Paid,
    Shipped,
}

impl Status {
    /// The stored text of the value.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::New => "new",
            Self::Paid => "paid",
            Self::Shipped => "shipped",
        }
    }

    /// Parses the stored text. Other text is a 500: the CHECK constraint
    /// allows only these values.
    pub fn parse(s: &str) -> Result<Self, ApiError> {
        match s {
            "new" => Ok(Self::New),
            "paid" => Ok(Self::Paid),
            "shipped" => Ok(Self::Shipped),
            _ => Err(ApiError::from(Problem::status(
                StatusCode::INTERNAL_SERVER_ERROR,
            ))),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct Order {
    pub id: i64,
    pub reference: String,
    pub total: f64,
    #[schema(inline)]
    pub status: Status,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct OrderInput {
    pub reference: String,
    pub total: f64,
    #[schema(inline)]
    pub status: Status,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct OrderList {
    pub items: Vec<Order>,
    pub total: i64,
}

/// A row of the orders table.
pub struct OrderRow {
    pub id: i64,
    pub reference: String,
    pub total: f64,
    pub status: String,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<OrderRow> for Order {
    type Error = ApiError;

    fn try_from(row: OrderRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            reference: row.reference,
            total: row.total,
            status: Status::parse(&row.status)?,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
