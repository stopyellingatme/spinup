//! The planner tests (Go `internal/upgrade/plan_test.go`).

use spinup_manifest::hash::hash;
use spinup_manifest::lock::{Class, FileEntry};
use spinup_merge::contract::MAX_SIZE;
use tempfile::TempDir;

use super::testing::{Fixture, lock, spec, version, version_with_stack, write};
use super::*;
use crate::contract::{Action, FileDecision, Plan};

#[derive(Default)]
struct Case {
    name: &'static str,
    class: Option<Class>,
    next_class: Option<&'static str>,
    disk: String,
    next: Option<String>,
    base: Option<String>,
    reason: &'static str,
    action: Option<Action>,
    missing: bool,
    removed: bool,
    tombstone: bool,
    no_old: bool,
    force: bool,
    mode: u32,
    merges: usize,
}

fn class_name(class: Class) -> &'static str {
    match class {
        Class::Managed => "managed",
        Class::Scaffold => "scaffold",
        Class::Seed => "seed",
    }
}

fn plan_decision_cases() -> Vec<Case> {
    use Action::*;
    use Class::*;
    let large = |c: &str| c.repeat(MAX_SIZE + 1);
    let mut cases = vec![
        Case {
            name: "tombstone",
            tombstone: true,
            action: Some(Keep),
            reason: TOMBSTONE,
            ..Case::default()
        },
        Case {
            name: "missing scaffold",
            class: Some(Scaffold),
            missing: true,
            action: Some(Keep),
            ..Case::default()
        },
        Case {
            name: "missing managed",
            class: Some(Managed),
            missing: true,
            action: Some(Keep),
            ..Case::default()
        },
        Case {
            name: "seed",
            class: Some(Seed),
            disk: "user".into(),
            action: Some(Keep),
            ..Case::default()
        },
        Case {
            name: "new seed free",
            next_class: Some("seed"),
            missing: true,
            action: Some(Create),
            ..Case::default()
        },
        Case {
            name: "new seed occupied",
            next_class: Some("seed"),
            disk: "user".into(),
            action: Some(Keep),
            ..Case::default()
        },
        Case {
            name: "becomes seed",
            class: Some(Managed),
            next_class: Some("seed"),
            disk: "user".into(),
            action: Some(Keep),
            ..Case::default()
        },
        Case {
            name: "removed unchanged",
            class: Some(Scaffold),
            disk: "old".into(),
            removed: true,
            action: Some(Delete),
            ..Case::default()
        },
        Case {
            name: "removed changed",
            class: Some(Managed),
            disk: "user".into(),
            removed: true,
            action: Some(Orphan),
            ..Case::default()
        },
        Case {
            name: "new free",
            missing: true,
            action: Some(Create),
            ..Case::default()
        },
        Case {
            name: "adopt identical",
            disk: "new".into(),
            action: Some(Unchanged),
            ..Case::default()
        },
        Case {
            name: "new occupied",
            disk: "user".into(),
            action: Some(Conflict),
            reason: "path exists",
            merges: 1,
            ..Case::default()
        },
        Case {
            name: "identical",
            class: Some(Managed),
            disk: "new".into(),
            action: Some(Unchanged),
            ..Case::default()
        },
        Case {
            name: "unchanged disk",
            class: Some(Managed),
            disk: "old".into(),
            action: Some(Overwrite),
            ..Case::default()
        },
        Case {
            name: "CRLF",
            class: Some(Scaffold),
            disk: "old\r\n\r\n".into(),
            action: Some(Overwrite),
            ..Case::default()
        },
        Case {
            name: "binary ours",
            class: Some(Managed),
            disk: "user\0".into(),
            action: Some(Keep),
            reason: UNMERGEABLE,
            ..Case::default()
        },
        Case {
            name: "binary theirs",
            class: Some(Scaffold),
            disk: "user".into(),
            next: Some("new\0".into()),
            action: Some(Keep),
            reason: UNMERGEABLE,
            ..Case::default()
        },
        Case {
            name: "binary base",
            class: Some(Scaffold),
            disk: "user".into(),
            base: Some("old\0".into()),
            action: Some(Keep),
            reason: UNMERGEABLE,
            ..Case::default()
        },
        Case {
            name: "large ours",
            class: Some(Managed),
            disk: large("u"),
            force: true,
            action: Some(Keep),
            reason: UNMERGEABLE,
            ..Case::default()
        },
        Case {
            name: "large theirs",
            class: Some(Scaffold),
            disk: "user".into(),
            next: Some(large("n")),
            action: Some(Keep),
            ..Case::default()
        },
        Case {
            name: "large base",
            class: Some(Scaffold),
            disk: "user".into(),
            base: Some(large("b")),
            action: Some(Keep),
            ..Case::default()
        },
        Case {
            name: "forced managed",
            class: Some(Managed),
            disk: "user".into(),
            force: true,
            action: Some(Overwrite),
            reason: "forced",
            ..Case::default()
        },
        Case {
            name: "ejected not forced",
            class: Some(Scaffold),
            next_class: Some("managed"),
            disk: "user".into(),
            force: true,
            action: Some(Conflict),
            merges: 1,
            ..Case::default()
        },
        Case {
            name: "scaffold not forced",
            class: Some(Scaffold),
            disk: "user".into(),
            force: true,
            action: Some(Conflict),
            merges: 1,
            ..Case::default()
        },
        Case {
            name: "managed conflict",
            class: Some(Managed),
            disk: "user".into(),
            action: Some(Conflict),
            merges: 1,
            ..Case::default()
        },
        Case {
            name: "scaffold clean",
            class: Some(Scaffold),
            disk: "user".into(),
            next: Some("old".into()),
            action: Some(Merge),
            merges: 1,
            ..Case::default()
        },
        Case {
            name: "managed clean",
            class: Some(Managed),
            disk: "user".into(),
            next: Some("old".into()),
            action: Some(Merge),
            merges: 1,
            ..Case::default()
        },
        Case {
            name: "no base changed",
            class: Some(Scaffold),
            disk: "user".into(),
            no_old: true,
            action: Some(Conflict),
            reason: NO_BASE,
            merges: 1,
            ..Case::default()
        },
        Case {
            name: "no base unchanged",
            class: Some(Managed),
            disk: "old".into(),
            no_old: true,
            action: Some(Overwrite),
            ..Case::default()
        },
    ];
    // Windows has no permission bits, so the planner never plans a mode change there.
    if cfg!(unix) {
        cases.push(Case {
            name: "mode",
            class: Some(Scaffold),
            disk: "new".into(),
            mode: 0o755,
            action: Some(Overwrite),
            reason: "mode",
            ..Case::default()
        });
    }
    cases
}

