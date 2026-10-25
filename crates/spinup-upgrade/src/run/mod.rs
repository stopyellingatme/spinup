//! Upgrade planning, isolated application, and recovery.

use anyhow::{Result, anyhow, bail};
use spinup_exec::runner::{Cancellation, Runner as ExecRunner};
use spinup_manifest::lock::Lock;
use spinup_merge::contract::Merger;
use spinup_plugin_api::{Declared, FetchRequest, Loaded, PluginSource, null_source::NullSource};
use spinup_tplstore::source::{Source, Version};
use spinup_ui::prompt::Prompter;
use std::{collections::BTreeMap, io::Write, path::PathBuf, sync::Arc};

use crate::{
    contract::{EXTENSIONS, Options, Phases, Step},
    lock::{PlanReport, answers, next_lock},
    module::ModuleChange,
    plan::{PlanInput, build_plan, require_base},
    source::{self, PackRenderer, PackSet, Packs, Templates},
    state::{self, State, Status},
    worktree::Git,
};

mod files;
mod preview;
mod recovery;
pub use files::{copy_plan_tree, write_plan};
#[cfg(test)]
mod tests;

/// Coordinates one project upgrade. Its seams permit offline testing.
pub struct Runner<'a> {
    /// Main checkout of the application.
    pub root: PathBuf,
    /// Loads template versions.
    pub source: &'a dyn Source,
    /// Runs every subprocess.
    pub exec: &'a dyn ExecRunner,
    /// Merges changed text.
    pub merger: &'a dyn Merger,
    /// Shared cancellation signal.
    pub cancel: &'a Cancellation,
    /// Progress output.
    pub out: &'a mut (dyn Write + Send),
    /// Renders core packs. Defaults to the real renderer.
    pub renderer: &'a dyn PackRenderer,
    /// Fetches and renders plugins. R1 defaults to the null source.
    pub plugins: &'a dyn PluginSource,
    /// Permits confirmation only when the caller has a terminal.
    pub interactive: bool,
    /// Supplies interactive confirmation.
    pub prompter: Option<&'a mut dyn Prompter>,
    /// Running CLI version, checked against the lock.
    pub cli_version: String,
    /// Parsed extension flags. The `ui` choice persists in recovery state.
    pub matches: Option<&'a clap::ArgMatches>,
    #[cfg(test)]
    phases: Option<Phases>,
}

impl<'a> Runner<'a> {
    /// Creates a runner with real packs, no plugins, and no terminal prompts.
    pub fn new(
        root: impl Into<PathBuf>,
        source: &'a dyn Source,
        exec: &'a dyn ExecRunner,
        merger: &'a dyn Merger,
        cancel: &'a Cancellation,
        out: &'a mut (dyn Write + Send),
    ) -> Self {
        Self {
            root: root.into(),
            source,
            exec,
            merger,
            cancel,
            out,
            renderer: &Packs,
            plugins: &NullSource,
            interactive: false,
            prompter: None,
            cli_version: "dev".into(),
            matches: None,
            #[cfg(test)]
            phases: None,
        }
    }

