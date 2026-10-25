//! Test helpers for codemods (Go `internal/migrate/migratetest`).
//!
//! [`run`] copies a `before` tree to a scratch directory and checks three rules:
//! `plan` does not change the tree, `apply` gives the `after` tree, and a second `apply`
//! changes nothing. Every subprocess call is a violation, unless the options give a
//! runner.

use std::collections::BTreeMap;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::sync::atomic::{AtomicU64, Ordering};

use spinup_exec::runner::{Cancellation, Cmd, ExecError, Output, Runner, Sinks};
use spinup_manifest::lock::Lock;
use spinup_stack::stack::Stack;

use crate::contract::{Ctx, Migration};

/// The options of one fixture run.
#[derive(Default, Clone, Copy)]
pub struct Options<'a> {
    /// Permits subprocess calls through this runner (Go `WithRunner`).
    pub runner: Option<&'a dyn Runner>,
    /// The old stack versions of the context. The default is `None`.
    pub old_stack: Option<&'a Stack>,
    /// The new stack versions of the context. The default is an empty stack.
    pub new_stack: Option<&'a Stack>,
}

/// Runs the fixture check and panics with every violation (Go `migratetest.Run`).
///
/// # Panics
/// Panics when [`check`] reports a violation.
#[track_caller]
pub fn run(m: &Migration, before: &Path, after: &Path, opts: Options<'_>) {
    let violations = check(m, before, after, opts);
    assert!(
        violations.is_empty(),
        "migratetest: migration {:?}:\n{}",
        m.id,
        violations.join("\n")
    );
}

/// Runs the fixture check and returns every violation. An empty list means success.
///
/// The context has the scratch root, `from` `0.0.0`, `to` the `since` version of `m`,
/// empty answers, and a default lock.
#[must_use]
pub fn check(m: &Migration, before: &Path, after: &Path, opts: Options<'_>) -> Vec<String> {
    let denied = DeniedRunner::default();
    let violations = check_with(m, before, after, opts, &denied);
    let mut all = denied.messages.into_inner().unwrap_or_default();
    all.extend(violations);
    all
}

fn check_with(
    m: &Migration,
    before: &Path,
    after: &Path,
    opts: Options<'_>,
    denied: &DeniedRunner,
) -> Vec<String> {
    let mut out = Vec::new();
    let scratch = match Scratch::new() {
        Ok(scratch) => scratch,
        Err(e) => return vec![format!("create the scratch directory: {e}")],
    };
    let root = scratch.0.as_path();
    let original = match read_tree(before) {
        Ok(tree) => tree,
        Err(e) => return vec![format!("read fixture tree {}: {e}", before.display())],
    };
    if let Err(e) = copy_tree(root, &original) {
        return vec![format!("copy fixture tree: {e}")];
    }
    let read = |path: &Path| read_tree(path).map_err(|e| format!("read fixture tree: {e}"));
    let result = (|| -> Result<(), String> {
        let initial = read(root)?;
        let lock = Lock::default();
        let empty = Stack::default();
        let cancel = Cancellation::new();
        let mut answers = serde_json::Map::new();
        let mut ctx = Ctx {
            root,
            from: "0.0.0",
            to: m.since,
            answers: &mut answers,
            lock: &lock,
            runner: opts.runner.unwrap_or(denied as &dyn Runner),
            cancel: &cancel,
            old_stack: opts.old_stack,
            new_stack: opts.new_stack.unwrap_or(&empty),
        };
        (m.plan)(&mut ctx).map_err(|e| format!("migration {:?} plan: {e:#}", m.id))?;
        compare_trees(&mut out, "Plan changed the tree", &initial, &read(root)?);
        (m.apply)(&mut ctx).map_err(|e| format!("migration {:?} apply: {e:#}", m.id))?;
        let first = read(root)?;
        compare_trees(&mut out, "Apply differs from after", &read(after)?, &first);
        (m.apply)(&mut ctx).map_err(|e| format!("migration {:?} second apply: {e:#}", m.id))?;
        compare_trees(&mut out, "Apply is not idempotent", &first, &read(root)?);
        Ok(())
    })();
    if let Err(e) = result {
        out.push(e);
    }
    out
}