#[test]
fn plan_decisions() {
    for tc in plan_decision_cases() {
        let name = tc.name;
        let root = TempDir::new().unwrap();
        let mut lock = lock("old", tc.class);
        if tc.tombstone {
            lock.tombstone("file.txt");
        }
        if !tc.missing {
            write(root.path(), "file.txt", &tc.disk, 0o644);
        }
        let next = tc.next.clone().unwrap_or_else(|| "new".into());
        let base = tc.base.clone().unwrap_or_else(|| "old".into());
        let old = version("0.1.0", &[spec("file.txt", &base)]);
        let next_class = tc
            .next_class
            .or(tc.class.map(class_name))
            .unwrap_or("scaffold");
        let mode = if tc.mode == 0 { 0o644 } else { tc.mode };
        let newer = if tc.removed {
            version("0.2.0", &[])
        } else {
            version(
                "0.2.0",
                &[spec("file.txt", &next).class(next_class).mode(mode)],
            )
        };
        let fixture = Fixture {
            force_managed: tc.force,
            ..Fixture::default()
        };
        let old = (!tc.no_old).then_some(&old);
        let p = build_plan(&fixture.input(root.path(), &lock, old, &newer)).unwrap();
        assert_eq!(p.files.len(), 1, "{name}: {:?}", p.files);
        let d = &p.files[0];
        assert_eq!(Some(d.action), tc.action, "{name}: {d:?}");
        if !tc.reason.is_empty() {
            assert_eq!(d.reason, tc.reason, "{name}");
        }
        assert_eq!(fixture.merger.calls().len(), tc.merges, "{name}");
        if tc.missing && tc.class.is_some() {
            assert_eq!(p.tombstoned, ["file.txt"], "{name}");
            if tc.class == Some(Class::Managed) {
                assert_eq!(p.missing_managed, ["file.txt"], "{name}");
            }
        }
        let next_lock = fixture.next_lock(&lock, &p, &newer);
        if d.action == Action::Orphan {
            assert_eq!(next_lock.orphans, ["file.txt"], "{name}");
        }
        let entry = next_lock.files.get("file.txt");
        if matches!(d.action, Action::Delete | Action::Orphan)
            || tc.tombstone
            || (tc.missing && tc.class.is_some())
        {
            assert!(entry.is_none(), "{name}: retained removed entry");
        } else {
            let entry = entry.unwrap();
            assert!(
                entry.class == Class::Seed || entry.hash == hash(next.as_bytes()),
                "{name}: lock does not hold NEW"
            );
        }
    }
}

