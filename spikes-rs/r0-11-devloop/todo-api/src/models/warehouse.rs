//! The warehouse model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Warehouse {
    pub id: i64,
    pub name: String,
    pub capacity: i64,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct WarehouseInput {
    pub name: String,
    pub capacity: i64,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct WarehouseList {
    pub items: Vec<Warehouse>,
    pub total: i64,
}

/// A row of the warehouses table.
pub struct WarehouseRow {
    pub id: i64,
    pub name: String,
    pub capacity: i64,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<WarehouseRow> for Warehouse {
    type Error = ApiError;

    fn try_from(row: WarehouseRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            capacity: row.capacity,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