/// A runner that records every call as a violation.
#[derive(Default)]
struct DeniedRunner {
    messages: Mutex<Vec<String>>,
}

impl Runner for DeniedRunner {
    fn run(&self, cmd: &Cmd, _: &Cancellation, _: Sinks<'_>) -> Result<Output, ExecError> {
        let message = format!("unexpected subprocess: {cmd}");
        if let Ok(mut messages) = self.messages.lock() {
            messages.push(format!("migratetest: {message}"));
        }
        Err(ExecError::Io {
            cmd: cmd.to_string(),
            source: io::Error::other(message),
        })
    }
}

/// A scratch directory that is removed on drop.
struct Scratch(PathBuf);

impl Scratch {
    fn new() -> io::Result<Self> {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let n = NEXT.fetch_add(1, Ordering::Relaxed);
        let path =
            std::env::temp_dir().join(format!("spinup-migratetest-{}-{n}", std::process::id()));
        if path.exists() {
            fs::remove_dir_all(&path)?;
        }
        fs::create_dir_all(&path)?;
        Ok(Self(path.canonicalize()?))
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        make_writable(&self.0);
        let _ = fs::remove_dir_all(&self.0);
    }
}

/// Makes every directory writable, so that the drop can remove a read-only tree.
fn make_writable(dir: &Path) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(dir, fs::Permissions::from_mode(0o755));
    }
    if let Ok(entries) = fs::read_dir(dir) {
        for entry in entries.flatten() {
            if entry.file_type().is_ok_and(|t| t.is_dir()) {
                make_writable(&entry.path());
            }
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Kind {
    Dir,
    File(Vec<u8>),
    Link(PathBuf),
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct Entry {
    mode: u32,
    kind: Kind,
}

type Tree = BTreeMap<String, Entry>;

fn mode_of(meta: &fs::Metadata) -> u32 {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        meta.permissions().mode()
    }
    #[cfg(not(unix))]
    {
        u32::from(meta.permissions().readonly())
    }
}

fn read_tree(root: &Path) -> io::Result<Tree> {
    let mut out = Tree::new();
    walk(root, "", &mut out)?;
    Ok(out)
}

fn walk(dir: &Path, prefix: &str, out: &mut Tree) -> io::Result<()> {
    for entry in fs::read_dir(dir)? {
        let entry = entry?;
        let name = entry.file_name().to_string_lossy().into_owned();
        let relative = if prefix.is_empty() {
            name
        } else {
            format!("{prefix}/{name}")
        };
        let path = entry.path();
        let meta = fs::symlink_metadata(&path)?;
        let file_type = meta.file_type();
        let kind = if file_type.is_symlink() {
            Kind::Link(fs::read_link(&path)?)
        } else if file_type.is_dir() {
            walk(&path, &relative, out)?;
            Kind::Dir
        } else if file_type.is_file() {
            Kind::File(fs::read(&path)?)
        } else {
            return Err(io::Error::other(format!(
                "unsupported fixture file {:?}",
                path.display()
            )));
        };
        out.insert(
            relative,
            Entry {
                mode: mode_of(&meta),
                kind,
            },
        );
    }
    Ok(())
}

fn copy_tree(root: &Path, source: &Tree) -> io::Result<()> {
    // Directories stay writable until their contents are in place.
    for (path, entry) in source {
        let target = root.join(path);
        match &entry.kind {
            Kind::Dir => fs::create_dir_all(&target)?,
            Kind::File(data) => {
                if let Some(parent) = target.parent() {
                    fs::create_dir_all(parent)?;
                }
                fs::write(&target, data)?;
                set_mode(&target, entry.mode)?;
            }
            Kind::Link(link) => {
                if let Some(parent) = target.parent() {
                    fs::create_dir_all(parent)?;
                }
                symlink(link, &target)?;
            }
        }
    }
    for (path, entry) in source.iter().rev() {
        if entry.kind == Kind::Dir {
            set_mode(&root.join(path), entry.mode)?;
        }
    }
    Ok(())
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) -> io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode & 0o7777))
}

