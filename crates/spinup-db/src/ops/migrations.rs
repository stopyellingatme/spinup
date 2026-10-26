use super::*;
use crate::migrate::{Sub, app_command};
use crate::names::{MIGRATIONS_DIR, migration_parts, next_migration_name, sql_files};
use std::fs::OpenOptions;

/// Create a reversible migration pair, or print its paths in a dry run.
pub fn new(o: &mut Options<'_>, name: &str) -> anyhow::Result<()> {
    let what = "Cannot create the migration";
    let (root, _) = load(o, what)?;
    let dir = root.join(MIGRATIONS_DIR);
    let (up, down) = next_migration_name(&dir, name)?;
    if o.dry_run {
        return write_out(
            o,
            what,
            &format!("Would create {MIGRATIONS_DIR}/{up}\nWould create {MIGRATIONS_DIR}/{down}\n"),
        );
    }
    std::fs::create_dir_all(&dir).map_err(|e| op_error(what, e))?;
    // Reserve both files before writing. Never overwrite a file from another command.
    let mut up_file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(dir.join(&up))
        .map_err(|e| op_error(what, e))?;
    let result = (|| -> std::io::Result<()> {
        let mut down_file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(dir.join(&down))?;
        let result = up_file
            .write_all(b"-- Write the SQL for this migration here.\n")
            .and_then(|()| {
                down_file.write_all(b"-- Write the SQL that reverses the up migration here.\n")
            });
        if result.is_err() {
            let _ = std::fs::remove_file(dir.join(&down));
        }
        result
    })();
    if let Err(error) = result {
        let _ = std::fs::remove_file(dir.join(&up));
        return Err(op_error(what, error));
    }
    write_out(
        o,
        what,
        &format!("Created {MIGRATIONS_DIR}/{up}\nCreated {MIGRATIONS_DIR}/{down}\n"),
    )
}

/// Print kit migration status, then app migration status.
pub fn status(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> anyhow::Result<()> {
    crate::migrate::run(o, Sub::Status, runner, cancel)
}

/// Revert the latest app migration and apply it again.
/// Require acknowledgement and confirm protected environments first.
pub fn redo(o: &mut Options<'_>, runner: &dyn Runner, cancel: &Cancellation) -> anyhow::Result<()> {
    let what = "Cannot redo the migration";
    if !o.i_know {
        return Err(
            Error::new("This rolls back the last migration and can lose data")
                .why("redo requires explicit acknowledgement")
                .try_hint("spinup db redo --i-know")
                .into(),
        );
    }
    let (root, config) = load(o, what)?;
    guard_migration(o, &config, what, "db redo", true)?;
    // SQLx has no redo command. Restore precisely the former version, not all pending migrations.
    let current = if o.dry_run {
        None
    } else {
        Some(current_version(o, runner, cancel, &root, what)?)
    };
    let revert = database_cmd(o, &root, app_command(&root, "revert"), what)?;
    let mut apply = app_command(&root, "run");
    if let Some(version) = current {
        if version == 0 {
            return Ok(());
        }
        apply = apply.args(["--target-version".into(), version.to_string()]);
    }
    let apply = database_cmd(o, &root, apply, what)?;
    stream(o, runner, cancel, what, &revert)?;
    stream(o, runner, cancel, what, &apply)
}

/// Migrate only the app to a version. Guard moves that can lose data.
/// A dry run treats a target below the newest file as a rollback.
pub fn to(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
    version: &str,
) -> anyhow::Result<()> {
    let what = "Cannot migrate to the version";
    let target = version
        .parse::<i64>()
        .ok()
        .filter(|v| *v >= 0)
        .ok_or_else(|| {
            Error::new(what)
                .why(format!("{version:?} is not a migration version"))
                .try_hint("spinup db to <version>, for example spinup db to 1")
                .code(spinup_ui::exit::USAGE)
        })?;
    let (root, config) = load(o, what)?;
    let newest = sql_files(&root.join(MIGRATIONS_DIR))
        .map_err(|e| op_error(what, e))?
        .iter()
        .filter_map(|file| migration_parts(file).and_then(|(n, _)| n.parse::<i64>().ok()))
        .max()
        .unwrap_or(0);
    let mut down = target < newest;
    if down && !o.dry_run {
        down = target < current_version(o, runner, cancel, &root, what)?;
    }
    if down && !o.i_know {
        return Err(Error::new("This rolls back migrations and can lose data")
            .why("a move down requires explicit acknowledgement")
            .try_hint(format!("spinup db to {version} --i-know"))
            .into());
    }
    guard_migration(o, &config, what, &format!("db to {version}"), down)?;
    let cmd = app_command(&root, if down { "revert" } else { "run" })
        .args(["--target-version".into(), target.to_string()]);
    let cmd = database_cmd(o, &root, cmd, what)?;
    stream(o, runner, cancel, what, &cmd)
}

fn current_version(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
    root: &Path,
    what: &str,
) -> anyhow::Result<i64> {
    let cmd = database_cmd(o, root, app_command(root, "info"), what)?;
    let output = capture(o, runner, cancel, what, &cmd, false)?;
    let text = String::from_utf8_lossy(&output.stdout);
    let mut current = 0;
    for line in text.lines().filter(|line| !line.trim().is_empty()) {
        let Some((version, status)) = line.trim().split_once('/') else {
            return Err(Error::new(what)
                .why("sqlx migrate info did not print migration versions")
                .try_hint(SQLX_INSTALL_TRY)
                .into());
        };
        let version = version.parse::<i64>().map_err(|_| {
            Error::new(what)
                .why("sqlx migrate info did not print migration versions")
                .try_hint(SQLX_INSTALL_TRY)
        })?;
        if version < 0 || (!status.starts_with("installed ") && !status.starts_with("pending ")) {
            return Err(Error::new(what)
                .why("sqlx migrate info did not print migration versions")
                .try_hint(SQLX_INSTALL_TRY)
                .into());
        }
        if status.starts_with("installed ") {
            current = current.max(version);
        }
    }
    Ok(current)
}
