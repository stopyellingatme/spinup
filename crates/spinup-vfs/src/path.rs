//! Portable lexical rules and a capability-based adapter for Go vfs policy.
use anyhow::{Result, bail, ensure};
use cap_std::fs::Dir;
use std::{
    io::ErrorKind,
    path::{Path, PathBuf},
};

/// Validates and normalizes a portable, slash-separated relative file path.
pub fn clean_path(name: &str) -> Result<String> {
    let invalid = |reason| {
        anyhow::anyhow!(
            "invalid file path {}: {reason}\nTry: use a portable relative file path",
            quote(name)
        )
    };
    if name.is_empty() || name == "." || name.starts_with('/') {
        return Err(invalid("use a relative slash-separated path"));
    }
    if name.contains([':', '\\']) || name.chars().any(char::is_control) {
        return Err(invalid(
            "colons, backslashes, and control characters are not allowed",
        ));
    }
    let mut parts = Vec::new();
    for part in name.split('/') {
        if part == ".." {
            return Err(invalid("parent paths are not allowed"));
        }
        if part.ends_with([' ', '.']) {
            return Err(invalid("path segments cannot end with a space or dot"));
        }
        let base = part.split('.').next().unwrap_or("").to_uppercase();
        let device = matches!(base.as_str(), "CON" | "PRN" | "AUX" | "NUL")
            || (base.len() == 4
                && (base.starts_with("COM") || base.starts_with("LPT"))
                && matches!(base.as_bytes()[3], b'1'..=b'9'));
        if device {
            return Err(invalid("Windows device names are not allowed"));
        }
        if !part.is_empty() {
            parts.push(part);
        }
    }
    if parts.is_empty() {
        return Err(invalid("use a file path"));
    }
    Ok(parts.join("/"))
}

fn quote(name: &str) -> String {
    let mut quoted = String::from("\"");
    for c in name.chars() {
        match c {
            '\\' => quoted.push_str("\\\\"),
            '"' => quoted.push_str("\\\""),
            '\n' => quoted.push_str("\\n"),
            '\r' => quoted.push_str("\\r"),
            '\t' => quoted.push_str("\\t"),
            '\x07' => quoted.push_str("\\a"),
            '\x08' => quoted.push_str("\\b"),
            '\x0c' => quoted.push_str("\\f"),
            '\x0b' => quoted.push_str("\\v"),
            c if c.is_ascii_control() => quoted.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() => quoted.push_str(&format!("\\u{:04x}", c as u32)),
            c => quoted.push(c),
        }
    }
    quoted.push('"');
    quoted
}

// Use one-character case mappings only. Full case expansion would incorrectly
// equate paths such as `ss` and `ß`. Turkish I mappings are not simple folds.
pub(crate) fn fold(name: &str) -> String {
    fn single(mut chars: impl Iterator<Item = char>, fallback: char) -> char {
        let first = chars.next().unwrap_or(fallback);
        if chars.next().is_none() {
            first
        } else {
            fallback
        }
    }
    name.chars()
        .map(|c| {
            if matches!(c, 'ı' | 'İ') {
                return c;
            }
            let upper = single(c.to_uppercase(), c);
            single(upper.to_lowercase(), upper)
        })
        .collect()
}

pub(crate) fn user_error(operation: &str, error: impl std::fmt::Display) -> anyhow::Error {
    let message = error.to_string();
    if message.contains("\nTry:") {
        anyhow::anyhow!("{operation}: {message}")
    } else {
        anyhow::anyhow!("{operation}: {message}\nTry: check the target path and its permissions")
    }
}

pub(crate) fn open_root(root: &Path) -> Result<(PathBuf, Dir)> {
    let resolved =
        std::fs::canonicalize(root).map_err(|e| user_error(&format!("read root {:?}", root), e))?;
    let dir = Dir::open_ambient_dir(&resolved, cap_std::ambient_authority())
        .map_err(|e| user_error(&format!("read root {:?}", root), e))?;
    Ok((resolved, dir))
}

/// Validate through cap-std. Return a resolved relative path for the operation.
/// Writes reject parent symlinks. Diff and removal permit internal parent symlinks.
pub(crate) fn rooted_path(dir: &Dir, root: &Path, name: &str, write: bool) -> Result<PathBuf> {
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
                    if existing != *part && fold(&existing) == fold(part) {
                        let prefix = parts[..i].join("/");
                        let conflict = if prefix.is_empty() {
                            existing.into_owned()
                        } else {
                            format!("{prefix}/{existing}")
                        };
                        bail!(
                            "path {name:?} conflicts with existing entry {conflict:?} under case folding\nTry: use the existing spelling or rename the entry"
                        );
                    }
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
                ensure!(
                    i + 1 != parts.len(),
                    "path {name:?} is a symlink\nTry: replace the symlink with a regular file, or remove it from the template"
                );
                ensure!(
                    !write,
                    "path {name:?} uses a parent symlink during apply\nTry: replace the symlink with a directory"
                );
                // Resolve absolute links in a chain with the Go path policy.
                // Then address the result through the root capability. Actual
                // reads and mutations remain confined even if the path changes.
                let resolved = std::fs::canonicalize(root.join(&current))
                    .map_err(|_| anyhow::anyhow!("path {name:?} uses a symlink outside the root or a missing target\nTry: replace the symlink with a directory inside the root"))?;
                let relative = resolved.strip_prefix(root)
                    .map_err(|_| anyhow::anyhow!("path {name:?} uses a symlink outside the root\nTry: replace the symlink with a directory inside the root"))?;
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

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;
    #[derive(Deserialize)]
    struct Case {
        input: String,
        clean: String,
        error: String,
    }
    #[test]
    fn clean_paths_equal_go() {
        let cases: Vec<Case> =
            serde_json::from_str(include_str!("../../../testdata/oracle/clean-paths.json"))
                .unwrap();
        for case in cases {
            match clean_path(&case.input) {
                Ok(clean) => {
                    assert!(case.error.is_empty(), "{:?}", case.input);
                    assert_eq!(clean, case.clean);
                }
                Err(error) => assert_eq!(error.to_string(), case.error, "{:?}", case.input),
            }
        }
    }
}
