use super::*;
use crate::plan::testing::{FakeMerger, FakeRenderer, lock, spec, version, write};
use spinup_exec::os_runner::OsRunner;
use spinup_manifest::lock::Class;
use spinup_tplstore::source::SourceError;
use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Arc;

struct Versions(BTreeMap<String, Arc<Version>>);
impl Source for Versions {
    fn versions(&self) -> std::result::Result<Vec<String>, SourceError> {
        Ok(self.0.keys().cloned().collect())
    }
    fn load(&self, name: &str) -> std::result::Result<Arc<Version>, SourceError> {
        self.0
            .get(name)
            .cloned()
            .ok_or_else(|| SourceError::Unavailable {
                version: name.into(),
                reason: "not cached".into(),
                hint: "restore the template version".into(),
            })
    }
}
fn git(root: &Path, args: &[&str]) -> String {
    let cmd = spinup_exec::runner::Cmd::new("git")
        .args(args.iter().copied())
        .dir(root)
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .env("GIT_AUTHOR_NAME", "Spinup Test")
        .env("GIT_AUTHOR_EMAIL", "test@example.com")
        .env("GIT_COMMITTER_NAME", "Spinup Test")
        .env("GIT_COMMITTER_EMAIL", "test@example.com");
    String::from_utf8(OsRunner.output(&cmd, &Cancellation::new()).unwrap().stdout).unwrap()
}
fn fixture(edit: bool) -> (tempfile::TempDir, Versions) {
    let dir = tempfile::tempdir().unwrap();
    let root = dir.path();
    git(root, &["init"]);
    // Windows runners set core.autocrlf=true globally: keep the test repository byte-exact.
    git(root, &["config", "core.autocrlf", "false"]);
    git(root, &["config", "user.name", "Spinup Test"]);
    git(root, &["config", "user.email", "test@example.com"]);
    write(root, ".gitignore", ".spinup/\n", 0o644);
    write(root, "file.txt", "old\n", 0o644);
    lock("old\n", Some(Class::Scaffold))
        .save(root.join("spinup.lock"))
        .unwrap();
    git(root, &["add", "."]);
    git(root, &["commit", "-m", "initial"]);
    if edit {
        write(root, "file.txt", "user\n", 0o644);
        git(root, &["commit", "-am", "user edit"]);
    }
    let source = Versions(
        [("0.1.0", "old\n"), ("0.2.0", "new\n")]
            .into_iter()
            .map(|(v, data)| (v.into(), Arc::new(version(v, &[spec("file.txt", data)]))))
            .collect(),
    );
    (dir, source)
}

#[test]
fn run_worktree_preserves_main_and_commits_lock() {
    let (dir, source) = fixture(false);
    let mut output = Vec::new();
    let cancel = Cancellation::new();
    let merger = FakeMerger::default();
    let mut runner = Runner::new(
        dir.path(),
        &source,
        &OsRunner,
        &merger,
        &cancel,
        &mut output,
    );
    runner.renderer = &FakeRenderer;
    runner
        .run(Options {
            to: Some("0.2.0".into()),
            yes: true,
            ..Options::default()
        })
        .unwrap();
    assert_eq!(
        std::fs::read(dir.path().join("file.txt")).unwrap(),
        b"old\n"
    );
    assert_eq!(git(dir.path(), &["status", "--porcelain"]), "");
    let work = dir.path().join(".spinup/upgrade/0.2.0");
    assert_eq!(
        Lock::load(work.join("spinup.lock"))
            .unwrap()
            .template_version,
        "0.2.0"
    );
    assert_eq!(git(&work, &["status", "--porcelain"]), "");
    assert!(git(&work, &["log", "--format=%s"]).contains("spinup upgrade: lock 0.2.0"));
}

