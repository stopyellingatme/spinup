//! `next_lock` and the plan JSON (Go `internal/upgrade/plan_lock.go` and `PlanReport` in
//! `run_plan.go`).

use std::collections::BTreeMap;

use anyhow::{Result, anyhow};
use serde::Serialize;
use serde_json::value::{RawValue, to_raw_value};
use serde_json::{Map, Value};
use spinup_manifest::hash::hash;
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_tplstore::source::{ENGINE, Version};

use crate::contract::{Action, Plan};
use crate::plan::{GENERATOR_OWNED, TOMBSTONE, class_of, plan_class};
use crate::source::{PackSet, Templates, render};

#[cfg(test)]
mod tests;

/// Decodes the answers of a lock.
pub fn answers(lock: &Lock) -> Result<Map<String, Value>> {
    lock.answers
        .iter()
        .map(|(key, raw)| {
            let value = serde_json::from_str(raw.get()).map_err(|e| {
                anyhow!("read locked answer {key:?}: {e}\nTry: restore spinup.lock")
            })?;
            Ok((key.clone(), value))
        })
        .collect()
}

/// Builds the lock after one step (Go `NextLock`). It returns a new lock and changes
/// neither `old` nor `plan`.
///
/// The lock records the NEW render, not the content on disk, so a later upgrade still sees
/// the user's edits. `target` selects the NEW packs and plugins (a set without names keeps
/// `old.packs`). The lock records the completed answers, the stack image references of
/// `to`, and the engine `minijinja`. R1 formats no file, so `formatter` is empty (ADR 0020
/// §2). Tombstones and generator-owned files keep their old entry.
pub fn next_lock(
    templates: &Templates<'_>,
    old: &Lock,
    plan: &Plan,
    to: &Version,
    answers: &Map<String, Value>,
    target: &PackSet,
) -> Result<Lock> {
    old.validate()?;
    let rendered = render(templates, to, target, &old.packs, answers)?;
    let files: BTreeMap<&str, &spinup_vfs::plan::File> = rendered
        .plan
        .files
        .iter()
        .map(|f| (f.path.as_str(), f))
        .collect();
    let mut next = old.clone();
    next.template_version.clone_from(&to.version);
    next.formatter = String::new();
    next.engine = ENGINE.to_string();
    next.answers = raw_answers(old, &rendered.answers)?;
    next.packs = target.names_or(&old.packs).to_vec();
    next.plugins = target.lock_plugins();
    next.stack_versions = to
        .stack
        .images
        .iter()
        .map(|(key, image)| (key.clone(), image.reference()))
        .collect();
    for name in &plan.tombstoned {
        next.tombstone(name);
    }
    for d in &plan.files {
        if let Some(from) = &d.from {
            next.files.remove(from);
        }
        match d.action {
            Action::Orphan => {
                next.files.remove(&d.path);
                next.orphans.push(d.path.clone());
            }
            Action::Delete => {
                next.files.remove(&d.path);
            }
            _ => {
                if next.tombstones.contains(&d.path)
                    || d.reason == TOMBSTONE
                    || d.reason == GENERATOR_OWNED
                {
                    continue;
                }
                let Some(file) = files.get(d.path.as_str()) else {
                    continue;
                };
                let previous = old.files.get(d.from.as_deref().unwrap_or(&d.path));
                let class = class_of(&file.class);
                let class = previous.map_or(class, |p| plan_class(p.class, class));
                let entry = if class == Class::Seed {
                    FileEntry {
                        class,
                        template: String::new(),
                        hash: String::new(),
                        mode: String::new(),
                    }
                } else {
                    FileEntry {
                        class,
                        template: file.template.clone(),
                        hash: hash(&file.data),
                        mode: format!("{:04o}", file.mode & 0o777),
                    }
                };
                next.files.insert(d.path.clone(), entry);
                next.orphans.retain(|name| name != &d.path);
            }
        }
    }
    next.tombstones.sort();
    next.tombstones.dedup();
    next.orphans.sort();
    next.orphans.dedup();
    next.validate()?;
    Ok(next)
}