#[cfg(not(unix))]
fn set_mode(path: &Path, mode: u32) -> io::Result<()> {
    let mut permissions = fs::metadata(path)?.permissions();
    permissions.set_readonly(mode != 0);
    fs::set_permissions(path, permissions)
}

#[cfg(unix)]
fn symlink(link: &Path, target: &Path) -> io::Result<()> {
    std::os::unix::fs::symlink(link, target)
}

#[cfg(not(unix))]
fn symlink(_: &Path, target: &Path) -> io::Result<()> {
    Err(io::Error::other(format!(
        "symbolic link {:?} is not supported on this platform",
        target.display()
    )))
}

fn compare_trees(out: &mut Vec<String>, label: &str, want: &Tree, got: &Tree) {
    let paths: std::collections::BTreeSet<&String> = want.keys().chain(got.keys()).collect();
    for path in paths {
        let (w, g) = match (want.get(path), got.get(path)) {
            (None, _) => {
                out.push(format!("{label}: unexpected path {path}"));
                continue;
            }
            (_, None) => {
                out.push(format!("{label}: missing path {path}"));
                continue;
            }
            (Some(w), Some(g)) => (w, g),
        };
        if w.mode != g.mode {
            out.push(format!(
                "{label}: {path} mode: want {:o}, got {:o}",
                w.mode, g.mode
            ));
        }
        if w.kind != g.kind {
            out.push(format!("{label}:\n{}", describe(path, &w.kind, &g.kind)));
        }
    }
}