#[test]
fn plan_rename() {
    for (name, disk, next, action) in [
        ("unchanged", "old", "new", Action::Rename),
        ("identical destination", "old", "new", Action::Rename),
        ("edited clean", "user", "old", Action::Rename),
        ("edited conflict", "user", "new", Action::Conflict),
    ] {
        let root = TempDir::new().unwrap();
        write(root.path(), "file.txt", disk, 0o644);
        if name == "identical destination" {
            write(root.path(), "moved.txt", next, 0o644);
        }
        let lock = lock("old", Some(Class::Scaffold));
        let newer = version(
            "0.2.0",
            &[spec("moved.txt", next).from("file.txt").mode(0o755)],
        );
        let old = version("0.1.0", &[spec("file.txt", "old")]);
        let fixture = Fixture::default();
        let p = build_plan(&fixture.input(root.path(), &lock, Some(&old), &newer)).unwrap();
        assert_eq!(p.files.len(), 1, "{name}: {:?}", p.files);
        let d = &p.files[0];
        assert_eq!(d.action, action, "{name}: {d:?}");
        assert_eq!(d.from.as_deref(), Some("file.txt"), "{name}");
        assert_eq!(d.mode, 0o755, "{name}");
        let next_lock = fixture.next_lock(&lock, &p, &newer);
        assert_eq!(next_lock.files.len(), 1, "{name}");
        assert_eq!(
            next_lock.files["moved.txt"].hash,
            hash(next.as_bytes()),
            "{name}"
        );
    }
}

#[test]
fn plan_read_errors() {
    let outside = TempDir::new().unwrap();
    let mut kinds = vec!["escape", "directory", "merge", "cancel", "unclean"];
    if cfg!(unix) {
        kinds.push("symlink");
    }
    for kind in kinds {
        let root = TempDir::new().unwrap();
        let mut lock = lock("old", Some(Class::Managed));
        let mut fixture = Fixture::default();
        match kind {
            "escape" => {
                let entry = lock.files["file.txt"].clone();
                lock.files.insert("../escape".into(), entry);
            }
            "unclean" => {
                let entry = lock.files["file.txt"].clone();
                lock.files.insert("dir/./file.txt".into(), entry);
            }
            "symlink" => symlink_outside(root.path(), outside.path()),
            "directory" => std::fs::create_dir(root.path().join("file.txt")).unwrap(),
            "merge" => {
                write(root.path(), "file.txt", "user", 0o644);
                fixture.merger.fail = true;
            }
            "cancel" => fixture.cancel.cancel(),
            _ => unreachable!(),
        }
        let newer = version("0.2.0", &[spec("file.txt", "new")]);
        let err = build_plan(&fixture.input(root.path(), &lock, None, &newer)).unwrap_err();
        assert!(format!("{err:#}").contains("Try:"), "{kind}: {err:#}");
    }
}

