//! The shared parts of the drift commands (Go `internal/drift/drift.go`): the project
//! lookup, the render of the locked templates, and the safe read of tracked files.

use std::io;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use anyhow::{Result, anyhow};
use spinup_manifest::lock::{Class, Lock};
use spinup_project::config::LOCK_FILE;
use spinup_tplstore::source::Source;
use spinup_upgrade::source::{PackRenderer, Packs, locked_packs};
use spinup_vfs::plan::Plan;

use super::Options;

/// An application with its lock and the render of its locked templates.
pub(crate) struct Application {
    /// The project root (the directory of `spinup.toml`).
    pub root: PathBuf,
    /// The lock of the project.
    pub lock: Lock,
    /// The render of the locked template version, packs, and answers.
    pub plan: Plan,
}

/// Finds the project root from `dir` (Go `find`).
pub(crate) fn find(dir: &Path) -> Result<PathBuf> {
    let dir = if dir.as_os_str().is_empty() {
        Path::new(".")
    } else {
        dir
    };
    spinup_project::root::find_root(dir).map_err(|e| {
        anyhow!("cannot locate application: {e}\nTry: run inside a spinup project or pass --dir")
    })
}

/// Returns the template source of the options, or the default store (Go `store.Default`).
pub(crate) fn source(o: &Options) -> Result<Arc<dyn Source>> {
    if let Some(source) = &o.source {
        return Ok(Arc::clone(source));
    }
    Ok(Arc::new(spinup_tplstore::store::default(
        &o.version, o.offline,
    )?))
}

/// Loads the lock of the project and renders its locked templates (Go `load`).
pub(crate) fn load(o: &Options) -> Result<Application> {
    let root = find(&o.dir)?;
    let lock = Lock::load(root.join(LOCK_FILE))?;
    render_app(o, root, lock)
}

/// Renders the locked template version, packs, and answers (Go `renderApp`).
///
/// Go called `upgrade.LockedPacks` and `upgrade.Render`. This port calls
/// [`locked_packs`] and the real [`PackRenderer`] of `spinup-upgrade`.
fn render_app(o: &Options, root: PathBuf, lock: Lock) -> Result<Application> {
    let source = source(o)?;
    let version = source.load(&lock.template_version)?;
    check_plugins(&lock)?;
    let set = locked_packs(&lock, Some(&version), &[])?;
    let answers = spinup_upgrade::lock::answers(&lock)?;
    let rendered = Packs.render(&version, set.names_or(&lock.packs), &answers)?;
    Ok(Application {
        root,
        lock,
        plan: rendered.plan,
    })
}

/// The plugin seam of drift (Go `pack.ResolveLocked` in `upgrade.LockedPacks`).
///
/// R1 has no plugin support (the null source of ADR 0020 §1). A lock without plugins
/// renders with no historical plugins. A lock with plugins is an error with the text of
/// `PluginError::Unsupported`. R4 resolves the locked plugins here and passes them to
/// [`locked_packs`].
fn check_plugins(lock: &Lock) -> Result<()> {
    if lock.plugins.is_empty() {
        return Ok(());
    }
    let names: Vec<&str> = lock.plugins.iter().map(|p| p.name.as_str()).collect();
    Err(anyhow!(
        "plugin support is not in this build: the project declares {}\nTry: use a spinup build with plugin support, or remove the plugins from spinup.toml",
        names.join(", ")
    ))
}

/// Returns the lock name of a class.
pub(crate) fn class_name(class: Class) -> &'static str {
    match class {
        Class::Managed => "managed",
        Class::Scaffold => "scaffold",
        Class::Seed => "seed",
    }
}

/// Converts the class name of a rendered file. The renderer accepts only the three names.
pub(crate) fn class_of(name: &str) -> Class {
    match name {
        "scaffold" => Class::Scaffold,
        "seed" => Class::Seed,
        _ => Class::Managed,
    }
}

/// The failure of a safe read of a tracked file.
#[derive(Debug)]
pub(crate) enum ReadError {
    /// The file does not exist.
    NotFound,
    /// The path is not a regular file (a directory, a symbolic link, or a device).
    NotRegular,
    /// Any other failure, such as a path that leaves the project root.
    Io(io::Error),
}

/// Reads a tracked file inside `root` (Go `read` through `os.Root`).
///
/// `root` must be canonical. The final component must be a regular file: a symbolic link
/// is [`ReadError::NotRegular`]. A parent directory that resolves outside `root` is an
/// error.
pub(crate) fn read(root: &Path, name: &str) -> Result<Vec<u8>, ReadError> {
    let path = root.join(name);
    let parent = path.parent().unwrap_or(root);
    match spinup_vfs::path::canonical(parent) {
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Err(ReadError::NotFound),
        Err(e) => return Err(ReadError::Io(e)),
        Ok(resolved) if !resolved.starts_with(root) => {
            return Err(ReadError::Io(io::Error::other(format!(
                "openat {name}: path escapes from parent"
            ))));
        }
        Ok(_) => {}
    }
    let info = match std::fs::symlink_metadata(&path) {
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Err(ReadError::NotFound),
        Err(e) => return Err(ReadError::Io(e)),
        Ok(info) => info,
    };
    if !info.file_type().is_file() {
        return Err(ReadError::NotRegular);
    }
    std::fs::read(&path).map_err(ReadError::Io)
}

/// Returns the canonical project root for [`read`].
pub(crate) fn canonical(root: &Path) -> Result<PathBuf> {
    spinup_vfs::path::canonical(root).map_err(disk_error)
}

/// The error of a failed disk inspection (Go `diskError`).
pub(crate) fn disk_error(e: impl std::fmt::Display) -> anyhow::Error {
    anyhow!("cannot inspect application files: {e}\nTry: check the file paths and permissions")
}

impl std::fmt::Display for ReadError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::NotFound => f.write_str("file does not exist"),
            Self::NotRegular => f.write_str("not regular"),
            Self::Io(e) => write!(f, "{e}"),
        }
    }
}
