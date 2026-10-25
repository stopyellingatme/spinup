//! The coupon model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::{Date, OffsetDateTime};
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct Coupon {
    pub id: i64,
    pub code: String,
    pub discount: f64,
    #[schema(required = true)]
    pub expires: Option<Date>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct CouponInput {
    pub code: String,
    pub discount: f64,
    #[serde(default)]
    pub expires: Option<Date>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct CouponList {
    pub items: Vec<Coupon>,
    pub total: i64,
}

/// A row of the coupons table.
pub struct CouponRow {
    pub id: i64,
    pub code: String,
    pub discount: f64,
    pub expires: Option<Date>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<CouponRow> for Coupon {
    type Error = ApiError;

    fn try_from(row: CouponRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            code: row.code,
            discount: row.discount,
            expires: row.expires,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
