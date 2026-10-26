//! Preserve the local kit patch, including paths relative to the main checkout.

use std::path::{Path, PathBuf};

use anyhow::{Result, anyhow, bail};
use toml_edit::{InlineTable, Item, Table, Value};

use super::{exists, print, read, write};
use crate::contract::Step;

pub(super) fn run(s: &mut Step<'_>) -> Result<()> {
    apply(s, std::env::var_os("SPINUP_KIT_REPLACE").map(PathBuf::from))
}

pub(super) fn apply(s: &mut Step<'_>, replacement: Option<PathBuf>) -> Result<()> {
    if !exists(&s.worktree, "api/Cargo.toml")? {
        return Ok(());
    }
    let manifest = s.worktree.join("api/Cargo.toml");
    let mut document = read(&manifest)?;
    let source_api = main_api(&s.worktree);
    let replacement = replacement.filter(|p| !p.as_os_str().is_empty());
    let (patch, target) = if let Some(path) = replacement {
        let path = std::path::absolute(path).map_err(|e| {
            anyhow!("resolve the kit patch: {e}\nTry: set SPINUP_KIT_REPLACE to the kit directory")
        })?;
        let mut patch = InlineTable::new();
        patch.insert("path", Value::from(path_text(&path)));
        (Item::Value(Value::InlineTable(patch)), path)
    } else {
        let original = read(&source_api.join("Cargo.toml"))?;
        let Some(mut patch) = original
            .get("patch")
            .and_then(|p| p.get("crates-io"))
            .and_then(|p| p.get("spinup-kit"))
            .cloned()
        else {
            return Ok(());
        };
        let Some(path) = patch.get("path").and_then(Item::as_str) else {
            return Ok(());
        };
        let path = PathBuf::from(path);
        let target = std::path::absolute(source_api.join(&path)).map_err(|e| {
            anyhow!(
                "resolve the kit patch: {e}\nTry: repair the spinup-kit patch in api/Cargo.toml"
            )
        })?;
        if path.is_relative() && !s.worktree.join("api").join(&path).is_dir() {
            patch["path"] = toml_edit::value(path_text(&target));
            print(
                s,
                &format!(
                    "Warning: the kit patch {} points outside the repository. The upgrade uses {}.\n",
                    path.display(),
                    target.display()
                ),
            )?;
        }
        (patch, target)
    };
    if !target.is_dir() {
        bail!(
            "inspect the kit patch {}: not an existing directory\nTry: set SPINUP_KIT_REPLACE to the kit directory",
            target.display()
        );
    }
    if !document.contains_key("patch") {
        document["patch"] = Item::Table(Table::new());
    }
    if document["patch"].as_table_like().is_none() {
        bail!("edit the kit patch: patch is not a table\nTry: repair api/Cargo.toml");
    }
    if document["patch"].get("crates-io").is_none() {
        document["patch"]["crates-io"] = Item::Table(Table::new());
    }
    let table = document["patch"]["crates-io"]
        .as_table_like_mut()
        .ok_or_else(|| {
            anyhow!(
                "edit the kit patch: patch.crates-io is not a table\nTry: repair api/Cargo.toml"
            )
        })?;
    table.insert("spinup-kit", patch);
    // The pack lock already contains the path form until R4 (ADR 0021 §4).
    write(&manifest, &document)
}

fn main_api(worktree: &Path) -> PathBuf {
    if let Some(state) = worktree.parent().and_then(Path::parent)
        && state.file_name().is_some_and(|name| name == ".spinup")
    {
        let api = state.parent().unwrap_or(worktree).join("api");
        if api.join("Cargo.toml").is_file() {
            return api;
        }
    }
    worktree.join("api")
}

fn path_text(path: &Path) -> String {
    if cfg!(windows) {
        path.to_string_lossy().replace('\\', "/")
    } else {
        path.to_string_lossy().into_owned()
    }
}
