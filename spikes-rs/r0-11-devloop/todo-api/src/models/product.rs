//! The product model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct Product {
    pub id: i64,
    pub name: String,
    pub price: f64,
    pub stock: i32,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct ProductInput {
    pub name: String,
    pub price: f64,
    pub stock: i32,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct ProductList {
    pub items: Vec<Product>,
    pub total: i64,
}

/// A row of the products table.
pub struct ProductRow {
    pub id: i64,
    pub name: String,
    pub price: f64,
    pub stock: i32,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<ProductRow> for Product {
    type Error = ApiError;

    fn try_from(row: ProductRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            price: row.price,
            stock: row.stock,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
