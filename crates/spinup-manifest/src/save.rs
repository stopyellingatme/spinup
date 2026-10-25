//! Atomic lock loading and replacement.
use std::io::Write;
use std::path::Path;

use crate::lock::{Error, Lock};

/// Read and validate the lock at the supplied path.
pub fn load(path: impl AsRef<Path>) -> Result<Lock, Error> {
    let path = path.as_ref();
    let bytes = std::fs::read(path).map_err(|error| {
        Error::new(
            format!("load spinup.lock at {path:?}: {error}"),
            "check the lock path and file permissions",
        )
    })?;
    Lock::parse(&bytes)
}

impl Lock {
    /// Read and validate the lock at the supplied path.
    pub fn load(path: impl AsRef<Path>) -> Result<Self, Error> {
        load(path)
    }

    /// Atomically replace the destination with canonical JSON and mode 0644.
    /// Create and sync the temporary file in the destination directory.
    pub fn save(&self, path: impl AsRef<Path>) -> Result<(), Error> {
        let path = path.as_ref();
        let bytes = self.marshal()?;
        let save_error = |error: std::io::Error| {
            Error::new(
                format!("save spinup.lock at {path:?}: {error}"),
                "check the destination directory and file permissions",
            )
        };
        let parent = path
            .parent()
            .filter(|p| !p.as_os_str().is_empty())
            .unwrap_or_else(|| Path::new("."));
        let mut temp = tempfile::Builder::new()
            .prefix(".spinup.lock-")
            .tempfile_in(parent)
            .map_err(save_error)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            temp.as_file()
                .set_permissions(std::fs::Permissions::from_mode(0o644))
                .map_err(save_error)?;
        }
        temp.write_all(&bytes).map_err(save_error)?;
        temp.as_file().sync_all().map_err(save_error)?;
        temp.persist(path)
            .map_err(|error| save_error(error.error))?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn save_load_replace_and_cleanup() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("spinup.lock");
        let mut lock = Lock::new();
        lock.save(&path).unwrap();
        assert_eq!(std::fs::read(&path).unwrap(), lock.marshal().unwrap());
        lock.cli_version = "v1".into();
        lock.save(&path).unwrap();
        assert_eq!(load(&path).unwrap().cli_version, "v1");
        assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 1);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
                0o644
            );
        }
        let before = std::fs::read(&path).unwrap();
        lock.schema = 0;
        assert!(lock.save(&path).is_err());
        assert_eq!(std::fs::read(&path).unwrap(), before);
    }

    #[test]
    fn disk_errors_have_hints_and_remove_temporary_files() {
        let dir = tempfile::tempdir().unwrap();
        let lock = Lock::new();
        for path in [dir.path().join("absent/lock"), dir.path().to_path_buf()] {
            assert!(lock.save(&path).unwrap_err().to_string().contains("\nTry:"));
        }
        assert!(
            load(dir.path().join("absent"))
                .unwrap_err()
                .to_string()
                .contains("\nTry:")
        );
        assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 0);
    }
}
