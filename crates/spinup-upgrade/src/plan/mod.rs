//! The per-file decisions of one upgrade step (Go `internal/upgrade/plan.go` and
//! `plan_format.go`).
//!
//! [`build_plan`] reads the project files, renders the OLD and NEW template versions, and
//! returns one [`FileDecision`] for each path. It never writes files. [`Plan::format`]
//! prints the plan.

use std::collections::{BTreeMap, BTreeSet};
use std::io::ErrorKind;
use std::path::{Path, PathBuf};

use anyhow::{Result, anyhow, bail};
use serde_json::{Map, Value};
use spinup_exec::runner::Cancellation;
use spinup_manifest::hash::{hash, is_binary, normalize};
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_merge::contract::{Input, MAX_SIZE, Merger};
use spinup_tplstore::source::Version;
use spinup_vfs::path::clean_path;
use spinup_vfs::plan::File;

use crate::contract::{Action, FileDecision, Plan};
use crate::source::{PackSet, Templates, render};

mod format;
#[cfg(test)]
pub(crate) mod testing;
#[cfg(test)]
mod tests;

/// The reason of a kept Go module file. The decision keeps the NEW render in `data`, because
/// the dependency step reads it.
pub const GO_MOD_KEEP_REASON: &str = "go.mod: the dependency step applies the template changes";

/// The reason of a file that a generator wrote last.
pub const GENERATOR_OWNED: &str = "generator-owned";

/// The reason of a tombstone.
pub const TOMBSTONE: &str = "tombstone";

/// The reason of a changed file without an OLD render.
pub const NO_BASE: &str = "no base version";

const PATH_EXISTS: &str = "path exists";
const UNMERGEABLE: &str = "binary or large file changed on disk";

/// The disk, the lock, the template versions, and the policy of one step (Go `PlanInput`).
#[derive(Clone, Copy)]
pub struct PlanInput<'a> {
    /// The project directory whose files the planner reads.
    pub root: &'a Path,
    /// The current lock. It must be valid.
    pub lock: &'a Lock,
    /// The template version before the step. `None` means that it cannot be loaded: changed
    /// files then merge without a base.
    pub old: Option<&'a Version>,
    /// The template version after the step.
    pub new: &'a Version,
    /// The answers of the OLD render (the lock answers).
    pub old_answers: &'a Map<String, Value>,
    /// The answers of the NEW render.
    pub new_answers: &'a Map<String, Value>,
    /// The OLD packs. A set without names uses `lock.packs`.
    pub old_packs: &'a PackSet,
    /// The NEW packs. A set without names uses `lock.packs`.
    pub new_packs: &'a PackSet,
    /// The render seams.
    pub templates: Templates<'a>,
    /// Merges changed text. A step that must merge without a merger fails.
    pub merger: Option<&'a dyn Merger>,
    /// `--force-managed`: replace changed managed files.
    pub force_managed: bool,
    /// The cancel signal.
    pub cancel: &'a Cancellation,
}

impl std::fmt::Debug for PlanInput<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("PlanInput")
            .field("root", &self.root)
            .field("old", &self.old.map(|v| &v.version))
            .field("new", &self.new.version)
            .field("force_managed", &self.force_managed)
            .finish_non_exhaustive()
    }
}

#[derive(Debug, Clone, Default)]
struct Disk {
    data: Vec<u8>,
    mode: u32,
    exists: bool,
}

