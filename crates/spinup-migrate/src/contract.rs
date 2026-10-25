//! The codemod contract: [`Stage`], [`Ctx`], [`Change`], [`Migration`], and the
//! [`MIGRATIONS`] slice (Go `internal/migrate/contract.go`, ADR 0006 §5, ADR 0020 §1 and §5).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it.
//!
//! A codemod registers itself in its own module:
//!
//! ```ignore
//! #[linkme::distributed_slice(spinup_migrate::contract::MIGRATIONS)]
//! static WEB_SVMIGRATE: Migration = Migration { id: "0001_web_svmigrate", /* ... */ };
//! ```

use std::path::Path;

use spinup_exec::runner::{Cancellation, Runner};
use spinup_manifest::lock::Lock;
use spinup_stack::stack::Stack;

/// When a migration runs during an upgrade step.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Stage {
    /// Runs before the OLD and NEW renders. It may change answers or move files, so that
    /// the OLD render stays valid.
    Before,
    /// Runs after the new files and dependencies are in place.
    After,
}

/// The input of one migration.
pub struct Ctx<'a> {
    /// The upgrade worktree. Migrations read and write files only under `root`.
    pub root: &'a Path,
    /// The template version before the current upgrade step.
    pub from: &'a str,
    /// The template version after the current upgrade step.
    pub to: &'a str,
    /// The render answers. A `Before` migration may change them.
    pub answers: &'a mut serde_json::Map<String, serde_json::Value>,
    /// The lock of the step. Migrations do not write it. The runner records the IDs.
    pub lock: &'a Lock,
    /// Runs every subprocess.
    pub runner: &'a dyn Runner,
    /// The cancel signal of the CLI.
    pub cancel: &'a Cancellation,
    /// The stack versions of the OLD template version, so a codemod can compare pins.
    /// It is `None` when the old version cannot be loaded.
    pub old_stack: Option<&'a Stack>,
    /// The stack versions of the NEW template version.
    pub new_stack: &'a Stack,
}

/// One planned file change, for `upgrade --plan`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Change {
    /// The slash-separated project path.
    pub path: String,
    /// One short line that describes the change.
    pub desc: String,
}

/// One codemod. It runs when `from < since <= to` and its ID is not in the applied
/// migrations of the lock. `plan` is pure. `apply` is idempotent: two runs give the same
/// tree as one run.
pub struct Migration {
    /// The ID, such as `0001_web_svmigrate`. It is unique and sorts in run order.
    pub id: &'static str,
    /// The first template version that needs the migration.
    pub since: &'static str,
    /// One short line for `upgrade --plan`.
    pub desc: &'static str,
    /// When the migration runs.
    pub stage: Stage,
    /// Plans the changes without writing.
    pub plan: fn(&mut Ctx<'_>) -> anyhow::Result<Vec<Change>>,
    /// Applies the changes.
    pub apply: fn(&mut Ctx<'_>) -> anyhow::Result<()>,
}

impl std::fmt::Debug for Migration {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Migration")
            .field("id", &self.id)
            .field("since", &self.since)
            .field("stage", &self.stage)
            .finish_non_exhaustive()
    }
}

/// Every registered migration, in link order. The registry (unit R1.13) sorts them by ID.
#[linkme::distributed_slice]
pub static MIGRATIONS: [Migration];
