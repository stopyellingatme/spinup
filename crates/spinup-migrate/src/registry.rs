//! The migration registry and runner (Go `internal/migrate/registry.go`).
//!
//! The codemods register themselves in [`MIGRATIONS`]. [`global`] checks and sorts them
//! once. [`Registry`] also serves tests that need their own set of migrations.

use std::collections::BTreeSet;
use std::fmt;
use std::sync::OnceLock;

use anyhow::anyhow;
use spinup_compat::version::Version;

use crate::contract::{Change, Ctx, MIGRATIONS, Migration, Stage};

/// A migration that the registry does not accept.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum RegisterError {
    /// The migration has an empty ID.
    #[error("migrate: empty migration ID")]
    EmptyId,
    /// The `since` version does not parse.
    #[error("migrate: migration {id:?}: {reason}")]
    Since {
        /// The migration ID.
        id: String,
        /// The parse error text.
        reason: String,
    },
    /// Two migrations have the same ID.
    #[error("migrate: duplicate migration {0:?}")]
    Duplicate(String),
}

/// Stores migrations in semantic version order, then ID order.
#[derive(Debug, Default, Clone)]
pub struct Registry<'a> {
    migrations: Vec<(Version, &'a Migration)>,
}

impl<'a> Registry<'a> {
    /// Returns an empty registry.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Adds `m`.
    ///
    /// # Errors
    /// Returns an error when the ID is empty or already present, or when `since` is not a
    /// semantic version. The registry does not change on an error.
    pub fn register(&mut self, m: &'a Migration) -> Result<(), RegisterError> {
        if m.id.trim().is_empty() {
            return Err(RegisterError::EmptyId);
        }
        let since = Version::parse(m.since).map_err(|e| RegisterError::Since {
            id: m.id.to_string(),
            reason: e.to_string(),
        })?;
        if self.migrations.iter().any(|(_, x)| x.id == m.id) {
            return Err(RegisterError::Duplicate(m.id.to_string()));
        }
        let at = self
            .migrations
            .partition_point(|(v, x)| (v, x.id) <= (&since, m.id));
        self.migrations.insert(at, (since, m));
        Ok(())
    }

    /// Returns every migration in semantic version and ID order.
    #[must_use]
    pub fn all(&self) -> Vec<&'a Migration> {
        self.migrations.iter().map(|(_, m)| *m).collect()
    }

    /// Returns the migrations of `stage` whose `since` falls in `(from, to]` and whose ID is
    /// not in `applied`.
    ///
    /// # Errors
    /// Returns an error when a version does not parse or when `from` is after `to`.
    pub fn pending(
        &self,
        from: &str,
        to: &str,
        applied: &[String],
        stage: Stage,
    ) -> anyhow::Result<Vec<&'a Migration>> {
        let low = Version::parse(from).map_err(|e| {
            anyhow!(
                "select migrations: invalid source version: {e}\nTry: check the source template version"
            )
        })?;
        let high = Version::parse(to).map_err(|e| {
            anyhow!(
                "select migrations: invalid target version: {e}\nTry: choose a valid target template version"
            )
        })?;
        if low > high {
            return Err(anyhow!(
                "select migrations: source {from:?} exceeds target {to:?}\nTry: choose a target version at or after the source"
            ));
        }
        let done: BTreeSet<&str> = applied.iter().map(String::as_str).collect();
        Ok(self
            .migrations
            .iter()
            .filter(|(since, m)| {
                m.stage == stage && !done.contains(m.id) && low < *since && *since <= high
            })
            .map(|(_, m)| *m)
            .collect())
    }
}

/// Returns the registry of every migration in [`MIGRATIONS`].
///
/// # Panics
/// Panics when a registered migration is invalid or duplicated, as Go `Register` does.
/// The registry test of the binary finds such a fault before a release.
pub fn global() -> &'static Registry<'static> {
    static GLOBAL: OnceLock<Registry<'static>> = OnceLock::new();
    GLOBAL.get_or_init(|| {
        let mut registry = Registry::new();
        for m in MIGRATIONS {
            if let Err(e) = registry.register(m) {
                panic!("{e}");
            }
        }
        registry
    })
}

/// Returns the registered migrations in semantic version and ID order (Go `All`).
#[must_use]
pub fn all() -> Vec<&'static Migration> {
    global().all()
}