#[cfg(unix)]
fn symlink_outside(root: &Path, outside: &Path) {
    write(outside, "file.txt", "outside", 0o644);
    std::os::unix::fs::symlink(outside.join("file.txt"), root.join("file.txt")).unwrap();
}

#[cfg(not(unix))]
fn symlink_outside(_root: &Path, _outside: &Path) {}

#[test]
fn plan_missing_root() {
    let lock = lock("", None);
    let newer = version("0.2.0", &[]);
    let fixture = Fixture::default();
    let missing = Path::new("/nonexistent/spinup-upgrade-root");
    let err = build_plan(&fixture.input(missing, &lock, None, &newer)).unwrap_err();
    assert!(format!("{err:#}").contains("Try: select a readable project directory"));
}

fn decision(path: &str, action: Action) -> FileDecision {
    FileDecision {
        path: path.into(),
        from: None,
        action,
        class: Class::Managed,
        template: String::new(),
        data: Vec::new(),
        mode: 0o644,
        reason: String::new(),
    }
}

struct Closed;

impl std::io::Write for Closed {
    fn write(&mut self, _buf: &[u8]) -> std::io::Result<usize> {
        Err(std::io::Error::other("closed"))
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn plan_format_and_sorting() {
    let with = |mut d: FileDecision, reason: &str, from: Option<&str>| {
        d.reason = reason.into();
        d.from = from.map(Into::into);
        d
    };
    let p = Plan {
        from: "0.1.0".into(),
        to: "0.2.0".into(),
        files: vec![
            decision("unchanged.txt", Action::Unchanged),
            with(decision("z.txt", Action::Conflict), NO_BASE, None),
            with(decision("keep.txt", Action::Keep), "seed", None),
            decision("orphan.txt", Action::Orphan),
            decision("delete.txt", Action::Delete),
            with(decision("rename.txt", Action::Rename), "", Some("old.txt")),
            decision("create.txt", Action::Create),
            decision("overwrite.txt", Action::Overwrite),
            decision("merge.txt", Action::Merge),
            decision("a.txt", Action::Conflict),
        ],
        ..Plan::default()
    };
    let mut out = Vec::new();
    p.format(&mut out).unwrap();
    let want = "Upgrade 0.1.0 -> 0.2.0\nconflict: 2\n  a.txt\n  z.txt (no base version)\nmerge: 1\n  merge.txt\noverwrite: 1\n  overwrite.txt\ncreate: 1\n  create.txt\nrename: 1\n  old.txt -> rename.txt\ndelete: 1\n  delete.txt\norphan: 1\n  orphan.txt\nkeep: 1\n  keep.txt (seed)\nunchanged: 1\n";
    assert_eq!(String::from_utf8(out).unwrap(), want);
    assert_eq!(p.conflicts(), ["a.txt", "z.txt"]);
    assert_eq!(p.summary()[&Action::Keep], ["keep.txt"]);
    assert_eq!(p.files[0].path, "unchanged.txt", "format changed the plan");
    let err = p.format(&mut Closed).unwrap_err();
    assert!(format!("{err:#}").contains("Try: use a writable output"));

    let root = TempDir::new().unwrap();
    let newer = version("0.2.0", &[spec("z", "z"), spec("a", "a")]);
    let lock = lock("", None);
    let fixture = Fixture::default();
    let built = build_plan(&fixture.input(root.path(), &lock, None, &newer)).unwrap();
    let paths: Vec<_> = built.files.iter().map(|f| f.path.as_str()).collect();
    assert_eq!(paths, ["a", "z"]);
}

#[test]
fn plan_next_lock_preserves_edits_across_steps() {
    let root = TempDir::new().unwrap();
    // The first step has no template change. The merger keeps the user's edit.
    write(root.path(), "file.txt", "user edit", 0o644);
    let lock = lock("old", Some(Class::Scaffold));
    let old = version("0.1.0", &[spec("file.txt", "old")]);
    let middle = version("0.2.0", &[spec("file.txt", "old")]);
    let fixture = Fixture::default();
    let first = build_plan(&fixture.input(root.path(), &lock, Some(&old), &middle)).unwrap();
    assert_eq!(first.files[0].action, Action::Merge);
    assert_eq!(first.files[0].data, b"user edit\n");
    let next = fixture.next_lock(&lock, &first, &middle);
    assert_eq!(
        next.files["file.txt"].hash,
        hash(b"old"),
        "lock recorded merged content"
    );
    write(
        root.path(),
        "file.txt",
        &first.files[0].data,
        first.files[0].mode,
    );
    let newest = version("0.3.0", &[spec("file.txt", "newest")]);
    let second = build_plan(&fixture.input(root.path(), &next, Some(&middle), &newest)).unwrap();
    assert_eq!(
        second.files[0].action,
        Action::Conflict,
        "the second step would overwrite the user's edit"
    );
    assert_eq!(lock.template_version, "0.1.0");
    assert_eq!(
        lock.files["file.txt"].hash,
        hash(b"old"),
        "old lock changed"
    );
}

#[test]
fn plan_old_render_missing_path() {
    let root = TempDir::new().unwrap();
    write(root.path(), "file.txt", "user\r\n", 0o644);
    let lock = lock("old", Some(Class::Scaffold));
    let old = version("0.1.0", &[]);
    let newer = version("0.2.0", &[spec("file.txt", "new")]);
    let fixture = Fixture::default();
    let p = build_plan(&fixture.input(root.path(), &lock, Some(&old), &newer)).unwrap();
    assert_eq!(p.files[0].reason, NO_BASE);
    let calls = fixture.merger.calls();
    assert!(calls[0].0.is_empty());
    assert_eq!(calls[0].1, b"user\n");
}

#[test]
fn plan_does_not_write() {
    let root = TempDir::new().unwrap();
    write(root.path(), "file.txt", "user", 0o644);
    write(root.path(), "untracked.txt", "untouched", 0o600);
    let lock = lock("old", Some(Class::Managed));
    let before = lock.marshal().unwrap();
    let old = version("0.1.0", &[spec("file.txt", "old")]);
    let newer = version("0.2.0", &[spec("file.txt", "new")]);
    let fixture = Fixture::default();
    build_plan(&fixture.input(root.path(), &lock, Some(&old), &newer)).unwrap();
    assert_eq!(
        lock.marshal().unwrap(),
        before,
        "the planner changed the lock"
    );
    for (name, want) in [("file.txt", "user"), ("untracked.txt", "untouched")] {
        assert_eq!(
            std::fs::read_to_string(root.path().join(name)).unwrap(),
            want
        );
    }
    assert_eq!(std::fs::read_dir(root.path()).unwrap().count(), 2);
}

#[test]
fn plan_rename_cannot_merge_binary() {
    for (kind, disk) in [
        ("binary", "user\0".to_string()),
        ("large", "user".repeat(MAX_SIZE)),
    ] {
        let root = TempDir::new().unwrap();
        write(root.path(), "file.txt", &disk, 0o644);
        let lock = lock("old", Some(Class::Managed));
        let old = version("0.1.0", &[spec("file.txt", "old")]);
        let newer = version("0.2.0", &[spec("moved.txt", "new").from("file.txt")]);
        let fixture = Fixture::default();
        let err = build_plan(&fixture.input(root.path(), &lock, Some(&old), &newer)).unwrap_err();
        assert!(format!("{err:#}").contains("Try:"), "{kind}: {err:#}");
    }
}

#[test]
fn plan_next_lock_snapshot() {
    let mut old = lock("old", Some(Class::Scaffold));
    old.behavior_version = 3;
    old.kit.crate_name = "example.com/kit".into();
    old.kit.version = "v0.1.0".into();
    old.orphans = vec!["adopted.txt".into(), "previous.txt".into()];
    old.tombstones = vec!["gone.txt".into()];
    old.applied_migrations = vec!["0001".into()];
    old.formatter = "go1.27.2".into();
    let stack = serde_json::json!({"images": {
        "second": {"image": "second", "tag": "2", "digest": "sha256:two"},
        "first": {"image": "first", "tag": "1", "digest": "sha256:one"},
    }});
    let newer = version_with_stack(
        "0.2.0",
        &[
            spec("file.txt", "new").mode(0o755),
            spec("adopted.txt", "adopted"),
            spec("seed.txt", "seed").class("seed"),
        ],
        stack,
    );
    let root = TempDir::new().unwrap();
    let mut fixture = Fixture::default();
    fixture
        .answers
        .insert("nested".into(), serde_json::json!({"items": ["original"]}));
    let p = build_plan(&fixture.input(root.path(), &old, None, &newer)).unwrap();
    let next = fixture.next_lock(&old, &p, &newer);
    next.validate().unwrap();
    assert_eq!(next.template_version, "0.2.0");
    assert_eq!(next.behavior_version, 3);
    assert_eq!(next.kit, old.kit);
    assert_eq!(next.packs, old.packs);
    assert_eq!(next.applied_migrations, old.applied_migrations);
    assert_eq!(next.engine, "minijinja");
    assert_eq!(next.formatter, "", "R1 formats no file");
    assert_eq!(
        next.stack_versions.iter().collect::<Vec<_>>(),
        [
            (&"first".to_string(), &"first:1@sha256:one".to_string()),
            (&"second".to_string(), &"second:2@sha256:two".to_string()),
        ]
    );
    assert_eq!(next.orphans, ["previous.txt"]);
    assert_eq!(next.tombstones, ["file.txt", "gone.txt"]);
    assert_eq!(
        next.files["seed.txt"],
        FileEntry {
            class: Class::Seed,
            template: String::new(),
            hash: String::new(),
            mode: String::new(),
        }
    );
    assert_eq!(next.files["adopted.txt"].mode, "0644");
    assert_eq!(next.answers["nested"].get(), r#"{"items":["original"]}"#);
    assert_eq!(
        old.tombstones,
        ["gone.txt"],
        "next lock changed the old lock"
    );
}

#[test]
fn plan_rename_occupied_destination() {
    for destination in ["aaa.txt", "moved.txt"] {
        let root = TempDir::new().unwrap();
        write(root.path(), "file.txt", "old", 0o644);
        write(root.path(), destination, "USER WORK", 0o600);
        let lock = lock("old", Some(Class::Scaffold));
        let newer = version("0.2.0", &[spec(destination, "new").from("file.txt")]);
        let fixture = Fixture::default();
        let p = build_plan(&fixture.input(root.path(), &lock, None, &newer)).unwrap();
        let conflict = p.files.iter().find(|d| d.path == destination).unwrap();
        assert_eq!(conflict.action, Action::Conflict, "{destination}");
        assert_eq!(conflict.reason, "path exists", "{destination}");
        assert_eq!(conflict.from, None, "{destination}");
        assert!(String::from_utf8_lossy(&conflict.data).contains("USER WORK"));
        assert!(String::from_utf8_lossy(&conflict.data).contains("<<<<<<<"));
        let source = p.files.iter().find(|d| d.path == "file.txt").unwrap();
        assert_eq!(source.action, Action::Orphan, "{destination}");
        assert_eq!(source.reason, "rename destination exists");
        let calls = fixture.merger.calls();
        assert_eq!(calls.len(), 1, "{destination}");
        assert!(calls[0].0.is_empty());
        assert_eq!(calls[0].1, b"USER WORK\n");
        let next = fixture.next_lock(&lock, &p, &newer);
        assert_eq!(next.orphans, ["file.txt"], "{destination}");
        assert_eq!(next.files[destination].hash, hash(b"new"), "{destination}");
    }
}

#[test]
fn plan_ejected_class() {
    for force in [false, true] {
        let root = TempDir::new().unwrap();
        write(root.path(), "file.txt", "user", 0o644);
        let lock = lock("old", Some(Class::Scaffold));
        let old = version("0.1.0", &[spec("file.txt", "old").class("managed")]);
        let newer = version("0.2.0", &[spec("file.txt", "new").class("managed")]);
        let fixture = Fixture {
            force_managed: force,
            ..Fixture::default()
        };
        let p = build_plan(&fixture.input(root.path(), &lock, Some(&old), &newer)).unwrap();
        assert_eq!(p.files[0].action, Action::Conflict, "force={force}");
        assert_eq!(p.files[0].class, Class::Scaffold, "force={force}");
        let next = fixture.next_lock(&lock, &p, &newer);
        assert_eq!(
            next.files["file.txt"].class,
            Class::Scaffold,
            "force={force}"
        );
    }
}

#[test]
fn plan_class_transitions() {
    let classes = [Class::Managed, Class::Scaffold, Class::Seed];
    for old_class in classes {
        for new_class in classes {
            let root = TempDir::new().unwrap();
            write(root.path(), "file.txt", "user", 0o644);
            let lock = lock("old", Some(old_class));
            let newer = version(
                "0.2.0",
                &[spec("file.txt", "new").class(class_name(new_class))],
            );
            let fixture = Fixture {
                force_managed: true,
                ..Fixture::default()
            };
            let p = build_plan(&fixture.input(root.path(), &lock, None, &newer)).unwrap();
            let want_class = if old_class == Class::Scaffold && new_class == Class::Managed {
                Class::Scaffold
            } else {
                new_class
            };
            let want_action = match want_class {
                Class::Managed => Action::Overwrite,
                Class::Seed => Action::Keep,
                Class::Scaffold => Action::Conflict,
            };
            let label = format!("{old_class:?} to {new_class:?}");
            assert_eq!(p.files[0].class, want_class, "{label}");
            assert_eq!(p.files[0].action, want_action, "{label}");
            let next = fixture.next_lock(&lock, &p, &newer);
            assert_eq!(next.files["file.txt"].class, want_class, "{label}");
        }
    }
}

#[test]
fn plan_rename_ejected_class() {
    let root = TempDir::new().unwrap();
    write(root.path(), "file.txt", "old", 0o644);
    let lock = lock("old", Some(Class::Scaffold));
    let newer = version(
        "0.2.0",
        &[spec("moved.txt", "new").from("file.txt").class("managed")],
    );
    let fixture = Fixture {
        force_managed: true,
        ..Fixture::default()
    };
    let p = build_plan(&fixture.input(root.path(), &lock, None, &newer)).unwrap();
    assert_eq!(p.files[0].class, Class::Scaffold);
    assert_eq!(p.files[0].action, Action::Rename);
    let next = fixture.next_lock(&lock, &p, &newer);
    assert_eq!(next.files["moved.txt"].class, Class::Scaffold);
}

#[test]
fn plan_keeps_generator_owned_files() {
    let root = TempDir::new().unwrap();
    write(root.path(), "file.txt", "generated routes", 0o644);
    write(root.path(), "queries.sql", "generated queries", 0o644);
    let mut lock = lock("generated routes", Some(Class::Managed));
    let generated = |class, data: &str| FileEntry {
        class,
        template: "gen/resource".into(),
        hash: hash(data.as_bytes()),
        mode: "0644".into(),
    };
    lock.files.insert(
        "file.txt".into(),
        generated(Class::Managed, "generated routes"),
    );
    lock.files.insert(
        "queries.sql".into(),
        generated(Class::Scaffold, "generated queries"),
    );
    // The template renders an empty file.txt and knows nothing about queries.sql.
    let old = version("0.1.0", &[spec("file.txt", "empty").class("managed")]);
    let newer = version("0.2.0", &[spec("file.txt", "empty").class("managed")]);
    let fixture = Fixture::default();
    let p = build_plan(&fixture.input(root.path(), &lock, Some(&old), &newer)).unwrap();
    for d in &p.files {
        assert_eq!(d.action, Action::Keep, "{}", d.path);
        assert_eq!(d.reason, GENERATOR_OWNED, "{}", d.path);
        assert_eq!(d.template, "gen/resource", "{}", d.path);
    }
    let next = fixture.next_lock(&lock, &p, &newer);
    for name in ["file.txt", "queries.sql"] {
        assert_eq!(next.files[name], lock.files[name], "{name}");
    }
}

#[test]
fn plan_generator_template_ids() {
    for good in [
        "gen/resource",
        "plugin/x/@gen/greeting",
        "plugin/x/@exec/run",
        "plugin/x/gen/y",
    ] {
        assert!(generator_template(good), "{good}");
    }
    for bad in [
        "base/gen/x",
        "plugin/x/files/y",
        "plugin/x/@gen",
        "generator",
    ] {
        assert!(!generator_template(bad), "{bad}");
    }
}

#[test]
fn plan_keeps_go_module_files() {
    let root = TempDir::new().unwrap();
    write(
        root.path(),
        "api/go.mod",
        "module app\nrequire x v1.1.0\n",
        0o644,
    );
    let mut lock = lock("", None);
    lock.files.insert(
        "api/go.mod".into(),
        FileEntry {
            class: Class::Managed,
            template: "base/api/go.mod.tmpl".into(),
            hash: hash(b"module app\nrequire x v1.0.0\n"),
            mode: "0644".into(),
        },
    );
    let old = version(
        "0.1.0",
        &[spec("api/go.mod", "module app\nrequire x v1.0.0\n").class("managed")],
    );
    let newer = version(
        "0.2.0",
        &[spec("api/go.mod", "module app\nrequire x v2.0.0\n").class("managed")],
    );
    let fixture = Fixture::default();
    let p = build_plan(&fixture.input(root.path(), &lock, Some(&old), &newer)).unwrap();
    let d = &p.files[0];
    assert_eq!(d.action, Action::Keep);
    assert_eq!(d.reason, GO_MOD_KEEP_REASON);
    assert_eq!(
        d.data, b"module app\nrequire x v2.0.0\n",
        "the NEW render stays"
    );
    assert!(fixture.merger.calls().is_empty());
}

#[test]
fn plan_require_base() {
    let mut plan = Plan {
        from: "0.1.0".into(),
        to: "0.2.0".into(),
        ..Plan::default()
    };
    plan.files.push(decision("ok.txt", Action::Merge));
    require_base(&plan, false, "0.1.0").unwrap();
    let mut conflict = decision("b.txt", Action::Conflict);
    conflict.reason = NO_BASE.into();
    plan.files.push(conflict.clone());
    conflict.path = "a.txt".into();
    plan.files.push(conflict);
    require_base(&plan, true, "0.1.0").unwrap();
    let err = require_base(&plan, false, "0.1.0").unwrap_err();
    let text = format!("{err:#}");
    assert!(
        text.contains("2 changed files have no merge base: b.txt, a.txt"),
        "{text}"
    );
    assert!(text.contains("Try: run spinup cache warm 0.1.0"), "{text}");
}

#[test]
fn plan_rejects_bad_renames() {
    let root = TempDir::new().unwrap();
    write(root.path(), "file.txt", "old", 0o644);
    let lock = lock("old", Some(Class::Scaffold));
    let fixture = Fixture::default();
    // Two destinations move the same source.
    let twice = version(
        "0.2.0",
        &[
            spec("a.txt", "new").from("file.txt"),
            spec("b.txt", "new").from("file.txt"),
        ],
    );
    let err = build_plan(&fixture.input(root.path(), &lock, None, &twice)).unwrap_err();
    assert!(format!("{err:#}").contains("Try: give each rename a unique source"));
    // The source is still rendered.
    let still = version(
        "0.2.0",
        &[
            spec("a.txt", "new").from("file.txt"),
            spec("file.txt", "old"),
        ],
    );
    let err = build_plan(&fixture.input(root.path(), &lock, None, &still)).unwrap_err();
    assert!(format!("{err:#}").contains("Try: remove the source from the new templates"));
}

#[test]
fn plan_missing_pack_names_pack_and_version() {
    let root = TempDir::new().unwrap();
    let mut lock = lock("", None);
    lock.packs = vec!["missing".into()];
    let newer = version("0.2.0", &[]);
    let fixture = Fixture::default();
    let err = build_plan(&fixture.input(root.path(), &lock, None, &newer)).unwrap_err();
    let text = format!("{err:#}");
    assert!(
        text.contains("\"missing\"") && text.contains("0.2.0") && text.contains("Try:"),
        "{text}"
    );
}
