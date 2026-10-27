//! Upgrade phases for Rust applications (ADR 0021 §8).

use std::path::Path;

use anyhow::{Result, anyhow, bail};
use spinup_exec::runner::{Cmd, Output};
use spinup_manifest::hash::normalize;
use toml_edit::DocumentMut;

use crate::contract::{Action, Phases, Step};

mod apply;
mod deps;
mod migrations;
#[cfg(test)]
mod tests;
mod verify;

/// Returns all six phases for the upgrade state machine.
pub fn phases() -> Phases {
    Phases {
        before: Some(migrations::before),
        apply: Some(apply::run),
        deps: Some(deps::run),
        fix: Some(|_| Ok(false)),
        after: Some(migrations::after),
        verify: Some(verify::run),
    }
}

/// Returns the keep reason for a dependency manifest, or `None` for other files.
/// The planner retains the new render for the dependency phase.
pub fn kept_manifest(path: &str) -> Option<&'static str> {
    match path {
        "api/Cargo.toml" | "api/Cargo.lock" => {
            Some("Cargo: the dependency step applies the template changes")
        }
        "api/go.mod" | "api/go.sum" => Some(crate::plan::GO_MOD_KEEP_REASON),
        _ => None,
    }
}

fn exists(root: &Path, path: &str) -> Result<bool> {
    match std::fs::metadata(root.join(path)) {
        Ok(_) => Ok(true),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(e) => bail!("inspect {path}: {e}\nTry: check the upgrade worktree permissions"),
    }
}

fn read(path: &Path) -> Result<DocumentMut> {
    let text = std::fs::read_to_string(path).map_err(|e| {
        anyhow!(
            "read {}: {e}\nTry: check the upgrade worktree permissions",
            path.display()
        )
    })?;
    parse(&text, &path.display().to_string())
}

fn parse(text: &str, name: &str) -> Result<DocumentMut> {
    if text.lines().any(|line| line.starts_with("<<<<<<< ")) {
        bail!(
            "read {name}: the file has merge conflict markers\nTry: resolve the conflict, then run spinup upgrade --continue or --abort"
        );
    }
    text.parse()
        .map_err(|e| anyhow!("parse {name}: {e}\nTry: repair the manifest in the upgrade worktree"))
}

fn write(path: &Path, document: &DocumentMut) -> Result<()> {
    let bytes = normalize(document.to_string().as_bytes());
    if std::fs::read(path).is_ok_and(|old| old == bytes) {
        return Ok(());
    }
    std::fs::write(path, bytes).map_err(|e| {
        anyhow!(
            "write {}: {e}\nTry: check the upgrade worktree permissions",
            path.display()
        )
    })
}

fn print(s: &mut Step<'_>, text: &str) -> Result<()> {
    s.out
        .write_all(text.as_bytes())
        .map_err(|e| anyhow!("print upgrade progress: {e}\nTry: use a writable output"))
}

fn conflict(s: &Step<'_>, path: &str) -> bool {
    s.conflicts.iter().any(|p| p == path)
        || s.plan.as_ref().is_some_and(|p| {
            p.files
                .iter()
                .any(|f| f.path == path && f.action == Action::Conflict)
        })
}

// Cmd has no env_remove operation. Replace the inherited environment after filtering it.
fn clean_env(mut cmd: Cmd) -> Cmd {
    const OMIT: [&str; 3] = ["CARGO_ENCODED_RUSTFLAGS", "RUSTFLAGS", "LLVM_PROFILE_FILE"];
    let mut env: std::collections::BTreeMap<String, String> = if cmd.replace_env {
        Default::default()
    } else {
        std::env::vars_os()
            .filter_map(|(k, v)| Some((k.into_string().ok()?, v.into_string().ok()?)))
            .collect()
    };
    env.extend(cmd.env);
    env.retain(|key, _| !OMIT.contains(&key.as_str()));
    cmd.env = env.into_iter().collect();
    cmd.replace_env = true;
    cmd
}

fn command(s: &Step<'_>, cmd: Cmd) -> Result<Output> {
    let cmd = clean_env(cmd);
    let output = s.runner.output(&cmd, s.cancel).map_err(|e| {
        let diagnostics = e.output().map(|o| {
            format!("\n{}{}", String::from_utf8_lossy(&o.stdout), String::from_utf8_lossy(&o.stderr))
        }).unwrap_or_default();
        anyhow!("run {cmd}: {e}{diagnostics}\nTry: correct the failure in the upgrade worktree and run spinup upgrade --continue")
    })?;
    if output.exit_code != 0 {
        bail!(
            "run {cmd}: exit code {}\n{}{}\nTry: correct the failure in the upgrade worktree and run spinup upgrade --continue",
            output.exit_code,
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(output)
}
