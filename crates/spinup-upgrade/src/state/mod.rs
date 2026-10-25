//! Persistent upgrade recovery. File replacement goes through `spinup-vfs`.

use std::path::{Path, PathBuf};

use anyhow::{Result, anyhow, bail};
use serde::{Deserialize, Serialize};
use spinup_manifest::lock::{Class, Lock};
use spinup_vfs::{
    apply::{ApplyOptions, apply},
    path::clean_path,
    plan::{File, Plan as Writes},
};

use crate::{
    contract::{Action, FileDecision, Options, Plan},
    module::ModuleChange,
};

/// The next operation of a recoverable upgrade.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Status {
    /// Plan and apply a step.
    Step,
    /// Wait for conflicts from planning.
    Conflicts,
    /// Run the fix phase.
    Fix,
    /// Run the after phase and extensions.
    After,
    /// Wait for conflicts from an extension.
    AfterConflicts,
    /// Write and commit the lock.
    Lock,
    /// Verify the committed result.
    Verify,
}

/// Recovery metadata stored in the main checkout.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct State {
    /// State format version.
    pub schema: u32,
    /// Template versions in upgrade order.
    pub chain: Vec<String>,
    /// Current index in the chain.
    #[serde(rename = "step")]
    pub index: usize,
    /// Isolated checkout path.
    pub worktree: PathBuf,
    /// Isolated branch name.
    pub branch: String,
    /// Next operation.
    pub status: Status,
    /// Files awaiting resolution.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub conflicts: Vec<String>,
    /// Conflicts committed by explicit policy.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub committed_conflicts: Vec<String>,
    /// Lock for the current operation.
    pub lock: Lock,
    /// Current plan, including merge content.
    #[serde(default, with = "optional_plan")]
    pub plan: Option<Plan>,
    /// Original policy.
    #[serde(with = "OptionsData")]
    pub options: Options,
    /// Saved module selection and answers.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub module: Option<ModuleChange>,
    /// Saved `--ui` policy.
    #[serde(default)]
    pub ui: bool,
}

#[derive(Serialize, Deserialize)]
#[serde(remote = "Options")]
struct OptionsData {
    to: Option<String>,
    allow_dirty: bool,
    force_managed: bool,
    no_verify: bool,
    yes: bool,
    commit_conflicts: bool,
    offline: bool,
    dry_run: bool,
}

#[derive(Serialize, Deserialize)]
#[serde(remote = "FileDecision")]
struct DecisionData {
    path: String,
    from: Option<String>,
    action: Action,
    class: Class,
    template: String,
    data: Vec<u8>,
    mode: u32,
    reason: String,
}

