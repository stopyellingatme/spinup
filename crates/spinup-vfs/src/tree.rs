//! An in-memory, read-only file tree: the Rust form of the Go `fs.FS` values that hold
//! template packs (ADR 0020 §1).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it.
//!
//! The embedded packs, a template archive, a `SPINUP_TEMPLATES_DIR` version, and a plugin
//! pack all load into a [`Tree`]. Iteration is in byte order of the paths, so every walk
//! over a tree is deterministic.

use std::collections::BTreeMap;
use std::fmt;
use std::sync::Arc;

/// One regular file of a [`Tree`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TreeFile {
    /// The file content.
    pub data: Arc<[u8]>,
    /// The Unix permission bits, such as `0o644` or `0o755`.
    pub mode: u32,
}

/// A path that a [`Tree`] does not accept.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct InvalidPath {
    /// The rejected path.
    pub path: String,
}

impl fmt::Display for InvalidPath {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "tree path {:?} is not a clean relative path\nTry: use slash-separated segments without \".\", \"..\", or empty parts",
            self.path
        )
    }
}

impl std::error::Error for InvalidPath {}

/// A set of regular files, keyed by clean, slash-separated, relative paths.
///
/// A path has one or more segments. No segment is empty, `.`, or `..`, and no path holds a
/// backslash or a NUL byte. Directories are implicit: they exist when a file is below them.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Tree {
    files: BTreeMap<String, TreeFile>,
}

impl Tree {
    /// Returns an empty tree.
    pub fn new() -> Self {
        Self::default()
    }

    /// Adds or replaces the file at `path`.
    pub fn insert(
        &mut self,
        path: impl Into<String>,
        data: impl Into<Arc<[u8]>>,
        mode: u32,
    ) -> Result<(), InvalidPath> {
        let path = path.into();
        if !valid_path(&path) {
            return Err(InvalidPath { path });
        }
        self.files.insert(
            path,
            TreeFile {
                data: data.into(),
                mode,
            },
        );
        Ok(())
    }

    /// Removes the file at `path` and returns it.
    pub fn remove(&mut self, path: &str) -> Option<TreeFile> {
        self.files.remove(path)
    }

    /// Returns the file at `path`.
    pub fn get(&self, path: &str) -> Option<&TreeFile> {
        self.files.get(path)
    }

    /// Returns true when a file exists at `path`.
    pub fn contains(&self, path: &str) -> bool {
        self.files.contains_key(path)
    }

    /// Returns true when at least one file is below the directory `dir`.
    pub fn is_dir(&self, dir: &str) -> bool {
        let prefix = format!("{dir}/");
        self.files
            .range(prefix.clone()..)
            .next()
            .is_some_and(|(path, _)| path.starts_with(&prefix))
    }

    /// Returns the number of files.
    pub fn len(&self) -> usize {
        self.files.len()
    }

    /// Returns true when the tree holds no file.
    pub fn is_empty(&self) -> bool {
        self.files.is_empty()
    }

    /// Iterates over all files in path order.
    pub fn iter(&self) -> impl Iterator<Item = (&str, &TreeFile)> {
        self.files.iter().map(|(path, file)| (path.as_str(), file))
    }

    /// Returns the files below the directory `dir`, with `dir/` removed from each path.
    /// An empty `dir` returns a copy of the whole tree.
    pub fn sub(&self, dir: &str) -> Tree {
        if dir.is_empty() {
            return self.clone();
        }
        let prefix = format!("{dir}/");
        let files = self
            .files
            .range(prefix.clone()..)
            .take_while(|(path, _)| path.starts_with(&prefix))
            .map(|(path, file)| (path[prefix.len()..].to_string(), file.clone()))
            .collect();
        Tree { files }
    }

    /// Returns the names of the direct children (files and directories) of `dir`, sorted
    /// and without duplicates. An empty `dir` lists the top level.
    pub fn children(&self, dir: &str) -> Vec<String> {
        let prefix = if dir.is_empty() {
            String::new()
        } else {
            format!("{dir}/")
        };
        let mut names: Vec<String> = self
            .files
            .range(prefix.clone()..)
            .take_while(|(path, _)| path.starts_with(&prefix))
            .map(|(path, _)| {
                let rest = &path[prefix.len()..];
                rest.split('/').next().unwrap_or(rest).to_string()
            })
            .collect();
        names.sort();
        names.dedup();
        names
    }
}

fn valid_path(path: &str) -> bool {
    !path.is_empty()
        && !path.contains(['\\', '\0'])
        && path
            .split('/')
            .all(|segment| !segment.is_empty() && segment != "." && segment != "..")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_unclean_paths() {
        let mut tree = Tree::new();
        for path in ["", "/a", "a/", "a//b", "./a", "a/../b", "a\\b", "a\0b"] {
            assert!(tree.insert(path, b"x".to_vec(), 0o644).is_err(), "{path:?}");
        }
        assert!(tree.insert("a/b.txt", b"x".to_vec(), 0o644).is_ok());
    }

    #[test]
    fn walks_in_path_order() {
        let mut tree = Tree::new();
        for path in ["b/z", "a/y", "a/x/1", "c"] {
            tree.insert(path, b"x".to_vec(), 0o644).unwrap();
        }
        let paths: Vec<&str> = tree.iter().map(|(path, _)| path).collect();
        assert_eq!(paths, ["a/x/1", "a/y", "b/z", "c"]);
        assert_eq!(tree.children(""), ["a", "b", "c"]);
        assert_eq!(tree.children("a"), ["x", "y"]);
        assert!(tree.is_dir("a/x"));
        assert!(!tree.is_dir("c"));
        let sub = tree.sub("a");
        assert_eq!(sub.iter().map(|(p, _)| p).collect::<Vec<_>>(), ["x/1", "y"]);
    }
}
