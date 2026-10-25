//! Persistent transitions after worktree creation.

use super::*;

impl Runner<'_> {
    pub(super) fn resume(&mut self, state: &mut State) -> Result<()> {
        let phases = self.phases();
        while state.index < state.chain.len() {
            if self.cancel.is_cancelled() {
                bail!("resume upgrade: canceled\nTry: run spinup upgrade --continue or --abort");
            }
            let to = self.source.load(&state.chain[state.index])?;
            let old_name = state
                .plan
                .as_ref()
                .map_or(state.lock.template_version.as_str(), |p| &p.from);
            let old = self.load_old(old_name)?;
            // The contract requires an Arc even when the historical render is unavailable.
            // Only the planner receives None. Extensions detect the empty pack tree.
            let from = old.clone().unwrap_or_else(|| {
                Arc::new(Version {
                    version: old_name.into(),
                    packs: Arc::default(),
                    stack: Arc::default(),
                    kit_migration: None,
                })
            });
            let initial_answers = if state.status == Status::Step {
                state
                    .module
                    .as_ref()
                    .map_or_else(|| answers(&state.lock), |m| Ok(m.answers.clone()))?
            } else {
                answers(&state.lock)?
            };
            let mut step = Step {
                from,
                to,
                worktree: state.worktree.clone(),
                lock: state.lock.clone(),
                answers: initial_answers,
                plan: state.plan.clone(),
                options: state.options.clone(),
                runner: self.exec,
                cancel: self.cancel,
                out: self.out,
                merger: self.merger,
                conflicts: Vec::new(),
            };
            if state.status == Status::Step {
                let old_answers = answers(&state.lock)?;
                call_phase(phases.before, &mut step)?;
                // Release the output borrow while resolving plugin render seams.
                let step_answers = step.answers.clone();
                let prepared_lock = step.lock.clone();
                let prepared_conflicts = step.conflicts.clone();
                drop(step);
                let to = self.source.load(&state.chain[state.index])?;
                let (old_packs, new_packs) = self.pack_sets(
                    &prepared_lock,
                    old.as_deref(),
                    &to,
                    state.module.as_ref().map(|m| m.packs.as_slice()),
                    state.options.offline,
                )?;
                let mut plan = build_plan(&PlanInput {
                    root: &state.worktree,
                    lock: &prepared_lock,
                    old: old.as_deref(),
                    new: &to,
                    old_answers: &old_answers,
                    new_answers: &step_answers,
                    old_packs: &old_packs,
                    new_packs: &new_packs,
                    templates: self.templates(),
                    merger: Some(self.merger),
                    force_managed: state.options.force_managed,
                    cancel: self.cancel,
                })?;
                require_base(&plan, old.is_some(), &state.lock.template_version)?;
                if let Some(module) = &state.module {
                    module.edit_plan(&state.worktree, &mut plan)?;
                }
                plan.format(self.out)?;
                let mut lock = next_lock(
                    &self.templates(),
                    &prepared_lock,
                    &plan,
                    &to,
                    &step_answers,
                    &new_packs,
                )?;
                lock.cli_version.clone_from(&self.cli_version);
                write_plan(&state.worktree, &plan)?;
                step = Step {
                    from: old.clone().unwrap_or_else(|| {
                        Arc::new(Version {
                            version: plan.from.clone(),
                            packs: Arc::default(),
                            stack: Arc::default(),
                            kit_migration: None,
                        })
                    }),
                    to,
                    worktree: state.worktree.clone(),
                    lock,
                    answers: step_answers,
                    plan: Some(plan),
                    options: state.options.clone(),
                    runner: self.exec,
                    cancel: self.cancel,
                    out: self.out,
                    merger: self.merger,
                    conflicts: prepared_conflicts,
                };
                call_phase(phases.apply, &mut step)?;
                call_phase(phases.deps, &mut step)?;
                state.plan.clone_from(&step.plan);
                state.lock = step.lock.clone();
                state.conflicts = step.plan.as_ref().expect("planned step").conflicts();
                state.conflicts.extend_from_slice(&step.conflicts);
                state.conflicts.sort();
                state.conflicts.dedup();
                if !state.conflicts.is_empty() && !step.options.commit_conflicts {
                    state.status = Status::Conflicts;
                    state.save(&self.root)?;
                    return Err(state::conflict_error(&state.worktree, &state.conflicts));
                }
                let plan = step.plan.as_ref().expect("planned step");
                Git {
                    runner: self.exec,
                    cancel: self.cancel,
                }
                .commit(
                    &step.worktree,
                    &format!("spinup upgrade {} -> {}", plan.from, plan.to),
                    true,
                )?;
                state
                    .committed_conflicts
                    .extend_from_slice(&state.conflicts);
                state.committed_conflicts.sort();
                state.committed_conflicts.dedup();
                state.status = Status::Fix;
                state.save(&self.root)?;
            }
            if state.status == Status::Fix {
                if let Some(fix) = phases.fix {
                    let changed = fix(&mut step).map_err(|e| phase_error("fix", e))?;
                    if changed {
                        Git {
                            runner: self.exec,
                            cancel: self.cancel,
                        }
                        .commit(
                            &step.worktree,
                            "spinup upgrade: cargo fix",
                            false,
                        )?;
                    }
                }
                state.lock = step.lock.clone();
                state.status = Status::After;
                state.save(&self.root)?;
            }
            if state.status == Status::After {
                call_phase(phases.after, &mut step)?;
                let mut extensions: Vec<_> = EXTENSIONS.iter().collect();
                extensions.sort_by_key(|e| e.name);
                let matches = crate::ui::saved_matches(state.ui)?;
                for extension in extensions {
                    let flags = if extension.name == "ui" {
                        &matches
                    } else {
                        self.matches.unwrap_or(&matches)
                    };
                    (extension.run)(&mut step, flags)
                        .map_err(|e| phase_error(extension.name, e))?;
                }
                state.lock = step.lock.clone();
                state.plan.clone_from(&step.plan);
                if !step.conflicts.is_empty() && !step.options.commit_conflicts {
                    state.conflicts = step.conflicts.clone();
                    state.conflicts.sort();
                    state.conflicts.dedup();
                    state.status = Status::AfterConflicts;
                    state.save(&self.root)?;
                    return Err(state::conflict_error(&state.worktree, &state.conflicts));
                }
                state.committed_conflicts.extend_from_slice(&step.conflicts);
                state.committed_conflicts.sort();
                state.committed_conflicts.dedup();
                state.status = Status::Lock;
                state.save(&self.root)?;
            }
            if state.status == Status::Lock {
                step.lock.save(step.worktree.join("spinup.lock"))?;
                Git {
                    runner: self.exec,
                    cancel: self.cancel,
                }
                .commit(
                    &step.worktree,
                    &format!("spinup upgrade: lock {}", step.to.version),
                    true,
                )?;
                state.status = Status::Verify;
                state.save(&self.root)?;
            }
            if state.status == Status::Verify && !step.options.no_verify {
                call_phase(phases.verify, &mut step)?;
            }
            state.index += 1;
            if state.index < state.chain.len() {
                state.status = Status::Step;
                state.plan = None;
                state.conflicts.clear();
                state.save(&self.root)?;
            }
        }
        state::clear(&self.root)?;
        if !state.committed_conflicts.is_empty() {
            self.print(&format!("Warning: committed conflict markers in: {}\nResolve these files before running git merge {}.\nWorktree: {}\n", state.committed_conflicts.join(", "), state.branch, state.worktree.display()))
        } else {
            self.print(&format!(
                "Next: git merge {}\nWorktree: {}\nRemove it with: git worktree remove {}\n",
                state.branch,
                state.worktree.display(),
                state.worktree.display()
            ))
        }
    }
}

fn call_phase(phase: Option<crate::contract::PhaseFn>, step: &mut Step<'_>) -> Result<()> {
    if let Some(phase) = phase {
        phase(step).map_err(|e| phase_error("phase", e))?;
    }
    Ok(())
}
fn phase_error(name: &str, error: anyhow::Error) -> anyhow::Error {
    if format!("{error:#}").contains("\nTry: ") {
        error.context(format!("upgrade {name} failed"))
    } else {
        anyhow!(
            "upgrade {name} failed: {error:#}\nTry: correct the failure, then run spinup upgrade --continue or --abort"
        )
    }
}