#[derive(Serialize, Deserialize)]
struct Decision(#[serde(with = "DecisionData")] FileDecision);

#[derive(Serialize, Deserialize)]
struct PlanData {
    from: String,
    to: String,
    files: Vec<Decision>,
    tombstoned: Vec<String>,
    missing_managed: Vec<String>,
}
impl From<Plan> for PlanData {
    fn from(p: Plan) -> Self {
        Self {
            from: p.from,
            to: p.to,
            files: p.files.into_iter().map(Decision).collect(),
            tombstoned: p.tombstoned,
            missing_managed: p.missing_managed,
        }
    }
}
impl From<PlanData> for Plan {
    fn from(p: PlanData) -> Self {
        Self {
            from: p.from,
            to: p.to,
            files: p.files.into_iter().map(|d| d.0).collect(),
            tombstoned: p.tombstoned,
            missing_managed: p.missing_managed,
        }
    }
}
mod optional_plan {
    use super::*;
    pub fn serialize<S: serde::Serializer>(
        plan: &Option<Plan>,
        s: S,
    ) -> std::result::Result<S::Ok, S::Error> {
        plan.clone().map(PlanData::from).serialize(s)
    }
    pub fn deserialize<'de, D: serde::Deserializer<'de>>(
        d: D,
    ) -> std::result::Result<Option<Plan>, D::Error> {
        Ok(Option::<PlanData>::deserialize(d)?.map(Plan::from))
    }
}

impl State {
    /// Requires a safe worktree, branch, chain, lock, and plan before recovery.
    pub fn validate(&self, root: &Path) -> Result<()> {
        let invalid = || {
            anyhow!(
                "load upgrade state: invalid or unsafe state\nTry: restore .spinup/upgrade.json from a valid copy"
            )
        };
        if self.schema != 1 || self.chain.is_empty() || self.index >= self.chain.len() {
            return Err(invalid());
        }
        for v in &self.chain {
            crate::chain::parse(v).map_err(|_| invalid())?;
        }
        let to = self.chain.last().ok_or_else(invalid)?;
        let branch = match &self.module {
            Some(m) if m.valid() && self.chain.len() == 1 => m.branch(),
            Some(_) => return Err(invalid()),
            None => format!("spinup/upgrade-{to}"),
        };
        if self.worktree != root.join(".spinup/upgrade").join(to) || self.branch != branch {
            return Err(invalid());
        }
        self.lock.validate()?;
        for name in self.conflicts.iter().chain(&self.committed_conflicts) {
            safe_path(name).map_err(|_| invalid())?;
        }
        if self.status != Status::Step
            && self
                .plan
                .as_ref()
                .is_none_or(|p| p.to != self.chain[self.index])
        {
            return Err(invalid());
        }
        if let Some(p) = &self.plan {
            for file in &p.files {
                safe_path(&file.path).map_err(|_| invalid())?;
                if let Some(from) = &file.from {
                    safe_path(from).map_err(|_| invalid())?;
                }
            }
        }
        Ok(())
    }

    /// Atomically saves the state with mode 0600.
    pub fn save(&self, root: &Path) -> Result<()> {
        self.validate(root)?;
        let data = serde_json::to_vec_pretty(self)
            .map_err(|e| anyhow!("encode upgrade state: {e}\nTry: use valid upgrade metadata"))?;
        write_json(root, ".spinup/upgrade.json", data)
    }

    /// Loads and validates the saved state.
    pub fn load(root: &Path) -> Result<Self> {
        let data = read_file(root, ".spinup/upgrade.json")?.ok_or_else(|| {
            anyhow!("load upgrade state: no upgrade is in progress\nTry: start spinup upgrade")
        })?;
        let state: Self = serde_json::from_slice(&data).map_err(|e| {
            anyhow!("load upgrade state: {e}\nTry: restore .spinup/upgrade.json from a valid copy")
        })?;
        state.validate(root)?;
        Ok(state)
    }

    /// Records resolved bytes, or a deletion, for later phases.
    pub fn resolve_plan_conflicts(&mut self) -> Result<()> {
        if let Some(plan) = &mut self.plan {
            for file in &mut plan.files {
                if file.action == Action::Conflict && self.conflicts.contains(&file.path) {
                    match read_file(&self.worktree, &file.path)? {
                        Some(bytes) => {
                            file.action = Action::Merge;
                            file.data = bytes;
                        }
                        None => {
                            file.action = Action::Delete;
                            file.data.clear();
                        }
                    }
                }
            }
        }
        Ok(())
    }
}

/// Writes metadata through the atomic VFS apply operation.
pub fn write_json(root: &Path, path: &str, mut data: Vec<u8>) -> Result<()> {
    data.push(b'\n');
    apply(
        root,
        &Writes {
            files: vec![File {
                path: path.into(),
                data,
                mode: 0o600,
                ..File::default()
            }],
        },
        ApplyOptions::default(),
    )?;
    Ok(())
}

/// Removes the state through VFS. A missing state is harmless.
pub fn clear(root: &Path) -> Result<()> {
    apply(
        root,
        &Writes {
            files: vec![File {
                path: ".spinup/upgrade.json".into(),
                delete: true,
                ..File::default()
            }],
        },
        ApplyOptions::default(),
    )?;
    Ok(())
}

/// Rejects metadata paths and non-canonical paths in an upgrade plan.
pub(crate) fn safe_path(name: &str) -> Result<()> {
    if clean_path(name)? != name || matches!(name.split('/').next(), Some(".git" | ".spinup")) {
        bail!("write upgrade file {name:?}: unsafe destination\nTry: correct the template path");
    }
    Ok(())
}

/// Reads a regular file confined to the project. Missing files return `None`.
pub(crate) fn read_file(root: &Path, name: &str) -> Result<Option<Vec<u8>>> {
    if clean_path(name)? != name {
        bail!("read upgrade file {name:?}: unsafe path\nTry: use a clean relative path");
    }
    let root = spinup_vfs::path::canonical(root)
        .map_err(|e| anyhow!("open upgrade worktree: {e}\nTry: restore the upgrade worktree"))?;
    let path = match spinup_vfs::path::canonical(root.join(name)) {
        Ok(path) => path,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => bail!("read upgrade file {name:?}: {e}\nTry: check the file permissions"),
    };
    if !path.starts_with(&root) || !path.is_file() {
        bail!(
            "read upgrade file {name:?}: unsafe or non-regular file\nTry: use a regular file inside the project"
        );
    }
    std::fs::read(path)
        .map(Some)
        .map_err(|e| anyhow!("read upgrade file {name:?}: {e}\nTry: check the file permissions"))
}

/// Lists unresolved conflict blocks. Deleted files count as resolved.
pub fn unresolved(work: &Path, paths: &[String]) -> Result<Vec<String>> {
    let mut result = Vec::new();
    for path in paths {
        safe_path(path)?;
        let Some(data) = read_file(work, path)? else {
            continue;
        };
        let mut open = false;
        for line in data.split(|b| *b == b'\n') {
            if line.starts_with(b"<<<<<<<") {
                open = true;
            }
            if open && line.starts_with(b">>>>>>>") {
                result.push(path.clone());
                break;
            }
        }
    }
    result.sort();
    result.dedup();
    Ok(result)
}

/// Returns the shared conflict error with exit code 3.
pub fn conflict_error(work: &Path, paths: &[String]) -> anyhow::Error {
    spinup_ui::error::Error::new("Upgrade has conflicts")
        .why(paths.join(", "))
        .try_hint(format!(
            "resolve the markers in {}, then run spinup upgrade --continue",
            work.display()
        ))
        .code(spinup_ui::exit::CONFLICTS)
        .into()
}