/// Encodes the answers. An answer with the same value as in `old` keeps its old JSON
/// text, so number tokens keep their spelling.
fn raw_answers(
    old: &Lock,
    answers: &Map<String, Value>,
) -> Result<BTreeMap<String, Box<RawValue>>> {
    answers
        .iter()
        .map(|(key, value)| {
            if let Some(raw) = old.answers.get(key)
                && serde_json::from_str::<Value>(raw.get()).is_ok_and(|v| &v == value)
            {
                return Ok((key.clone(), raw.clone()));
            }
            let raw = to_raw_value(value)
                .map_err(|e| anyhow!("encode answer {key:?}: {e}\nTry: use JSON answer values"))?;
            Ok((key.clone(), raw))
        })
        .collect()
}

/// The plans of a complete version chain (Go `PlanReport`). The JSON form holds no file
/// content.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PlanReport {
    /// Each plan, in upgrade order.
    pub steps: Vec<Plan>,
}

#[derive(Serialize)]
struct ReportFile<'a> {
    path: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    from: &'a str,
    action: Action,
    class: Class,
    #[serde(skip_serializing_if = "str::is_empty")]
    template: &'a str,
    mode: String,
    #[serde(skip_serializing_if = "str::is_empty")]
    reason: &'a str,
}

#[derive(Serialize)]
struct ReportStep<'a> {
    from: &'a str,
    to: &'a str,
    files: Vec<ReportFile<'a>>,
    /// Go writes `null` for an empty list.
    tombstoned: Option<&'a [String]>,
    missing_managed: Option<&'a [String]>,
}

#[derive(Serialize)]
struct Report<'a> {
    steps: Vec<ReportStep<'a>>,
}

impl PlanReport {
    /// Encodes the report as `.spinup/plan.json` holds it: Go `json.MarshalIndent` with two
    /// spaces, without the final newline. Each file has its path, the rename source, the
    /// action, the class, the template, the mode (four octal digits), and the reason.
    pub fn to_json(&self) -> Result<Vec<u8>> {
        let report = Report {
            steps: self
                .steps
                .iter()
                .map(|plan| ReportStep {
                    from: &plan.from,
                    to: &plan.to,
                    files: plan
                        .files
                        .iter()
                        .map(|f| ReportFile {
                            path: &f.path,
                            from: f.from.as_deref().unwrap_or_default(),
                            action: f.action,
                            class: f.class,
                            template: &f.template,
                            mode: format!("{:04o}", f.mode & 0o777),
                            reason: &f.reason,
                        })
                        .collect(),
                    tombstoned: non_empty(&plan.tombstoned),
                    missing_managed: non_empty(&plan.missing_managed),
                })
                .collect(),
        };
        let data = serde_json::to_vec_pretty(&report)
            .map_err(|e| anyhow!("encode upgrade plan: {e}\nTry: use valid upgrade metadata"))?;
        go_escape(data)
    }
}

/// Escapes `<`, `>`, `&`, U+2028, and U+2029 as Go `encoding/json` does. These characters
/// occur only inside JSON strings, so a byte-level rewrite is safe.
fn go_escape(data: Vec<u8>) -> Result<Vec<u8>> {
    let text = String::from_utf8(data)
        .map_err(|e| anyhow!("encode upgrade plan: {e}\nTry: use UTF-8 paths"))?;
    if !text.contains(['<', '>', '&', '\u{2028}', '\u{2029}']) {
        return Ok(text.into_bytes());
    }
    let mut out = String::with_capacity(text.len() + 16);
    for c in text.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    Ok(out.into_bytes())
}

/// Returns `None` for an empty list. Go encodes a nil list as `null`.
fn non_empty(list: &[String]) -> Option<&[String]> {
    (!list.is_empty()).then_some(list)
}