#[test]
fn run_conflicts_survive_restart_continue_and_abort() {
    for abort in [false, true] {
        let (dir, source) = fixture(true);
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        let mut runner = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        runner.renderer = &FakeRenderer;
        let error = runner
            .run(Options {
                to: Some("0.2.0".into()),
                yes: true,
                ..Options::default()
            })
            .unwrap_err();
        assert_eq!(spinup_ui::exit::exit_code(&error), 3);
        drop(runner);
        let mut resumed = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        resumed.renderer = &FakeRenderer;
        let error = resumed
            .continue_upgrade(Options {
                yes: true,
                ..Options::default()
            })
            .unwrap_err();
        assert_eq!(spinup_ui::exit::exit_code(&error), 3);
        let work = dir.path().join(".spinup/upgrade/0.2.0");
        if abort {
            resumed
                .abort(Options {
                    yes: true,
                    ..Options::default()
                })
                .unwrap();
            assert!(!work.exists());
        } else {
            write(&work, "file.txt", "resolved\n", 0o644);
            resumed
                .continue_upgrade(Options {
                    yes: true,
                    ..Options::default()
                })
                .unwrap();
            assert_eq!(std::fs::read(work.join("file.txt")).unwrap(), b"resolved\n");
        }
        assert!(!dir.path().join(".spinup/upgrade.json").exists());
    }
}

