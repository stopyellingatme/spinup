//! `spinup restore`: writes the rendered file again and clears its tombstone (Go
//! `drift.Restore`).

use anyhow::{Result, anyhow, bail};
use spinup_manifest::hash::hash;
use spinup_manifest::lock::FileEntry;
use spinup_project::config::LOCK_FILE;
use spinup_vfs::apply::{ApplyOptions, apply};
use spinup_vfs::path::clean_path;
use spinup_vfs::plan::Plan;

pub use crate::diff::Options;
use crate::diff::app::{ReadError, canonical, class_of, disk_error, load, read};

#[cfg(test)]
mod tests;

/// Writes the locked template render of `name` and records it in the lock.
///
/// The path must be tracked or tombstoned, and the locked template must render it. A file
/// on disk that differs from the render needs `force`. The lock keeps the class of a
/// tracked path. With [`Options::dry_run`], nothing is written.
///
/// # Errors
/// Returns an error when the project cannot be loaded, the path is not portable, the
/// template does not render the path, the path is neither tracked nor tombstoned, the file
/// differs without `force`, or the write fails (also through a symbolic link).
pub fn restore(o: &Options, name: &str, force: bool) -> Result<()> {
    let mut app = load(o)?;
    let name = clean_path(name)?;
    let Some(file) = app.plan.get(&name) else {
        bail!(
            "cannot restore {name:?}: the template does not contain this path\nTry: select a path from spinup diff"
        );
    };
    let tracked = app.lock.files.get(&name).map(|e| e.class);
    if tracked.is_none() && !app.lock.tombstones.contains(&name) {
        bail!(
            "cannot restore {name:?}: the path is not tracked or tombstoned\nTry: select a path from spinup diff"
        );
    }
    match read(&canonical(&app.root)?, &name) {
        Err(ReadError::NotFound) => {}
        Err(e) => return Err(disk_error(format!("{name:?}: {e}"))),
        Ok(data) if data != file.data && !force => bail!(
            "cannot restore {name:?}: the existing file differs from the template\nTry: spinup restore {name} --force"
        ),
        Ok(_) => {}
    }
    let class = tracked.unwrap_or_else(|| class_of(&file.class));
    app.lock.set_file(
        &name,
        FileEntry {
            class,
            template: file.template.clone(),
            hash: hash(&file.data),
            mode: format!("{:04o}", file.mode & 0o777),
        },
    )?;
    if o.dry_run {
        return Ok(());
    }
    let plan = Plan { files: vec![file] };
    apply(&app.root, &plan, ApplyOptions::default())
        .map_err(|e| anyhow!("cannot restore {name:?}: {e:#}\nTry: check the file permissions"))?;
    app.lock.save(app.root.join(LOCK_FILE))?;
    Ok(())
}
