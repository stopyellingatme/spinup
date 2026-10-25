//! Atomic file replacement, backups, and rollback for a staged plan.
//!
//! An operation failure restores earlier changes. A process kill can leave a partial
//! tree. Callers that need crash recovery must use a separate recovery mechanism.

use crate::{
    diff::{Change, ChangeKind, compare},
    path::{open_root, rooted_path, user_error},
    plan::Plan,
};
use anyhow::{Result, bail};
use cap_std::fs::{Dir, OpenOptions, Permissions};
use std::{
    ffi::OsString,
    io::{self, Write},
    path::{Path, PathBuf},
};

/// Controls whether apply writes files.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct ApplyOptions {
    /// Compares files and produces text diffs without writes.
    pub dry_run: bool,
}

/// Applies a plan. Only a dry run returns text diffs.
pub fn apply(root: impl AsRef<Path>, plan: &Plan, options: ApplyOptions) -> Result<Vec<Change>> {
    apply_with_hook(root.as_ref(), plan, options, |_, _| Ok(()))
}

fn apply_with_hook(
    root: &Path,
    plan: &Plan,
    options: ApplyOptions,
    mut hook: impl FnMut(&str, &str) -> io::Result<()>,
) -> Result<Vec<Change>> {
    let (root, dir) = open_root(root)?;
    let changes = compare(&dir, &root, plan, options.dry_run)?;
    if options.dry_run {
        return Ok(changes);
    }
    let temporary = tempfile::Builder::new()
        .prefix(".spinup-vfs-")
        .tempdir_in(&root)
        .map_err(|e| user_error("prepare atomic apply", e))?;
    let backup = dir
        .open_dir(temporary.path().file_name().expect("backup directory name"))
        .map_err(|e| user_error("open apply backups", e))?;
    let mut applied = Vec::new();
    let mut directories = Vec::new();
    for change in &changes {
        if change.kind == ChangeKind::Unchanged {
            continue;
        }
        let result = (|| {
            hook(&change.path, "before-change")?;
            let path = rooted_path(&dir, &root, &change.path, change.kind != ChangeKind::Remove)?;
            if change.kind != ChangeKind::Remove {
                ensure_parents(&dir, &path, &mut directories)?;
            }
            apply_change(&dir, &backup, &path, change, &mut applied, &mut hook)
        })();
        if let Err(error) = result {
            let rollback_errors = rollback(&dir, &backup, applied, directories);
            if !rollback_errors.is_empty() {
                let saved = temporary.keep();
                bail!(
                    "apply {:?} failed: {error}. Rollback failed: {}. Backups remain at {:?}\nTry: restore the files from the backups and check the target permissions",
                    change.path,
                    rollback_errors.join(", "),
                    saved
                );
            }
            return Err(user_error(
                &format!("apply {:?} failed", change.path),
                error,
            ));
        }
    }
    Ok(changes)
}

struct Applied {
    parent: Dir,
    name: OsString,
    backup: Option<String>,
    kind: ChangeKind,
    permissions: Option<Permissions>,
}

fn ensure_parents(dir: &Dir, path: &Path, created: &mut Vec<PathBuf>) -> Result<()> {
    let mut current = PathBuf::new();
    for component in path.parent().unwrap_or(Path::new("")).components() {
        current.push(component);
        match dir.symlink_metadata(&current) {
            Ok(metadata) if metadata.is_dir() && !metadata.is_symlink() => {}
            Ok(_) => bail!("parent {:?} is not a directory", current),
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                dir.create_dir(&current)?;
                created.push(current.clone());
                #[cfg(unix)]
                {
                    use cap_std::fs::PermissionsExt;
                    dir.set_permissions(&current, Permissions::from_mode(0o755))?;
                }
            }
            Err(e) => return Err(e.into()),
        }
    }
    Ok(())
}

