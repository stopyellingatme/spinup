//! The shipment model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Shipment {
    pub id: i64,
    pub tracking: String,
    #[schema(required = true)]
    #[serde(with = "time::serde::rfc3339::option")]
    pub shipped_at: Option<OffsetDateTime>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ShipmentInput {
    pub tracking: String,
    #[serde(default)]
    #[serde(with = "time::serde::rfc3339::option")]
    pub shipped_at: Option<OffsetDateTime>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct ShipmentList {
    pub items: Vec<Shipment>,
    pub total: i64,
}

/// A row of the shipments table.
pub struct ShipmentRow {
    pub id: i64,
    pub tracking: String,
    pub shipped_at: Option<OffsetDateTime>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<ShipmentRow> for Shipment {
    type Error = ApiError;

    fn try_from(row: ShipmentRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            tracking: row.tracking,
            shipped_at: row.shipped_at,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