fn describe(path: &str, want: &Kind, got: &Kind) -> String {
    let text = |kind: &Kind| -> Option<String> {
        match kind {
            Kind::File(data) => String::from_utf8(data.clone()).ok(),
            Kind::Link(link) => Some(format!("{}\n", link.display())),
            Kind::Dir => None,
        }
    };
    match (text(want), text(got)) {
        (Some(w), Some(g)) => similar::TextDiff::from_lines(&w, &g)
            .unified_diff()
            .header(&format!("want/{path}"), &format!("got/{path}"))
            .to_string(),
        _ => format!("{path}: want {want:?}, got {got:?}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::{Change, Stage};
    use spinup_exec::fake::Fake;

    fn fixture() -> tempfile::TempDir {
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir(dir.path().join("empty")).unwrap();
        fs::write(dir.path().join("file"), "before\n").unwrap();
        dir
    }

    fn nothing(_: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
        Ok(Vec::new())
    }
    fn apply_nothing(_: &mut Ctx<'_>) -> anyhow::Result<()> {
        Ok(())
    }
    fn plan_writes(c: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
        fs::write(c.root.join("file"), "changed\n")?;
        Ok(Vec::new())
    }
    fn apply_counts(c: &mut Ctx<'_>) -> anyhow::Result<()> {
        let path = c.root.join("file");
        let data = fs::read_to_string(&path)?;
        fs::write(path, format!("{data}x"))?;
        Ok(())
    }
    fn apply_runs(c: &mut Ctx<'_>) -> anyhow::Result<()> {
        let _ = c.runner.output(&Cmd::new("forbidden"), c.cancel);
        Ok(())
    }
    fn plan_allowed(c: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
        c.runner.output(&Cmd::new("allowed"), c.cancel)?;
        Ok(Vec::new())
    }
    fn apply_allowed(c: &mut Ctx<'_>) -> anyhow::Result<()> {
        c.runner.output(&Cmd::new("allowed"), c.cancel)?;
        Ok(())
    }

    const fn migration(
        plan: fn(&mut Ctx<'_>) -> anyhow::Result<Vec<Change>>,
        apply: fn(&mut Ctx<'_>) -> anyhow::Result<()>,
    ) -> Migration {
        Migration {
            id: "example",
            since: "0.2.0",
            desc: "",
            stage: Stage::After,
            plan,
            apply,
        }
    }

    #[test]
    fn detects_violations() {
        for (m, want) in [
            (
                migration(plan_writes, apply_nothing),
                "Plan changed the tree",
            ),
            (migration(nothing, apply_counts), "Apply is not idempotent"),
            (migration(nothing, apply_runs), "unexpected subprocess"),
        ] {
            let root = fixture();
            let violations = check(&m, root.path(), root.path(), Options::default());
            let joined = violations.join("\n");
            assert!(joined.contains(want), "missing {want:?} in {joined}");
        }
    }

    #[test]
    fn reports_after_differences() {
        let root = fixture();
        let after = tempfile::tempdir().unwrap();
        fs::write(after.path().join("file"), "after\n").unwrap();
        fs::write(after.path().join("new"), "").unwrap();
        let violations = check(
            &migration(nothing, apply_nothing),
            root.path(),
            after.path(),
            Options::default(),
        );
        let joined = violations.join("\n");
        assert!(joined.contains("unexpected path empty"), "{joined}");
        assert!(joined.contains("missing path new"), "{joined}");
        assert!(joined.contains("-after\n+before"), "{joined}");
        assert!(joined.contains("want/file"), "{joined}");
    }

    #[test]
    fn with_runner() {
        let root = fixture();
        let fake = Fake::new();
        for _ in 0..3 {
            fake.expect("allowed", [] as [&str; 0]);
        }
        run(
            &migration(plan_allowed, apply_allowed),
            root.path(),
            root.path(),
            Options {
                runner: Some(&fake),
                ..Options::default()
            },
        );
        fake.assert_done().unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn keeps_modes_and_links() {
        use std::os::unix::fs::PermissionsExt;
        let root = fixture();
        let script = root.path().join("run.sh");
        fs::write(&script, "#!/bin/sh\n").unwrap();
        fs::set_permissions(&script, fs::Permissions::from_mode(0o755)).unwrap();
        std::os::unix::fs::symlink("file", root.path().join("link")).unwrap();
        let locked = root.path().join("empty");
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o555)).unwrap();
        run(
            &migration(nothing, apply_nothing),
            root.path(),
            root.path(),
            Options::default(),
        );
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o755)).unwrap();
    }

    /// Renames `app.name` from `old` to `new` with the YAML editor (Go `example_test.go`).
    fn rename(c: &Ctx<'_>) -> anyhow::Result<(String, bool)> {
        let src = fs::read_to_string(c.root.join("app.yaml"))?;
        Ok(spinup_editors::yaml::edit(&src, |doc| {
            let node = doc.get(&["app", "name"]);
            if node.as_ref().and_then(|n| n.as_str()) != Some("old") {
                return Ok(false);
            }
            doc.set(&["app", "name"], &spinup_editors::yaml::Node::str("new"))?;
            Ok(true)
        })?)
    }
    fn plan_rename(c: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
        let (_, changed) = rename(c)?;
        Ok(if changed {
            vec![Change {
                path: "app.yaml".into(),
                desc: "Rename the app".into(),
            }]
        } else {
            Vec::new()
        })
    }
    fn apply_rename(c: &mut Ctx<'_>) -> anyhow::Result<()> {
        let (out, changed) = rename(c)?;
        if changed {
            fs::write(c.root.join("app.yaml"), out)?;
        }
        Ok(())
    }

    #[test]
    fn example_migration() {
        let before = tempfile::tempdir().unwrap();
        let after = tempfile::tempdir().unwrap();
        let text = "# App configuration.\napp:\n  name: old # Keep this comment.\n";
        fs::write(before.path().join("app.yaml"), text).unwrap();
        fs::write(after.path().join("app.yaml"), text.replace("old", "new")).unwrap();
        run(
            &migration(plan_rename, apply_rename),
            before.path(),
            after.path(),
            Options::default(),
        );
    }

    #[test]
    #[should_panic(expected = "Apply is not idempotent")]
    fn run_panics() {
        let root = fixture();
        run(
            &migration(nothing, apply_counts),
            root.path(),
            root.path(),
            Options::default(),
        );
    }
}
