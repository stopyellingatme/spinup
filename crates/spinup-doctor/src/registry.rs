//! The doctor check registry (Go `internal/doctor/registry.go`).
//!
//! A check registers itself in the [`CHECKS`] slice with `#[distributed_slice(CHECKS)]`
//! (ADR 0020 §5). The binary reaches the slice through the blank import in `wire_gen.rs`.

use std::any::Any;
use std::collections::BTreeMap;
use std::panic::{self, AssertUnwindSafe};
use std::path::PathBuf;
use std::sync::Arc;

use linkme::distributed_slice;
use spinup_exec::runner::{Cancellation, Runner};

/// Identifies where a check runs.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Scope {
    /// The check runs on the local machine.
    Local,
    /// The check runs against a remote environment.
    Remote,
}

/// The state of a check result.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Status {
    /// The check passed.
    Ok,
    /// The check passed with a warning.
    Warn,
    /// The check failed.
    Fail,
    /// The check did not run.
    Skip,
}

impl Status {
    /// Returns the text form of the status (`ok`, `warn`, `fail`, or `skip`).
    pub fn as_str(self) -> &'static str {
        match self {
            Status::Ok => "ok",
            Status::Warn => "warn",
            Status::Fail => "fail",
            Status::Skip => "skip",
        }
    }
}

impl std::fmt::Display for Status {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The result of one check.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Outcome {
    /// The state of the result.
    pub status: Status,
    /// The message for the user.
    pub message: String,
    /// The hint that follows `Try:`. It can be empty.
    pub try_hint: String,
}

impl Outcome {
    /// Returns a result with no hint.
    pub fn new(status: Status, message: impl Into<String>) -> Self {
        Self {
            status,
            message: message.into(),
            try_hint: String::new(),
        }
    }

    /// Returns a failed result with a hint.
    pub fn fail(message: impl Into<String>, try_hint: impl Into<String>) -> Self {
        Self {
            status: Status::Fail,
            message: message.into(),
            try_hint: try_hint.into(),
        }
    }
}

/// The values that all checks share.
#[derive(Default)]
pub struct Context {
    /// The project root, or an empty path when there is no project.
    pub root: PathBuf,
    /// The environment name, for remote checks.
    pub env: String,
    /// True when the run is the quick run.
    pub quick: bool,
    /// The runner for subprocesses. A check uses the OS runner when this is `None`.
    pub runner: Option<Arc<dyn Runner>>,
    /// The cancellation flag for subprocesses.
    pub cancel: Cancellation,
    /// Other values that a check reads by name (Go `Context.Deps`).
    pub deps: BTreeMap<String, Arc<dyn Any + Send + Sync>>,
}

impl Context {
    /// Returns a context for a project root.
    pub fn new(root: impl Into<PathBuf>) -> Self {
        Self {
            root: root.into(),
            ..Self::default()
        }
    }

    /// Returns the dependency `name` when it has the type `T`.
    pub fn dep<T: Any + Send + Sync>(&self, name: &str) -> Option<&T> {
        self.deps.get(name)?.downcast_ref::<T>()
    }

    /// Reports whether the context has no project root.
    pub fn no_root(&self) -> bool {
        self.root.as_os_str().is_empty()
    }
}

/// One registered doctor check.
pub struct Check {
    /// The unique identifier. The checks run in order of this value.
    pub id: &'static str,
    /// The title for the user.
    pub title: &'static str,
    /// Where the check runs.
    pub scope: Scope,
    /// True when the quick run includes the check.
    pub quick: bool,
    /// Runs the check.
    pub run: fn(&Context) -> Outcome,
}

/// A check with its result.
pub struct Report<'a> {
    /// The check that ran.
    pub check: &'a Check,
    /// The result of the check.
    pub result: Outcome,
}

/// The slice of all registered checks.
// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[distributed_slice]
pub static CHECKS: [Check];

