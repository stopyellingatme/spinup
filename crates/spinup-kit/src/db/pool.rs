//! The connection pool (Go `kit/db/open.go`).

use std::time::Duration;

use sqlx::PgPool;
use sqlx::postgres::{PgConnectOptions, PgPoolOptions};

use super::DbError;
use crate::config::Base;

/// The default `statement_timeout` of app connections.
pub const DEFAULT_STATEMENT_TIMEOUT: Duration = Duration::from_secs(30);
/// The default `lock_timeout` of app connections.
pub const DEFAULT_LOCK_TIMEOUT: Duration = Duration::from_secs(5);
/// The default `application_name` of app connections.
pub const DEFAULT_APP_NAME: &str = "spinup-app";
/// The pool size when neither an option nor the URL sets one. Two colors with two API
/// replicas and a worker each stay far below the `max_connections` of 200 in the deploy
/// Postgres.
pub const DEFAULT_MAX_CONNS: u32 = 20;

/// How [`open`] configures the pool (Go `db.Option`). A field that is `None` (or 0 for
/// `max_conns`) keeps the value of the URL, else the default.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PoolOptions {
    /// `statement_timeout` of every connection. Zero turns the timeout off.
    pub statement_timeout: Option<Duration>,
    /// `lock_timeout` of every connection. Zero turns the timeout off.
    pub lock_timeout: Option<Duration>,
    /// `application_name` of every connection.
    pub app_name: Option<String>,
    /// The maximum pool size. It wins over `pool_max_conns` in the URL. Zero keeps the URL
    /// value or [`DEFAULT_MAX_CONNS`]. Pass `Base::database_max_conns` here.
    pub max_conns: u32,
}

impl PoolOptions {
    /// Sets `statement_timeout`.
    #[must_use]
    pub fn statement_timeout(mut self, d: Duration) -> Self {
        self.statement_timeout = Some(d);
        self
    }

    /// Sets `lock_timeout`.
    #[must_use]
    pub fn lock_timeout(mut self, d: Duration) -> Self {
        self.lock_timeout = Some(d);
        self
    }

    /// Sets `application_name`.
    #[must_use]
    pub fn app_name(mut self, name: impl Into<String>) -> Self {
        self.app_name = Some(name.into());
        self
    }

    /// Sets the maximum pool size.
    #[must_use]
    pub fn max_conns(mut self, n: u32) -> Self {
        self.max_conns = n;
        self
    }
}

/// A database URL that is split into the sqlx options and the values that the kit reads.
pub(crate) struct ParsedUrl {
    pub(crate) options: PgConnectOptions,
    pub(crate) pool_max_conns: Option<String>,
    pub(crate) statement_timeout: Option<String>,
    pub(crate) lock_timeout: Option<String>,
}

/// Parses a `postgres://` URL. The kit reads the query keys `pool_max_conns`,
/// `statement_timeout`, and `lock_timeout` itself, because sqlx does not know them. The error
/// never holds the URL.
pub(crate) fn parse_url(url: &str, what: &str) -> Result<ParsedUrl, DbError> {
    if url.is_empty() {
        return Err(DbError::empty_url(what));
    }
    if !(url.starts_with("postgres://") || url.starts_with("postgresql://")) {
        return Err(DbError::bad_url(what));
    }
    let (base, query) = match url.split_once('?') {
        Some((b, q)) => (b, q),
        None => (url, ""),
    };
    let mut parsed = ParsedUrl {
        options: PgConnectOptions::new_without_pgpass(),
        pool_max_conns: None,
        statement_timeout: None,
        lock_timeout: None,
    };
    let mut rest = form_urlencoded::Serializer::new(String::new());
    let mut kept = 0;
    for (k, v) in form_urlencoded::parse(query.as_bytes()) {
        match k.as_ref() {
            "pool_max_conns" => parsed.pool_max_conns = Some(v.into_owned()),
            "statement_timeout" => parsed.statement_timeout = Some(v.into_owned()),
            "lock_timeout" => parsed.lock_timeout = Some(v.into_owned()),
            _ => {
                rest.append_pair(&k, &v);
                kept += 1;
            }
        }
    }
    let clean = if kept == 0 {
        base.to_owned()
    } else {
        format!("{base}?{}", rest.finish())
    };
    parsed.options = clean.parse().map_err(|_| DbError::bad_url(what))?;
    Ok(parsed)
}

