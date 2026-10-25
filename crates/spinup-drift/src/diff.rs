//! `spinup diff`: the differences between the locked templates and the disk (Go
//! `drift.Diff`).
//!
//! This module also holds [`Options`], the input of every drift command.

use std::collections::BTreeSet;
use std::path::PathBuf;
use std::sync::Arc;

use anyhow::Result;
use serde::Serialize;
use spinup_manifest::hash::is_binary;
use spinup_tplstore::source::Source;
use spinup_ui::printer::Printer;
use spinup_vfs::path::clean_path;

pub(crate) mod app;
#[cfg(test)]
pub(crate) mod testing;
#[cfg(test)]
mod tests;

use app::{ReadError, canonical, class_name, disk_error, load, read};

/// The command inputs of `diff`, `check`, `eject`, `restore`, and `doctor` (Go
/// `drift.Options`).
#[derive(Clone, Default)]
pub struct Options {
    /// The start directory of the project lookup. An empty path is the current directory.
    pub dir: PathBuf,
    /// The CLI version. The default template store uses it for the current packs.
    pub version: String,
    /// Forbid downloads of template versions.
    pub offline: bool,
    /// Write JSON, not text.
    pub json: bool,
    /// Do not write the lock or files.
    pub dry_run: bool,
    /// The template source. `None` uses the default store (Go `store.Default`). Tests set it.
    pub source: Option<Arc<dyn Source>>,
}

impl std::fmt::Debug for Options {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Options")
            .field("dir", &self.dir)
            .field("version", &self.version)
            .field("offline", &self.offline)
            .field("json", &self.json)
            .field("dry_run", &self.dry_run)
            .field("source", &self.source.as_ref().map(|_| "custom"))
            .finish()
    }
}

/// A path that differs from its template (Go `drift.File`).
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct File {
    /// The slash-separated path in the project.
    pub path: String,
    /// The class of the file. It is empty for an untracked or tombstoned path that the
    /// template does not render.
    pub class: String,
    /// One of `missing`, `modified`, `untracked`, or `tombstone`.
    pub state: String,
}

#[derive(Serialize)]
struct Files<'a> {
    files: &'a [File],
}

/// Prints the differences between the locked templates and the disk, in path order.
///
/// `paths` limits the output to these paths and the paths below them. A modified text
/// file prints a unified diff from `a/<path>` (the template) to `b/<path>` (the disk).
/// Other states print `<state>\t<class>\t<path>`. With [`Options::json`], it prints
/// `{"files": [...]}`.
///
/// # Errors
/// Returns an error when the project, the lock, the template version, or a file cannot be
/// read, or when a path is not a portable relative path.
pub fn diff(o: &Options, paths: &[String], out: &Printer) -> Result<()> {
    let app = load(o)?;
    let paths = paths
        .iter()
        .map(|p| clean_path(p))
        .collect::<Result<Vec<_>>>()?;
    let mut names: BTreeSet<&str> = app.lock.files.keys().map(String::as_str).collect();
    names.extend(app.lock.tombstones.iter().map(String::as_str));
    names.extend(app.plan.files.iter().map(|f| f.path.as_str()));
    let root = canonical(&app.root)?;
    let mut files = Vec::new();
    for name in names {
        if !selected(name, &paths) {
            continue;
        }
        let entry = app.lock.files.get(name);
        let rendered = app.plan.files.iter().find(|f| f.path == name);
        let rendered_class = rendered.map(|f| f.class.clone()).unwrap_or_default();
        let mut data = Vec::new();
        let state = if app.lock.tombstones.iter().any(|t| t == name) {
            ("tombstone", rendered_class)
        } else if let Some(entry) = entry {
            let class = class_name(entry.class).to_string();
            match read(&root, name) {
                Err(ReadError::NotFound) => ("missing", class),
                Err(e) => return Err(disk_error(format!("{name:?}: {e}"))),
                Ok(bytes) => match rendered {
                    Some(r) if r.data != bytes => {
                        data = bytes;
                        ("modified", class)
                    }
                    _ => continue,
                },
            }
        } else {
            ("untracked", rendered_class)
        };
        let file = File {
            path: name.to_string(),
            class: state.1,
            state: state.0.to_string(),
        };
        if !o.json {
            if file.state == "modified" {
                let template = rendered.map(|r| r.data.as_slice()).unwrap_or_default();
                out.print(&unified(name, template, &data));
            } else {
                out.print(&format!("{}\t{}\t{}\n", file.state, file.class, name));
            }
        }
        files.push(file);
    }
    if o.json {
        out.json(&Files { files: &files })?;
    }
    Ok(())
}

/// Returns the diff text of a modified file (Go `textdiff.Diff("a/"+name, ...)`).
///
/// It uses the patience diff of `spinup-vfs`. Console text is equal with Go at regex level
/// only (ADR 0020 §8): the header lines `diff`, `---`, and `+++` are the same, and the hunks
/// can differ.
fn unified(name: &str, template: &[u8], disk: &[u8]) -> String {
    if is_binary(template) || is_binary(disk) {
        return format!("{name}: binary files differ\n");
    }
    let body = spinup_vfs::diff::file_diff(name, template, disk);
    let mut text = format!("diff a/{name} b/{name}\n");
    text.push_str(&String::from_utf8_lossy(&body));
    if !text.ends_with('\n') {
        text.push('\n');
    }
    text
}

/// Reports whether `name` is one of `paths`, or below one of them. No paths select all.
fn selected(name: &str, paths: &[String]) -> bool {
    paths.is_empty()
        || paths.iter().any(|p| {
            name == p
                || name
                    .strip_prefix(p.as_str())
                    .is_some_and(|rest| rest.starts_with('/'))
        })
}
