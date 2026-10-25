//! File application and isolated preview storage.
use crate::{
    contract::{Action, Plan},
    state::{read_file, safe_path},
};
use anyhow::{Result, anyhow, bail};
use spinup_exec::runner::Cancellation;
use spinup_vfs::{
    apply::{ApplyOptions, apply},
    plan::{File, Plan as Writes},
};
use std::{
    collections::BTreeSet,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

/// Applies a planned step through VFS. Keep decisions change only the mode.
pub fn write_plan(root: &Path, plan: &Plan) -> Result<()> {
    let mut writes = Writes::new();
    for decision in &plan.files {
        safe_path(&decision.path)?;
        if let Some(from) = &decision.from {
            safe_path(from)?;
        }
        let mut file = File {
            path: decision.path.clone(),
            data: decision.data.clone(),
            mode: decision.mode,
            ..File::default()
        };
        match decision.action {
            Action::Delete => file.delete = true,
            Action::Overwrite
            | Action::Merge
            | Action::Conflict
            | Action::Create
            | Action::Rename => {}
            Action::Keep | Action::Orphan | Action::Unchanged => {
                if decision.mode & 0o777 == 0 {
                    continue;
                }
                match read_file(root, &decision.path)? {
                    Some(data) => file.data = data,
                    None if decision.action == Action::Keep => continue,
                    None => bail!(
                        "apply upgrade file {:?}: file is missing\nTry: restore the file before continuing",
                        decision.path
                    ),
                }
            }
        }
        writes.add(file)?;
        if matches!(
            decision.action,
            Action::Rename | Action::Conflict | Action::Merge | Action::Overwrite | Action::Create
        ) && let Some(from) = &decision.from
        {
            writes.add(File {
                path: from.clone(),
                delete: true,
                ..File::default()
            })?;
        }
    }
    apply(root, &writes, ApplyOptions::default())?;
    Ok(())
}

/// Copies only the files that the planner reads. Unrelated files stay outside the copy.
pub fn copy_plan_tree(
    source: &Path,
    target: &Path,
    paths: &BTreeSet<String>,
    cancel: &Cancellation,
) -> Result<()> {
    let mut writes = Writes::new();
    for path in paths {
        if cancel.is_cancelled() {
            bail!("copy project for planning: canceled\nTry: retry the upgrade");
        }
        safe_path(path)?;
        let Some(data) = read_file(source, path)? else {
            continue;
        };
        let meta = std::fs::metadata(source.join(path)).map_err(|e| {
            anyhow!("copy project file {path:?}: {e}\nTry: check project file permissions")
        })?;
        #[cfg(unix)]
        let mode = {
            use std::os::unix::fs::PermissionsExt;
            meta.permissions().mode() & 0o777
        };
        #[cfg(not(unix))]
        let mode = {
            let _ = meta;
            0o644
        };
        writes.add(File {
            path: path.clone(),
            data,
            mode,
            ..File::default()
        })?;
    }
    apply(target, &writes, ApplyOptions::default())?;
    Ok(())
}

// Temporary directory lifecycle only. Every file inside it is written through VFS.
// The upgrade crate has no production dependency on tempfile.
pub(super) struct Scratch(pub PathBuf);
impl Scratch {
    pub fn new() -> Result<Self> {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        for _ in 0..1000 {
            let path = std::env::temp_dir().join(format!(
                "spinup-plan-{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            let mut builder = std::fs::DirBuilder::new();
            #[cfg(unix)]
            {
                use std::os::unix::fs::DirBuilderExt;
                builder.mode(0o700);
            }
            match builder.create(&path) {
                Ok(()) => return Ok(Self(path)),
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => continue,
                Err(e) => {
                    bail!("create plan directory: {e}\nTry: check temporary directory permissions")
                }
            }
        }
        bail!(
            "create plan directory: temporary names are occupied\nTry: remove stale spinup-plan directories"
        );
    }
}
impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
