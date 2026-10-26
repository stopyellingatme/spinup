//! The user errors of the package (Go `kit/db/errors.go`) and the problem mapping of
//! database errors.

use std::fmt;

use axum::http::StatusCode;

use crate::httpx::{ApiError, Problem};

/// An error for a person to read (Go `userError`). Its text is `what: why`, then a
/// `Try:` line.
#[derive(Debug)]
pub struct DbError {
    what: String,
    why: String,
    try_: String,
    source: Option<Box<dyn std::error::Error + Send + Sync + 'static>>,
}

impl DbError {
    /// Returns an error that says what failed, why, and what to try.
    pub fn new(what: impl Into<String>, why: impl Into<String>, try_: impl Into<String>) -> Self {
        Self {
            what: what.into(),
            why: why.into(),
            try_: try_.into(),
            source: None,
        }
    }

    /// Adds the cause of the error.
    #[must_use]
    pub fn with_source(mut self, err: impl std::error::Error + Send + Sync + 'static) -> Self {
        self.source = Some(Box::new(err));
        self
    }

    /// The error of a missing database URL.
    pub(crate) fn empty_url(what: &str) -> Self {
        Self::new(
            what,
            "the database URL is empty",
            "set DATABASE_URL (see .env.example)",
        )
    }

    /// The error of a database URL that does not parse. It never holds the URL or the
    /// parser text, because both can hold the password.
    pub(crate) fn bad_url(what: &str) -> Self {
        Self::new(
            what,
            "the database URL is not valid (it is not shown because it can hold a password)",
            "use the form postgres://user:password@host:5432/dbname?sslmode=disable",
        )
    }
}

impl fmt::Display for DbError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.what)?;
        if !self.why.is_empty() {
            write!(f, ": {}", self.why)?;
        }
        if !self.try_.is_empty() {
            write!(f, "\nTry: {}", self.try_)?;
        }
        Ok(())
    }
}

impl std::error::Error for DbError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.source
            .as_deref()
            .map(|e| e as &(dyn std::error::Error + 'static))
    }
}

/// Maps a database error to a problem (Go `ResponseError`). `delete` selects the answer
/// to a foreign key violation: 409 for a delete, 422 for every other write.
///
/// | Error | Status | Detail |
/// |---|---|---|
/// | no row | 404 | The record does not exist. |
/// | `23505` unique violation | 409 | A record with the same unique value already exists. |
/// | `23503` on a delete | 409 | Other records refer to this record. |
/// | `23503` | 422 | A referenced record does not exist. |
/// | `23502`, `23514` | 422 | A value breaks a rule of the database. |
/// | class `22` | 422 | A value has an invalid format or is out of range. |
/// | every other error | 500 | (none; the error goes to the log) |
pub fn problem_from(err: &sqlx::Error, delete: bool) -> Problem {
    if matches!(err, sqlx::Error::RowNotFound) {
        return ApiError::not_found().problem().clone();
    }
    if let sqlx::Error::Database(db) = err {
        let code = db.code().unwrap_or_default();
        let found = match code.as_ref() {
            "23505" => Some((
                StatusCode::CONFLICT,
                "A record with the same unique value already exists.",
            )),
            "23503" if delete => {
                Some((StatusCode::CONFLICT, "Other records refer to this record."))
            }
            "23503" => Some((
                StatusCode::UNPROCESSABLE_ENTITY,
                "A referenced record does not exist.",
            )),
            "23502" | "23514" => Some((
                StatusCode::UNPROCESSABLE_ENTITY,
                "A value breaks a rule of the database.",
            )),
            c if c.starts_with("22") => Some((
                StatusCode::UNPROCESSABLE_ENTITY,
                "A value has an invalid format or is out of range.",
            )),
            _ => None,
        };
        if let Some((status, detail)) = found {
            return Problem::new(status, detail);
        }
    }
    tracing::error!(error = %err, "database operation failed");
    Problem::status(StatusCode::INTERNAL_SERVER_ERROR)
}

impl From<sqlx::Error> for ApiError {
    /// Maps the error with [`problem_from`] for a write that is not a delete.
    fn from(err: sqlx::Error) -> Self {
        ApiError::from(problem_from(&err, false))
    }
}

/// Maps the error of a delete: a foreign key violation becomes a 409.
///
/// ```no_run
/// # async fn delete(pool: spinup_kit::db::PgPool, id: i64) -> spinup_kit::httpx::ApiResult<()> {
/// sqlx::query("DELETE FROM todos WHERE id = $1")
///     .bind(id)
///     .execute(&pool)
///     .await
///     .map_err(spinup_kit::db::on_delete)?;
/// # Ok(())
/// # }
/// ```
pub fn on_delete(err: sqlx::Error) -> ApiError {
    ApiError::from(problem_from(&err, true))
}
