//! `spinup eject`: a managed file becomes a scaffold file in the lock (Go `drift.Eject`).

use anyhow::{Result, bail};
use spinup_manifest::lock::Class;
use spinup_project::config::LOCK_FILE;
use spinup_vfs::path::clean_path;

pub use crate::diff::Options;
use crate::diff::app::load;

#[cfg(test)]
mod tests;

/// Changes the class of the managed file `name` to scaffold and saves the lock.
///
/// The file content does not change. An upgrade then merges the file with the user's
/// changes. With [`Options::dry_run`], the lock does not change.
///
/// # Errors
/// Returns an error when the project cannot be loaded, the path is not portable, the path
/// is not a managed file of the lock, or the lock cannot be saved.
pub fn eject(o: &Options, name: &str) -> Result<()> {
    let mut app = load(o)?;
    let name = clean_path(name)?;
    let Some(entry) = app
        .lock
        .files
        .get(&name)
        .filter(|e| e.class == Class::Managed)
    else {
        bail!(
            "cannot eject {name:?}: the path is not managed\nTry: select a managed path with spinup diff --json"
        );
    };
    let mut entry = entry.clone();
    entry.class = Class::Scaffold;
    app.lock.set_file(&name, entry)?;
    if o.dry_run {
        return Ok(());
    }
    app.lock.save(app.root.join(LOCK_FILE))?;
    Ok(())
}
