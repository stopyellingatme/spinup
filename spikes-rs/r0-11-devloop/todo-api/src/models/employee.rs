//! The employee model. `spinup g resource` wrote this file. You may edit it.

use kit::ApiError;
use serde::{Deserialize, Serialize};
use time::{Date, OffsetDateTime};
use utoipa::ToSchema;

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct Employee {
    pub id: i64,
    pub name: String,
    pub hired: Date,
    #[schema(required = true)]
    pub salary: Option<f64>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct EmployeeInput {
    pub name: String,
    pub hired: Date,
    #[serde(default)]
    pub salary: Option<f64>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema)]
pub struct EmployeeList {
    pub items: Vec<Employee>,
    pub total: i64,
}

/// A row of the employees table.
pub struct EmployeeRow {
    pub id: i64,
    pub name: String,
    pub hired: Date,
    pub salary: Option<f64>,
    pub created_at: OffsetDateTime,
    pub updated_at: OffsetDateTime,
}

impl TryFrom<EmployeeRow> for Employee {
    type Error = ApiError;

    fn try_from(row: EmployeeRow) -> Result<Self, ApiError> {
        Ok(Self {
            id: row.id,
            name: row.name,
            hired: row.hired,
            salary: row.salary,
            created_at: row.created_at,
            updated_at: row.updated_at,
        })
    }
}
