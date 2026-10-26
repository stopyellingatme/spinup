//! The migrator (Go `kit/db/migrate.go`): the kit migrations, then the app migrations, on
//! one connection with migration timeouts, under the advisory lock of `sqlx migrate`.

use std::collections::{BTreeSet, HashMap};
use std::fmt;
use std::io::Write;
use std::sync::Arc;
use std::time::Duration;

use anyhow::Context as _;
use sqlx::migrate::{AppliedMigration, Migrate, Migration, Migrator};
use sqlx::postgres::PgConnectOptions;
use sqlx::{Connection, PgConnection};

use super::DbError;
use super::pool::{millis, parse_url};

/// The migrations table of the kit. The app keeps the sqlx default, `_sqlx_migrations`.
/// Two tables keep the version numbers of the kit and the app apart.
pub const KIT_MIGRATIONS_TABLE: &str = "_spinup_kit_migrations";

/// The default `lock_timeout` of migration connections.
pub const DEFAULT_MIGRATION_LOCK_TIMEOUT: Duration = Duration::from_secs(5);
/// The default `statement_timeout` of migration connections.
pub const DEFAULT_MIGRATION_STATEMENT_TIMEOUT: Duration = Duration::from_secs(5 * 60);
/// The `application_name` of migration connections when the URL sets none.
const MIGRATION_APP_NAME: &str = "spinup-migrate";

/// The advisory lock probe: one try each second, for up to 15 minutes. A plain
/// `pg_advisory_lock` would fail after `lock_timeout`.
const LOCK_PROBE: Duration = Duration::from_secs(1);
const LOCK_ATTEMPTS: u32 = 900;

const WHAT: &str = "db: prepare migrations";

/// One migration and its state. It is the type of the dev tools (`app::Migration`).
pub type MigrationStatus = crate::app::Migration;

/// Returns the migrator of the kit tables (the files in `crates/spinup-kit/migrations/`,
/// on the table [`KIT_MIGRATIONS_TABLE`]).
pub fn kit_migrator() -> Migrator {
    let mut m = sqlx::migrate!("./migrations");
    m.dangerous_set_table_name(KIT_MIGRATIONS_TABLE);
    m
}

/// Returns the name of a migration: the version with five digits, then the description
/// with `_` for each space, such as `00001_create_todos`.
pub fn migration_name(version: i64, description: &str) -> String {
    format!("{version:05}_{}", description.replace(' ', "_"))
}

fn name_of(m: &Migration) -> String {
    migration_name(m.version, &m.description)
}

/// The lookup of an environment variable.
type Lookup = Arc<dyn Fn(&str) -> Option<String> + Send + Sync>;

/// The migrations of an app (Go `db.Migrator`): the kit migrations and the app migrations.
/// Every operation opens one connection with `lock_timeout` 5 s, `statement_timeout` 5 min,
/// and `application_name` `spinup-migrate` (unless the URL sets it), and closes it at the
/// end. `up`, `down`, `redo`, and `to` hold the advisory lock of `sqlx migrate`, so two
/// runners never migrate one database at once.
///
/// `up` applies the kit migrations first, so app migrations can refer to kit tables.
/// `down`, `redo`, `to`, and `version` act on the app migrations only.
pub struct Migrations {
    kit: Migrator,
    app: Migrator,
    database_url: String,
    lookup: Lookup,
    lock_timeout: Duration,
    statement_timeout: Duration,
}

impl fmt::Debug for Migrations {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Migrations")
            .field("app", &self.app.iter().map(name_of).collect::<Vec<_>>())
            .field("lock_timeout", &self.lock_timeout)
            .field("statement_timeout", &self.statement_timeout)
            .finish_non_exhaustive()
    }
}

impl Migrations {
    /// Returns the migrations of `app` (usually `sqlx::migrate!("./migrations")`) plus the
    /// kit migrations. The database URL comes from [`Migrations::database_url`], else from
    /// `DATABASE_URL`.
    pub fn new(app: Migrator) -> Self {
        Self {
            kit: kit_migrator(),
            app,
            database_url: String::new(),
            lookup: Arc::new(|k| std::env::var(k).ok()),
            lock_timeout: DEFAULT_MIGRATION_LOCK_TIMEOUT,
            statement_timeout: DEFAULT_MIGRATION_STATEMENT_TIMEOUT,
        }
    }

    /// Sets the database URL (Go `WithDatabaseURL`). An empty URL reads `DATABASE_URL`.
    #[must_use]
    pub fn database_url(mut self, url: impl Into<String>) -> Self {
        self.database_url = url.into();
        self
    }