/// Formats a duration as whole milliseconds. Postgres reads a timeout with no unit as
/// milliseconds.
pub(crate) fn millis(d: Duration) -> String {
    d.as_millis().to_string()
}

/// Returns a lazy pool for `url` (Go `db.Open`). It does not connect: the pool connects on
/// first use, so an API can start and serve `/healthz` while the database is down. Call it
/// inside a tokio runtime.
///
/// Each connection gets `statement_timeout` (default 30 s), `lock_timeout` (default 5 s),
/// and `application_name` (default `spinup-app`). The pool holds at most
/// [`DEFAULT_MAX_CONNS`] connections. An option overrides a value. Without an option, a
/// value in the URL query (`pool_max_conns` for the pool size) wins over the default.
///
/// # Errors
///
/// A [`DbError`] when the URL is empty or not valid (the error never shows the URL), or when
/// a pool size is not valid.
pub fn open(url: &str, opts: PoolOptions) -> Result<PgPool, DbError> {
    const WHAT: &str = "db: open the database pool";
    if opts.max_conns > i32::MAX as u32 {
        return Err(bad_max_conns(WHAT, &opts.max_conns.to_string()));
    }
    let parsed = parse_url(url, WHAT)?;
    let mut options = parsed.options;
    let statement = opts
        .statement_timeout
        .map(millis)
        .or(parsed.statement_timeout)
        .unwrap_or_else(|| millis(DEFAULT_STATEMENT_TIMEOUT));
    let lock = opts
        .lock_timeout
        .map(millis)
        .or(parsed.lock_timeout)
        .unwrap_or_else(|| millis(DEFAULT_LOCK_TIMEOUT));
    options = options.options([
        ("statement_timeout", statement.as_str()),
        ("lock_timeout", lock.as_str()),
    ]);
    match (&opts.app_name, options.get_application_name()) {
        (Some(name), _) => options = options.application_name(name),
        (None, None) => options = options.application_name(DEFAULT_APP_NAME),
        (None, Some(_)) => {}
    }
    let max = match (opts.max_conns, parsed.pool_max_conns) {
        (0, Some(v)) => match v.trim().parse::<u32>() {
            Ok(n) if n > 0 && n <= i32::MAX as u32 => n,
            _ => return Err(bad_max_conns(WHAT, &v)),
        },
        (0, None) => DEFAULT_MAX_CONNS,
        (n, _) => n,
    };
    Ok(PgPoolOptions::new()
        .max_connections(max)
        .connect_lazy_with(options))
}

/// Returns the pool of the app configuration: `DATABASE_URL` and `DATABASE_MAX_CONNS`, with
/// the defaults of [`open`].
///
/// # Errors
///
/// The errors of [`open`].
pub fn pool(config: &Base) -> Result<PgPool, DbError> {
    open(
        &config.database_url,
        PoolOptions::default().max_conns(config.database_max_conns),
    )
}

fn bad_max_conns(what: &str, value: &str) -> DbError {
    DbError::new(
        what,
        format!("the maximum pool size {value} is not valid"),
        format!(
            "set DATABASE_MAX_CONNS to a positive number, or remove it to use the default of {DEFAULT_MAX_CONNS}"
        ),
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn db_parse_url_takes_the_kit_params() {
        let p = parse_url(
            "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conns=7&lock_timeout=750",
            "x",
        )
        .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(p.pool_max_conns.as_deref(), Some("7"));
        assert_eq!(p.lock_timeout.as_deref(), Some("750"));
        assert_eq!(p.statement_timeout, None);
        assert_eq!(p.options.get_database(), Some("app"));
    }

    #[test]
    fn db_millis() {
        assert_eq!(millis(Duration::from_millis(1500)), "1500");
        assert_eq!(millis(Duration::ZERO), "0");
    }
}
