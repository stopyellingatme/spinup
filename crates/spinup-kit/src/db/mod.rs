//! The database pool, transactions, migrations, and the `migrate` command (Go `kit/db`).
//!
//! - [`open`] and [`pool`] return a lazy [`PgPool`] with the Go defaults: at most 20
//!   connections, `statement_timeout` 30 s, `lock_timeout` 5 s, and `application_name`
//!   `spinup-app`.
//! - [`in_tx`] runs a closure in a transaction.
//! - [`Migrations`] runs the kit migrations (table [`KIT_MIGRATIONS_TABLE`]) and then the app
//!   migrations (table `_sqlx_migrations`). [`migrate_command`] makes the app command
//!   `migrate` with the Go output lines.
//! - [`problem_from`] and `From<sqlx::Error> for ApiError` map database errors to problem
//!   answers.
//! - [`readiness`], [`migration_status`], and [`migration_version`] feed `/readyz`, the dev
//!   tools, and `deployx`.
//!
//! ```no_run
//! use spinup_kit::app::App;
//! use spinup_kit::config::Base;
//! use spinup_kit::db::{self, Migrations};
//!
//! # async fn start(config: Base) -> anyhow::Result<App> {
//! let pool = db::pool(&config)?;
//! let migrations = Migrations::new(sqlx::migrate!("./migrations"))
//!     .database_url(&config.database_url);
//! Ok(App::new(&config)
//!     .readiness("db", db::readiness(pool.clone()))
//!     .command("migrate", db::migrate_command(migrations)))
//! # }
//! ```

mod command;
mod errors;
mod health;
mod migrate;
mod pool;
mod tx;

pub use command::{MIGRATE_USAGE, migrate_command};
pub use errors::{DbError, on_delete, problem_from};
pub use health::{migration_status, migration_version, readiness};
pub use migrate::{
    DEFAULT_MIGRATION_LOCK_TIMEOUT, DEFAULT_MIGRATION_STATEMENT_TIMEOUT, KIT_MIGRATIONS_TABLE,
    MigrationStatus, Migrations, kit_migrator, migration_name,
};
pub use pool::{
    DEFAULT_APP_NAME, DEFAULT_LOCK_TIMEOUT, DEFAULT_MAX_CONNS, DEFAULT_STATEMENT_TIMEOUT,
    PoolOptions, open, pool,
};
pub use sqlx::PgPool;
pub use sqlx::migrate::Migrator;
pub use tx::in_tx;

#[cfg(feature = "testkit")]
pub(crate) use migrate::up_all;
