//! The registered `--ui` extension for vendored shadcn-svelte seed files.

use crate::{
    contract::{Action, EXTENSIONS, Extension, FileDecision, Plan, Step},
    source::{PackRenderer, Packs},
    state::read_file,
};
use anyhow::{Result, anyhow, bail};
use clap::{Arg, ArgAction, ArgMatches, Command};
use serde_json::value::to_raw_value;
use spinup_manifest::{
    hash::{hash, normalize},
    lock::{Class, FileEntry},
};
use spinup_merge::contract::{Input, MAX_SIZE};

const PREFIX: &str = "web/src/lib/components/ui/";

// SAFETY: linkme collects immutable extension descriptors in its linker section.
#[allow(unsafe_code)]
#[linkme::distributed_slice(EXTENSIONS)]
static UI: Extension = Extension {
    name: "ui",
    args,
    run,
};

/// Adds the explicit opt-in flag for component merges.
pub fn args(command: Command) -> Command {
    command.arg(
        Arg::new("ui")
            .long("ui")
            .action(ArgAction::SetTrue)
            .help("merge shadcn-svelte component updates"),
    )
}

/// Restores the UI policy while retaining other parsed extension arguments.
pub(crate) fn saved_matches(enabled: bool) -> Result<ArgMatches> {
    let arguments = if enabled {
        vec!["upgrade", "--ui"]
    } else {
        vec!["upgrade"]
    };
    args(Command::new("upgrade"))
        .try_get_matches_from(arguments)
        .map_err(|e| anyhow!("restore UI policy: {e}\nTry: restart the upgrade"))
}

/// Updates only seed components. Conflicts stop the state machine with exit code 3.
pub fn run(step: &mut Step<'_>, matches: &ArgMatches) -> Result<()> {
    let enabled = matches
        .try_get_one::<bool>("ui")
        .ok()
        .flatten()
        .copied()
        .unwrap_or(false);
    run_ui(step, enabled)
}

fn run_ui(step: &mut Step<'_>, enabled: bool) -> Result<()> {
    if !step.lock.packs.iter().any(|p| p == "web") {
        return Ok(());
    }
    let pack = spinup_packs::loader::load(&step.to.packs, "web")?;
    let registry = pack
        .questions
        .iter()
        .find(|q| q.key == "shadcn_registry")
        .map(|q| q.default.as_str())
        .filter(|s| !s.is_empty())
        .ok_or_else(|| {
            anyhow!(
                "load UI registry: shadcn_registry has no default\nTry: restore the target web pack"
            )
        })?;
    let locked_answers = crate::lock::answers(&step.lock)?;
    if !enabled {
        let current = locked_answers
            .get("shadcn_registry")
            .and_then(serde_json::Value::as_str)
            .unwrap_or("");
        if current != registry {
            writeln!(step.out, "UI components use registry {current}. Registry {registry} is available. Try: spinup upgrade --ui")
                .map_err(|e| anyhow!("print UI registry: {e}\nTry: use a writable output"))?;
        }
        return Ok(());
    }
    if step.from.packs.is_empty() {
        bail!(
            "update UI components: the old template version is unavailable\nTry: restore the old templates with spinup cache warm"
        );
    }
    let old = Packs.render(&step.from, &["web".into()], &locked_answers)?;
    let mut answers = step.answers.clone();
    answers.insert("shadcn_registry".into(), registry.into());
    let newer = Packs.render(&step.to, &["web".into()], &answers)?;
    let mut updates = Plan::default();
    for file in newer.plan.sorted().files {
        if !file.path.starts_with(PREFIX)
            || file.class != "seed"
            || step.lock.tombstones.contains(&file.path)
        {
            continue;
        }
        let base = old.plan.get(&file.path);
        if base.as_ref().is_some_and(|f| f.class != "seed") {
            continue;
        }
        if step.cancel.is_cancelled() {
            bail!("update UI components: canceled\nTry: run spinup upgrade --continue");
        }
        let disk = read_file(&step.worktree, &file.path)?;
        if base.is_some() && disk.is_none() {
            continue;
        }
        let mut d = FileDecision {
            path: file.path.clone(),
            from: None,
            action: Action::Overwrite,
            class: Class::Seed,
            template: file.template.clone(),
            data: file.data.clone(),
            mode: file.mode,
            reason: String::new(),
        };
        match disk {
            None => d.action = Action::Create,
            Some(ours) if normalize(&ours) == normalize(&file.data) => d.action = Action::Unchanged,
            Some(ours)
                if base
                    .as_ref()
                    .is_some_and(|b| normalize(&b.data) == normalize(&ours)) => {}
            Some(ours) => {
                let base = base.as_ref().map_or(&[][..], |b| b.data.as_slice());
                if [&ours[..], base, &file.data[..]]
                    .iter()
                    .any(|bytes| bytes.len() > MAX_SIZE || bytes.contains(&0))
                {
                    d.action = Action::Keep;
                    d.reason = "binary or large file changed on disk".into();
                } else {
                    let merged = step.merger.merge(
                        &Input {
                            path: &file.path,
                            base: &normalize(base),
                            ours: &normalize(&ours),
                            theirs: &normalize(&file.data),
                        },
                        step.cancel,
                    )?;
                    d.action = if merged.conflicted {
                        Action::Conflict
                    } else {
                        Action::Merge
                    };
                    d.data = merged.data;
                    d.reason = "UI component update".into();
                }
            }
        }
        updates.files.push(d);
    }
    crate::run::write_plan(&step.worktree, &updates)?;
    for decision in &updates.files {
        let file = newer.plan.get(&decision.path).expect("rendered UI file");
        step.lock.files.insert(
            decision.path.clone(),
            FileEntry {
                class: Class::Seed,
                template: file.template,
                hash: hash(&file.data),
                mode: format!("{:04o}", file.mode & 0o777),
            },
        );
        if let Some(plan) = &mut step.plan {
            if let Some(previous) = plan.files.iter_mut().find(|d| d.path == decision.path) {
                *previous = decision.clone();
            } else {
                plan.files.push(decision.clone());
            }
        }
    }
    if let Some(plan) = &mut step.plan {
        plan.files.sort_by(|a, b| a.path.cmp(&b.path));
    }
    step.lock
        .answers
        .insert("shadcn_registry".into(), to_raw_value(registry)?);
    step.answers
        .insert("shadcn_registry".into(), registry.into());
    step.conflicts.extend(updates.conflicts());
    Ok(())
}

#[cfg(test)]
mod tests;
