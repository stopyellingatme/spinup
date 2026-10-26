//! Sequential names for reversible SQLx migrations.

use std::path::Path;

/// The app migrations directory, relative to the project root.
pub const MIGRATIONS_DIR: &str = "api/migrations";
/// The seed directory, relative to the project root.
pub const SEEDS_DIR: &str = "api/seeds";

/// Return the next five-digit up and down filenames.
/// Reject invalid names, duplicate names, and exhausted numbers.
pub fn next_migration_name(dir: &Path, name: &str) -> anyhow::Result<(String, String)> {
    let what = "Cannot name the migration";
    if !regex::Regex::new(r"^[a-z][a-z0-9]*(_[a-z0-9]+)*$")?.is_match(name) {
        return Err(spinup_ui::error::Error::new(what)
            .why(format!("{name:?} is not snake case"))
            .try_hint("use lower-case words joined by underscores, for example add_widgets")
            .code(spinup_ui::exit::USAGE)
            .into());
    }
    let mut highest = 0;
    for file in sql_files(dir).map_err(|e| crate::ops::op_error(what, e))? {
        let Some((number, found)) = migration_parts(&file) else {
            continue;
        };
        if found == name {
            return Err(spinup_ui::error::Error::new(what)
                .why(format!("the migration {file} already has the name {name}"))
                .try_hint(format!("choose another name, or edit the migration {file}"))
                .into());
        }
        if number.len() == 5 {
            highest = highest.max(number.parse::<u32>()?);
        }
    }
    if highest >= 99999 {
        return Err(spinup_ui::error::Error::new(what)
            .why("the migration numbers reached 99999")
            .try_hint("squash old migrations into one file")
            .into());
    }
    let stem = format!("{:05}_{name}", highest + 1);
    Ok((format!("{stem}.up.sql"), format!("{stem}.down.sql")))
}

pub(crate) fn migration_parts(file: &str) -> Option<(&str, &str)> {
    let stem = file
        .strip_suffix(".up.sql")
        .or_else(|| file.strip_suffix(".down.sql"))?;
    let (number, name) = stem.split_once('_')?;
    (!number.is_empty() && number.bytes().all(|c| c.is_ascii_digit()) && !name.is_empty())
        .then_some((number, name))
}

pub(crate) fn sql_files(dir: &Path) -> std::io::Result<Vec<String>> {
    let entries = match std::fs::read_dir(dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(error),
    };
    let mut files = Vec::new();
    for entry in entries {
        let entry = entry?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if !entry.file_type()?.is_dir() && name.ends_with(".sql") {
            files.push(name);
        }
    }
    files.sort();
    Ok(files)
}

#[cfg(test)]
mod tests;
