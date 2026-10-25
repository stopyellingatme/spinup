//! Project root lookup.

use std::path::{Component, Path, PathBuf};

use crate::config::{CONFIG_FILE, Error};

/// Find the nearest parent containing a `spinup.toml` file.
/// A file as the starting path selects its containing directory.
pub fn find_root(start: impl AsRef<Path>) -> Result<PathBuf, Error> {
    let start = start.as_ref();
    let lookup = if start.as_os_str().is_empty() {
        Path::new(".")
    } else {
        start
    };
    let absolute = std::path::absolute(lookup).map_err(|e| {
        Error::new(
            format!("resolve project directory {start:?}: {e}"),
            "check the directory, or pass --dir",
        )
    })?;
    // Match filepath.Abs: clean lexical components without resolving symlinks.
    let mut path = PathBuf::new();
    for component in absolute.components() {
        match component {
            Component::ParentDir => {
                path.pop();
            }
            Component::CurDir => {}
            other => path.push(other.as_os_str()),
        }
    }
    let info = std::fs::metadata(&path).map_err(|e| {
        Error::new(
            format!("inspect project directory {path:?}: {e}"),
            "check the directory, or pass --dir",
        )
    })?;
    if !info.is_dir() {
        path.pop();
    }
    loop {
        if std::fs::metadata(path.join(CONFIG_FILE)).is_ok_and(|info| !info.is_dir()) {
            return Ok(path);
        }
        if !path.pop() {
            break;
        }
    }
    Err(Error::new(
        format!("could not find {CONFIG_FILE} from {start:?}."),
        "run this inside a spinup project, or pass --dir",
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nearest_root_from_directory_or_file() {
        let root = tempfile::tempdir().unwrap();
        std::fs::write(root.path().join(CONFIG_FILE), "[app]\nname = 'demo'\n").unwrap();
        let child = root.path().join("one/two");
        std::fs::create_dir_all(&child).unwrap();
        assert_eq!(find_root(&child).unwrap(), root.path());
        assert_eq!(find_root(child.join("missing/..")).unwrap(), root.path());
        std::fs::write(child.join("source.txt"), "").unwrap();
        assert_eq!(find_root(child.join("source.txt")).unwrap(), root.path());
        std::fs::create_dir(child.join(CONFIG_FILE)).unwrap();
        assert_eq!(find_root(&child).unwrap(), root.path());
        std::fs::remove_dir(child.join(CONFIG_FILE)).unwrap();
        std::fs::write(child.join(CONFIG_FILE), "").unwrap();
        assert_eq!(find_root(child.join("./")).unwrap(), child);
        assert!(crate::config::load(root.path()).is_ok());
    }

    #[test]
    fn empty_start_means_current_directory() {
        let outcome = |start| {
            find_root(start).map_err(|e| e.to_string().starts_with("could not find spinup.toml"))
        };
        assert_eq!(outcome(""), outcome("."));
    }

    #[test]
    fn missing_root_and_missing_start_have_hints() {
        let root = tempfile::tempdir().unwrap();
        let err = find_root(root.path()).unwrap_err().to_string();
        assert!(err.contains("Try: run this inside a spinup project, or pass --dir"));
        assert!(
            find_root(root.path().join("missing"))
                .unwrap_err()
                .to_string()
                .contains("inspect project directory")
        );
        assert!(
            crate::config::load(root.path())
                .unwrap_err()
                .to_string()
                .contains("\nTry:")
        );
    }
}
