//! Read the local kit patch from the API manifest.

use anyhow::{Context, bail};
use std::path::{Path, PathBuf};

/// Read the kit patch path exactly as the manifest specifies it.
///
/// # Errors
/// Return an error when the manifest cannot be read or parsed.
pub fn kit_replace_target(api_dir: &Path) -> anyhow::Result<Option<String>> {
    let manifest = api_dir.join("Cargo.toml");
    let read = || -> anyhow::Result<Option<String>> {
        let text = std::fs::read_to_string(&manifest)?;
        let value: toml::Value = toml::from_str(&text)?;
        let patch = value
            .get("patch")
            .and_then(|p| p.get("crates-io"))
            .and_then(|p| p.get("spinup-kit"));
        match patch.and_then(|p| p.get("path")) {
            None => Ok(None),
            Some(path) => Ok(Some(
                path.as_str()
                    .context("the kit patch path must be a string")?
                    .into(),
            )),
        }
    };
    read().map_err(|error| anyhow::anyhow!("Cannot read the kit replace in {}: {error:#}\nTry: check api/Cargo.toml and run the command again", manifest.display()))
}

/// Resolve the kit patch to an existing absolute directory.
/// Relative paths start at the API directory.
///
/// # Errors
/// Return an error when the manifest or the kit directory is invalid.
pub fn kit_replace_dir(api_dir: &Path) -> anyhow::Result<Option<PathBuf>> {
    let Some(target) = kit_replace_target(api_dir)? else {
        return Ok(None);
    };
    let path = api_dir.join(target);
    match path.canonicalize() {
        Ok(path) if path.is_dir() => Ok(Some(path)),
        _ => bail!(
            "Cannot use the local kit replace in api/Cargo.toml: {} does not exist or is not a directory\nTry: point the patch at the kit directory or remove it and use a published kit version",
            path.display()
        ),
    }
}

#[cfg(test)]
mod tests;
