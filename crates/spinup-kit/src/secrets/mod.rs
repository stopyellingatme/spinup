//! Secret values from `NAME_FILE` or `NAME` (Go `kit/secrets`).
//!
//! `NAME_FILE` wins over `NAME`. The loader removes one final newline (`\n` or `\r\n`). An
//! error never holds the secret value.

use std::io;
use std::path::Path;

/// Reads the secret `name` from the process environment: the file that `name_FILE` names,
/// else the variable `name` (Go `secrets.Load`).
pub fn load(name: &str) -> anyhow::Result<String> {
    load_from(
        name,
        |key| std::env::var_os(key).map(|v| v.to_string_lossy().into_owned()),
        |path| std::fs::read(path),
    )
}

/// Reads the secret `name` with `lookup` and `read_file`. Tests use it.
///
/// An unreadable `name_FILE` is an error. It does not fall back to `name`.
pub fn load_from(
    name: &str,
    lookup: impl Fn(&str) -> Option<String>,
    read_file: impl Fn(&Path) -> io::Result<Vec<u8>>,
) -> anyhow::Result<String> {
    if let Some(path) = lookup(&format!("{name}_FILE")) {
        let data = read_file(Path::new(&path)).map_err(|_| {
            anyhow::anyhow!(
                "read secret {name}: cannot read {name}_FILE\nTry: check the secret file and its permissions"
            )
        })?;
        return Ok(trim_newline(&String::from_utf8_lossy(&data)));
    }
    if let Some(value) = lookup(name) {
        return Ok(trim_newline(&value));
    }
    anyhow::bail!(
        "read secret {name}: no file or environment value is set\nTry: set {name}_FILE or {name}"
    )
}

/// Returns [`load`]`(name)`, or panics with an error that holds no secret value (Go
/// `secrets.Must`).
///
/// # Panics
///
/// Panics when the secret is not set or its file cannot be read.
pub fn must(name: &str) -> String {
    match load(name) {
        Ok(v) => v,
        Err(e) => panic!("{e}"),
    }
}

fn trim_newline(value: &str) -> String {
    match value.strip_suffix('\n') {
        Some(rest) => rest.strip_suffix('\r').unwrap_or(rest).to_string(),
        None => value.to_string(),
    }
}