fn apply_change(
    dir: &Dir,
    backups: &Dir,
    path: &Path,
    change: &Change,
    applied: &mut Vec<Applied>,
    hook: &mut impl FnMut(&str, &str) -> io::Result<()>,
) -> Result<()> {
    let parent_path = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let parent = dir.open_dir(parent_path)?;
    let name = path
        .file_name()
        .expect("validated file path")
        .to_os_string();
    let mut entry = Applied {
        parent,
        name,
        backup: None,
        kind: change.kind,
        permissions: None,
    };
    if change.kind == ChangeKind::ModeOnly {
        hook(&change.path, "before-mode")?;
        let file = entry.parent.open(&entry.name)?;
        entry.permissions = Some(file.metadata()?.permissions());
        set_mode(&file, change.new_mode)?;
        applied.push(entry);
        return Ok(());
    }
    if matches!(change.kind, ChangeKind::Update | ChangeKind::Remove) {
        hook(&change.path, "before-backup")?;
        let backup_name = applied.len().to_string();
        // Move the original into the backups, as Go does. The write below then renames into a
        // free name.
        retry(|| entry.parent.rename(&entry.name, backups, &backup_name))
            .map_err(|e| step("move the original to the backups", e))?;
        entry.backup = Some(backup_name);
        if change.kind == ChangeKind::Remove {
            applied.push(entry);
            return Ok(());
        }
    }
    let (temp_name, mut temp) = create_temporary(&entry.parent)?;
    let result: Result<()> = (|| {
        hook(&change.path, "before-write")?;
        temp.write_all(&change.new)?;
        set_mode(&temp, change.new_mode)?;
        temp.sync_all()?;
        drop(temp);
        hook(&change.path, "before-rename")?;
        retry(|| entry.parent.rename(&temp_name, &entry.parent, &entry.name))
            .map_err(|e| step("rename the new content into place", e))?;
        Ok(())
    })();
    let _ = retry(|| entry.parent.remove_file(&temp_name));
    // Record the entry also on failure when it holds a backup, so the rollback restores it.
    if result.is_ok() || entry.backup.is_some() {
        applied.push(entry);
    }
    result
}

/// Runs a rename or a removal. On Windows it retries for up to two seconds while the error is
/// "access denied" or a sharing violation: right after a write, a scanner or the indexer can
/// hold the file open without delete sharing (as the Go toolchain's robustio and cargo do).
fn retry<T>(mut op: impl FnMut() -> io::Result<T>) -> io::Result<T> {
    #[cfg(windows)]
    {
        use std::time::{Duration, Instant};
        const ERROR_SHARING_VIOLATION: i32 = 32;
        let deadline = Instant::now() + Duration::from_secs(2);
        let mut delay = Duration::from_millis(5);
        loop {
            match op() {
                Err(error)
                    if (error.kind() == io::ErrorKind::PermissionDenied
                        || error.raw_os_error() == Some(ERROR_SHARING_VIOLATION))
                        && Instant::now() < deadline =>
                {
                    std::thread::sleep(delay);
                    delay = (delay * 2).min(Duration::from_millis(200));
                }
                result => return result,
            }
        }
    }
    #[cfg(not(windows))]
    {
        op()
    }
}

/// Names the apply step in an I/O error and keeps its kind.
fn step(label: &str, error: io::Error) -> io::Error {
    io::Error::new(error.kind(), format!("{label}: {error}"))
}

fn create_temporary(parent: &Dir) -> io::Result<(String, cap_std::fs::File)> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    for index in 0u64.. {
        let name = format!(".spinup-vfs-{index}");
        match parent.open_with(&name, &options) {
            Ok(file) => return Ok((name, file)),
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(e) => return Err(e),
        }
    }
    Err(io::Error::other("temporary file names are exhausted"))
}

fn set_mode(file: &cap_std::fs::File, mode: u32) -> io::Result<()> {
    #[cfg(unix)]
    {
        use cap_std::fs::PermissionsExt;
        file.set_permissions(Permissions::from_mode(mode))
    }
    #[cfg(not(unix))]
    {
        let mut permissions = file.metadata()?.permissions();
        permissions.set_readonly(mode & 0o200 == 0);
        file.set_permissions(permissions)
    }
}

fn rollback(
    dir: &Dir,
    backups: &Dir,
    applied: Vec<Applied>,
    directories: Vec<PathBuf>,
) -> Vec<String> {
    let mut errors = Vec::new();
    for entry in applied.into_iter().rev() {
        let result = match entry.kind {
            ChangeKind::Create => retry(|| entry.parent.remove_file(&entry.name)),
            ChangeKind::Remove | ChangeKind::Update => {
                // Remove the replacement before restoring the saved file, as Go does.
                // An update that failed before its rename left no replacement to remove.
                let remove = if entry.kind == ChangeKind::Update {
                    match retry(|| entry.parent.remove_file(&entry.name)) {
                        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
                        other => other,
                    }
                } else {
                    Ok(())
                };
                remove.and_then(|()| {
                    retry(|| {
                        backups.rename(
                            entry.backup.as_ref().expect("saved backup"),
                            &entry.parent,
                            &entry.name,
                        )
                    })
                })
            }
            ChangeKind::ModeOnly => entry
                .parent
                .set_permissions(&entry.name, entry.permissions.expect("saved permissions")),
            ChangeKind::Unchanged => Ok(()),
        };
        if let Err(error) = result {
            errors.push(format!("{:?}: {error}", entry.name));
        }
    }
    for path in directories.into_iter().rev() {
        if let Err(error) = dir.remove_dir(&path) {
            errors.push(format!("{path:?}: {error}"));
        }
    }
    errors
}

#[cfg(test)]
#[path = "tests/apply.rs"]
mod tests;
