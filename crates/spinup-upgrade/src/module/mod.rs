//! Module additions use a same-version upgrade with saved render answers.

use crate::{
    contract::{Action, Options, Plan},
    run::Runner,
    state::read_file,
};
use anyhow::{Result, anyhow, bail};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use spinup_render::modules;
use spinup_ui::prompt::Prompter;
use std::path::Path;

/// Requested modules, answers, and upgrade policy.
#[derive(Default)]
pub struct ModuleOptions<'a> {
    /// Module names, with duplicates permitted.
    pub names: Vec<String>,
    /// Explicit answers for new packs.
    pub answers: Map<String, Value>,
    /// Questions for an interactive session.
    pub prompter: Option<&'a mut dyn Prompter>,
    /// Confirmation and upgrade choices.
    pub options: Options,
}

/// Saved module inputs. Recovery never asks the questions again.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModuleChange {
    /// Requested module names in sorted order.
    pub names: Vec<String>,
    /// Target packs in dependency order.
    pub packs: Vec<String>,
    /// Newly enabled modules.
    pub added: Vec<String>,
    /// Completed render answers.
    pub answers: Map<String, Value>,
}
impl ModuleChange {
    /// Returns the branch for this selection.
    pub fn branch(&self) -> String {
        format!("spinup/module-{}", self.names.join("-"))
    }
    /// Checks the saved selection before recovery.
    pub fn valid(&self) -> bool {
        !self.names.is_empty()
            && !self.packs.is_empty()
            && self.names.windows(2).all(|p| p[0] < p[1])
            && self.names.iter().all(|n| modules::get(n).is_some())
    }
    /// Edits only the module configuration and preserves comments.
    pub fn edit_plan(&self, root: &Path, plan: &mut Plan) -> Result<()> {
        let bytes = read_file(root, "spinup.toml")?.ok_or_else(|| {
            anyhow!("read module configuration: spinup.toml is missing\nTry: restore spinup.toml")
        })?;
        let original = std::str::from_utf8(&bytes).map_err(|e| {
            anyhow!("read module configuration: {e}\nTry: use UTF-8 in spinup.toml")
        })?;
        let mut enabled: Vec<_> = self
            .packs
            .iter()
            .filter(|n| modules::get(n).is_some())
            .cloned()
            .collect();
        enabled.sort();
        let mut data = spinup_editors::toml::set(original, &[], "modules", enabled)?;
        if self.added.iter().any(|n| n == "monitoring")
            && let Some(profile) = self
                .answers
                .get("monitoring_profile")
                .and_then(Value::as_str)
        {
            data = spinup_editors::toml::set(&data, &["monitoring"], "profile", profile)?;
        }
        let Some(file) = plan.files.iter_mut().find(|f| f.path == "spinup.toml") else {
            bail!(
                "plan module configuration: spinup.toml is not tracked by the base pack\nTry: restore the base pack entry in spinup.lock"
            );
        };
        file.action = if original == data {
            Action::Unchanged
        } else {
            Action::Merge
        };
        file.data = data.into_bytes();
        file.reason = "module configuration".into();
        Ok(())
    }
}

impl Runner<'_> {
    /// Selects modules, then applies them in an isolated branch at the locked version.
    pub fn add_modules(&mut self, mut options: ModuleOptions<'_>) -> Result<()> {
        self.normalize_root()?;
        self.require_no_state("cannot add modules")?;
        let lock = self.load_lock()?;
        let config = spinup_project::config::load(&self.root)?;
        let version = self.source.load(&lock.template_version)?;
        self.current_plugins(&lock, options.options.offline)?;
        let selection = modules::select(modules::SelectOptions {
            config: &config,
            lock: &lock,
            fs: &version.packs,
            names: &options.names,
            answers: &options.answers,
            interactive: self.interactive,
            prompter: options
                .prompter
                .as_mut()
                .map(|p| &mut **p as &mut dyn Prompter),
        })?;
        let mut configured = config.modules.clone();
        configured.sort();
        if selection.added.is_empty()
            && serde_json::to_value(configured)? == selection.answers["modules"]
        {
            return self.print("Modules are already enabled.\n");
        }
        if !selection.required.is_empty() {
            self.print(&format!(
                "Adding required modules: {}\n",
                selection.required.join(", ")
            ))?;
        }
        let mut names = options.names;
        names.sort();
        names.dedup();
        let change = ModuleChange {
            names,
            packs: selection.packs,
            added: selection.added,
            answers: selection.answers,
        };
        let mut policy = options.options;
        policy.to = Some(lock.template_version.clone());
        let versions = vec![lock.template_version.clone()];
        if !policy.dry_run {
            self.git().check_repository(&self.root)?;
            if !self
                .git()
                .run(&self.root, &["status", "--porcelain"])?
                .is_empty()
            {
                bail!(
                    "cannot add modules: the Git tree has uncommitted changes\nTry: commit the changes before adding modules"
                );
            }
            spinup_merge::git::check_git_version(self.exec, self.cancel)?;
        }
        self.preview(&lock, &versions, &policy, Some(&change))?;
        if policy.dry_run {
            return Ok(());
        }
        self.confirm(&policy, "Add modules")?;
        self.start(lock, versions, policy, Some(change))
    }
}
