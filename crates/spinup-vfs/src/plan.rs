//! Staged file changes. The apply step performs all disk writes.

use crate::{
    path::{clean_path, fold},
    tree::Tree,
};
use anyhow::{Result, bail};

/// One staged file change.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct File {
    /// A portable relative path.
    pub path: String,
    /// The new file content.
    pub data: Vec<u8>,
    /// Unix permission bits. Zero selects `0o644`.
    pub mode: u32,
    /// Removes the file when true.
    pub delete: bool,
    /// The template class: managed, scaffold, or seed.
    pub class: String,
    /// The source template path.
    pub template: String,
}

/// The files to write. Only the apply step writes them to disk.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Plan {
    /// Staged changes in insertion order. Apply validates these again.
    pub files: Vec<File>,
}

impl Plan {
    /// Returns an empty plan.
    pub fn new() -> Self {
        Self::default()
    }

    /// Adds a file after normalization and case collision checks.
    pub fn add(&mut self, mut file: File) -> Result<()> {
        let clean = clean_path(&file.path)?;
        if let Some(existing) = self.files.iter().find(|f| fold(&f.path) == fold(&clean)) {
            bail!(
                "add file {clean:?}: path conflicts with {:?} under case folding\nTry: give each file a distinct path",
                existing.path
            );
        }
        file.path = clean;
        self.files.push(file);
        Ok(())
    }

    /// Returns a copy of the file with the normalized path.
    pub fn get(&self, path: &str) -> Option<File> {
        let clean = clean_path(path).ok()?;
        self.files.iter().find(|f| f.path == clean).cloned()
    }

    /// Returns the sorted paths.
    pub fn paths(&self) -> Vec<String> {
        let mut paths: Vec<_> = self.files.iter().map(|f| f.path.clone()).collect();
        paths.sort();
        paths
    }

    /// Returns a sorted copy of the plan.
    pub fn sorted(&self) -> Self {
        let mut plan = self.clone();
        plan.files.sort_by(|a, b| a.path.cmp(&b.path));
        plan
    }

    /// Combines two plans and rejects conflicting paths.
    pub fn merge(&self, other: &Self) -> Result<Self> {
        let mut result = Self::new();
        for file in self.files.iter().chain(&other.files) {
            result
                .add(file.clone())
                .map_err(|e| crate::path::user_error("merge plans", e))?;
        }
        Ok(result.sorted())
    }

    /// Returns a read-only tree without deleted files.
    pub fn fs(&self) -> Result<Tree> {
        let validated = self.merge(&Self::default())?;
        let mut tree = Tree::new();
        for file in validated.files.iter().filter(|f| !f.delete) {
            tree.insert(file.path.clone(), file.data.clone(), file_mode(file.mode))?;
        }
        Ok(tree)
    }
}

pub(crate) fn file_mode(mode: u32) -> u32 {
    if mode == 0 { 0o644 } else { mode & 0o777 }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;
    #[derive(Deserialize)]
    struct Case {
        paths: Vec<String>,
        valid: bool,
    }
    #[test]
    fn plan_paths_equal_go() {
        let cases: Vec<Case> =
            serde_json::from_str(include_str!("../../../testdata/oracle/plan-paths.json")).unwrap();
        for case in cases {
            let plan = Plan {
                files: case
                    .paths
                    .iter()
                    .map(|path| File {
                        path: path.clone(),
                        ..File::default()
                    })
                    .collect(),
            };
            assert_eq!(
                plan.merge(&Plan::new()).is_ok(),
                case.valid,
                "{:?}",
                case.paths
            );
        }
    }
    #[test]
    fn plan_copies_sorts_merges_and_builds_tree() {
        let mut plan = Plan::new();
        for path in ["b", "dir//a", "gone"] {
            plan.add(File {
                path: path.into(),
                data: b"content".to_vec(),
                delete: path == "gone",
                ..File::default()
            })
            .unwrap();
        }
        assert_eq!(plan.paths(), ["b", "dir/a", "gone"]);
        let mut copy = plan.get("dir//a").unwrap();
        copy.data.clear();
        assert_eq!(plan.get("dir/a").unwrap().data, b"content");
        assert!(plan.get("../escape").is_none());
        assert!(plan.merge(&plan).is_err());
        let merged = plan.merge(&Plan::new()).unwrap();
        assert_eq!(merged.files[0].path, "b");
        let tree = plan.fs().unwrap();
        assert_eq!(tree.len(), 2);
        assert_eq!(tree.get("dir/a").unwrap().mode, 0o644);
        assert!(!tree.contains("gone"));
    }
    #[test]
    fn simple_fold_collisions() {
        for (a, b) in [
            ("K", "K"),
            ("S", "ſ"),
            ("Σ", "ς"),
            ("ß", "ẞ"),
            ("Μ", "µ"),
            ("Θ", "ϑ"),
        ] {
            let mut plan = Plan::new();
            plan.add(File {
                path: a.into(),
                ..File::default()
            })
            .unwrap();
            assert!(
                plan.add(File {
                    path: b.into(),
                    ..File::default()
                })
                .is_err()
            );
            assert_eq!(plan.files.len(), 1);
        }
        assert_ne!(fold("I"), fold("ı"));
        assert_ne!(fold("ss"), fold("ß"));
    }
}
