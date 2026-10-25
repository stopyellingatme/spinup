//! The supplier model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Supplier {
    pub id: i64,
    pub name: String,
    pub country: String,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct SupplierInput {
    pub name: String,
    pub country: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct SupplierList {
    pub items: Vec<Supplier>,
    pub total: i64,
}

/// A row of the suppliers table.
pub struct SupplierRow {
    pub id: i64,
    pub name: String,
    pub country: String,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<SupplierRow> for Supplier {
    type Error = ApiError;

    fn try_from(row: SupplierRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            country: row.country,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
