//! The database pool, the error mapping, and the `migrate` command, as the
//! Go `kit/db` package.

use std::collections::HashSet;
use std::io::Write;

use axum::http::StatusCode;
use sqlx::PgPool;
use sqlx::migrate::{Migrate, Migrator};
use sqlx::postgres::{PgConnectOptions, PgPoolOptions};

use crate::config::Config;
use crate::problem::{ApiError, Problem};

/// The default pool size (Go `db.DefaultMaxConns`).
pub const DEFAULT_MAX_CONNS: u32 = 20;

/// The migrations table of the kit. The app keeps the sqlx default,
/// `_sqlx_migrations`. Two tables keep the version numbers of the kit and
/// the app apart.
pub const KIT_MIGRATIONS_TABLE: &str = "_spinup_kit_migrations";

/// Returns the migrator of the kit tables (the job queue).
pub fn kit_migrator() -> Migrator {
    let mut m = sqlx::migrate!("./migrations");
    m.dangerous_set_table_name(KIT_MIGRATIONS_TABLE);
    m
}

/// A database setup error with a `Try:` line.
#[derive(Debug)]
pub struct DbError(pub String);

impl std::fmt::Display for DbError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for DbError {}

/// Opens a lazy pool from `DATABASE_URL`. It does not connect, so the API
/// can serve `/healthz` while the database is down. Each connection gets
/// `statement_timeout` 30 s, `lock_timeout` 5 s, and `application_name`
/// `spinup-app` unless the URL sets it.
pub fn pool(cfg: &Config) -> Result<PgPool, DbError> {
    if cfg.database_url.is_empty() {
        return Err(DbError(
            "db: cannot open the database pool: DATABASE_URL is empty\nTry: set DATABASE_URL to a postgres:// URL".into(),
        ));
    }
    let mut opts: PgConnectOptions = cfg.database_url.parse().map_err(|_| {
        DbError("db: cannot open the database pool: DATABASE_URL is not a valid URL\nTry: check DATABASE_URL".into())
    })?;
    if opts.get_application_name().is_none() {
        opts = opts.application_name("spinup-app");
    }
    opts = opts.options([("statement_timeout", "30000"), ("lock_timeout", "5000")]);
    let max = match cfg.database_max_conns {
        0 => DEFAULT_MAX_CONNS,
        n => n,
    };
    Ok(PgPoolOptions::new()
        .max_connections(max)
        .connect_lazy_with(opts))
}

/// Maps a database error to the problem of the Go `ResponseError`. `delete`
/// selects the answer to a foreign key violation: 409 for a delete, 422
/// otherwise. A 500 is logged.
pub fn problem_from(err: &sqlx::Error, delete: bool) -> Problem {
    use StatusCode as S;
    if matches!(err, sqlx::Error::RowNotFound) {
        return *ApiError::not_found().0;
    }
    if let sqlx::Error::Database(db) = err {
        let code = db.code().unwrap_or_default();
        let p = match code.as_ref() {
            "23505" => Some(Problem::new(
                S::CONFLICT,
                "A record with the same unique value already exists.",
            )),
            "23503" if delete => Some(Problem::new(
                S::CONFLICT,
                "Other records refer to this record.",
            )),
            "23503" => Some(Problem::new(
                S::UNPROCESSABLE_ENTITY,
                "A referenced record does not exist.",
            )),
            "23502" | "23514" => Some(Problem::new(
                S::UNPROCESSABLE_ENTITY,
                "A value breaks a rule of the database.",
            )),
            c if c.starts_with("22") => Some(Problem::new(
                S::UNPROCESSABLE_ENTITY,
                "A value has an invalid format or is out of range.",
            )),
            _ => None,
        };
        if let Some(p) = p {
            return p;
        }
    }
    tracing::error!(error = %err, "request failed");
    Problem::status(S::INTERNAL_SERVER_ERROR)
}

impl From<sqlx::Error> for ApiError {
    fn from(err: sqlx::Error) -> Self {
        ApiError::from(problem_from(&err, false))
    }
}

/// Maps the error of a delete operation.
pub fn on_delete(err: sqlx::Error) -> ApiError {
    ApiError::from(problem_from(&err, true))
}

/// The usage of the migrate command.
pub const MIGRATE_USAGE: &str = "migrate up | down | status | version";

fn file_name(version: i64, description: &str) -> String {
    format!("{version:05}_{}", description.replace(' ', "_"))
}

async fn applied(pool: &PgPool, m: &Migrator) -> anyhow::Result<HashSet<i64>> {
    let mut conn = pool.acquire().await?;
    conn.ensure_migrations_table(&m.table_name).await?;
    Ok(conn
        .list_applied_migrations(&m.table_name)
        .await?
        .into_iter()
        .map(|a| a.version)
        .collect())
}

fn ups(m: &Migrator) -> impl Iterator<Item = &sqlx::migrate::Migration> {
    m.iter().filter(|x| x.migration_type.is_up_migration())
}

/// Runs the `migrate` command. The kit migrations run first, so app
/// migrations can refer to kit tables. The output has the line format of the
/// Go `db.MigrateCommand`.
pub async fn migrate_command(
    pool: &PgPool,
    app: &Migrator,
    sub: &str,
    out: &mut (dyn Write + Send),
) -> anyhow::Result<()> {
    let kit = kit_migrator();
    match sub {
        "up" => {
            let mut pending = Vec::new();
            for m in [&kit, app] {
                let done = applied(pool, m).await?;
                pending.extend(
                    ups(m)
                        .filter(|x| !done.contains(&x.version))
                        .map(|x| file_name(x.version, &x.description)),
                );
            }
            if pending.is_empty() {
                writeln!(out, "migrate: no pending migrations")?;
                return Ok(());
            }
            writeln!(out, "migrate: {} pending", pending.len())?;
            kit.run(pool).await?;
            app.run(pool).await?;
            for p in pending {
                writeln!(out, "applied  {p}")?;
            }
        }
        "down" => {
            let done = applied(pool, app).await?;
            let mut versions: Vec<i64> = ups(app)
                .map(|x| x.version)
                .filter(|v| done.contains(v))
                .collect();
            versions.sort_unstable();
            let Some(last) = versions.pop() else {
                writeln!(out, "migrate: nothing to roll back")?;
                return Ok(());
            };
            app.undo(pool, versions.last().copied().unwrap_or(0))
                .await?;
            let m = ups(app).find(|x| x.version == last).expect("applied");
            writeln!(out, "rolled back  {}", file_name(m.version, &m.description))?;
        }
        "status" => {
            for m in [&kit, app] {
                let done = applied(pool, m).await?;
                for x in ups(m) {
                    let state = if done.contains(&x.version) {
                        "applied"
                    } else {
                        "pending"
                    };
                    writeln!(out, "{state}  {}", file_name(x.version, &x.description))?;
                }
            }
        }
        "version" => {
            let done = applied(pool, app).await?;
            writeln!(out, "{}", done.into_iter().max().unwrap_or(0))?;
        }
        _ => {
            let why = if sub.is_empty() {
                "no subcommand given".to_owned()
            } else {
                format!("unknown subcommand {sub:?}")
            };
            anyhow::bail!(
                "migrate: {why} (want up, down, status, or version)\nTry: {MIGRATE_USAGE}"
            );
        }
    }
    Ok(())
}