/// Validates a set of checks. It panics on an empty ID or a duplicate ID, as Go `Register` does.
pub fn validate(checks: &[Check]) {
    let mut seen = std::collections::BTreeSet::new();
    for check in checks {
        assert!(!check.id.is_empty(), "doctor: register check with empty ID");
        assert!(
            seen.insert(check.id),
            "doctor: duplicate check ID {:?}",
            check.id
        );
    }
}

/// Returns the registered checks for a scope, sorted by ID.
pub fn checks(scope: Scope, quick_only: bool) -> Vec<&'static Check> {
    select(&CHECKS, scope, quick_only)
}

/// Runs all registered checks for a scope in ID order.
pub fn run_all(scope: Scope, ctx: &Context) -> Vec<Report<'static>> {
    run_checks(checks(scope, false), ctx)
}

/// Returns the checks of `items` for a scope, sorted by ID.
pub fn select(items: &[Check], scope: Scope, quick_only: bool) -> Vec<&Check> {
    validate(items);
    let mut selected: Vec<&Check> = items
        .iter()
        .filter(|check| check.scope == scope && (!quick_only || check.quick))
        .collect();
    selected.sort_by_key(|check| check.id);
    selected
}

/// Runs `checks` in order. A check that panics gives a `Fail` result.
pub fn run_checks<'a>(checks: Vec<&'a Check>, ctx: &Context) -> Vec<Report<'a>> {
    checks
        .into_iter()
        .map(|check| Report {
            check,
            result: run_one(check, ctx),
        })
        .collect()
}

fn run_one(check: &Check, ctx: &Context) -> Outcome {
    match panic::catch_unwind(AssertUnwindSafe(|| (check.run)(ctx))) {
        Ok(outcome) => outcome,
        Err(payload) => {
            let reason = payload
                .downcast_ref::<&str>()
                .map(|text| (*text).to_string())
                .or_else(|| payload.downcast_ref::<String>().cloned())
                .unwrap_or_else(|| "unknown panic".to_string());
            Outcome::new(
                Status::Fail,
                format!("check {:?} panicked: {reason}", check.id),
            )
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ok(_: &Context) -> Outcome {
        Outcome::new(Status::Ok, "")
    }

    fn boom(_: &Context) -> Outcome {
        panic!("broken")
    }

    fn check(id: &'static str, scope: Scope, quick: bool, run: fn(&Context) -> Outcome) -> Check {
        Check {
            id,
            title: "",
            scope,
            quick,
            run,
        }
    }

    #[test]
    fn select_sorts_and_filters() {
        let items = [
            check("z", Scope::Local, false, ok),
            check("a", Scope::Local, true, ok),
            check("remote", Scope::Remote, false, ok),
        ];
        let ids: Vec<_> = select(&items, Scope::Local, false)
            .iter()
            .map(|c| c.id)
            .collect();
        assert_eq!(ids, ["a", "z"]);
        let quick: Vec<_> = select(&items, Scope::Local, true)
            .iter()
            .map(|c| c.id)
            .collect();
        assert_eq!(quick, ["a"]);
    }

    #[test]
    fn validate_panics() {
        let duplicate = [
            check("one", Scope::Local, false, ok),
            check("one", Scope::Local, false, ok),
        ];
        assert!(panic::catch_unwind(|| validate(&duplicate)).is_err());
        let empty = [check("", Scope::Local, false, ok)];
        assert!(panic::catch_unwind(|| validate(&empty)).is_err());
        let fine = [check("one", Scope::Local, false, ok)];
        validate(&fine);
    }

    #[test]
    fn run_recovers_panic() {
        let items = [
            check("panic", Scope::Local, false, boom),
            check("ok", Scope::Local, false, ok),
        ];
        let reports = run_checks(select(&items, Scope::Local, false), &Context::default());
        assert_eq!(reports.len(), 2);
        assert_eq!(reports[0].check.id, "ok");
        assert_eq!(reports[0].result.status, Status::Ok);
        assert_eq!(reports[1].result.status, Status::Fail);
        assert!(reports[1].result.message.contains("broken"));
    }

    #[test]
    fn status_text() {
        assert_eq!(Status::Warn.to_string(), "warn");
        assert_eq!(Status::Skip.as_str(), "skip");
    }
}
