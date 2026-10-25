//! The converted packs and Rust-only overlays embedded by `build.rs`.

use anyhow::Result;
use spinup_vfs::tree::Tree;

include!(concat!(env!("OUT_DIR"), "/embedded.rs"));

/// Returns the embedded packs after applying the Rust-only overlays.
/// The returned tree includes `ENGINE` and all files, including hidden files.
pub fn embedded() -> Result<Tree> {
    let converted = from_entries(PACKS)?;
    crate::engine::validate(&converted)?;
    let merged = crate::overlay::merge(&converted, &from_entries(OVERLAY)?)?;
    crate::engine::validate(&merged)?;
    Ok(merged)
}

fn from_entries(entries: &[(&str, &[u8])]) -> Result<Tree> {
    let mut tree = Tree::new();
    for (path, data) in entries {
        tree.insert(*path, data.to_vec(), 0o644)?;
    }
    Ok(tree)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn list_and_embedded() {
        let tree = embedded().unwrap();
        let names = crate::loader::list(&tree).unwrap();
        assert!(names.iter().any(|n| n == "_sample"));
        assert!(!names.iter().any(|n| n == "_inline"));
        for name in &names {
            crate::loader::load(&tree, name).unwrap_or_else(|error| panic!("{name}: {error:#}"));
        }
        let p = crate::loader::load(&tree, "_sample").unwrap();
        assert_eq!(p.name, "sample");
        assert_eq!(p.questions.len(), 5);
        assert_eq!(p.files.len(), 8);
        assert!(p.files.iter().all(|f| f.condition.is_some()));
        assert!(tree.iter().any(|(path, _)| path.contains("/.")));
        assert!(tree.contains("_sample/files/api/cmd/main.txt.tmpl"));
    }
}

#[cfg(test)]
#[path = "digest.rs"]
mod digest;
