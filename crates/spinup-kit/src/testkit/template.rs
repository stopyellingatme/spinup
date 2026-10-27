//! Template databases for tests (Go `kit/testkit/template.go`).
//!
//! A template is named for a hash of the kit migrations and the app migrations (version,
//! kind, description, and checksum). A finished template stays on the server, so later test
//! runs and other test processes reuse it. Each test database clones it with
//! `CREATE DATABASE ... TEMPLATE`, which is fast.

use std::collections::BTreeSet;
use std::sync::Mutex;

use sha2::{Digest, Sha256};
use sqlx::migrate::Migrator;
use sqlx::{AssertSqlSafe, Connection, PgConnection};

use super::db::{admin, quote, with_database};
use crate::db::kit_migrator;

/// Changes the template names when the build steps change.
const TEMPLATE_SALT: &str = "spinup-testkit-template-rs-v1";

/// The templates that this process built or found.
static READY: Mutex<BTreeSet<String>> = Mutex::new(BTreeSet::new());

/// Returns the hex SHA-256 of the kit migrations and the migrations of `app`, in version
/// order. Each field has a length prefix, so field boundaries cannot shift.
pub fn migrations_hash(app: &Migrator) -> String {
    let mut h = Sha256::new();
    field(&mut h, TEMPLATE_SALT.as_bytes());
    for m in [&kit_migrator(), app] {
        field(&mut h, m.table_name.as_bytes());
        for x in m.iter() {
            field(&mut h, &x.version.to_be_bytes());
            field(&mut h, x.migration_type.label().as_bytes());
            field(&mut h, x.description.as_bytes());
            field(&mut h, &x.checksum);
        }
    }
    hex::encode(h.finalize())
}

/// Returns the name of the template database of `app`: `tpl_` and 24 hex digits of
/// [`migrations_hash`].
pub fn template_name(app: &Migrator) -> String {
    format!("tpl_{}", &migrations_hash(app)[..24])
}

fn field(h: &mut Sha256, b: &[u8]) {
    h.update((b.len() as u64).to_be_bytes());
    h.update(b);
}

/// Returns the name of the template of `app`, and builds the template when the server does
/// not have it.
///
/// # Panics
///
/// When the build fails.
pub(crate) async fn ensure(server: &str, app: &Migrator) -> String {
    let hash = migrations_hash(app);
    let name = format!("tpl_{}", &hash[..24]);
    if READY.lock().is_ok_and(|r| r.contains(&name)) {
        return name;
    }
    if let Err(e) = build(server, &name, &hash, app).await {
        panic!("testkit: build template database {name}: {e}");
    }
    if let Ok(mut r) = READY.lock() {
        r.insert(name.clone());
    }
    name
}

/// Creates the template database `name` when it does not exist. An advisory lock keeps
/// other tests and processes from building the same template at the same time. The build
/// happens in a scratch database that is renamed at the end, so a template that exists is
/// always complete.
async fn build(server: &str, name: &str, hash: &str, app: &Migrator) -> Result<(), String> {
    let mut conn = admin(server).await?;
    let mut key = [0u8; 8];
    let raw = hex::decode(&hash[..16]).map_err(|e| e.to_string())?;
    key.copy_from_slice(&raw);
    let lock_id = i64::from_be_bytes(key);
    let result = async {
        sqlx::query("SELECT pg_advisory_lock($1)")
            .bind(lock_id)
            .execute(&mut conn)
            .await
            .map_err(|e| format!("lock: {e}"))?;
        let built = build_locked(&mut conn, server, name, app).await;
        let _ = sqlx::query("SELECT pg_advisory_unlock($1)")
            .bind(lock_id)
            .execute(&mut conn)
            .await;
        built
    }
    .await;
    let _ = conn.close().await;
    result
}

async fn build_locked(
    conn: &mut PgConnection,
    server: &str,
    name: &str,
    app: &Migrator,
) -> Result<(), String> {
    let exists: bool =
        sqlx::query_scalar("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)")
            .bind(name)
            .fetch_one(&mut *conn)
            .await
            .map_err(|e| e.to_string())?;
    if exists {
        return Ok(());
    }
    let scratch = format!("{name}_build");
    exec(
        conn,
        format!("DROP DATABASE IF EXISTS {} WITH (FORCE)", quote(&scratch)),
    )
    .await?;
    exec(conn, format!("CREATE DATABASE {}", quote(&scratch))).await?;
    let url = with_database(server, &scratch)?;
    crate::db::up_all(&url, app, &mut std::io::sink())
        .await
        .map_err(|e| format!("{e:#}"))?;
    // Disconnect any session that is left on the scratch database, then publish it.
    sqlx::query(
        "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()",
    )
    .bind(&scratch)
    .execute(&mut *conn)
    .await
    .map_err(|e| e.to_string())?;
    exec(
        conn,
        format!(
            "ALTER DATABASE {} RENAME TO {}",
            quote(&scratch),
            quote(name)
        ),
    )
    .await?;
    exec(
        conn,
        format!(
            "ALTER DATABASE {} WITH IS_TEMPLATE true ALLOW_CONNECTIONS false",
            quote(name)
        ),
    )
    .await
}

async fn exec(conn: &mut PgConnection, sql: String) -> Result<(), String> {
    sqlx::query(AssertSqlSafe(sql))
        .execute(&mut *conn)
        .await
        .map(|_| ())
        .map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use std::borrow::Cow;

    use sqlx::SqlSafeStr;
    use sqlx::migrate::{Migration, MigrationType};

    use super::*;

    fn migrator(sql: &'static str) -> Migrator {
        Migrator::with_migrations(vec![Migration::new(
            1,
            Cow::Borrowed("todos"),
            MigrationType::Simple,
            sql.into_sql_str(),
            false,
        )])
    }

    #[test]
    fn testkit_template_name_follows_the_migrations() {
        let a = template_name(&migrator("CREATE TABLE a (id int)"));
        let b = template_name(&migrator("CREATE TABLE b (id int)"));
        assert!(a.starts_with("tpl_") && a.len() == 28, "{a}");
        assert_ne!(a, b);
        assert_eq!(a, template_name(&migrator("CREATE TABLE a (id int)")));
    }
}
