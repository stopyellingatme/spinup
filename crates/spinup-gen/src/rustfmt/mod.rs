//! Plan C: format staged Rust files before the project apply step.

mod modules;
#[cfg(test)]
mod tests;

use std::path::{Component, Path, PathBuf};

use anyhow::{anyhow, bail};
use spinup_exec::runner::{Cancellation, Cmd, Runner};
use spinup_vfs::plan::Plan;

/// Formats every planned `.rs` file and returns the formatter name and version.
///
/// The command runs in `api/` when that directory exists. Only the staging directory changes.
/// A plan without Rust files returns `None` and runs no command.
///
/// # Errors
/// Returns an error with a `Try:` line for a staging, tool, or output failure.
#[allow(
    clippy::disallowed_methods,
    reason = "rustfmt staging directory, not the project"
)]
pub fn format(
    root: &Path,
    plan: &mut Plan,
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> anyhow::Result<Option<String>> {
    let mut files: Vec<_> = plan
        .files
        .iter()
        .enumerate()
        .filter(|(_, f)| !f.delete && f.path.ends_with(".rs"))
        .collect();
    files.sort_by_key(|(_, f)| &f.path);
    if files.is_empty() {
        return Ok(None);
    }
    let stage = tempfile::tempdir().map_err(|e| failure("create rustfmt staging directory", e))?;
    for (_, file) in &files {
        spinup_vfs::path::clean_path(&file.path)?;
        let path = stage.path().join(&file.path);
        std::fs::create_dir_all(path.parent().unwrap())
            .map_err(|e| failure("create rustfmt staging parent", e))?;
        std::fs::write(&path, &file.data).map_err(|e| failure("write rustfmt staged file", e))?;
    }
    // Populate missing children after all planned files exist. Never copy project files.
    for (_, file) in &files {
        let source =
            std::str::from_utf8(&file.data).map_err(|e| failure("read Rust source as UTF-8", e))?;
        for child in modules::children(Path::new(&file.path), source) {
            let child = safe_child(&child)?;
            let path = stage.path().join(child);
            if path.exists() || path.with_extension("").join("mod.rs").exists() {
                continue;
            }
            std::fs::create_dir_all(path.parent().unwrap())
                .map_err(|e| failure("create rustfmt module parent", e))?;
            std::fs::write(path, []).map_err(|e| failure("write rustfmt module placeholder", e))?;
        }
    }
    let root = root
        .canonicalize()
        .map_err(|e| failure("locate rustfmt project root", e))?;
    let api = root.join("api");
    let cwd = if api.is_dir() { &api } else { &root };
    let mut cmd = Cmd::new("rustfmt").args(["--edition", "2024"]).dir(cwd);
    if let Some(config) = [api.join("rustfmt.toml"), api.join(".rustfmt.toml")]
        .into_iter()
        .find(|p| p.is_file())
    {
        cmd = cmd.args([
            "--config-path".into(),
            config.to_string_lossy().into_owned(),
        ]);
    }
    cmd = cmd.args(
        files
            .iter()
            .map(|(_, f)| stage.path().join(&f.path).to_string_lossy().into_owned()),
    );
    tool(runner, cancel, &cmd)?;
    let version = tool(
        runner,
        cancel,
        &Cmd::new("rustfmt").args(["--version"]).dir(cwd),
    )?;
    let version =
        String::from_utf8(version).map_err(|e| failure("read rustfmt version as UTF-8", e))?;
    let mut words = version.split_whitespace();
    if words.next() != Some("rustfmt") || words.next().is_none() {
        bail!(
            "read rustfmt version failed: expected rustfmt <version>, received {version:?}\nTry: check rustfmt --version in api/"
        );
    }
    let mut formatted = Vec::with_capacity(files.len());
    for (index, file) in files {
        let bytes = std::fs::read(stage.path().join(&file.path))
            .map_err(|e| failure("read rustfmt staged result", e))?;
        formatted.push((index, bytes));
    }
    for (index, bytes) in formatted {
        plan.files[index].data = bytes;
    }
    Ok(Some(version.trim().to_string()))
}

fn safe_child(path: &Path) -> anyhow::Result<PathBuf> {
    let mut relative = PathBuf::new();
    for component in path.components() {
        match component {
            Component::Normal(c) => relative.push(c),
            Component::CurDir => {}
            Component::ParentDir if relative.pop() => {}
            _ => bail!(
                "stage Rust child module {path:?} failed: its path leaves the staging directory\nTry: use module paths inside the project"
            ),
        }
    }
    Ok(relative)
}

fn tool(runner: &dyn Runner, cancel: &Cancellation, cmd: &Cmd) -> anyhow::Result<Vec<u8>> {
    match runner.output(cmd, cancel) {
        Ok(output) if output.exit_code == 0 => Ok(output.stdout),
        result => {
            let (why, output) = match &result {
                Ok(output) => (format!("exit code {}", output.exit_code), Some(output)),
                Err(error) => (
                    crate::run::commands::reason(&error.to_string()),
                    error.output(),
                ),
            };
            let text = output
                .map(|o| {
                    format!(
                        "\n{}{}",
                        String::from_utf8_lossy(&o.stdout),
                        String::from_utf8_lossy(&o.stderr)
                    )
                })
                .unwrap_or_default();
            Err(anyhow!(
                "format generator Rust files failed: {why}{text}\nTry: correct the Rust source, or install rustfmt for the toolchain in api/"
            ))
        }
    }
}

fn failure(what: &str, error: impl std::fmt::Display) -> anyhow::Error {
    anyhow!(
        "{what} failed: {error}\nTry: check the Rust source and temporary directory permissions"
    )
}
