//! The invoice model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::{Date, OffsetDateTime};
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct Invoice {
    pub id: i64,
    pub number: String,
    pub amount: f64,
    pub paid: bool,
    #[schema(required = true)]
    pub due: Option<Date>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct InvoiceInput {
    pub number: String,
    pub amount: f64,
    pub paid: bool,
    #[serde(default)]
    pub due: Option<Date>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct InvoiceList {
    pub items: Vec<Invoice>,
    pub total: i64,
}

/// A row of the invoices table.
pub struct InvoiceRow {
    pub id: i64,
    pub number: String,
    pub amount: f64,
    pub paid: bool,
    pub due: Option<Date>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<InvoiceRow> for Invoice {
    type Error = ApiError;

    fn try_from(row: InvoiceRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            number: row.number,
            amount: row.amount,
            paid: row.paid,
            due: row.due,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