    /// Sets the lookup of `DATABASE_URL` (default: the process environment). Tests pass a
    /// map.
    #[must_use]
    pub fn lookup(mut self, f: impl Fn(&str) -> Option<String> + Send + Sync + 'static) -> Self {
        self.lookup = Arc::new(f);
        self
    }

    /// Sets `lock_timeout` of the migration connection (Go `WithMigrationLockTimeout`).
    #[must_use]
    pub fn lock_timeout(mut self, d: Duration) -> Self {
        self.lock_timeout = d;
        self
    }

    /// Sets `statement_timeout` of the migration connection (Go
    /// `WithMigrationStatementTimeout`).
    #[must_use]
    pub fn statement_timeout(mut self, d: Duration) -> Self {
        self.statement_timeout = d;
        self
    }

    /// Reports whether an app migration has the version `v`.
    pub fn has_version(&self, v: i64) -> bool {
        ups(&self.app).any(|m| m.version == v)
    }

    async fn connect(&self) -> anyhow::Result<PgConnection> {
        let url = if self.database_url.is_empty() {
            (self.lookup)("DATABASE_URL").unwrap_or_default()
        } else {
            self.database_url.clone()
        };
        let options = connect_options(&url, self.lock_timeout, self.statement_timeout)?;
        connect(&options).await
    }

    /// Returns every kit migration, then every app migration, in version order, with its
    /// state.
    ///
    /// # Errors
    ///
    /// A connection or query error.
    pub async fn status(&self) -> anyhow::Result<Vec<MigrationStatus>> {
        let mut conn = self.connect().await?;
        let mut list = Vec::new();
        for m in [&self.kit, &self.app] {
            let done = applied_versions(&mut conn, m).await?;
            list.extend(ups(m).map(|x| MigrationStatus {
                name: name_of(x),
                applied: done.contains(&x.version),
            }));
        }
        close(conn).await;
        Ok(list)
    }

    /// Returns the names of the migrations that are not applied, kit migrations first.
    ///
    /// # Errors
    ///
    /// A connection or query error.
    pub async fn pending(&self) -> anyhow::Result<Vec<String>> {
        Ok(self
            .status()
            .await?
            .into_iter()
            .filter(|s| !s.applied)
            .map(|s| s.name)
            .collect())
    }

    /// Returns the highest applied app migration version, or 0 when none is applied.
    ///
    /// # Errors
    ///
    /// A connection or query error.
    pub async fn version(&self) -> anyhow::Result<i64> {
        let mut conn = self.connect().await?;
        let v = current_version(&mut conn, &self.app).await?;
        close(conn).await;
        Ok(v)
    }

    /// Applies every pending migration, kit migrations first. It writes the plan first,
    /// `migrate: no pending migrations` or `migrate: N pending`, and then one
    /// `applied  <name>` line for each migration. When another runner applied them while
    /// this one waited on the lock, it writes `migrate: another runner applied them`.
    ///
    /// # Errors
    ///
    /// A connection, lock, or migration error. The lines of the migrations that it applied
    /// come first.
    pub async fn up(&self, out: &mut (dyn Write + Send)) -> anyhow::Result<()> {
        let mut conn = self.connect().await?;
        let result = run_up(&mut conn, &[&self.kit, &self.app], out).await;
        close(conn).await;
        result
    }

    /// Rolls back the most recent app migration. It writes `rolled back  <name>`, or
    /// `migrate: nothing to roll back`.
    ///
    /// # Errors
    ///
    /// A connection, lock, or migration error, or an app migration without a down file.
    pub async fn down(&self, out: &mut (dyn Write + Send)) -> anyhow::Result<()> {
        let (mut conn, id) = self.connect_locked().await?;
        let result = async {
            let conn = &mut conn;
            validate(conn, &self.app).await?;
            let Some(last) = current_applied(conn, &self.app).await? else {
                writeln!(out, "migrate: nothing to roll back")?;
                return Ok(());
            };
            revert(conn, &self.app, last, out).await
        }
        .await;
        release(conn, id).await;
        result
    }

    /// Rolls back the most recent app migration and applies it again. It writes
    /// `rolled back  <name>` and `applied  <name>`, or `migrate: nothing to redo`.
    ///
    /// # Errors
    ///
    /// A connection, lock, or migration error.
    pub async fn redo(&self, out: &mut (dyn Write + Send)) -> anyhow::Result<()> {
        let (mut conn, id) = self.connect_locked().await?;
        let result = async {
            let conn = &mut conn;
            validate(conn, &self.app).await?;
            let Some(last) = current_applied(conn, &self.app).await? else {
                writeln!(out, "migrate: nothing to redo")?;
                return Ok(());
            };
            revert(conn, &self.app, last, out).await?;
            apply_pending(conn, &self.kit, i64::MAX, out).await?;
            apply_pending(conn, &self.app, last, out).await?;
            Ok(())
        }
        .await;
        release(conn, id).await;
        result
    }

