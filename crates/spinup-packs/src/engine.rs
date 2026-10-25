//! Validate the engine marker of a converted pack tree.

use spinup_vfs::tree::Tree;

/// The engine required by the Rust CLI (ADR 0020 §3).
pub const ENGINE: &str = "minijinja";

/// A pack tree from another engine, including a Go-era tree without a marker.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error(
    "template tree uses the template engine {found:?}, but this spinup needs {ENGINE:?}\nTry: upgrade to a template version from a Rust release of spinup"
)]
pub struct Error {
    /// The declared engine, or an empty string when the marker is missing.
    pub found: String,
}

/// Rejects missing, invalid, and unsupported `ENGINE` markers.
pub fn validate(tree: &Tree) -> Result<(), Error> {
    let found = tree
        .get("ENGINE")
        .map(|file| String::from_utf8_lossy(&file.data).trim().to_string())
        .unwrap_or_default();
    if found == ENGINE {
        Ok(())
    } else {
        Err(Error { found })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn engine_marker() {
        assert_eq!(validate(&Tree::new()).unwrap_err().found, "");
        for (marker, valid) in [
            ("minijinja\n", true),
            ("go\n", false),
            ("", false),
            ("minijinja\nother", false),
        ] {
            let mut tree = Tree::new();
            tree.insert("ENGINE", marker.as_bytes().to_vec(), 0o644)
                .unwrap();
            assert_eq!(validate(&tree).is_ok(), valid);
            if !valid {
                assert!(validate(&tree).unwrap_err().to_string().contains("\nTry:"));
            }
        }
        let mut tree = Tree::new();
        tree.insert("ENGINE", vec![0xff], 0o644).unwrap();
        assert!(validate(&tree).is_err());
    }
}
