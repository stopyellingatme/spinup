//! Compare tracked files within a rooted directory.

use std::fmt;
use std::io::{self, Read};
use std::path::Path;

use cap_std::fs::Dir;

use crate::hash::hash;
use crate::lock::{Class, Error, FileEntry, Lock, entry_problems, validation_result};

/// A tracked file's content relative to its locked hash.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FileState {
    /// Normalized content matches the locked hash.
    Unchanged,
    /// Normalized content differs from the locked hash.
    Modified,
    /// The tracked file does not exist.
    Missing,
}

impl fmt::Display for FileState {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Self::Unchanged => "unchanged",
            Self::Modified => "modified",
            Self::Missing => "missing",
        })
    }
}

/// A tracked file's path, locked metadata, and current state.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FileStatus {
    /// The relative file path.
    pub path: String,
    /// The locked file metadata.
    pub entry: FileEntry,
    /// The content comparison result.
    pub state: FileState,
}

impl Lock {
    /// Compare normalized disk content with locked hashes in path order.
    /// Skip seeds and tombstones. Keep file access within the root.
    pub fn status(&self, root: impl AsRef<Path>) -> Result<Vec<FileStatus>, Error> {
        let paths = self.compared_paths()?;
        let root = root.as_ref();
        let dir = Dir::open_ambient_dir(root, cap_std::ambient_authority())
            .map_err(|error| disk_error("check file status", root, error))?;
        let mut statuses = Vec::with_capacity(paths.len());
        for path in paths {
            let entry = &self.files[&path];
            let state = match read_tracked_file(&dir, &path) {
                Err(error) if error.kind() == io::ErrorKind::NotFound => FileState::Missing,
                Err(error) => return Err(disk_error("check file status", Path::new(&path), error)),
                Ok(bytes) if hash(&bytes) != entry.hash => FileState::Modified,
                Ok(_) => FileState::Unchanged,
            };
            statuses.push(FileStatus {
                path,
                entry: entry.clone(),
                state,
            });
        }
        Ok(statuses)
    }

    /// Tombstone missing scaffold files and report missing managed files in path order.
    /// Skip seeds and tombstones. Leave the lock unchanged if any file check fails.
    /// Return the tombstoned paths first, then the missing managed paths.
    pub fn reconcile_missing(
        &mut self,
        root: impl AsRef<Path>,
    ) -> Result<(Vec<String>, Vec<String>), Error> {
        let paths = self.compared_paths()?;
        let root = root.as_ref();
        let dir = Dir::open_ambient_dir(root, cap_std::ambient_authority())
            .map_err(|error| disk_error("reconcile missing files", root, error))?;
        let mut tombstoned = Vec::new();
        let mut managed = Vec::new();
        for path in paths {
            match dir.metadata(&path) {
                Err(error) if error.kind() == io::ErrorKind::NotFound => {
                    if self.files[&path].class == Class::Scaffold {
                        tombstoned.push(path);
                    } else {
                        managed.push(path);
                    }
                }
                result => {
                    result
                        .and_then(|info| require_regular(info.is_file()))
                        .map_err(|error| {
                            disk_error("reconcile missing files", Path::new(&path), error)
                        })?;
                }
            }
        }
        for path in &tombstoned {
            self.tombstone(path);
        }
        Ok((tombstoned, managed))
    }

    fn compared_paths(&self) -> Result<Vec<String>, Error> {
        let mut problems = Vec::new();
        let mut paths = Vec::new();
        let tombstones: std::collections::BTreeSet<_> = self.tombstones.iter().collect();
        for (path, entry) in &self.files {
            if entry.class == Class::Seed || tombstones.contains(path) {
                continue;
            }
            entry_problems(path, entry, &mut problems);
            paths.push(path.clone());
        }
        validation_result(
            problems,
            "compare tracked files",
            "repair the listed entries in spinup.lock",
        )?;
        Ok(paths)
    }
}

fn read_tracked_file(root: &Dir, path: &str) -> io::Result<Vec<u8>> {
    let mut file = root.open(path)?;
    require_regular(file.metadata()?.is_file())?;
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes)?;
    Ok(bytes)
}

fn require_regular(is_file: bool) -> io::Result<()> {
    if is_file {
        Ok(())
    } else {
        Err(io::Error::other("tracked path is not a regular file"))
    }
}