    /// Moves the app migrations up or down to the version `target`. It writes one
    /// `applied  <name>` or `rolled back  <name>` line for each migration, or
    /// `migrate: already at version <target>`. A move up applies the pending kit migrations
    /// first.
    ///
    /// # Errors
    ///
    /// No app migration has the version `target` (0 is always valid), or a connection,
    /// lock, or migration error.
    pub async fn to(&self, target: i64, out: &mut (dyn Write + Send)) -> anyhow::Result<()> {
        if target != 0 && !self.has_version(target) {
            return Err(DbError::new(
                "migrate to",
                format!("no migration file has version {target}"),
                "migrate status (it lists the migration files), then migrate to <version>",
            )
            .into());
        }
        let (mut conn, id) = self.connect_locked().await?;
        let result = async {
            let conn = &mut conn;
            validate(conn, &self.app).await?;
            let current = current_version(conn, &self.app).await?;
            if target > current {
                apply_pending(conn, &self.kit, i64::MAX, out).await?;
                apply_pending(conn, &self.app, target, out).await?;
            } else if target < current {
                let done = applied_versions(conn, &self.app).await?;
                let mut above: Vec<i64> = ups(&self.app)
                    .map(|m| m.version)
                    .filter(|v| *v > target && done.contains(v))
                    .collect();
                above.sort_unstable_by(|a, b| b.cmp(a));
                for v in above {
                    revert(conn, &self.app, v, out).await?;
                }
            } else {
                writeln!(out, "migrate: already at version {target}")?;
            }
            Ok(())
        }
        .await;
        release(conn, id).await;
        result
    }

    /// Opens a connection and takes the migration lock.
    async fn connect_locked(&self) -> anyhow::Result<(PgConnection, i64)> {
        let mut conn = self.connect().await?;
        match lock(&mut conn).await {
            Ok(id) => Ok((conn, id)),
            Err(e) => {
                close(conn).await;
                Err(e)
            }
        }
    }
}

/// Releases the migration lock and closes the connection.
async fn release(mut conn: PgConnection, id: i64) {
    unlock(&mut conn, id).await;
    close(conn).await;
}

/// Returns the connect options of a migration connection.
pub(crate) fn connect_options(
    url: &str,
    lock_timeout: Duration,
    statement_timeout: Duration,
) -> Result<PgConnectOptions, DbError> {
    let parsed = parse_url(url, WHAT)?;
    let mut options = parsed.options.options([
        ("lock_timeout", millis(lock_timeout).as_str()),
        ("statement_timeout", millis(statement_timeout).as_str()),
    ]);
    if options.get_application_name().is_none() {
        options = options.application_name(MIGRATION_APP_NAME);
    }
    Ok(options)
}

async fn connect(options: &PgConnectOptions) -> anyhow::Result<PgConnection> {
    PgConnection::connect_with(options).await.map_err(|e| {
        DbError::new(
            "db: connect to the database for migrations",
            e.to_string(),
            "check DATABASE_URL, and check that the database runs",
        )
        .with_source(e)
        .into()
    })
}

async fn close(conn: PgConnection) {
    let _ = conn.close().await;
}

/// Applies the pending kit migrations and then the pending migrations of `app` to the
/// database at `url`, with the default migration timeouts. The testkit builds its template
/// databases with it.
#[cfg(feature = "testkit")]
pub(crate) async fn up_all(
    url: &str,
    app: &Migrator,
    out: &mut (dyn Write + Send),
) -> anyhow::Result<()> {
    let options = connect_options(
        url,
        DEFAULT_MIGRATION_LOCK_TIMEOUT,
        DEFAULT_MIGRATION_STATEMENT_TIMEOUT,
    )?;
    let mut conn = connect(&options).await?;
    let kit = kit_migrator();
    let result = run_up(&mut conn, &[&kit, app], out).await;
    close(conn).await;
    result
}

