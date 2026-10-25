//! `SPINUP_KIT_REPLACE`.
//!
use std::path::{Path, PathBuf};

use anyhow::{Result, bail};
use spinup_vfs::plan::Plan;
use toml_edit::{DocumentMut, InlineTable, Item, Table, Value};

/// The Go kit reference retained by R1 locks and Go apps.
pub const KIT_MODULE: &str = "github.com/stopyellingatme/spinup/kit";

/// Applies a local kit override to every supported API manifest in the plan.
pub fn apply_replace(plan: &mut Plan, directory: &Path) -> Result<PathBuf> {
    let directory = crate::select::absolute(directory)?;
    let metadata = std::fs::metadata(&directory).map_err(|e| anyhow::anyhow!(
        "cannot read kit replace {directory:?}: {e}\nTry: set SPINUP_KIT_REPLACE to the kit directory"
    ))?;
    if !metadata.is_dir() {
        bail!(
            "kit replace {directory:?} is not a directory\nTry: set SPINUP_KIT_REPLACE to the kit directory"
        );
    }
    for file in &mut plan.files {
        if file.path == "api/Cargo.toml" {
            file.data = patch_cargo(&file.data, &directory)?;
        } else if file.path == "api/go.mod" {
            let replacement = format!(
                "\nreplace {KIT_MODULE} => {}\n",
                quote_module_path(&directory)
            );
            file.data.extend_from_slice(replacement.as_bytes());
            file.data = spinup_manifest::hash::normalize(&file.data);
        }
    }
    Ok(directory)
}

/// Edits the crates.io patch while preserving other TOML entries and comments.
pub fn patch_cargo(data: &[u8], directory: &Path) -> Result<Vec<u8>> {
    let edit = || -> Result<Vec<u8>> {
        let mut document = std::str::from_utf8(data)?.parse::<DocumentMut>()?;
        let path = directory
            .to_str()
            .ok_or_else(|| anyhow::anyhow!("kit path is not UTF-8"))?;
        let mut table: &mut dyn toml_edit::TableLike = document.as_table_mut();
        for key in ["patch", "crates-io"] {
            if !table.contains_key(key) {
                table.insert(key, Item::Table(Table::new()));
            }
            table = table
                .get_mut(key)
                .and_then(Item::as_table_like_mut)
                .ok_or_else(|| anyhow::anyhow!("{key} must be a table"))?;
        }
        let mut replacement = InlineTable::new();
        replacement.insert("path", Value::from(path));
        table.insert("spinup-kit", Item::Value(Value::InlineTable(replacement)));
        Ok(spinup_manifest::hash::normalize(
            document.to_string().as_bytes(),
        ))
    };
    edit().map_err(|e| {
        anyhow::anyhow!(
            "cannot patch api/Cargo.toml: {e}\nTry: check the manifest and SPINUP_KIT_REPLACE"
        )
    })
}

fn quote_module_path(path: &Path) -> String {
    let text = path.to_string_lossy();
    let text = if cfg!(windows) {
        text.replace('\\', "/")
    } else {
        text.into_owned()
    };
    if text.contains([' ', '\t', '\r', '\n', '"', '`', '(', ')']) || text.contains("//") {
        spinup_render::functions::quote(&text)
    } else {
        text
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use spinup_vfs::plan::{File, Plan};

    #[test]
    fn patches_rust_and_go_manifests_before_hashing() {
        let kit = tempfile::tempdir().unwrap();
        let mut plan = Plan::new();
        for (path, data) in [
            (
                "api/Cargo.toml",
                "# application\n[package]\nname = 'demo'\nversion = '0.1.0'\n[patch.crates-io]\nother = { path = '../other' }\n",
            ),
            ("api/go.mod", "module example.com/demo\n"),
        ] {
            plan.add(File {
                path: path.into(),
                data: data.as_bytes().to_vec(),
                ..File::default()
            })
            .unwrap();
        }
        let path = apply_replace(&mut plan, kit.path()).unwrap();
        let cargo = String::from_utf8(plan.get("api/Cargo.toml").unwrap().data).unwrap();
        let parsed = cargo.parse::<toml_edit::DocumentMut>().unwrap();
        assert_eq!(
            parsed["patch"]["crates-io"]["spinup-kit"]["path"].as_str(),
            path.to_str()
        );
        assert!(cargo.contains("# application"));
        assert!(cargo.contains("other = { path = '../other' }"));
        let go = String::from_utf8(plan.get("api/go.mod").unwrap().data).unwrap();
        assert!(go.contains(&format!(
            "replace {KIT_MODULE} => {}",
            quote_module_path(&path)
        )));
        let first = plan.clone();
        // The Rust edit replaces the existing patch rather than adding another table.
        let rust = patch_cargo(&first.get("api/Cargo.toml").unwrap().data, &path).unwrap();
        assert_eq!(rust, first.get("api/Cargo.toml").unwrap().data);
    }

    #[test]
    fn rejects_invalid_kit_paths_and_toml() {
        let dir = tempfile::tempdir().unwrap();
        for path in [dir.path().join("missing"), dir.path().join("file")] {
            if path.ends_with("file") {
                std::fs::write(&path, b"").unwrap();
            }
            assert!(
                apply_replace(&mut Plan::new(), &path)
                    .unwrap_err()
                    .to_string()
                    .contains("Try:")
            );
        }
        assert!(
            patch_cargo(b"[broken", dir.path())
                .unwrap_err()
                .to_string()
                .contains("Try:")
        );
        assert_eq!(
            quote_module_path(std::path::Path::new("/tmp/my kit")),
            "\"/tmp/my kit\""
        );
    }
}