/// Reads the project files and returns the sorted decisions of one step. It writes no file
/// and does not change the lock (Go `BuildPlan`).
pub fn build_plan(input: &PlanInput<'_>) -> Result<Plan> {
    let lock = input.lock;
    lock.validate()?;
    check_cancel(input.cancel)?;
    let newer = render(
        &input.templates,
        input.new,
        input.new_packs,
        &lock.packs,
        input.new_answers,
    )?;
    let older = match input.old {
        Some(old) => {
            render(
                &input.templates,
                old,
                input.old_packs,
                &lock.packs,
                input.old_answers,
            )?
            .plan
        }
        None => Default::default(),
    };
    let renames = &newer.renames;
    let new_files = index(&newer.plan.files);
    let old_files = index(&older.files);
    let root = spinup_vfs::path::canonical(input.root).map_err(|e| {
        anyhow!(
            "open upgrade root {:?}: {e}\nTry: select a readable project directory",
            input.root
        )
    })?;
    if !root.is_dir() {
        bail!(
            "open upgrade root {:?}: not a directory\nTry: select a readable project directory",
            input.root
        );
    }
    let tombstoned = |name: &str| lock.tombstones.iter().any(|t| t == name);
    let mut plan = Plan {
        from: lock.template_version.clone(),
        to: input.new.version.clone(),
        ..Plan::default()
    };
    let paths: BTreeSet<&str> = lock
        .files
        .keys()
        .map(String::as_str)
        .chain(new_files.keys().copied())
        .chain(lock.tombstones.iter().map(String::as_str))
        .collect();
    let mut disks: BTreeMap<&str, Disk> = BTreeMap::new();
    for &name in &paths {
        check_cancel(input.cancel)?;
        if tombstoned(name) {
            continue;
        }
        let disk = read_disk(&root, name)?;
        if let Some(entry) = lock.files.get(name)
            && !disk.exists
        {
            plan.tombstoned.push(name.to_string());
            if entry.class == Class::Managed {
                plan.missing_managed.push(name.to_string());
            }
        }
        disks.insert(name, disk);
    }
    let exists = |name: &str| disks.get(name).is_some_and(|d| d.exists);
    // Reserve the rename sources before the removed paths are decided, in any sort order.
    let mut sources: BTreeMap<&str, &str> = BTreeMap::new();
    for (&path, file) in &new_files {
        let Some(from) = renames.get(path).map(String::as_str) else {
            continue;
        };
        let Some(entry) = lock.files.get(from) else {
            continue;
        };
        if lock.files.contains_key(path)
            || !exists(from)
            || plan_class(entry.class, class_of(&file.class)) == Class::Seed
            || tombstoned(path)
        {
            continue;
        }
        if let Some(previous) = sources.get(from) {
            bail!(
                "plan rename {path:?}: source {from:?} also moves to {previous:?}\nTry: give each rename a unique source"
            );
        }
        if new_files.contains_key(from) {
            bail!(
                "plan rename {path:?}: source {from:?} is still rendered\nTry: remove the source from the new templates"
            );
        }
        sources.insert(from, path);
    }
    let empty = Disk::default();
    for &name in &paths {
        if let Some(&target) = sources.get(name) {
            let file = new_files[target];
            if let Some(disk) = disks.get(target)
                && disk.exists
                && disk.data != file.data
            {
                let entry = &lock.files[name];
                plan.files.push(FileDecision {
                    path: name.to_string(),
                    from: None,
                    action: Action::Orphan,
                    class: entry.class,
                    template: entry.template.clone(),
                    data: Vec::new(),
                    mode: 0,
                    reason: "rename destination exists".into(),
                });
            }
            continue;
        }
        let entry = lock.files.get(name);
        let file = new_files.get(name).copied();
        let rendered = file.is_some();
        let mut d = FileDecision {
            path: name.to_string(),
            from: None,
            action: Action::Keep,
            // A path that the template does not render takes the class of its lock entry. A
            // path that is neither tracked nor rendered is a tombstone. Go leaves its class
            // empty. Seed is the nearest class: the planner never writes it.
            class: file
                .map(|f| class_of(&f.class))
                .or(entry.map(|e| e.class))
                .unwrap_or(Class::Seed),
            template: file.map(|f| f.template.clone()).unwrap_or_default(),
            data: file.map(|f| f.data.clone()).unwrap_or_default(),
            mode: file.map_or(0, |f| f.mode),
            reason: String::new(),
        };
        if let Some(entry) = entry {
            d.class = plan_class(entry.class, d.class);
        }
        let disk = disks.get(name).unwrap_or(&empty);
        let rename_from = renames
            .get(name)
            .map(String::as_str)
            .filter(|from| sources.get(from) == Some(&name));
        if tombstoned(name) {
            d.reason = TOMBSTONE.into();
        } else if let Some(entry) = entry.filter(|e| {
            generator_template(&e.template)
                && !rendered_pack_template(&old_files, name, &e.template)
        }) {
            // A generator wrote this file last (ADR 0007 §1). The templates never render or
            // delete it again. For a pack file, the version of the generator wins.
            d.class = entry.class;
            d.template.clone_from(&entry.template);
            d.reason = GENERATOR_OWNED.into();
        } else if entry.is_some() && !disk.exists {
            d.reason = "missing on disk".into();
        } else if (!rendered && entry.is_some_and(|e| e.class == Class::Seed))
            || d.class == Class::Seed
        {
            d.reason = "seed".into();
            if entry.is_none() && !disk.exists && rendered {
                d.action = Action::Create;
                d.reason.clear();
            }
        } else if let Some(from) = rename_from {
            if disk.exists && Some(&disk.data) != file.map(|f| &f.data) {
                let theirs = d.data.clone();
                d = merge_file(input, d, &[], &disk.data, &theirs, PATH_EXISTS)?;
            } else {
                let source_entry = &lock.files[from];
                d.class = plan_class(source_entry.class, d.class);
                d.from = Some(from.to_string());
                let source = disks.get(from).unwrap_or(&empty);
                d = decide_tracked(input, d, source_entry, source, &old_files, from, true)?;
            }
        } else if let (Some(entry), false) = (entry, rendered) {
            d.action = if hash(&disk.data) == entry.hash {
                Action::Delete
            } else {
                Action::Orphan
            };
        } else if let Some(entry) = entry {
            d = decide_tracked(input, d, entry, disk, &old_files, name, false)?;
        } else if !disk.exists {
            d.action = Action::Create;
        } else if Some(&disk.data) == file.map(|f| &f.data) {
            d.action = Action::Unchanged;
        } else {
            let theirs = d.data.clone();
            d = merge_file(input, d, &[], &disk.data, &theirs, PATH_EXISTS)?;
        }
        if matches!(d.action, Action::Keep | Action::Delete | Action::Orphan) {
            if let (Some(entry), false) = (entry, rendered) {
                d.class = entry.class;
                d.template.clone_from(&entry.template);
            }
            // The dependency step reads the NEW render of a kept dependency manifest. The write
            // of the plan never writes the data of a keep.
            if crate::cargo::kept_manifest(&d.path) != Some(d.reason.as_str()) {
                d.data.clear();
            }
        }
        plan.files.push(d);
    }
    Ok(plan)
}

