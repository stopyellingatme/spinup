//! The plan output and the plan summary (Go `internal/upgrade/plan_format.go`).

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::io::Write;

use anyhow::{Result, anyhow};

use crate::contract::{Action, Plan};

/// The order of the action groups in the plan output.
const ORDER: [Action; 9] = [
    Action::Conflict,
    Action::Merge,
    Action::Overwrite,
    Action::Create,
    Action::Rename,
    Action::Delete,
    Action::Orphan,
    Action::Keep,
    Action::Unchanged,
];

impl Plan {
    /// Returns the sorted paths of the decisions with conflicts.
    pub fn conflicts(&self) -> Vec<String> {
        self.summary().remove(&Action::Conflict).unwrap_or_default()
    }

    /// Returns the sorted paths of each action in the plan.
    pub fn summary(&self) -> BTreeMap<Action, Vec<String>> {
        let mut result: BTreeMap<Action, Vec<String>> = BTreeMap::new();
        for d in &self.files {
            result.entry(d.action).or_default().push(d.path.clone());
        }
        for paths in result.values_mut() {
            paths.sort();
        }
        result
    }

    /// Writes the plan grouped by action, with the unchanged files as a count. The output
    /// is stable: the groups have a fixed order, and each group is sorted by path.
    pub fn format(&self, w: &mut dyn Write) -> Result<()> {
        let mut out = format!("Upgrade {} -> {}\n", self.from, self.to);
        let mut files: Vec<_> = self.files.iter().collect();
        files.sort_by(|a, b| a.path.cmp(&b.path));
        for action in ORDER {
            let group: Vec<_> = files.iter().filter(|d| d.action == action).collect();
            if group.is_empty() {
                continue;
            }
            let _ = writeln!(out, "{}: {}", action.as_str(), group.len());
            if action == Action::Unchanged {
                continue;
            }
            for d in group {
                match d.from.as_deref().filter(|from| !from.is_empty()) {
                    Some(from) => {
                        let _ = write!(out, "  {from} -> {}", d.path);
                    }
                    None => {
                        let _ = write!(out, "  {}", d.path);
                    }
                }
                if !d.reason.is_empty() {
                    let _ = write!(out, " ({})", d.reason);
                }
                out.push('\n');
            }
        }
        w.write_all(out.as_bytes())
            .and_then(|()| w.flush())
            .map_err(|e| anyhow!("format upgrade plan: {e}\nTry: use a writable output"))
    }
}
