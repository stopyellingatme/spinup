//! Find the repository through its workspace manifest.

use anyhow::{Result, bail};
use std::path::{Path, PathBuf};

/// Finds the spinup workspace above the current directory.
pub fn repo_root() -> Result<PathBuf> {
    let current = std::env::current_dir().map_err(|error| {
        anyhow::anyhow!("find repository root: {error}\nTry: run tests from the repository")
    })?;
    find_repo_root(&current)
}

/// Finds the workspace manifest that declares spinup crates.
/// Crate manifests and standalone spike workspaces do not match this marker.
pub fn find_repo_root(start: &Path) -> Result<PathBuf> {
    for directory in start.ancestors() {
        let path = directory.join("Cargo.toml");
        let Ok(text) = std::fs::read_to_string(path) else {
            continue;
        };
        let Ok(manifest) = toml::from_str::<toml::Value>(&text) else {
            continue;
        };
        let members = manifest
            .get("workspace")
            .and_then(|v| v.get("members"))
            .and_then(toml::Value::as_array);
        if members.is_some_and(|members| {
            members.iter().any(|member| {
                member
                    .as_str()
                    .is_some_and(|value| value == "crates/*" || value == "crates/spinup-testutil")
            })
        }) {
            return Ok(directory.to_path_buf());
        }
    }
    bail!(
        "find repository root: spinup workspace Cargo.toml was not found\nTry: run tests from the repository"
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn finds_workspace_above_crate_and_spike_manifests() {
        let root = crate::golden::tests::temp_dir();
        let nested = root.join("crates/example/src");
        std::fs::create_dir_all(&nested).unwrap();
        std::fs::write(
            root.join("Cargo.toml"),
            "[workspace]\nmembers = [\"crates/*\"]\n",
        )
        .unwrap();
        std::fs::write(
            root.join("crates/example/Cargo.toml"),
            "[package]\nname = \"example\"\n",
        )
        .unwrap();
        assert_eq!(find_repo_root(&nested).unwrap(), root);
        std::fs::write(root.join("crates/example/Cargo.toml"), "[workspace]\n").unwrap();
        assert_eq!(find_repo_root(&nested).unwrap(), root);
        std::fs::remove_file(root.join("Cargo.toml")).unwrap();
        assert!(find_repo_root(&nested).is_err());
        std::fs::remove_dir_all(root).unwrap();
    }
}
