//! The customer model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct Customer {
    pub id: i64,
    pub name: String,
    pub email: String,
    #[schema(required = true)]
    pub phone: Option<String>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct CustomerInput {
    pub name: String,
    pub email: String,
    #[serde(default)]
    pub phone: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema)]
pub struct CustomerList {
    pub items: Vec<Customer>,
    pub total: i64,
}

/// A row of the customers table.
pub struct CustomerRow {
    pub id: i64,
    pub name: String,
    pub email: String,
    pub phone: Option<String>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<CustomerRow> for Customer {
    type Error = ApiError;

    fn try_from(row: CustomerRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            email: row.email,
            phone: row.phone,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
