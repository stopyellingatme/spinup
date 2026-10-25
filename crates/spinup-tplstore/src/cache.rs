//! Bounded cache reads and publication through `spinup-vfs`.

use crate::archive::{MAX_ARCHIVE, read_limited};
use anyhow::{Result, ensure};
use spinup_vfs::{
    apply::{ApplyOptions, apply},
    plan::{File, Plan},
};
use std::{
    fs, io,
    path::{Path, PathBuf},
};

/// Returns the release asset name for a validated version.
pub fn archive_name(version: &str) -> Result<String> {
    crate::store::validate_version(version)?;
    Ok(format!("templates-v{version}.tar.zst"))
}

pub(crate) fn read(path: &Path, limit: usize) -> Result<Vec<u8>> {
    read_limited(fs::File::open(path)?, limit)
}

pub(crate) fn version_from_name(name: &str) -> Option<&str> {
    let version = name.strip_prefix("templates-v")?.strip_suffix(".tar.zst")?;
    crate::store::validate_version(version).ok()?;
    Some(version)
}

pub(crate) fn directory_names(path: &Path) -> Result<Vec<(String, bool)>> {
    match fs::read_dir(path) {
        Ok(entries) => entries
            .map(|entry| {
                let entry = entry?;
                let kind = entry.file_type()?;
                Ok((
                    entry.file_name().to_string_lossy().into_owned(),
                    kind.is_dir(),
                ))
            })
            .collect(),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(Vec::new()),
        Err(error) => Err(error.into()),
    }
}

/// Publishes the signature before the archive. Readers verify each pair again.
pub fn publish(directory: &Path, version: &str, data: &[u8], signature: &[u8]) -> Result<()> {
    let name = archive_name(version)?;
    ensure!(
        data.len() <= MAX_ARCHIVE && signature.len() == 64,
        "cannot cache template archive: archive or signature exceeds the size limit\nTry: use a valid signed templates archive"
    );
    let (root, prefix) = existing_root(directory)?;
    // Separate plans preserve signature-first publication, even when an archive exists.
    for (name, bytes) in [(format!("{name}.sig"), signature), (name, data)] {
        let mut plan = Plan::new();
        plan.add(File {
            path: portable(&prefix.join(name))?,
            data: bytes.to_vec(),
            ..File::default()
        })?;
        apply(&root, &plan, ApplyOptions::default())?;
    }
    Ok(())
}

fn portable(path: &Path) -> Result<String> {
    let name = path.to_str().ok_or_else(|| anyhow::anyhow!(
        "cannot access template cache: path is not UTF-8\nTry: configure a UTF-8 cache directory"
    ))?;
    Ok(name.replace(std::path::MAIN_SEPARATOR, "/"))
}

fn existing_root(directory: &Path) -> Result<(PathBuf, PathBuf)> {
    let absolute = if directory.is_absolute() {
        directory.to_owned()
    } else {
        std::env::current_dir()?.join(directory)
    };
    // Reject parent traversal before selecting a writable ancestor.
    ensure!(
        !absolute
            .components()
            .any(|c| matches!(c, std::path::Component::ParentDir)),
        "cannot access template cache: parent paths are not allowed\nTry: use an absolute cache directory without parent segments"
    );
    let mut root = absolute.as_path();
    loop {
        match fs::symlink_metadata(root) {
            Ok(metadata) => {
                ensure!(
                    metadata.is_dir() && !metadata.is_symlink(),
                    "cannot access template cache: {:?} is not a regular directory\nTry: use a cache directory without symlinks",
                    root
                );
                return Ok((root.to_owned(), absolute.strip_prefix(root)?.to_owned()));
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                root = root.parent().ok_or(error)?;
            }
            Err(error) => return Err(error.into()),
        }
    }
}

/// Deletes cache archives and signatures through a VFS plan.
/// Empty directories remain because a VFS plan only deletes files.
pub fn clean(directory: &Path) -> Result<()> {
    let entries = directory_names(directory)?;
    if entries.is_empty() {
        return Ok(());
    }
    let metadata = fs::symlink_metadata(directory)?;
    ensure!(
        metadata.is_dir() && !metadata.is_symlink(),
        "cannot remove template cache: cache root is not a regular directory\nTry: use a cache directory without symlinks"
    );
    let mut plan = Plan::new();
    for (name, is_dir) in entries {
        if !is_dir
            && (version_from_name(&name).is_some()
                || name
                    .strip_suffix(".sig")
                    .and_then(version_from_name)
                    .is_some())
        {
            plan.add(File {
                path: name,
                delete: true,
                ..File::default()
            })?;
        }
    }
    if !plan.files.is_empty() {
        apply(directory, &plan, ApplyOptions::default())?;
    }
    Ok(())
}
