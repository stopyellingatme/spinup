//! Complete-chain previews and kit notes.

use super::*;
use std::collections::BTreeSet;

impl Runner<'_> {
    pub(crate) fn preview(
        &mut self,
        initial: &Lock,
        versions: &[String],
        options: &Options,
        module: Option<&ModuleChange>,
    ) -> Result<PlanReport> {
        let scratch = files::Scratch::new()?;
        let mut paths: BTreeSet<_> = initial.files.keys().cloned().collect();
        let target = module.map(|m| m.packs.as_slice());
        let new_answers = module.map_or_else(|| answers(initial), |m| Ok(m.answers.clone()))?;
        for name in versions {
            let to = self.source.load(name)?;
            let from = self.load_old(&initial.template_version)?;
            let (_, selected) =
                self.pack_sets(initial, from.as_deref(), &to, target, options.offline)?;
            let rendered = source::render(
                &self.templates(),
                &to,
                &selected,
                &initial.packs,
                &new_answers,
            )?;
            paths.extend(rendered.plan.files.into_iter().map(|f| f.path));
        }
        copy_plan_tree(&self.root, &scratch.0, &paths, self.cancel)?;
        let mut lock = initial.clone();
        let mut report = PlanReport::default();
        if versions.is_empty() {
            self.print("Already up to date.\n")?;
        }
        for name in versions {
            let to = self.source.load(name)?;
            let from = self.load_old(&lock.template_version)?;
            let (old_packs, new_packs) =
                self.pack_sets(&lock, from.as_deref(), &to, target, options.offline)?;
            let old_answers = answers(&lock)?;
            let mut plan = build_plan(&PlanInput {
                root: &scratch.0,
                lock: &lock,
                old: from.as_deref(),
                new: &to,
                old_answers: &old_answers,
                new_answers: &new_answers,
                old_packs: &old_packs,
                new_packs: &new_packs,
                templates: self.templates(),
                merger: Some(self.merger),
                force_managed: options.force_managed,
                cancel: self.cancel,
            })?;
            require_base(&plan, from.is_some(), &lock.template_version)?;
            if let Some(change) = module {
                change.edit_plan(&scratch.0, &mut plan)?;
            }
            plan.format(self.out)?;
            self.print_kit_notes(&to, &lock)?;
            write_plan(&scratch.0, &plan)?;
            lock = next_lock(
                &self.templates(),
                &lock,
                &plan,
                &to,
                &new_answers,
                &new_packs,
            )?;
            report.steps.push(plan);
        }
        Ok(report)
    }

    fn print_kit_notes(&mut self, to: &Version, lock: &Lock) -> Result<()> {
        let Some(data) = &to.kit_migration else {
            return Ok(());
        };
        if data.is_empty() {
            return Ok(());
        }
        let notes = spinup_migrate::notes::parse_kit_notes(data)?;
        let behavior = spinup_project::config::load(&self.root)
            .map_or(lock.behavior_version.max(1), |c| {
                c.effective_behavior_version()
            });
        for note in notes.steps {
            self.print(&format!("Kit {} [{}]: {}\n", note.id, note.kind, note.desc))?;
        }
        for note in notes.behaviors {
            if note.version > behavior {
                self.print(&format!(
                    "Kit behavior {} (opt in with behavior_version = {} in spinup.toml): {}\n",
                    note.version, note.version, note.desc
                ))?;
            }
        }
        Ok(())
    }
}
