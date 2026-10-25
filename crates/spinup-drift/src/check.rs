//! `spinup check` (Go `drift.Check`) and the local doctor run (Go `drift.Doctor`).

use std::path::Path;
use std::sync::Arc;

use anyhow::{Result, anyhow};
use serde::Serialize;
use spinup_compat::version::parse_version;
use spinup_doctor::registry::{self, Check, Context, Scope, Status};
use spinup_exec::runner::Runner;
use spinup_manifest::hash::hash;
use spinup_manifest::lock::{Class, Lock};
use spinup_project::config::LOCK_FILE;
use spinup_tplstore::source::Source;
use spinup_ui::error::Error as UiError;
use spinup_ui::exit;
use spinup_ui::printer::Printer;

pub use crate::diff::Options;
use crate::diff::app::{ReadError, canonical, disk_error, find, source};

#[cfg(test)]
mod tests;

/// A problem that fails `spinup check`, with its correction (Go `drift.Problem`).
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Problem {
    /// The path of the problem. Lock problems name `spinup.lock`.
    pub path: String,
    /// One of `invalid-lock`, `template-unavailable`, `behind`, `missing`, `not-regular`,
    /// `drift`, or `conflict`.
    pub state: String,
    /// What is wrong.
    pub message: String,
    /// The next step for the user.
    #[serde(rename = "try")]
    pub try_hint: String,
}

impl Problem {
    fn new(
        path: &str,
        state: &str,
        message: impl Into<String>,
        try_hint: impl Into<String>,
    ) -> Self {
        Self {
            path: path.into(),
            state: state.into(),
            message: message.into(),
            try_hint: try_hint.into(),
        }
    }
}

#[derive(Serialize)]
struct Problems<'a> {
    problems: &'a [Problem],
}

/// Reports lock problems, managed drift, conflict markers, and newer template versions.
///
/// Without `allow_behind`, the locked template version must load, and a newer loadable
/// version is a problem. The problems print in path order of the lock, then the template
/// problems. Any problem returns an error with the exit code [`exit::DRIFT`].
///
/// # Errors
/// Returns an error when the project cannot be found or read, or when a problem is found.
pub fn check(o: &Options, allow_behind: bool, out: &Printer) -> Result<()> {
    let root = find(&o.dir)?;
    let mut problems = Vec::new();
    match Lock::load(root.join(LOCK_FILE)) {
        Err(e) => problems.push(Problem::new(
            LOCK_FILE,
            "invalid-lock",
            e.to_string(),
            "repair spinup.lock",
        )),
        Ok(lock) => {
            problems = inspect(&root, &lock)?;
            if !allow_behind {
                let source = source(o)?;
                match source.load(&lock.template_version) {
                    Err(e) => problems.push(Problem::new(
                        LOCK_FILE,
                        "template-unavailable",
                        format!("locked template cannot load: {e}"),
                        "spinup cache warm",
                    )),
                    Ok(_) => check_behind(source.as_ref(), &lock, &mut problems)?,
                }
            }
        }
    }
    if o.json {
        out.json(&Problems {
            problems: &problems,
        })?;
    } else {
        for p in &problems {
            out.print(&format!(
                "{}: {} ({})\nTry: {}\n",
                p.path, p.message, p.state, p.try_hint
            ));
        }
    }
    if !problems.is_empty() {
        return Err(UiError::new("Application check failed")
            .why("the project has drift")
            .try_hint("correct the reported problems and run spinup check")
            .code(exit::DRIFT)
            .into());
    }
    Ok(())
}

/// Adds a `behind` problem when the source has a newer loadable version (Go `checkBehind`).
fn check_behind(source: &dyn Source, lock: &Lock, problems: &mut Vec<Problem>) -> Result<()> {
    let versions = source.versions()?;
    let current = parse_version(&lock.template_version)
        .map_err(|e| anyhow!("cannot compare template version: {e}\nTry: repair spinup.lock"))?;
    for version in versions.iter().rev() {
        if parse_version(version)?.compare(&current) > 0 {
            source.load(version)?;
            problems.push(Problem::new(
                LOCK_FILE,
                "behind",
                format!("newer template available: {version}"),
                "spinup upgrade",
            ));
            break;
        }
    }
    Ok(())
}

