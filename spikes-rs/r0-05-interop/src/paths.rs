//! Portable lexical rules and a capability-based adapter for Go vfs policy.
use anyhow::{Result, bail, ensure};
use cap_std::fs::Dir;
use std::{
    io::ErrorKind,
    path::{Path, PathBuf},
};

pub fn clean_path(name: &str) -> Result<String> {
    ensure!(
        !name.is_empty() && name != "." && !name.starts_with('/'),
        "relative path required"
    );
    ensure!(
        !name.contains([':', '\\']) && !name.chars().any(char::is_control),
        "invalid character"
    );
    let mut parts = Vec::new();
    for part in name.split('/') {
        ensure!(part != "..", "parent path");
        ensure!(!part.ends_with([' ', '.']), "trailing space or dot");
        let base = part.split('.').next().unwrap_or("").to_uppercase();
        let device = matches!(base.as_str(), "CON" | "PRN" | "AUX" | "NUL")
            || (base.len() == 4
                && (base.starts_with("COM") || base.starts_with("LPT"))
                && matches!(base.as_bytes()[3], b'1'..=b'9'));
        ensure!(!device, "Windows device name");
        if !part.is_empty() {
            parts.push(part);
        }
    }
    ensure!(!parts.is_empty(), "file path required");
    Ok(parts.join("/"))
}

// Go EqualFold uses Unicode simple folding. These cover the extra simple-fold
// classes involved in path regressions, without full lowercase expansion.
fn fold(name: &str) -> String {
    name.chars()
        .map(|c| match c {
            'K' => 'k',
            'ſ' => 's',
            'ς' => 'σ',
            'µ' => 'μ',
            _ => {
                let mut lower = c.to_lowercase();
                let first = lower.next().expect("one lowercase character");
                if lower.next().is_none() { first } else { c }
            }
        })
        .collect()
}

/// Validate through cap-std. Return a resolved relative path for the operation.
/// Writes reject parent symlinks. Diff and removal permit internal parent symlinks.
pub fn rooted_path(dir: &Dir, root: &Path, name: &str, write: bool) -> Result<PathBuf> {
    let clean = clean_path(name)?;
    let parts: Vec<_> = clean.split('/').collect();
    let mut current = PathBuf::new();
    for (i, part) in parts.iter().enumerate() {
        match dir.read_dir(if current.as_os_str().is_empty() {
            Path::new(".")
        } else {
            &current
        }) {
            Ok(entries) => {
                for entry in entries {
                    let existing = entry?.file_name();
                    let existing = existing.to_string_lossy();
                    ensure!(
                        existing == *part || fold(&existing) != fold(part),
                        "case collision"
                    );
                }
            }
            Err(e) if e.kind() == ErrorKind::NotFound => {
                current.extend(&parts[i..]);
                return Ok(current);
            }
            Err(e) => return Err(e.into()),
        }
        current.push(part);
        match dir.symlink_metadata(&current) {
            Ok(metadata) if metadata.is_symlink() => {
                ensure!(i + 1 != parts.len(), "final symlink");
                ensure!(!write, "parent symlink during apply");
                let target = dir.read_link_contents(&current)?;
                let relative = if target.is_absolute() {
                    target.strip_prefix(root)?.to_path_buf()
                } else {
                    current.parent().unwrap_or(Path::new("")).join(target)
                };
                // canonicalize rejects escape attempts and missing link targets.
                current = dir.canonicalize(relative)?;
            }
            Ok(metadata) => ensure!(
                i + 1 == parts.len() || metadata.is_dir(),
                "parent is not a directory"
            ),
            Err(e) if e.kind() == ErrorKind::NotFound => {
                current.extend(&parts[i + 1..]);
                return Ok(current);
            }
            Err(e) => bail!(e),
        }
    }
    Ok(current)
}

/// Apply the Go Plan.Add checks to paths, including case collisions.
pub fn distinct_paths(names: &[String]) -> Result<Vec<String>> {
    let mut paths: Vec<String> = Vec::new();
    for name in names {
        let clean = clean_path(name)?;
        ensure!(
            !paths.iter().any(|p| fold(p) == fold(&clean)),
            "case collision"
        );
        paths.push(clean);
    }
    Ok(paths)
}