    pub(crate) fn normalize_root(&mut self) -> Result<()> {
        if self.root.as_os_str().is_empty() {
            self.root = PathBuf::from(".");
        }
        self.root = spinup_vfs::path::canonical(&self.root).map_err(|e| {
            anyhow!("resolve upgrade root: {e}\nTry: select an existing project with --dir")
        })?;
        Ok(())
    }
    pub(crate) fn git(&self) -> Git<'_> {
        Git {
            runner: self.exec,
            cancel: self.cancel,
        }
    }
    fn templates(&self) -> Templates<'_> {
        Templates {
            packs: self.renderer,
            plugins: self.plugins,
        }
    }
    pub(crate) fn print(&mut self, text: &str) -> Result<()> {
        self.out
            .write_all(text.as_bytes())
            .map_err(|e| anyhow!("print upgrade progress: {e}\nTry: use a writable output"))
    }
    pub(crate) fn load_lock(&self) -> Result<Lock> {
        let lock = Lock::load(self.root.join("spinup.lock"))?;
        spinup_compat::check::check_min_cli(&self.cli_version, &lock.min_cli)?;
        Ok(lock)
    }
    pub(crate) fn require_no_state(&self, action: &str) -> Result<()> {
        match std::fs::symlink_metadata(self.root.join(".spinup/upgrade.json")) {
            Ok(_) => bail!(
                "{action}: an upgrade is in progress, use --continue or --abort\nTry: spinup upgrade --continue or spinup upgrade --abort"
            ),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => bail!("inspect upgrade state: {e}\nTry: check .spinup/ permissions"),
        }
    }
    pub(crate) fn confirm(&mut self, options: &Options, action: &str) -> Result<()> {
        if options.yes {
            return Ok(());
        }
        if !self.interactive {
            bail!("cannot {action}: confirmation requires a terminal\nTry: pass --yes");
        }
        let prompt = self
            .prompter
            .as_deref_mut()
            .ok_or_else(|| anyhow!("cannot {action}: no terminal prompter\nTry: pass --yes"))?;
        if !prompt.confirm("yes", &format!("{action}?"), false)? {
            bail!("cannot {action}: confirmation declined\nTry: pass --yes when ready");
        }
        Ok(())
    }
    fn ui_enabled(&self) -> bool {
        self.matches.is_some_and(|m| {
            m.try_get_one::<bool>("ui")
                .ok()
                .flatten()
                .copied()
                .unwrap_or(false)
        })
    }
    fn phases(&self) -> Phases {
        #[cfg(test)]
        if let Some(phases) = self.phases {
            return phases;
        }
        crate::cargo::phases()
    }
    fn load_old(&self, version: &str) -> Result<Option<Arc<Version>>> {
        match self.source.load(version) {
            Ok(v) => Ok(Some(v)),
            Err(e) if e.is_unavailable() => Ok(None),
            Err(e) => Err(e.into()),
        }
    }

    /// Previews the complete chain and writes only `.spinup/plan.json` in the project.
    pub fn plan(&mut self, options: Options) -> Result<PlanReport> {
        self.normalize_root()?;
        let lock = self.load_lock()?;
        let plugins = self.current_plugins(&lock, options.offline)?;
        let versions = source::plugin_chain(
            &lock,
            &plugins,
            crate::chain::chain(
                Some(self.source),
                &lock.template_version,
                options.to.as_deref(),
                None,
            )?,
        );
        let report = self.preview(&lock, &versions, &options, None)?;
        state::write_json(&self.root, ".spinup/plan.json", report.to_json()?)?;
        Ok(report)
    }

    /// Applies the upgrade in a branch and worktree, leaving the main checkout intact.
    pub fn run(&mut self, options: Options) -> Result<()> {
        if options.dry_run {
            self.plan(options)?;
            return Ok(());
        }
        self.normalize_root()?;
        self.require_no_state("cannot start upgrade")?;
        self.git().check_repository(&self.root)?;
        if !options.allow_dirty
            && !self
                .git()
                .run(&self.root, &["status", "--porcelain"])?
                .is_empty()
        {
            bail!(
                "cannot upgrade: the Git tree has uncommitted changes\nTry: commit the changes or pass --allow-dirty"
            );
        }
        let lock = self.load_lock()?;
        spinup_merge::git::check_git_version(self.exec, self.cancel)?;
        let plugins = self.current_plugins(&lock, options.offline)?;
        let versions = source::plugin_chain(
            &lock,
            &plugins,
            crate::chain::chain(
                Some(self.source),
                &lock.template_version,
                options.to.as_deref(),
                None,
            )?,
        );
        if versions.is_empty() {
            return self.print("Already up to date.\n");
        }
        self.print(&format!(
            "Upgrade {} -> {} ({} steps)\n",
            lock.template_version,
            versions.last().expect("nonempty chain"),
            versions.len()
        ))?;
        self.preview(&lock, &versions, &options, None)?;
        self.confirm(&options, "Apply upgrade")?;
        self.start(lock, versions, options, None)
    }

    pub(crate) fn start(
        &mut self,
        lock: Lock,
        chain: Vec<String>,
        options: Options,
        module: Option<ModuleChange>,
    ) -> Result<()> {
        let to = chain
            .last()
            .ok_or_else(|| anyhow!("start upgrade: empty chain\nTry: select a target version"))?;
        let branch = module
            .as_ref()
            .map_or_else(|| format!("spinup/upgrade-{to}"), ModuleChange::branch);
        let worktree = self.git().create(&self.root, to, &branch)?;
        let mut state = State {
            schema: 1,
            chain,
            index: 0,
            worktree,
            branch,
            status: Status::Step,
            conflicts: Vec::new(),
            committed_conflicts: Vec::new(),
            lock,
            plan: None,
            options,
            module,
            ui: self.ui_enabled(),
        };
        state.save(&self.root)?;
        self.resume(&mut state)
    }

    /// Resumes the saved operation after checking conflict resolutions.
    pub fn continue_upgrade(&mut self, options: Options) -> Result<()> {
        self.normalize_root()?;
        let mut state = State::load(&self.root)?;
        self.git().check_repository(&self.root)?;
        self.git().check(&state.worktree, &state.branch)?;
        if matches!(state.status, Status::Conflicts | Status::AfterConflicts) {
            let unresolved = state::unresolved(&state.worktree, &state.conflicts)?;
            if !unresolved.is_empty() {
                return Err(state::conflict_error(&state.worktree, &unresolved));
            }
        }
        self.confirm(&options, "Continue upgrade")?;
        state.options.no_verify |= options.no_verify;
        state.options.commit_conflicts |= options.commit_conflicts;
        state.ui |= self.ui_enabled();
        if matches!(state.status, Status::Conflicts | Status::AfterConflicts) {
            state.resolve_plan_conflicts()?;
        }
        if state.status == Status::Conflicts {
            let plan = state.plan.as_ref().expect("validated plan");
            self.git().commit(
                &state.worktree,
                &format!("spinup upgrade {} -> {}", plan.from, plan.to),
                true,
            )?;
            state.conflicts.clear();
            state.status = Status::Fix;
        }
        if state.status == Status::AfterConflicts {
            state.conflicts.clear();
            state.status = Status::Lock;
        }
        state.save(&self.root)?;
        if state.status == Status::Step {
            self.git()
                .run(&state.worktree, &["reset", "--hard", "HEAD"])?;
            self.git()
                .run(&state.worktree, &["clean", "-fd", "-e", ".spinup"])?;
        }
        self.resume(&mut state)
    }

    /// Removes the recorded worktree and branch after confirmation.
    pub fn abort(&mut self, options: Options) -> Result<()> {
        self.normalize_root()?;
        let state = State::load(&self.root)?;
        self.git().check_repository(&self.root)?;
        self.confirm(&options, "Abort upgrade and remove its worktree")?;
        self.git()
            .remove(&self.root, &state.worktree, &state.branch)?;
        state::clear(&self.root)
    }

    pub(crate) fn current_plugins(&mut self, lock: &Lock, offline: bool) -> Result<Vec<Loaded>> {
        let declared = self.declared_plugins()?;
        let plugins = self.plugins.fetch(&mut FetchRequest {
            root: &self.root,
            declared: &declared,
            offline,
            versions: &BTreeMap::new(),
            out: self.out,
        })?;
        self.plugins.trust(&self.root, &plugins, lock)?;
        Ok(plugins)
    }
    fn declared_plugins(&self) -> Result<Vec<Declared>> {
        if state::read_file(&self.root, "spinup.toml")?.is_none() {
            return Ok(Vec::new());
        }
        Ok(spinup_project::config::load(&self.root)?
            .plugins
            .into_iter()
            .map(|p| Declared {
                name: p.name,
                source: p.source,
                version: p.version,
            })
            .collect())
    }
    fn pack_sets(
        &mut self,
        lock: &Lock,
        old: Option<&Version>,
        new: &Version,
        target: Option<&[String]>,
        offline: bool,
    ) -> Result<(PackSet, PackSet)> {
        let current = self.current_plugins(lock, offline)?;
        let mut declared = self.declared_plugins()?;
        let overrides = lock
            .plugins
            .iter()
            .map(|p| (p.name.clone(), p.version.clone()))
            .collect();
        declared.retain(|p| lock.plugins.iter().any(|l| l.name == p.name));
        for entry in &lock.plugins {
            if !declared.iter().any(|p| p.name == entry.name) {
                bail!(
                    "load locked plugin {:?}: plugin source is unavailable\nTry: restore the plugin declaration before upgrading",
                    entry.name
                );
            }
        }
        let historical = self.plugins.fetch(&mut FetchRequest {
            root: &self.root,
            declared: &declared,
            offline,
            versions: &overrides,
            out: self.out,
        })?;
        self.plugins.trust(&self.root, &historical, lock)?;
        Ok((
            source::locked_packs(lock, old, &historical)?,
            source::target_packs(lock, new, target, &current)?,
        ))
    }
}