/// Compares each tracked file with its lock entry (Go `inspect`).
fn inspect(root: &Path, lock: &Lock) -> Result<Vec<Problem>> {
    let root = canonical(root)?;
    let mut problems = Vec::new();
    for (name, entry) in &lock.files {
        let data = match crate::diff::app::read(&root, name) {
            Err(ReadError::NotFound) => {
                if entry.class == Class::Managed {
                    problems.push(Problem::new(
                        name,
                        "missing",
                        "managed file is missing",
                        format!("spinup restore {name}"),
                    ));
                }
                continue;
            }
            Err(ReadError::NotRegular) => {
                problems.push(Problem::new(
                    name,
                    "not-regular",
                    "tracked path is not a regular file",
                    format!("replace {name} with a regular file"),
                ));
                continue;
            }
            Err(e) => return Err(disk_error(format!("{name:?}: {e}"))),
            Ok(data) => data,
        };
        if entry.class == Class::Managed && hash(&data) != entry.hash {
            problems.push(Problem::new(
                name,
                "drift",
                "managed file differs from its lock hash",
                format!("spinup eject {name} or spinup restore {name}"),
            ));
        }
        if conflict_markers(&data) {
            problems.push(Problem::new(
                name,
                "conflict",
                "file contains conflict markers",
                format!("resolve the conflict markers in {name}"),
            ));
        }
    }
    Ok(problems)
}

/// Reports whether `data` has a `<<<<<<<` line followed later by a `>>>>>>>` line (Go
/// `conflictMarkers`). A line can end with `\r\n`.
pub(crate) fn conflict_markers(data: &[u8]) -> bool {
    let mut started = false;
    for line in data.split(|b| *b == b'\n') {
        let line = line.strip_suffix(b"\r").unwrap_or(line);
        if line.starts_with(b"<<<<<<<") {
            started = true;
            continue;
        }
        if started && line.starts_with(b">>>>>>>") {
            return true;
        }
    }
    false
}

/// One doctor result in the JSON output.
#[derive(Serialize)]
struct Report<'a> {
    id: &'a str,
    status: &'a str,
    message: &'a str,
    #[serde(rename = "try", skip_serializing_if = "str::is_empty")]
    try_hint: &'a str,
}

/// Runs the registered local doctor checks, also outside a project (Go `drift.Doctor`).
///
/// The checks come from the registry of `spinup-doctor` ([`registry::CHECKS`]). This is the
/// seam for R3c: the crate `spinup-doctor-checks` registers more checks in the same slice,
/// and this function runs them with no change. Each check gets the project root (empty
/// outside a project), `quick`, the runner, and the dependency `cli_version` (an
/// `Arc<String>`, read with `Context::dep::<String>`).
///
/// # Errors
/// Returns an error when `dir` cannot be read, or an error with the exit code
/// [`exit::ERROR`] when a check fails.
pub fn doctor(o: &Options, quick: bool, runner: Arc<dyn Runner>, out: &Printer) -> Result<()> {
    doctor_with(registry::checks(Scope::Local, false), o, quick, runner, out)
}

/// Runs `checks` as [`doctor`] does. Tests pass their own checks.
pub(crate) fn doctor_with(
    checks: Vec<&Check>,
    o: &Options,
    quick: bool,
    runner: Arc<dyn Runner>,
    out: &Printer,
) -> Result<()> {
    let dir = if o.dir.as_os_str().is_empty() {
        Path::new(".")
    } else {
        o.dir.as_path()
    };
    std::fs::metadata(dir).map_err(disk_error)?;
    let mut ctx = Context::new(find(dir).unwrap_or_default());
    ctx.quick = quick;
    ctx.runner = Some(runner);
    ctx.deps
        .insert("cli_version".into(), Arc::new(o.version.clone()));
    let reports = registry::run_checks(checks, &ctx);
    let mut failed = false;
    let mut result = Vec::with_capacity(reports.len());
    for r in &reports {
        failed |= r.result.status == Status::Fail;
        result.push(Report {
            id: r.check.id,
            status: r.result.status.as_str(),
            message: &r.result.message,
            try_hint: &r.result.try_hint,
        });
        if !o.json {
            out.print(&format!(
                "{}\t{}\t{}\n",
                r.result.status, r.check.id, r.result.message
            ));
            if !r.result.try_hint.is_empty() {
                out.print(&format!("Try: {}\n", r.result.try_hint));
            }
        }
    }
    if o.json {
        out.json(&result)?;
    }
    if failed {
        return Err(UiError::new("Doctor checks failed")
            .why("a local health check failed")
            .try_hint("correct the reported failures and run spinup doctor")
            .code(exit::ERROR)
            .into());
    }
    Ok(())
}