#[test]
fn run_plan_chain_is_stable_and_ignores_unrelated_files() {
    let (dir, mut source) = fixture(false);
    source.0.insert(
        "0.3.0".into(),
        Arc::new(version("0.3.0", &[spec("file.txt", "third\n")])),
    );
    Arc::make_mut(source.0.get_mut("0.2.0").unwrap()).kit_migration = Some(Arc::from(&br#"{"schema":1,"version":"0.2.0","steps":[{"id":"kit-note","desc":"Review this change","kind":"manual"}],"behaviors":[{"version":2,"desc":"Stricter cookies"}]}"#[..]));
    #[cfg(unix)]
    std::os::unix::fs::symlink("/missing/unrelated", dir.path().join("unrelated-link")).unwrap();
    git(dir.path(), &["add", "."]);
    git(dir.path(), &["commit", "--allow-empty", "-m", "unrelated"]);
    let mut output = Vec::new();
    let cancel = Cancellation::new();
    let merger = FakeMerger::default();
    let mut runner = Runner::new(
        dir.path(),
        &source,
        &OsRunner,
        &merger,
        &cancel,
        &mut output,
    );
    runner.renderer = &FakeRenderer;
    let policy = Options {
        to: Some("0.3.0".into()),
        yes: true,
        ..Options::default()
    };
    let report = runner.plan(policy.clone()).unwrap();
    assert_eq!(report.steps.len(), 2);
    assert_eq!(
        report.steps[1].files[0].action,
        crate::contract::Action::Overwrite
    );
    // The preview prints notes without copying application content into its report.
    let first = std::fs::read(dir.path().join(".spinup/plan.json")).unwrap();
    assert!(!String::from_utf8_lossy(&first).contains("third"));
    runner.plan(policy.clone()).unwrap();
    assert_eq!(
        std::fs::read(dir.path().join(".spinup/plan.json")).unwrap(),
        first
    );
    assert_eq!(git(dir.path(), &["status", "--porcelain"]), "");
    runner.run(policy).unwrap();
    let work = dir.path().join(".spinup/upgrade/0.3.0");
    assert_eq!(
        Lock::load(work.join("spinup.lock"))
            .unwrap()
            .template_version,
        "0.3.0"
    );
    assert!(git(&work, &["log", "--format=%s"]).contains("0.2.0 -> 0.3.0"));
    let text = String::from_utf8(output).unwrap();
    assert!(text.contains("Kit kit-note [manual]: Review this change"));
    assert!(text.contains(
        "Kit behavior 2 (opt in with behavior_version = 2 in spinup.toml): Stricter cookies"
    ));
}

#[test]
fn run_preconditions_and_allow_dirty_use_head() {
    for (dirty, allow, yes, want) in [
        (false, false, false, "--yes"),
        (true, false, true, "--allow-dirty"),
        (true, true, true, ""),
    ] {
        let (dir, source) = fixture(false);
        if dirty {
            write(dir.path(), "file.txt", "uncommitted\n", 0o644);
        }
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        let mut runner = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        runner.renderer = &FakeRenderer;
        let result = runner.run(Options {
            to: Some("0.2.0".into()),
            yes,
            allow_dirty: allow,
            ..Options::default()
        });
        if want.is_empty() {
            result.unwrap();
            assert_eq!(
                std::fs::read(dir.path().join("file.txt")).unwrap(),
                b"uncommitted\n"
            );
            assert_eq!(
                std::fs::read(dir.path().join(".spinup/upgrade/0.2.0/file.txt")).unwrap(),
                b"new\n"
            );
        } else {
            assert!(format!("{:#}", result.unwrap_err()).contains(want));
        }
        assert!(!dir.path().join(".spinup/upgrade.json").exists());
    }
}

#[test]
fn run_missing_base_stops_before_creating_worktree() {
    for edited in [false, true] {
        let (dir, mut source) = fixture(edited);
        source.0.remove("0.1.0");
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        let mut runner = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        runner.renderer = &FakeRenderer;
        let result = runner.run(Options {
            to: Some("0.2.0".into()),
            yes: true,
            ..Options::default()
        });
        if edited {
            assert!(format!("{:#}", result.unwrap_err()).contains("no merge base"));
            assert!(!dir.path().join(".spinup/upgrade/0.2.0").exists());
        } else {
            result.unwrap();
        }
    }
}

#[test]
fn run_commit_conflicts_warns_across_chain() {
    let (dir, mut source) = fixture(true);
    source
        .0
        .insert("0.3.0".into(), Arc::new(version("0.3.0", &[])));
    let mut output = Vec::new();
    let cancel = Cancellation::new();
    let merger = FakeMerger::default();
    let mut runner = Runner::new(
        dir.path(),
        &source,
        &OsRunner,
        &merger,
        &cancel,
        &mut output,
    );
    runner.renderer = &FakeRenderer;
    runner
        .run(Options {
            to: Some("0.3.0".into()),
            yes: true,
            commit_conflicts: true,
            ..Options::default()
        })
        .unwrap();
    let output = String::from_utf8(output).unwrap();
    assert!(output.contains("Warning: committed conflict markers in: file.txt"));
    assert!(!output.contains("Next: git merge"));
}

#[test]
fn run_continue_restarts_interrupted_apply_without_nested_markers() {
    fn interrupted(step: &mut Step<'_>) -> Result<()> {
        crate::state::write_json(&step.worktree, "partial/output.txt", b"partial".to_vec())?;
        bail!("interrupted apply");
    }
    for deps in [false, true] {
        let (dir, source) = fixture(true);
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        let mut runner = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        runner.renderer = &FakeRenderer;
        runner.phases = Some(if deps {
            Phases {
                deps: Some(interrupted),
                ..Phases::default()
            }
        } else {
            Phases {
                apply: Some(interrupted),
                ..Phases::default()
            }
        });
        let err = runner
            .run(Options {
                to: Some("0.2.0".into()),
                yes: true,
                ..Options::default()
            })
            .unwrap_err();
        assert!(format!("{err:#}").contains("interrupted apply"));
        let state = State::load(&runner.root).unwrap();
        assert_eq!(state.status, Status::Step);
        write(&state.worktree, ".spinup/keep.txt", "keep\n", 0o644);
        write(dir.path(), "main-only.txt", "user data\n", 0o644);
        runner.phases = None;
        assert_eq!(
            spinup_ui::exit::exit_code(
                &runner
                    .continue_upgrade(Options {
                        yes: true,
                        ..Options::default()
                    })
                    .unwrap_err()
            ),
            3
        );
        let data = std::fs::read_to_string(state.worktree.join("file.txt")).unwrap();
        assert_eq!(data.matches("<<<<<<<").count(), 1);
        assert!(!state.worktree.join("partial").exists());
        assert!(state.worktree.join(".spinup/keep.txt").exists());
        assert!(dir.path().join("main-only.txt").exists());
    }
}

#[test]
fn run_phase_order_and_verification_recovery() {
    fn before(s: &mut Step<'_>) -> Result<()> {
        s.lock.applied_migrations.push("before".into());
        Ok(())
    }
    fn apply(s: &mut Step<'_>) -> Result<()> {
        s.lock.applied_migrations.push("apply".into());
        Ok(())
    }
    fn deps(s: &mut Step<'_>) -> Result<()> {
        s.lock.applied_migrations.push("deps".into());
        Ok(())
    }
    fn fix(s: &mut Step<'_>) -> Result<bool> {
        s.lock.applied_migrations.push("fix".into());
        crate::state::write_json(&s.worktree, "fix.txt", b"fixed".to_vec())?;
        Ok(true)
    }
    fn after(s: &mut Step<'_>) -> Result<()> {
        s.lock.applied_migrations.push("after".into());
        Ok(())
    }
    fn verify(s: &mut Step<'_>) -> Result<()> {
        assert_eq!(
            s.lock.applied_migrations,
            ["before", "apply", "deps", "fix", "after"]
        );
        bail!("verification failed");
    }
    let (dir, source) = fixture(false);
    let mut output = Vec::new();
    let cancel = Cancellation::new();
    let merger = FakeMerger::default();
    let mut runner = Runner::new(
        dir.path(),
        &source,
        &OsRunner,
        &merger,
        &cancel,
        &mut output,
    );
    runner.renderer = &FakeRenderer;
    runner.phases = Some(Phases {
        before: Some(before),
        apply: Some(apply),
        deps: Some(deps),
        fix: Some(fix),
        after: Some(after),
        verify: Some(verify),
    });
    let err = runner
        .run(Options {
            to: Some("0.2.0".into()),
            yes: true,
            ..Options::default()
        })
        .unwrap_err();
    assert!(format!("{err:#}").contains("verification failed"));
    assert_eq!(State::load(&runner.root).unwrap().status, Status::Verify);
    runner
        .continue_upgrade(Options {
            yes: true,
            no_verify: true,
            ..Options::default()
        })
        .unwrap();
    let work = dir.path().join(".spinup/upgrade/0.2.0");
    assert!(git(&work, &["log", "--format=%s"]).contains("spinup upgrade: cargo fix"));
}

#[test]
fn run_resolved_content_reaches_phases_even_after_deletion() {
    fn check(s: &mut Step<'_>) -> Result<bool> {
        let file = &s.plan.as_ref().unwrap().files[0];
        assert_ne!(file.action, crate::contract::Action::Conflict);
        if file.action == crate::contract::Action::Merge {
            assert_eq!(file.data, b"Heading\n=======\n");
        } else {
            assert_eq!(file.action, crate::contract::Action::Delete);
        }
        Ok(false)
    }
    for deleted in [false, true] {
        let (dir, source) = fixture(true);
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        let mut runner = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        runner.renderer = &FakeRenderer;
        assert!(
            runner
                .run(Options {
                    to: Some("0.2.0".into()),
                    yes: true,
                    ..Options::default()
                })
                .is_err()
        );
        let work = dir.path().join(".spinup/upgrade/0.2.0");
        if deleted {
            std::fs::remove_file(work.join("file.txt")).unwrap();
        } else {
            write(&work, "file.txt", "Heading\n=======\n", 0o644);
        }
        runner.phases = Some(Phases {
            fix: Some(check),
            ..Phases::default()
        });
        runner
            .continue_upgrade(Options {
                yes: true,
                ..Options::default()
            })
            .unwrap();
    }
}

#[test]
fn worktree_abort_missing_worktree_and_branch() {
    for branch_gone in [false, true] {
        let (dir, source) = fixture(true);
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        let mut runner = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        runner.renderer = &FakeRenderer;
        assert!(
            runner
                .run(Options {
                    to: Some("0.2.0".into()),
                    yes: true,
                    ..Options::default()
                })
                .is_err()
        );
        std::fs::remove_dir_all(dir.path().join(".spinup/upgrade/0.2.0")).unwrap();
        if branch_gone {
            git(dir.path(), &["worktree", "prune"]);
            git(dir.path(), &["branch", "-D", "spinup/upgrade-0.2.0"]);
        }
        runner
            .abort(Options {
                yes: true,
                ..Options::default()
            })
            .unwrap();
        assert_eq!(
            git(dir.path(), &["branch", "--list", "spinup/upgrade-0.2.0"]),
            ""
        );
    }
}

#[test]
fn worktree_storage_only_allows_trust_files() {
    let (dir, _) = fixture(false);
    let cancel = Cancellation::new();
    let operations = Git {
        runner: &OsRunner,
        cancel: &cancel,
    };
    for (tracked, visible_worktree, valid) in [
        (false, false, true),
        (true, false, false),
        (false, true, false),
    ] {
        let ignore = if visible_worktree {
            ".spinup/*\n!.spinup/plugins.allow\n!.spinup/plugins.history\n!.spinup/upgrade/\n"
        } else {
            ".spinup/*\n!.spinup/plugins.allow\n!.spinup/plugins.history\n"
        };
        write(dir.path(), ".gitignore", ignore, 0o644);
        for file in [".spinup/plugins.allow", ".spinup/plugins.history"] {
            write(dir.path(), file, "trust\n", 0o644);
            git(dir.path(), &["add", file]);
        }
        if tracked {
            write(dir.path(), ".spinup/upgrade.json", "state\n", 0o600);
            git(dir.path(), &["add", "-f", ".spinup/upgrade.json"]);
        }
        assert_eq!(operations.check_ignored(dir.path()).is_ok(), valid);
        if tracked {
            git(dir.path(), &["rm", "--cached", ".spinup/upgrade.json"]);
        }
    }
}

#[test]
fn run_write_plan_preserves_kept_bytes_modes_and_occupied_rename() {
    use crate::contract::{Action, FileDecision, Plan};
    let dir = tempfile::tempdir().unwrap();
    for (name, data) in [
        ("old.txt", "old"),
        ("new.txt", "occupied"),
        ("delete.txt", "delete"),
        ("keep.txt", "user\r\n"),
    ] {
        write(dir.path(), name, data, 0o600);
    }
    let decision = |path: &str, action, from, data: &[u8]| FileDecision {
        path: path.into(),
        from,
        action,
        class: Class::Scaffold,
        template: String::new(),
        data: data.to_vec(),
        mode: 0o755,
        reason: String::new(),
    };
    write_plan(
        dir.path(),
        &Plan {
            files: vec![
                decision(
                    "new.txt",
                    Action::Conflict,
                    Some("old.txt".into()),
                    b"merged",
                ),
                decision("delete.txt", Action::Delete, None, b""),
                decision("keep.txt", Action::Keep, None, b"wrong"),
            ],
            ..Plan::default()
        },
    )
    .unwrap();
    assert!(!dir.path().join("old.txt").exists());
    assert!(!dir.path().join("delete.txt").exists());
    assert_eq!(
        std::fs::read(dir.path().join("new.txt")).unwrap(),
        b"merged"
    );
    assert_eq!(
        std::fs::read(dir.path().join("keep.txt")).unwrap(),
        b"user\r\n"
    );
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(dir.path().join("keep.txt"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o755
        );
    }
    for path in ["../escape", ".git/config", ".spinup/upgrade.json"] {
        assert!(
            write_plan(
                dir.path(),
                &Plan {
                    files: vec![decision(path, Action::Create, None, b"bad")],
                    ..Plan::default()
                }
            )
            .is_err()
        );
    }
}

#[test]
fn run_state_validation_and_conflict_markers() {
    let (dir, _) = fixture(false);
    let root = std::fs::canonicalize(dir.path()).unwrap();
    let base = State {
        schema: 1,
        chain: vec!["0.2.0".into()],
        index: 0,
        worktree: root.join(".spinup/upgrade/0.2.0"),
        branch: "spinup/upgrade-0.2.0".into(),
        status: Status::Step,
        lock: lock("old", Some(Class::Scaffold)),
        plan: None,
        options: Options::default(),
        module: None,
        conflicts: vec![],
        committed_conflicts: vec![],
        ui: false,
    };
    base.validate(&root).unwrap();
    let mutations: [fn(&mut State); 6] = [
        |s| s.index = 3,
        |s| s.worktree = PathBuf::from("/tmp"),
        |s| s.branch = "main".into(),
        |s| s.conflicts = vec!["../escape".into()],
        |s| s.committed_conflicts = vec![".git/config".into()],
        |s| s.status = Status::After,
    ];
    for mutate in mutations {
        let mut state = base.clone();
        mutate(&mut state);
        assert!(state.validate(&root).is_err());
    }
    for (data, conflicted) in [
        (
            "<<<<<<< ours\nours\n||||||| base\nbase\n=======\ntheirs\n>>>>>>> theirs\n",
            true,
        ),
        ("Heading\n=======\n", false),
        ("<<<<<<< ours\n", false),
        (">>>>>>> theirs\n<<<<<<< ours\n", false),
    ] {
        write(&root, "conflict.txt", data, 0o644);
        assert_eq!(
            !state::unresolved(&root, &["conflict.txt".into()])
                .unwrap()
                .is_empty(),
            conflicted
        );
    }
    assert!(
        state::unresolved(&root, &["deleted.txt".into()])
            .unwrap()
            .is_empty()
    );
    base.save(&root).unwrap();
    let loaded = State::load(&root).unwrap();
    assert_eq!(loaded.chain, base.chain);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(root.join(".spinup/upgrade.json"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
    }
}

#[test]
fn run_ui_choice_survives_interrupted_step_and_extension_conflicts() {
    for phase in ["conflicts", "deps", "after-conflicts"] {
        fn interrupted(_: &mut Step<'_>) -> Result<()> {
            bail!("interrupted dependencies");
        }
        let (dir, mut source) = fixture(phase == "conflicts");
        let button = "web/src/lib/components/ui/button/button.svelte";
        for (name, registry, content) in [("0.1.0", "1.7.0", "old\n"), ("0.2.0", "1.8.0", "new\n")]
        {
            let tree = Arc::make_mut(&mut Arc::make_mut(source.0.get_mut(name).unwrap()).packs);
            tree.insert("web/pack.toml", format!("name='web'\n[[questions]]\nkey='shadcn_registry'\nprompt='Registry'\ntype='string'\ndefault='{registry}'\n[[files]]\npath='{button}'\nsource='button.svelte'\nclass='seed'\n").into_bytes(), 0o644).unwrap();
            tree.insert("web/button.svelte", content.as_bytes().to_vec(), 0o644)
                .unwrap();
        }
        let mut lock = Lock::load(dir.path().join("spinup.lock")).unwrap();
        lock.packs.push("web".into());
        lock.answers.insert(
            "shadcn_registry".into(),
            serde_json::value::to_raw_value("1.7.0").unwrap(),
        );
        lock.save(dir.path().join("spinup.lock")).unwrap();
        write(
            dir.path(),
            button,
            if phase == "after-conflicts" {
                "user\n"
            } else {
                "old\n"
            },
            0o644,
        );
        git(dir.path(), &["add", "."]);
        git(dir.path(), &["commit", "-m", "web fixture"]);
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        let matches =
            crate::ui::args(clap::Command::new("upgrade")).get_matches_from(["upgrade", "--ui"]);
        {
            let mut runner = Runner::new(
                dir.path(),
                &source,
                &OsRunner,
                &merger,
                &cancel,
                &mut output,
            );
            runner.renderer = &FakeRenderer;
            runner.matches = Some(&matches);
            if phase == "deps" {
                runner.phases = Some(Phases {
                    deps: Some(interrupted),
                    ..Phases::default()
                });
            }
            let error = runner
                .run(Options {
                    to: Some("0.2.0".into()),
                    yes: true,
                    ..Options::default()
                })
                .unwrap_err();
            if phase != "deps" {
                assert_eq!(spinup_ui::exit::exit_code(&error), 3);
            }
            assert!(State::load(&runner.root).unwrap().ui);
        }
        let work = dir.path().join(".spinup/upgrade/0.2.0");
        if phase == "conflicts" {
            write(&work, "file.txt", "resolved\n", 0o644);
        }
        if phase == "after-conflicts" {
            write(&work, button, "resolved UI\n", 0o644);
        }
        let mut resumed = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        resumed.renderer = &FakeRenderer;
        resumed
            .continue_upgrade(Options {
                yes: true,
                ..Options::default()
            })
            .unwrap();
        let lock = Lock::load(work.join("spinup.lock")).unwrap();
        assert_eq!(answers(&lock).unwrap()["shadcn_registry"], "1.8.0");
        assert_eq!(
            std::fs::read_to_string(work.join(button)).unwrap(),
            if phase == "after-conflicts" {
                "resolved UI\n"
            } else {
                "new\n"
            }
        );
        assert_eq!(git(&work, &["status", "--porcelain"]), "");
    }
}

mod module;