/// Rejects a plan that merged changed files without a base, because the OLD template
/// version could not be loaded (Go `requireBase`). Such a merge makes each changed file a
/// whole-file conflict and hides the real template change.
pub fn require_base(plan: &Plan, old_loaded: bool, from: &str) -> Result<()> {
    if old_loaded {
        return Ok(());
    }
    let names: Vec<&str> = plan
        .files
        .iter()
        .filter(|f| f.action == Action::Conflict && f.reason == NO_BASE)
        .map(|f| f.path.as_str())
        .collect();
    if names.is_empty() {
        return Ok(());
    }
    bail!(
        "plan upgrade: template version {from} is unavailable, so {} changed files have no merge base: {}\nTry: run spinup cache warm {from}, or set SPINUP_TEMPLATES_DIR to a directory that holds that version",
        names.len(),
        names.join(", ")
    )
}

/// Keeps an ejected file (scaffold) when the template makes it managed. Every other class
/// change follows the template.
pub fn plan_class(old: Class, new: Class) -> Class {
    if old == Class::Scaffold && new == Class::Managed {
        Class::Scaffold
    } else {
        new
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

fn index(files: &[File]) -> BTreeMap<&str, &File> {
    files.iter().map(|f| (f.path.as_str(), f)).collect()
}

fn check_cancel(cancel: &Cancellation) -> Result<()> {
    if cancel.is_cancelled() {
        bail!("plan upgrade: canceled\nTry: retry the upgrade");
    }
    Ok(())
}

/// Reads one project file below `root` (canonical). A missing file is not an error. A path
/// that leaves the root, or that is not a regular file, is an error.
fn read_disk(root: &Path, name: &str) -> Result<Disk> {
    let clean = clean_path(name)?;
    if clean != name {
        bail!("read upgrade path {name:?}: path is not clean\nTry: repair the lock path");
    }
    let full: PathBuf = root.join(name);
    let resolved = match spinup_vfs::path::canonical(&full) {
        Ok(resolved) => resolved,
        Err(e) if e.kind() == ErrorKind::NotFound => return Ok(Disk::default()),
        Err(e) => bail!(
            "inspect upgrade path {name:?}: {e}\nTry: use a readable file inside the project root"
        ),
    };
    if !resolved.starts_with(root) {
        bail!(
            "inspect upgrade path {name:?}: path escapes from the project root\nTry: use a readable file inside the project root"
        );
    }
    let meta = std::fs::metadata(&resolved).map_err(|e| {
        anyhow!("inspect upgrade file {name:?}: {e}\nTry: check the file permissions")
    })?;
    if !meta.is_file() {
        bail!(
            "read upgrade file {name:?}: not a regular file\nTry: replace the path with a regular file"
        );
    }
    let data = std::fs::read(&resolved)
        .map_err(|e| anyhow!("read upgrade file {name:?}: {e}\nTry: check the file permissions"))?;
    Ok(Disk {
        data: normalize(&data),
        mode: permissions(&meta),
        exists: true,
    })
}

#[cfg(unix)]
fn permissions(meta: &std::fs::Metadata) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    meta.permissions().mode() & 0o777
}

/// Windows has no Unix permission bits. The planner then never plans a mode change.
#[cfg(not(unix))]
fn permissions(_meta: &std::fs::Metadata) -> u32 {
    NO_MODE
}

#[cfg(not(unix))]
const NO_MODE: u32 = u32::MAX;

fn same_mode(planned: u32, disk: u32) -> bool {
    #[cfg(not(unix))]
    if disk == NO_MODE {
        return true;
    }
    planned & 0o777 == disk
}

fn decide_tracked(
    input: &PlanInput<'_>,
    mut d: FileDecision,
    entry: &FileEntry,
    disk: &Disk,
    older: &BTreeMap<&str, &File>,
    base_path: &str,
    rename: bool,
) -> Result<FileDecision> {
    if !rename && d.data == disk.data {
        d.action = Action::Unchanged;
        if !same_mode(d.mode, disk.mode) {
            d.action = Action::Overwrite;
            d.reason = "mode".into();
        }
        return Ok(d);
    }
    if hash(&disk.data) == entry.hash {
        d.action = if rename {
            Action::Rename
        } else {
            Action::Overwrite
        };
        return Ok(d);
    }
    let base = older.get(base_path).map(|f| f.data.as_slice());
    if unmergeable(&[base.unwrap_or_default(), &disk.data, &d.data]) {
        if rename {
            bail!(
                "plan rename {:?} from {base_path:?}: changed binary or large file cannot be merged\nTry: move the source to the destination before upgrading",
                d.path
            );
        }
        d.action = Action::Keep;
        d.reason = UNMERGEABLE.into();
        return Ok(d);
    }
    if !rename && let Some(reason) = crate::cargo::kept_manifest(&d.path) {
        // The app changes its dependency manifest (go mod tidy, cargo update), so it differs
        // from the render. A text merge conflicts at every pin change. The dependency step
        // applies the template changes.
        d.action = Action::Keep;
        d.reason = reason.into();
        return Ok(d);
    }
    if !rename && d.class == Class::Managed && input.force_managed {
        d.action = Action::Overwrite;
        d.reason = "forced".into();
        return Ok(d);
    }
    let reason = if base.is_some() { "" } else { NO_BASE };
    let theirs = d.data.clone();
    let mut merged = merge_file(
        input,
        d,
        base.unwrap_or_default(),
        &disk.data,
        &theirs,
        reason,
    )?;
    if rename && merged.action == Action::Merge {
        merged.action = Action::Rename;
    }
    Ok(merged)
}

fn unmergeable(inputs: &[&[u8]]) -> bool {
    inputs
        .iter()
        .any(|data| is_binary(data) || data.len() > MAX_SIZE)
}

fn merge_file(
    input: &PlanInput<'_>,
    mut d: FileDecision,
    base: &[u8],
    ours: &[u8],
    theirs: &[u8],
    reason: &str,
) -> Result<FileDecision> {
    if unmergeable(&[base, ours, theirs]) {
        d.action = Action::Keep;
        d.reason = UNMERGEABLE.into();
        return Ok(d);
    }
    let Some(merger) = input.merger else {
        bail!(
            "merge upgrade file {:?}: merger is nil\nTry: configure a text merger",
            d.path
        );
    };
    let merged = merger
        .merge(
            &Input {
                path: &d.path,
                base,
                ours,
                theirs,
            },
            input.cancel,
        )
        .map_err(|e| {
            anyhow!(
                "merge upgrade file {:?}: {e:#}\nTry: correct the merge failure and retry",
                d.path
            )
        })?;
    d.action = if merged.conflicted || reason == PATH_EXISTS {
        Action::Conflict
    } else {
        Action::Merge
    };
    d.data = merged.data;
    d.reason = reason.to_string();
    Ok(d)
}

/// Separates the old generator IDs from the sources of the `gen/` pack.
fn rendered_pack_template(older: &BTreeMap<&str, &File>, name: &str, template: &str) -> bool {
    older.get(name).is_some_and(|f| f.template == template)
}

/// Accepts the reserved generator namespace and the old generator IDs.
pub(crate) fn generator_template(name: &str) -> bool {
    if name.starts_with("gen/") {
        return true;
    }
    let parts: Vec<&str> = name.splitn(4, '/').collect();
    parts.len() == 4 && parts[0] == "plugin" && matches!(parts[2], "@gen" | "@exec" | "gen")
}