async fn run_up(
    conn: &mut PgConnection,
    migrators: &[&Migrator],
    out: &mut (dyn Write + Send),
) -> anyhow::Result<()> {
    let mut pending = 0;
    for m in migrators {
        let done = applied_versions(conn, m).await?;
        pending += ups(m).filter(|x| !done.contains(&x.version)).count();
    }
    if pending == 0 {
        writeln!(out, "migrate: no pending migrations")?;
        return Ok(());
    }
    writeln!(out, "migrate: {pending} pending")?;
    let id = lock(conn).await?;
    let result = async {
        let mut applied = 0;
        for m in migrators {
            validate(conn, m).await?;
            applied += apply_pending(conn, m, i64::MAX, out).await?;
        }
        if applied == 0 {
            writeln!(out, "migrate: another runner applied them")?;
        }
        Ok(())
    }
    .await;
    unlock(conn, id).await;
    result
}

/// The up migrations of a migrator, in version order.
fn ups(m: &Migrator) -> impl Iterator<Item = &Migration> {
    m.iter().filter(|x| x.migration_type.is_up_migration())
}

/// Reports whether the migrations table exists. A read never creates it.
async fn table_exists(conn: &mut PgConnection, table: &str) -> anyhow::Result<bool> {
    let exists: bool = sqlx::query_scalar("SELECT to_regclass($1) IS NOT NULL")
        .bind(table)
        .fetch_one(&mut *conn)
        .await
        .with_context(|| format!("db: read the migrations table {table}"))?;
    Ok(exists)
}

async fn applied(conn: &mut PgConnection, m: &Migrator) -> anyhow::Result<Vec<AppliedMigration>> {
    if !table_exists(conn, &m.table_name).await? {
        return Ok(Vec::new());
    }
    conn.list_applied_migrations(&m.table_name)
        .await
        .with_context(|| format!("db: read the migrations table {}", m.table_name))
}

async fn applied_versions(conn: &mut PgConnection, m: &Migrator) -> anyhow::Result<BTreeSet<i64>> {
    Ok(applied(conn, m)
        .await?
        .into_iter()
        .map(|a| a.version)
        .collect())
}

/// The highest applied version, or 0.
async fn current_version(conn: &mut PgConnection, m: &Migrator) -> anyhow::Result<i64> {
    Ok(applied_versions(conn, m)
        .await?
        .last()
        .copied()
        .unwrap_or(0))
}

/// The highest applied version that has a migration file.
async fn current_applied(conn: &mut PgConnection, m: &Migrator) -> anyhow::Result<Option<i64>> {
    let done = applied_versions(conn, m).await?;
    Ok(ups(m).map(|x| x.version).filter(|v| done.contains(v)).max())
}

/// Fails on a dirty database, a changed migration file, or an applied migration that has
/// no file.
async fn validate(conn: &mut PgConnection, m: &Migrator) -> anyhow::Result<()> {
    let table = &m.table_name;
    if !table_exists(conn, table).await? {
        return Ok(());
    }
    if let Some(v) = conn
        .dirty_version(table)
        .await
        .with_context(|| format!("db: read the migrations table {table}"))?
    {
        return Err(DbError::new(
            "db: migrate",
            format!("migration {v} failed before and left the database dirty"),
            format!("repair the database by hand, then delete the row of version {v} from {table}"),
        )
        .into());
    }
    let files: HashMap<i64, &Migration> = ups(m).map(|x| (x.version, x)).collect();
    for a in applied(conn, m).await? {
        match files.get(&a.version) {
            Some(f) if f.checksum != a.checksum => {
                return Err(DbError::new(
                    "db: migrate",
                    format!("the migration {} changed after it was applied", name_of(f)),
                    "restore the applied file, and put the change in a new migration",
                )
                .into());
            }
            None if !m.ignore_missing => {
                return Err(DbError::new(
                    "db: migrate",
                    format!(
                        "the database has migration {} of {table}, but no migration file has that version",
                        a.version
                    ),
                    "restore the migration file, or run the binary that has it",
                )
                .into());
            }
            _ => {}
        }
    }
    Ok(())
}

/// Applies the pending migrations of `m` up to and including `target`, in version order.
/// It returns the number of migrations that it applied.
async fn apply_pending(
    conn: &mut PgConnection,
    m: &Migrator,
    target: i64,
    out: &mut (dyn Write + Send),
) -> anyhow::Result<usize> {
    let table = &m.table_name;
    conn.ensure_migrations_table(table)
        .await
        .with_context(|| format!("db: create the migrations table {table}"))?;
    let done = applied_versions(conn, m).await?;
    let mut n = 0;
    for x in ups(m).filter(|x| x.version <= target && !done.contains(&x.version)) {
        conn.apply(table, x)
            .await
            .with_context(|| format!("db: apply migration {}", name_of(x)))?;
        writeln!(out, "applied  {}", name_of(x))?;
        n += 1;
    }
    Ok(n)
}