/// Selects the registered migrations to run (Go `Pending`). See [`Registry::pending`].
///
/// # Errors
/// Returns an error when a version does not parse or when `from` is after `to`.
pub fn pending(
    from: &str,
    to: &str,
    applied: &[String],
    stage: Stage,
) -> anyhow::Result<Vec<&'static Migration>> {
    global().pending(from, to, applied, stage)
}

/// The failure of one migration in [`plan_all`] or [`run`].
#[derive(Debug)]
pub struct StepError {
    /// `plan` or `apply`.
    pub action: &'static str,
    /// The ID of the migration that failed.
    pub id: String,
    /// The error of the migration.
    pub source: anyhow::Error,
}

impl fmt::Display for StepError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let flag = if self.action == "plan" {
            "--plan"
        } else {
            "--continue"
        };
        write!(
            f,
            "{} migration {:?}: {:#}\nTry: correct the migration input and run spinup upgrade {flag}",
            self.action, self.id, self.source
        )
    }
}

impl std::error::Error for StepError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(self.source.as_ref())
    }
}

/// Plans `ms` in the given order (Go `PlanAll`). It returns the changes of the migrations
/// before a failure, and the failure.
pub fn plan_all(ctx: &mut Ctx<'_>, ms: &[&Migration]) -> (Vec<Change>, Result<(), StepError>) {
    let mut changes = Vec::new();
    for m in ms {
        match (m.plan)(ctx) {
            Ok(planned) => changes.extend(planned),
            Err(source) => {
                let error = StepError {
                    action: "plan",
                    id: m.id.to_string(),
                    source,
                };
                return (changes, Err(error));
            }
        }
    }
    (changes, Ok(()))
}

