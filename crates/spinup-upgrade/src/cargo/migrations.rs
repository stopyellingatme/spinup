//! Codemod execution and attribution for verification failures.

use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::{Mutex, OnceLock};

use anyhow::{Result, bail};
use spinup_migrate::contract::{Ctx, Migration, Stage};
use spinup_migrate::registry::{pending, plan_all, run};

use super::print;
use crate::contract::Step;

type Owners = BTreeMap<String, String>;
type History = BTreeMap<PathBuf, Owners>;

fn history() -> &'static Mutex<History> {
    static HISTORY: OnceLock<Mutex<History>> = OnceLock::new();
    HISTORY.get_or_init(Mutex::default)
}

fn context<'a>(s: &'a mut Step<'_>) -> Ctx<'a> {
    Ctx {
        root: &s.worktree,
        from: &s.from.version,
        to: &s.to.version,
        answers: &mut s.answers,
        lock: &s.lock,
        runner: s.runner,
        cancel: s.cancel,
        old_stack: Some(&s.from.stack),
        new_stack: &s.to.stack,
    }
}

pub(super) fn before(s: &mut Step<'_>) -> Result<()> {
    stage(s, Stage::Before)
}

pub(super) fn after(s: &mut Step<'_>) -> Result<()> {
    stage(s, Stage::After)
}

fn stage(s: &mut Step<'_>, stage: Stage) -> Result<()> {
    let ms = pending(
        &s.from.version,
        &s.to.version,
        &s.lock.applied_migrations,
        stage,
    )?;
    apply(s, &ms)
}

pub(super) fn apply(s: &mut Step<'_>, ms: &[&Migration]) -> Result<()> {
    for m in ms {
        if s.cancel.is_cancelled() {
            bail!(
                "run migration {}: context canceled\nTry: run spinup upgrade --continue",
                m.id
            );
        }
        let mut ctx = context(s);
        let (changes, planned) = plan_all(&mut ctx, &[m]);
        planned?;
        let (ids, result) = run(&mut ctx, &[m]);
        s.lock.applied_migrations.extend(ids);
        result?;
        let mut history = history().lock().unwrap();
        let owners = history.entry(s.worktree.clone()).or_default();
        for change in changes {
            owners.insert(change.path, m.id.into());
        }
        drop(history);
        print(s, &format!("Migration {}: {}\n", m.id, m.desc))?;
    }
    Ok(())
}

pub(super) fn provenance(s: &mut Step<'_>) -> Owners {
    let mut owners = Owners::new();
    // Rebuild attribution after --continue. A codemod may report no changes after apply.
    for stage in [Stage::Before, Stage::After] {
        let Ok(ms) = pending(&s.from.version, &s.to.version, &[], stage) else {
            continue;
        };
        for m in ms {
            if !s.lock.applied_migrations.iter().any(|id| id == m.id) {
                continue;
            }
            let (changes, result) = plan_all(&mut context(s), &[m]);
            if result.is_ok() {
                for change in changes {
                    owners.insert(change.path, m.id.into());
                }
            }
        }
    }
    if let Some(remembered) = history().lock().unwrap().get(&s.worktree) {
        owners.extend(remembered.clone());
    }
    owners
}

pub(super) fn attribute(mut message: String, owners: &Owners) -> String {
    let normalized = message.replace('\\', "/");
    for (path, id) in owners {
        let relative = path.strip_prefix("api/").unwrap_or(path);
        if normalized.contains(path) || normalized.contains(relative) {
            message.push_str(&format!("\n  {path}: last changed by migration {id}"));
        }
    }
    message
}
