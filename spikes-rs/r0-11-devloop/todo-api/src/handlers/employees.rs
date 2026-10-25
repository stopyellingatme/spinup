//! The employee handlers. `spinup g resource` wrote this file. You may edit
//! it.

use axum::Json;
use axum::extract::State;
use axum::http::StatusCode;
use kit::extract::{self, Path, Query, page};
use kit::{ApiError, ApiResult, Problem};
use serde::Deserialize;
use sqlx::PgPool;
use utoipa::IntoParams;
use utoipa_axum::router::OpenApiRouter;
use utoipa_axum::routes;

use crate::models::employee::{Employee, EmployeeInput, EmployeeList, EmployeeRow};

/// The page parameters of `listEmployees`. The handler checks the
/// range, so a bad value is a 422 with field errors, as in Go.
#[derive(Debug, Deserialize, IntoParams)]
#[into_params(parameter_in = Query)]
pub struct ListParams {
    /// The number of employees to return.
    #[param(minimum = 1, maximum = 200, default = 50, nullable = false)]
    pub limit: Option<i32>,
    /// The number of employees to skip.
    #[param(minimum = 0, default = 0, nullable = false)]
    pub offset: Option<i32>,
}

/// The operations of the resource. Several methods share one path.
pub fn routes() -> OpenApiRouter<PgPool> {
    OpenApiRouter::new()
        .routes(routes!(list_employees, create_employee))
        .routes(routes!(get_employee, update_employee, delete_employee))
}

#[utoipa::path(
    get,
    path = "/employees",
    operation_id = "listEmployees",
    summary = "List employees",
    tag = "employees",
    params(ListParams),
    responses(
        (status = 200, description = "A page of employees in id order.", body = EmployeeList),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn list_employees(
    State(db): State<PgPool>,
    Query(params): Query<ListParams>,
) -> ApiResult<Json<EmployeeList>> {
    let (limit, offset) = page::resolve(params.limit, params.offset)?;
    let rows = sqlx::query_as!(
        EmployeeRow,
        "SELECT id, name, hired, salary, created_at, updated_at
         FROM employees ORDER BY id LIMIT $1 OFFSET $2",
        limit,
        offset
    )
    .fetch_all(&db)
    .await?;
    let total = sqlx::query_scalar!(r#"SELECT count(*) AS "count!" FROM employees"#)
        .fetch_one(&db)
        .await?;
    let items = rows
        .into_iter()
        .map(Employee::try_from)
        .collect::<Result<_, _>>()?;
    Ok(Json(EmployeeList { items, total }))
}

#[utoipa::path(
    post,
    path = "/employees",
    operation_id = "createEmployee",
    summary = "Create an employee",
    tag = "employees",
    request_body = EmployeeInput,
    responses(
        (status = 201, description = "The new employee.", body = Employee),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn create_employee(
    State(db): State<PgPool>,
    extract::Json(input): extract::Json<EmployeeInput>,
) -> ApiResult<(StatusCode, Json<Employee>)> {
    let row = sqlx::query_as!(
        EmployeeRow,
        "INSERT INTO employees (name, hired, salary)
         VALUES ($1, $2, $3)
         RETURNING id, name, hired, salary, created_at, updated_at",
        input.name,
        input.hired,
        input.salary
    )
    .fetch_one(&db)
    .await?;
    Ok((StatusCode::CREATED, Json(row.try_into()?)))
}

#[utoipa::path(
    get,
    path = "/employees/{id}",
    operation_id = "getEmployee",
    summary = "Get an employee",
    tag = "employees",
    params(("id" = i64, Path)),
    responses(
        (status = 200, description = "The employee.", body = Employee),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The employee does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn get_employee(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<Json<Employee>> {
    let row = sqlx::query_as!(
        EmployeeRow,
        "SELECT id, name, hired, salary, created_at, updated_at
         FROM employees WHERE id = $1",
        id
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    put,
    path = "/employees/{id}",
    operation_id = "updateEmployee",
    summary = "Replace an employee",
    tag = "employees",
    params(("id" = i64, Path)),
    request_body = EmployeeInput,
    responses(
        (status = 200, description = "The updated employee.", body = Employee),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The employee does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = 422, description = "The request has invalid values.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn update_employee(
    State(db): State<PgPool>,
    Path(id): Path<i64>,
    extract::Json(input): extract::Json<EmployeeInput>,
) -> ApiResult<Json<Employee>> {
    let row = sqlx::query_as!(
        EmployeeRow,
        "UPDATE employees
         SET name = $2, hired = $3, salary = $4, updated_at = now()
         WHERE id = $1
         RETURNING id, name, hired, salary, created_at, updated_at",
        id,
        input.name,
        input.hired,
        input.salary
    )
    .fetch_one(&db)
    .await?;
    Ok(Json(row.try_into()?))
}

#[utoipa::path(
    delete,
    path = "/employees/{id}",
    operation_id = "deleteEmployee",
    summary = "Delete an employee",
    tag = "employees",
    params(("id" = i64, Path)),
    responses(
        (status = 204, description = "The employee is deleted."),
        (status = 400, description = "The request is malformed.", body = Problem, content_type = "application/problem+json"),
        (status = 404, description = "The employee does not exist.", body = Problem, content_type = "application/problem+json"),
        (status = 409, description = "The request conflicts with other data.", body = Problem, content_type = "application/problem+json"),
        (status = "default", description = "An unexpected error.", body = Problem, content_type = "application/problem+json"),
    )
)]
async fn delete_employee(State(db): State<PgPool>, Path(id): Path<i64>) -> ApiResult<StatusCode> {
    let done = sqlx::query!("DELETE FROM employees WHERE id = $1", id)
        .execute(&db)
        .await
        .map_err(kit::db::on_delete)?;
    if done.rows_affected() == 0 {
        return Err(ApiError::not_found());
    }
    Ok(StatusCode::NO_CONTENT)
}