fn disk_error(action: &str, path: &Path, error: io::Error) -> Error {
    Error::new(
        format!("{action} at {path:?}: {error}"),
        "check the project path and file permissions",
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hash::hash;
    use crate::lock::{Class, FileEntry, Lock};

    fn tracked(class: Class) -> FileEntry {
        FileEntry {
            class,
            template: "api/main.tmpl".into(),
            hash: hash(b"original"),
            mode: "0644".into(),
        }
    }

    fn write(root: &Path, path: &str, bytes: &[u8]) {
        let path = root.join(path);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, bytes).unwrap();
    }

    #[test]
    fn status_in_path_order_skips_seed_and_tombstones() {
        let root = tempfile::tempdir().unwrap();
        let mut lock = Lock::new();
        for (path, class) in [
            ("z/modified", Class::Managed),
            ("a/unchanged", Class::Scaffold),
            ("m/missing", Class::Managed),
            ("removed", Class::Scaffold),
        ] {
            lock.files.insert(path.into(), tracked(class));
        }
        // Skipped entries need no valid metadata, and tombstones take precedence.
        let mut seed = tracked(Class::Seed);
        seed.hash.clear();
        seed.template.clear();
        seed.mode.clear();
        lock.files.insert("seed".into(), seed);
        lock.tombstones.push("removed".into());
        write(root.path(), "z/modified", b"user change\n");
        write(root.path(), "a/unchanged", b"original\r\n\r\n");
        let statuses = lock.status(root.path()).unwrap();
        assert_eq!(
            statuses,
            vec![
                FileStatus {
                    path: "a/unchanged".into(),
                    entry: tracked(Class::Scaffold),
                    state: FileState::Unchanged
                },
                FileStatus {
                    path: "m/missing".into(),
                    entry: tracked(Class::Managed),
                    state: FileState::Missing
                },
                FileStatus {
                    path: "z/modified".into(),
                    entry: tracked(Class::Managed),
                    state: FileState::Modified
                },
            ]
        );
        for (state, text) in [
            (FileState::Unchanged, "unchanged"),
            (FileState::Modified, "modified"),
            (FileState::Missing, "missing"),
        ] {
            assert_eq!(state.to_string(), text);
        }
    }

    #[test]
    fn reconcile_tombstones_only_missing_scaffolds() {
        let root = tempfile::tempdir().unwrap();
        let mut lock = Lock::new();
        for path in ["z/scaffold", "a/scaffold", "present"] {
            lock.files.insert(path.into(), tracked(Class::Scaffold));
        }
        for path in ["z/managed", "a/managed", "modified"] {
            lock.files.insert(path.into(), tracked(Class::Managed));
        }
        lock.files.insert("seed".into(), tracked(Class::Seed));
        write(root.path(), "present", b"original");
        write(root.path(), "modified", b"user change");
        let (tombstoned, managed) = lock.reconcile_missing(root.path()).unwrap();
        assert_eq!(tombstoned, ["a/scaffold", "z/scaffold"]);
        assert_eq!(managed, ["a/managed", "z/managed"]);
        assert_eq!(lock.tombstones, tombstoned);
        assert_eq!(
            lock.files.keys().map(String::as_str).collect::<Vec<_>>(),
            ["a/managed", "modified", "present", "seed", "z/managed"]
        );
        let (tombstoned, again) = lock.reconcile_missing(root.path()).unwrap();
        assert!(tombstoned.is_empty());
        assert_eq!(again, managed);
        lock.validate().unwrap();
    }

    fn assert_disk_error(lock: &mut Lock, root: &Path) {
        let before = lock.marshal().ok();
        assert!(
            lock.status(root)
                .unwrap_err()
                .to_string()
                .contains("\nTry:")
        );
        assert!(
            lock.reconcile_missing(root)
                .unwrap_err()
                .to_string()
                .contains("\nTry:")
        );
        assert_eq!(lock.marshal().ok(), before);
        assert!(lock.tombstones.is_empty());
        assert!(lock.files.contains_key("0-missing-scaffold"));
    }

    fn lock_for_error() -> Lock {
        let mut lock = Lock::new();
        lock.files.insert("a".into(), tracked(Class::Managed));
        lock.files
            .insert("0-missing-scaffold".into(), tracked(Class::Scaffold));
        lock
    }

    #[test]
    fn disk_errors_leave_lock_unchanged() {
        let root = tempfile::tempdir().unwrap();
        std::fs::create_dir(root.path().join("a")).unwrap();
        assert_disk_error(&mut lock_for_error(), root.path());
        assert_disk_error(&mut lock_for_error(), &root.path().join("absent"));
        let mut invalid = lock_for_error();
        invalid
            .files
            .insert("../outside".into(), tracked(Class::Scaffold));
        assert_disk_error(&mut invalid, root.path());
    }

    #[cfg(unix)]
    #[test]
    fn symlinks_stay_inside_root() {
        use std::os::unix::fs::symlink;
        let root = tempfile::tempdir().unwrap();
        let outside = tempfile::tempdir().unwrap();
        write(outside.path(), "secret", b"original");
        symlink(outside.path().join("secret"), root.path().join("a")).unwrap();
        assert_disk_error(&mut lock_for_error(), root.path());
        std::fs::remove_file(root.path().join("a")).unwrap();
        symlink(outside.path(), root.path().join("parent")).unwrap();
        let mut lock = lock_for_error();
        lock.files.remove("a");
        lock.files
            .insert("parent/secret".into(), tracked(Class::Managed));
        assert_disk_error(&mut lock, root.path());
        write(root.path(), "target", b"original");
        symlink("target", root.path().join("a")).unwrap();
        let mut lock = lock_for_error();
        assert_eq!(
            lock.status(root.path()).unwrap()[1].state,
            FileState::Unchanged
        );
        assert_eq!(
            lock.reconcile_missing(root.path()).unwrap().1,
            Vec::<String>::new()
        );
        // A dangling internal link counts as missing.
        std::fs::remove_file(root.path().join("target")).unwrap();
        assert_eq!(
            lock.status(root.path()).unwrap()[0].state,
            FileState::Missing
        );
    }
}