/// Reverts the applied migration `version` of `m`.
async fn revert(
    conn: &mut PgConnection,
    m: &Migrator,
    version: i64,
    out: &mut (dyn Write + Send),
) -> anyhow::Result<()> {
    let up = ups(m)
        .find(|x| x.version == version)
        .with_context(|| format!("db: migrate: no migration file has version {version}"))?;
    let Some(down) = m
        .iter()
        .find(|x| x.version == version && x.migration_type.is_down_migration())
    else {
        return Err(DbError::new(
            "db: roll back migration",
            format!("the migration {} has no down file", name_of(up)),
            format!(
                "add migrations/{}.down.sql, or roll it back by hand",
                name_of(up)
            ),
        )
        .into());
    };
    conn.revert(&m.table_name, down)
        .await
        .with_context(|| format!("db: roll back migration {}", name_of(up)))?;
    writeln!(out, "rolled back  {}", name_of(up))?;
    Ok(())
}

/// Takes the advisory lock of `sqlx migrate` (the same key, so `spinup db migrate` and the
/// app binary never run at once). It returns the key.
async fn lock(conn: &mut PgConnection) -> anyhow::Result<i64> {
    let database: String = sqlx::query_scalar("SELECT current_database()")
        .fetch_one(&mut *conn)
        .await
        .context("db: read the database name")?;
    let id = lock_id(&database);
    for _ in 0..LOCK_ATTEMPTS {
        let got: bool = sqlx::query_scalar("SELECT pg_try_advisory_lock($1)")
            .bind(id)
            .fetch_one(&mut *conn)
            .await
            .context("db: take the migration lock")?;
        if got {
            return Ok(id);
        }
        tokio::time::sleep(LOCK_PROBE).await;
    }
    Err(DbError::new(
        "db: take the migration lock",
        "another runner held the lock for 15 minutes",
        "look for a migrate process that does not end (pg_locks), then run migrate again",
    )
    .into())
}

async fn unlock(conn: &mut PgConnection, id: i64) {
    if let Err(e) = sqlx::query("SELECT pg_advisory_unlock($1)")
        .bind(id)
        .execute(&mut *conn)
        .await
    {
        tracing::warn!(error = %e, "db: release the migration lock failed");
    }
}

/// The lock key of `sqlx migrate`: CRC-32 (ISO-HDLC) of the database name times
/// `0x3d32ad9e` (`sqlx-postgres` `generate_lock_id`).
fn lock_id(database: &str) -> i64 {
    0x3d32_ad9e * i64::from(crc32(database.as_bytes()))
}

fn crc32(data: &[u8]) -> u32 {
    let mut crc = !0u32;
    for &b in data {
        crc ^= u32::from(b);
        for _ in 0..8 {
            let mask = (crc & 1).wrapping_neg();
            crc = (crc >> 1) ^ (0xEDB8_8320 & mask);
        }
    }
    !crc
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn db_crc32_matches_the_check_value() {
        // The check value of CRC-32/ISO-HDLC.
        assert_eq!(crc32(b"123456789"), 0xCBF4_3926);
        assert_eq!(lock_id("app"), 0x3d32_ad9e * i64::from(crc32(b"app")));
    }

    #[test]
    fn db_migration_name_has_five_digits() {
        assert_eq!(migration_name(1, "create todos"), "00001_create_todos");
        assert_eq!(migration_name(20261025120000, "x"), "20261025120000_x");
    }

    #[test]
    fn db_kit_migrator_uses_the_kit_table() {
        let m = kit_migrator();
        assert_eq!(m.table_name, KIT_MIGRATIONS_TABLE);
        assert!(m.iter().any(|x| x.version == 1));
    }

    #[test]
    fn db_connect_options_set_the_migration_name() {
        let o = connect_options(
            "postgres://u:p@h/app",
            DEFAULT_MIGRATION_LOCK_TIMEOUT,
            DEFAULT_MIGRATION_STATEMENT_TIMEOUT,
        )
        .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(o.get_application_name(), Some(MIGRATION_APP_NAME));
        let o = connect_options(
            "postgres://u:p@h/app?application_name=mine",
            DEFAULT_MIGRATION_LOCK_TIMEOUT,
            DEFAULT_MIGRATION_STATEMENT_TIMEOUT,
        )
        .unwrap_or_else(|e| panic!("{e}"));
        assert_eq!(o.get_application_name(), Some("mine"));
    }
}
