//! The upgrade engine contract: [`Action`], [`FileDecision`], [`Plan`], [`Options`],
//! [`Step`], [`Phases`], [`Extension`], and the [`EXTENSIONS`] slice (Go
//! `internal/upgrade/contract.go`, ADR 0006 §4, ADR 0020 §1).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it.
//!
//! Unit R1.17 writes the planner, unit R1.18 the state machine, and unit R2.10 the cargo
//! phases in `cargo/` (an empty [`Phases`] in R1).

use std::io::Write;
use std::path::PathBuf;
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use spinup_exec::runner::{Cancellation, Runner};
use spinup_manifest::lock::{Class, Lock};
use spinup_merge::contract::Merger;
use spinup_tplstore::source::Version;

/// The planned outcome for one file (architecture §9, critic resolutions 2 and 4).
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Action {
    /// The NEW render equals the file on disk. Nothing is written.
    Unchanged,
    /// The disk hash equals the lock hash, or `--force-managed` applies. Write NEW.
    Overwrite,
    /// A clean three-way merge. Write the merge result.
    Merge,
    /// A three-way merge with conflict markers. Write the result with the markers.
    Conflict,
    /// The file is new in the template, and the path is free. Write NEW.
    Create,
    /// The template removed the file, and the disk hash equals the lock hash.
    Delete,
    /// The template moved the file (`renamed_from`). Write at `path`, remove `from`.
    Rename,
    /// The template removed the file, but the user changed it. Keep it and record an orphan.
    Orphan,
    /// Leave the disk alone (seed, tombstone, or a changed binary or large file).
    Keep,
}

impl Action {
    /// Returns the name of the action in plan output and plan JSON, such as `merge`.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Unchanged => "unchanged",
            Self::Overwrite => "overwrite",
            Self::Merge => "merge",
            Self::Conflict => "conflict",
            Self::Create => "create",
            Self::Delete => "delete",
            Self::Rename => "rename",
            Self::Orphan => "orphan",
            Self::Keep => "keep",
        }
    }
}

/// The plan for one project path.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FileDecision {
    /// The target path (slash-separated, relative to the project root).
    pub path: String,
    /// The old path of a rename.
    pub from: Option<String>,
    /// The planned outcome.
    pub action: Action,
    /// The class from the NEW render (or from the lock for delete, orphan, and keep).
    pub class: Class,
    /// The template path from the NEW render (or from the lock).
    pub template: String,
    /// The content to write for overwrite, merge, conflict, create, and rename.
    pub data: Vec<u8>,
    /// The Unix permission bits to apply. Modes are compared and applied apart from content.
    pub mode: u32,
    /// One short line for the plan output.
    pub reason: String,
}

/// The decision for every tracked or rendered path of one upgrade step.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Plan {
    /// The template version before the step.
    pub from: String,
    /// The template version after the step.
    pub to: String,
    /// The decisions, sorted by path.
    pub files: Vec<FileDecision>,
    /// Tracked files missing from disk that become tombstones (critic resolution 4).
    pub tombstoned: Vec<String>,
    /// Managed files missing from disk (`check` warns about them).
    pub missing_managed: Vec<String>,
}

/// The user's choices for `spinup upgrade`.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Options {
    /// The target version (`--to`). `None` means the newest loadable version.
    pub to: Option<String>,
    /// `--allow-dirty`.
    pub allow_dirty: bool,
    /// `--force-managed`.
    pub force_managed: bool,
    /// `--no-verify`.
    pub no_verify: bool,
    /// `--yes`.
    pub yes: bool,
    /// `--commit-conflicts`.
    pub commit_conflicts: bool,
    /// `--offline`.
    pub offline: bool,
    /// `--dry-run`.
    pub dry_run: bool,
}

/// One upgrade step (one template version to the next) inside the upgrade worktree.
pub struct Step<'a> {
    /// The template version before the step.
    pub from: Arc<Version>,
    /// The template version after the step.
    pub to: Arc<Version>,
    /// The root of the upgrade worktree. Every phase writes only there.
    pub worktree: PathBuf,
    /// The lock that this step builds. Phases may update it (for example the applied
    /// migrations). The state machine writes it after the `after` phase.
    pub lock: Lock,
    /// The render answers. The `before` phase may change them.
    pub answers: serde_json::Map<String, serde_json::Value>,
    /// The plan. It is `None` in the `before` phase.
    pub plan: Option<Plan>,
    /// The user's choices.
    pub options: Options,
    /// Runs every subprocess.
    pub runner: &'a dyn Runner,
    /// The cancel signal of the CLI.
    pub cancel: &'a Cancellation,
    /// Progress output.
    pub out: &'a mut (dyn Write + Send),
    /// The three-way merger of the step, for phases and extensions that merge files.
    pub merger: &'a dyn Merger,
    /// Paths that got conflict markers after planning, for example from an extension. The
    /// state machine stops with exit 3 on them, as on plan conflicts.
    pub conflicts: Vec<String>,
}

/// A phase of a step.
pub type PhaseFn = for<'s, 'a> fn(&'s mut Step<'a>) -> anyhow::Result<()>;

/// A phase that reports whether it changed files.
pub type FixFn = for<'s, 'a> fn(&'s mut Step<'a>) -> anyhow::Result<bool>;

/// The pluggable parts of a step. The state machine (unit R1.18) calls them in this order:
/// `before`, plan, write the files of the plan, `apply`, `deps`, commit, `fix` (its own
/// commit when it changed files), `after`, lock write, commit, `verify`. A `None` phase is
/// skipped. `cargo::phases()` returns the phases (empty in R1, unit R2.10 fills them).
///
/// Go called the `fix` slot `GoFix`. In Rust it holds the `cargo` fix-ups of R2.
#[derive(Debug, Clone, Copy, Default)]
pub struct Phases {
    /// Runs before the renders.
    pub before: Option<PhaseFn>,
    /// Extra work after the write of the plan (for example the kit patch).
    pub apply: Option<PhaseFn>,
    /// The dependency step.
    pub deps: Option<PhaseFn>,
    /// Fix-ups after the dependency step.
    pub fix: Option<FixFn>,
    /// Runs after the new files and dependencies are in place.
    pub after: Option<PhaseFn>,
    /// Verifies the result (skipped with `--no-verify`).
    pub verify: Option<PhaseFn>,
}

/// Adds flags and work to `spinup upgrade`. Unit R1.18 adds `--ui` with it.
pub struct Extension {
    /// The extension name. It is unique.
    pub name: &'static str,
    /// Adds the flags of the extension to the `upgrade` command.
    pub args: fn(clap::Command) -> clap::Command,
    /// Runs once for each step, after the `after` phase. `matches` are the parsed
    /// arguments of the `upgrade` command.
    pub run: for<'s, 'a> fn(&'s mut Step<'a>, &clap::ArgMatches) -> anyhow::Result<()>,
}

impl std::fmt::Debug for Extension {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Extension")
            .field("name", &self.name)
            .finish_non_exhaustive()
    }
}

/// Every registered upgrade extension. The state machine runs them sorted by name.
#[linkme::distributed_slice]
pub static EXTENSIONS: [Extension];
