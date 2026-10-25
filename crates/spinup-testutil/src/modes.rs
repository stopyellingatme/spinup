//! Read and write the Go golden permission manifest.

use anyhow::{Result, bail};
use spinup_vfs::tree::Tree;
use std::collections::BTreeSet;
use std::fmt::Write as _;

/// Serializes nondefault permissions in path order.
pub fn write(tree: &Tree) -> Result<String> {
    let mut manifest = String::new();
    for (path, file) in tree.iter() {
        if path == ".modes" || file.mode > 0o777 {
            bail!(
                "write .modes: invalid file {path:?}\nTry: use regular files with permission bits only"
            );
        }
        if file.mode != 0o644 {
            writeln!(manifest, "{:04o} {path}", file.mode)?;
        }
    }
    Ok(manifest)
}

/// Applies manifest permissions. Unlisted files receive mode `0644`.
/// Invalid entries return the Go diagnostic for the caller to add context.
pub fn apply(tree: &Tree, manifest: &str) -> Result<Tree> {
    let mut result = Tree::new();
    for (path, file) in tree.iter().filter(|(path, _)| *path != ".modes") {
        result.insert(path, file.data.clone(), 0o644)?;
    }
    let mut seen = BTreeSet::new();
    for line in manifest.split('\n').filter(|line| !line.is_empty()) {
        let parsed = line.split_once(' ').and_then(|(octal, path)| {
            if octal.is_empty() || !octal.bytes().all(|b| (b'0'..=b'7').contains(&b)) {
                return None;
            }
            let mode = u32::from_str_radix(octal, 8).ok()?;
            (mode <= 0o777 && result.contains(path) && seen.insert(path)).then_some((path, mode))
        });
        let Some((path, mode)) = parsed else {
            bail!("invalid .modes entry {line:?}");
        };
        let data = result.get(path).unwrap().data.clone();
        result.insert(path, data, mode)?;
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;
    use spinup_vfs::tree::Tree;

    #[derive(Deserialize)]
    struct Case {
        name: String,
        files: Vec<File>,
        manifest: Option<String>,
        error: String,
    }
    #[derive(Deserialize)]
    struct File {
        path: String,
        mode: String,
        data: String,
    }

    #[test]
    fn oracle_modes() {
        let cases: Vec<Case> =
            serde_json::from_str(include_str!("../../../testdata/oracle/golden-modes.json"))
                .unwrap();
        for case in cases {
            let mut actual = Tree::new();
            for file in case.files {
                actual
                    .insert(
                        file.path,
                        file.data.into_bytes(),
                        u32::from_str_radix(&file.mode, 8).unwrap(),
                    )
                    .unwrap();
            }
            if !case.name.starts_with("read-") && case.name != "missing manifest" {
                assert_eq!(
                    write(&actual).unwrap(),
                    case.manifest.unwrap(),
                    "{}",
                    case.name
                );
                continue;
            }
            let result = apply(&actual, case.manifest.as_deref().unwrap_or(""));
            let error = match result {
                Err(error) => format!("read golden oracle: {error}\nTry: run make golden PACK=oracle UPDATE=1"),
                Ok(expected) => crate::golden::compare_trees(&expected, &actual).into_iter()
                    .map(|report| format!("golden oracle: {report}\nTry: inspect the render or run make golden PACK=oracle UPDATE=1"))
                    .collect::<Vec<_>>().join("\n"),
            };
            assert_eq!(error, case.error, "{}", case.name);
        }
    }
}