/// Applies `ms` in the given order (Go `Run`). It returns the IDs of the migrations that
/// completed before a failure, and the failure. The caller records the IDs in the lock.
pub fn run(ctx: &mut Ctx<'_>, ms: &[&Migration]) -> (Vec<String>, Result<(), StepError>) {
    let mut applied = Vec::new();
    for m in ms {
        if let Err(source) = (m.apply)(ctx) {
            let error = StepError {
                action: "apply",
                id: m.id.to_string(),
                source,
            };
            return (applied, Err(error));
        }
        applied.push(m.id.to_string());
    }
    (applied, Ok(()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use spinup_exec::fake::Fake;
    use spinup_exec::runner::Cancellation;
    use spinup_manifest::lock::Lock;
    use spinup_stack::stack::Stack;

    fn with_ctx(f: impl FnOnce(&mut Ctx<'_>)) {
        let runner = Fake::new();
        let cancel = Cancellation::new();
        let lock = Lock::default();
        let stack = Stack::default();
        let mut answers = serde_json::Map::new();
        let mut ctx = Ctx {
            root: std::path::Path::new("."),
            from: "0.1.0",
            to: "0.2.0",
            answers: &mut answers,
            lock: &lock,
            runner: &runner,
            cancel: &cancel,
            old_stack: None,
            new_stack: &stack,
        };
        f(&mut ctx);
    }

    fn plan_id(ctx: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
        Ok(vec![Change {
            path: ctx.from.to_string(),
            desc: String::new(),
        }])
    }
    fn apply_ok(_: &mut Ctx<'_>) -> anyhow::Result<()> {
        Ok(())
    }
    fn plan_fail(_: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
        Err(anyhow::Error::new(Sentinel))
    }
    fn apply_fail(_: &mut Ctx<'_>) -> anyhow::Result<()> {
        Err(anyhow::Error::new(Sentinel))
    }
    fn apply_never(_: &mut Ctx<'_>) -> anyhow::Result<()> {
        panic!("ran past failure")
    }

    #[derive(Debug, thiserror::Error)]
    #[error("broken input")]
    struct Sentinel;

    const fn migration(id: &'static str, since: &'static str) -> Migration {
        Migration {
            id,
            since,
            desc: "",
            stage: Stage::After,
            plan: plan_id,
            apply: apply_ok,
        }
    }

    static RELEASE: Migration = migration("release", "0.2.0");
    static B: Migration = migration("b", "0.2.0-rc.10");
    static A: Migration = migration("a", "0.2.0-rc.2");
    static C: Migration = migration("c", "0.2.0-rc.2");

    fn ids(ms: &[&Migration]) -> Vec<&'static str> {
        ms.iter().map(|m| m.id).collect()
    }

    #[test]
    fn registry_orders_and_selects() {
        let mut r = Registry::new();
        for m in [&RELEASE, &B, &A, &C] {
            r.register(m).unwrap();
        }
        assert_eq!(ids(&r.all()), ["a", "c", "b", "release"]);
        type Case<'a> = (&'a str, &'a str, &'a [&'a str], Stage, &'a [&'a str], bool);
        let cases: [Case<'_>; 7] = [
            (
                "0.1.0",
                "0.2.0",
                &[],
                Stage::After,
                &["a", "c", "b", "release"],
                false,
            ),
            (
                "0.2.0-rc.2",
                "0.2.0",
                &["b"],
                Stage::After,
                &["release"],
                false,
            ),
            ("0.1.0", "0.2.0-rc.2", &[], Stage::After, &["a", "c"], false),
            ("0.1.0", "0.2.0", &[], Stage::Before, &[], false),
            ("bad", "0.2.0", &[], Stage::After, &[], true),
            ("0.1.0", "bad", &[], Stage::After, &[], true),
            ("0.2.0", "0.1.0", &[], Stage::After, &[], true),
        ];
        for (from, to, applied, stage, want, bad) in cases {
            let applied: Vec<String> = applied.iter().map(|s| (*s).to_string()).collect();
            match r.pending(from, to, &applied, stage) {
                Ok(ms) => {
                    assert!(!bad, "{from} {to}: want an error");
                    assert_eq!(ids(&ms), want, "{from} {to}");
                }
                Err(e) => {
                    assert!(bad, "{from} {to}: {e}");
                    assert!(e.to_string().contains("\nTry: "), "{e}");
                }
            }
        }
    }

    #[test]
    fn register_rejects_invalid() {
        static EMPTY: Migration = migration(" ", "0.2.0");
        static SINCE: Migration = migration("test", "no");
        static TEST: Migration = migration("test", "0.2.0");
        static TEST2: Migration = migration("test", "0.3.0");
        let mut r = Registry::new();
        assert_eq!(r.register(&EMPTY), Err(RegisterError::EmptyId));
        assert!(matches!(
            r.register(&SINCE),
            Err(RegisterError::Since { .. })
        ));
        r.register(&TEST).unwrap();
        assert_eq!(
            r.register(&TEST2),
            Err(RegisterError::Duplicate("test".into()))
        );
        assert_eq!(ids(&r.all()), ["test"]);
    }

    #[test]
    fn global_registry_is_valid() {
        let all = all();
        assert_eq!(all.len(), MIGRATIONS.len());
        let mut sorted = all.clone();
        sorted.sort_by(|a, b| {
            (Version::parse(a.since).unwrap(), a.id).cmp(&(Version::parse(b.since).unwrap(), b.id))
        });
        assert_eq!(ids(&all), ids(&sorted));
        assert!(
            pending("0.0.0", "0.0.0", &[], Stage::After)
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn run_and_plan_stop_at_failure() {
        static FIRST: Migration = migration("first", "0.2.0");
        static BAD: Migration = Migration {
            plan: plan_fail,
            apply: apply_fail,
            ..migration("bad", "0.2.0")
        };
        static LAST: Migration = Migration {
            apply: apply_never,
            ..migration("last", "0.2.0")
        };
        let ms = [&FIRST, &BAD, &LAST];
        let check = |err: StepError| {
            let text = err.to_string();
            assert!(text.contains("\"bad\""), "{text}");
            assert!(text.contains("broken input"), "{text}");
            assert!(text.contains("\nTry:"), "{text}");
            let source = std::error::Error::source(&err).unwrap();
            assert!(source.downcast_ref::<Sentinel>().is_some());
        };
        with_ctx(|ctx| {
            let (changes, result) = plan_all(ctx, &ms);
            assert_eq!(changes.len(), 1);
            check(result.unwrap_err());
            let (applied, result) = run(ctx, &ms);
            assert_eq!(applied, ["first"]);
            check(result.unwrap_err());
            let (applied, result) = run(ctx, &[&FIRST]);
            assert!(result.is_ok());
            assert_eq!(applied, ["first"]);
            let (changes, result) = plan_all(ctx, &[&FIRST]);
            assert!(result.is_ok());
            assert_eq!(changes.len(), 1);
        });
    }
}
