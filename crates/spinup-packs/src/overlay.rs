//! Merge converted packs and Rust-only overlays without changing either input.

use anyhow::{Result, bail};
use spinup_vfs::tree::Tree;

use crate::validate::clean_path;

/// Applies replacements, additions, removals, and fragments from an overlay tree.
/// Both trees are rooted at the directory that contains all packs.
/// Overlay fragments receive `~overlay-` prefixes for portable load order.
pub fn merge(converted: &Tree, overlay: &Tree) -> Result<Tree> {
    let mut result = converted.clone();
    for pack in overlay.children("") {
        if !overlay.is_dir(&pack) || overlay.contains(&pack) {
            bail!(
                "merge overlay {pack:?}: overlay entries must be pack directories\nTry: put each overlay inside its pack directory"
            );
        }
        if !converted.is_dir(&pack) {
            bail!(
                "merge overlay {pack:?}: overlay pack has no converted pack\nTry: remove overlay/{pack}, or convert the pack"
            );
        }
        let files = overlay.sub(&pack);
        if files.contains("pack.toml") || files.is_dir("pack.toml") {
            bail!(
                "merge overlay {pack:?}: overlay cannot replace pack.toml\nTry: use a fragment in overlay/{pack}/pack.d/"
            );
        }
        if let Some(remove) = files.get("remove.txt") {
            let text = std::str::from_utf8(&remove.data).map_err(|error| {
                anyhow::anyhow!(
                    "read overlay/{pack}/remove.txt: {error}\nTry: use UTF-8 paths in remove.txt"
                )
            })?;
            for line in text.lines() {
                let path = line.split('#').next().unwrap_or("").trim();
                if path.is_empty() {
                    continue;
                }
                clean_path(path)?;
                let path = format!("{pack}/{path}");
                if result.remove(&path).is_none() {
                    bail!(
                        "merge overlay/{pack}/remove.txt: listed path {path:?} does not exist\nTry: remove the line from remove.txt"
                    );
                }
            }
        }
        for (path, file) in files.iter() {
            if path == "remove.txt" {
                continue;
            }
            let target = match path.strip_prefix("pack.d/") {
                Some(name) if !name.contains('/') && name.ends_with(".toml") => {
                    format!("{pack}/pack.d/~overlay-{name}")
                }
                _ => format!("{pack}/{path}"),
            };
            result.insert(target, file.data.clone(), file.mode)?;
        }
    }
    // A real file system cannot hold a regular file and its children together.
    for (path, _) in result.iter() {
        if result.is_dir(path) {
            bail!(
                "merge overlay: {path:?} is both a file and a directory\nTry: remove the conflicting overlay entry"
            );
        }
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use spinup_vfs::tree::Tree;

    fn tree(entries: &[(&str, &str)]) -> Tree {
        let mut tree = Tree::new();
        for (path, data) in entries {
            tree.insert(*path, data.as_bytes().to_vec(), 0o644).unwrap();
        }
        tree
    }

    #[test]
    fn fixture_overlay() {
        let converted = tree(&[
            ("ENGINE", "minijinja\n"),
            (
                "example/pack.toml",
                "name='example'\n[[files]]\npath='out'\nsource='files/a'\nclass='managed'",
            ),
            ("example/files/a", "old"),
            ("example/files/delete", "delete"),
            (
                "example/pack.d/z.toml",
                "[[questions]]\nkey='converted'\ntype='bool'",
            ),
        ]);
        let overlay = tree(&[
            ("example/files/a", "replacement"),
            ("example/.hidden", "new"),
            (
                "example/remove.txt",
                "# deletion\n\n files/delete # comment\n",
            ),
            (
                "example/pack.d/b.toml",
                "[[questions]]\nkey='second'\ntype='bool'",
            ),
            (
                "example/pack.d/a.toml",
                "[[questions]]\nkey='first'\ntype='bool'",
            ),
            (
                "example/pack.d/z.toml",
                "[[questions]]\nkey='same_fragment_name'\ntype='bool'",
            ),
        ]);
        let result = super::merge(&converted, &overlay).unwrap();
        assert_eq!(
            result.get("example/files/a").unwrap().data.as_ref(),
            b"replacement"
        );
        assert_eq!(result.get("example/.hidden").unwrap().data.as_ref(), b"new");
        assert!(!result.contains("example/files/delete"));
        assert!(!result.contains("example/remove.txt"));
        assert!(result.contains("example/pack.d/~overlay-a.toml"));
        assert!(!result.contains("example/pack.d/a.toml"));
        assert_eq!(result.get("ENGINE"), converted.get("ENGINE"));
        let p = crate::loader::load(&result, "example").unwrap();
        assert_eq!(
            p.questions
                .iter()
                .map(|q| q.key.as_str())
                .collect::<Vec<_>>(),
            ["converted", "first", "second", "same_fragment_name"]
        );
        assert_eq!(
            converted.get("example/files/a").unwrap().data.as_ref(),
            b"old"
        );
        assert!(converted.contains("example/files/delete"));
    }

    #[test]
    fn invalid_overlays() {
        let converted = tree(&[
            ("example/pack.toml", "name='example'"),
            ("example/files/a", "a"),
        ]);
        for entries in [
            vec![("missing/files/a", "a")],
            vec![("example/pack.toml", "name='example'")],
            vec![("example/remove.txt", "files/missing")],
            vec![("example/remove.txt", "../escape")],
            vec![("example/remove.txt", "/escape")],
            vec![("example/remove.txt", "files/a\nfiles/a")],
            vec![("ENGINE", "other")],
        ] {
            let error = super::merge(&converted, &tree(&entries))
                .unwrap_err()
                .to_string();
            assert!(error.contains("\nTry:"), "{entries:?}: {error}");
            assert!(converted.contains("example/files/a"));
        }
    }
}
