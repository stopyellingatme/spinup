use super::*;

#[test]
fn cargo_installs_all_six_phases() {
    let p = phases();
    assert!(p.before.is_some());
    assert!(p.apply.is_some());
    assert!(p.deps.is_some());
    assert!(p.fix.is_some());
    assert!(p.after.is_some());
    assert!(p.verify.is_some());
}

#[test]
fn cargo_keeps_dependency_manifests() {
    for path in [
        "api/Cargo.toml",
        "api/Cargo.lock",
        "api/go.mod",
        "api/go.sum",
    ] {
        assert!(kept_manifest(path).is_some(), "{path}");
    }
    for path in ["Cargo.toml", "api/src/main.rs", "web/package.json"] {
        assert!(kept_manifest(path).is_none(), "{path}");
    }
}

use crate::contract::{FileDecision, Options, Plan, Step};
use crate::plan::testing::{FakeMerger, version, write as write_file};
use spinup_exec::fake::Fake;
use spinup_exec::runner::{Cancellation, Cmd, Runner as _};
use spinup_manifest::lock::{Class, Lock};
use std::sync::Arc;

fn with_step(f: impl FnOnce(&mut Step<'_>, &Fake)) {
    let dir = tempfile::tempdir().unwrap();
    let fake = Fake::new();
    let cancel = Cancellation::new();
    let merger = FakeMerger::default();
    let mut output = Vec::new();
    let mut s = Step {
        from: Arc::new(version("0.1.0", &[])),
        to: Arc::new(version("0.2.0", &[])),
        worktree: dir.path().into(),
        lock: Lock::new(),
        answers: Default::default(),
        plan: None,
        options: Options::default(),
        runner: &fake,
        cancel: &cancel,
        out: &mut output,
        merger: &merger,
        conflicts: vec![],
    };
    f(&mut s, &fake);
    fake.assert_done().unwrap();
}

fn decision(path: &str, data: &str) -> FileDecision {
    FileDecision {
        path: path.into(),
        from: None,
        action: Action::Keep,
        class: Class::Scaffold,
        template: String::new(),
        data: data.as_bytes().to_vec(),
        mode: 0o644,
        reason: kept_manifest(path).unwrap_or("").into(),
    }
}

#[test]
fn cargo_deps_raise_pins_preserve_aliases_and_update_precisely() {
    for offline in [false, true] {
        with_step(|s, fake| {
            write_file(
                &s.worktree,
                "api/Cargo.toml",
                r#"
# keep this comment
[package]
name = "api"
rust-version = "1.98"
[dependencies]
axum = "0.8.0"
kit = { package = "spinup-kit", version = "=0.1.0", features = ["db"] }
custom = "3.0.0"
[dev-dependencies]
kit = { package = "spinup-kit", version = "=0.1.0", features = ["testkit"] }
"#,
                0o644,
            );
            let stack = &mut Arc::make_mut(&mut s.to).stack;
            let stack = Arc::make_mut(stack);
            stack.crates.insert("axum".into(), "0.8.9".into());
            stack.crates.insert("spinup-kit".into(), "0.2.0".into());
            stack.crates.insert("custom".into(), "2.0.0".into());
            stack.rust.min = "1.99".into();
            s.options.offline = offline;
            fake.expect(
                "cargo",
                ["update", "-p", "axum@0.8.0", "--precise", "0.8.9"],
            );
            fake.expect(
                "cargo",
                ["update", "-p", "spinup-kit@0.1.0", "--precise", "0.2.0"],
            );
            deps::run(s).unwrap();
            let manifest = read(&s.worktree.join("api/Cargo.toml")).unwrap();
            assert_eq!(manifest["dependencies"]["axum"].as_str(), Some("0.8.9"));
            assert_eq!(
                manifest["dependencies"]["kit"]["version"].as_str(),
                Some("=0.2.0")
            );
            assert_eq!(
                manifest["dev-dependencies"]["kit"]["version"].as_str(),
                Some("=0.2.0")
            );
            assert_eq!(manifest["dependencies"]["custom"].as_str(), Some("3.0.0"));
            assert_eq!(manifest["package"]["rust-version"].as_str(), Some("1.99"));
            assert!(manifest.to_string().contains("# keep this comment"));
            for call in fake.calls() {
                assert_eq!(call.args.last().is_some_and(|v| v == "--offline"), offline);
                assert_eq!(call.dir, Some(s.worktree.join("api")));
            }
            // The next run preserves the bytes and performs no update.
            let first = std::fs::read(s.worktree.join("api/Cargo.toml")).unwrap();
            deps::run(s).unwrap();
            assert_eq!(
                std::fs::read(s.worktree.join("api/Cargo.toml")).unwrap(),
                first
            );
        });
    }
}

#[test]
fn cargo_deps_merge_new_dependencies_and_feature_defaults() {
    let old = parse(
        r#"
[features]
default = ["removed", "retained"]
"#,
        "old",
    )
    .unwrap();
    let mut disk = parse(
        r#"
[package]
name = "api"
rust-version = "1.100"
[dependencies]
axum = "0.8.0"
[features]
default = ["removed", "retained", "custom"]
custom = []
retained = ["user/member"]
"#,
        "disk",
    )
    .unwrap();
    let new = parse(
        r#"
[dependencies]
axum = { version = "0.8.9", features = ["macros"] }
new_dep = { version = "1.0.0", features = ["new"] }
[features]
default = ["retained", "added"]
retained = ["template/member"]
added = []
"#,
        "new",
    )
    .unwrap();
    let mut stack = spinup_stack::stack::Stack::default();
    stack.crates.insert("axum".into(), "0.8.9".into());
    stack.rust.min = "1.99".into();
    let mut raised = Default::default();
    deps::edit(&mut disk, Some(&old), Some(&new), &stack, &mut raised).unwrap();
    assert_eq!(
        disk["features"]["default"]
            .as_array()
            .unwrap()
            .iter()
            .filter_map(toml_edit::Value::as_str)
            .collect::<Vec<_>>(),
        ["retained", "added", "custom"]
    );
    assert_eq!(disk["package"]["rust-version"].as_str(), Some("1.100"));
    assert!(disk["dependencies"]["new_dep"].is_inline_table());
    assert_eq!(
        disk["dependencies"]["axum"]["features"][0].as_str(),
        Some("macros")
    );
    assert_eq!(disk["features"]["retained"].as_array().unwrap().len(), 2);
}

#[test]
fn cargo_deps_refresh_web_without_api_and_skip_conflicts() {
    for manager in ["pnpm", "bun"] {
        with_step(|s, fake| {
            write_file(&s.worktree, "web/package.json", "{}\n", 0o644);
            s.answers.insert("package_manager".into(), manager.into());
            fake.expect(manager, ["install", "--lockfile-only"]);
            deps::run(s).unwrap();
            assert_eq!(fake.calls()[0].dir, Some(s.worktree.join("web")));
            s.plan = Some(Plan {
                files: vec![FileDecision {
                    action: Action::Conflict,
                    ..decision("web/package.json", "")
                }],
                ..Plan::default()
            });
            deps::run(s).unwrap();
            assert_eq!(fake.calls().len(), 1);
        });
    }
}

#[test]
fn cargo_deps_errors_include_recovery_instructions() {
    with_step(|s, fake| {
        write_file(
            &s.worktree,
            "api/Cargo.toml",
            "[dependencies]\naxum = '0.8.0'\n",
            0o644,
        );
        Arc::make_mut(&mut Arc::make_mut(&mut s.to).stack)
            .crates
            .insert("axum".into(), "0.8.9".into());
        fake.expect("cargo", ["update"])
            .returns("", "resolution failed", 1);
        let error = deps::run(s).unwrap_err().to_string();
        assert!(error.contains("resolution failed"), "{error}");
        assert!(error.lines().last().unwrap().starts_with("Try:"));
    });
    with_step(|s, _| {
        write_file(&s.worktree, "api/Cargo.toml", "<<<<<<< ours\n", 0o644);
        let error = deps::run(s).unwrap_err().to_string();
        assert!(error.contains("conflict markers"));
        assert!(error.contains("Try:"));
    });
}

#[test]
fn cargo_apply_preserves_relative_patch_and_lock_bytes() {
    for copied in [false, true] {
        with_step(|s, _| {
            let main = s.worktree.clone();
            write_file(
                &main,
                "api/Cargo.toml",
                "[patch.crates-io]\nspinup-kit = { path = '../kit', features = ['db'] }\n",
                0o644,
            );
            std::fs::create_dir_all(main.join("kit")).unwrap();
            s.worktree = main.join(".spinup/upgrade/0.2.0");
            write_file(
                &s.worktree,
                "api/Cargo.toml",
                "# keep\n[package]\nname = 'api'\n",
                0o644,
            );
            write_file(&s.worktree, "api/Cargo.lock", "unmodified lock\n", 0o644);
            if copied {
                std::fs::create_dir_all(s.worktree.join("kit")).unwrap();
            }
            apply::apply(s, None).unwrap();
            let manifest = read(&s.worktree.join("api/Cargo.toml")).unwrap();
            let patch = &manifest["patch"]["crates-io"]["spinup-kit"];
            if copied {
                assert_eq!(patch["path"].as_str(), Some("../kit"));
            } else {
                assert!(std::path::Path::new(patch["path"].as_str().unwrap()).is_absolute());
            }
            assert_eq!(patch["features"][0].as_str(), Some("db"));
            assert_eq!(
                std::fs::read(s.worktree.join("api/Cargo.lock")).unwrap(),
                b"unmodified lock\n"
            );
            let first = manifest.to_string();
            apply::apply(s, None).unwrap();
            assert_eq!(
                read(&s.worktree.join("api/Cargo.toml"))
                    .unwrap()
                    .to_string(),
                first
            );
        });
    }
}

#[test]
fn cargo_apply_override_wins_and_rejects_missing_directory() {
    with_step(|s, _| {
        write_file(
            &s.worktree,
            "api/Cargo.toml",
            "[patch.crates-io]\nother = { path = '../other' }\nspinup-kit = { path = '/missing' }\n",
            0o644,
        );
        let kit = s.worktree.join("local kit");
        std::fs::create_dir_all(&kit).unwrap();
        apply::apply(s, Some(kit.clone())).unwrap();
        let doc = read(&s.worktree.join("api/Cargo.toml")).unwrap();
        assert_eq!(
            doc["patch"]["crates-io"]["spinup-kit"]["path"].as_str(),
            kit.to_str()
        );
        assert!(doc["patch"]["crates-io"].get("other").is_some());
        let error = apply::apply(s, Some(kit.join("absent")))
            .unwrap_err()
            .to_string();
        assert!(error.contains("Try:"));
    });
}

#[test]
fn cargo_verify_runs_cargo_web_and_doctor_and_collects_failures() {
    with_step(|s, fake| {
        write_file(
            &s.worktree,
            "api/Cargo.toml",
            "[package]\nname = 'api'\n",
            0o644,
        );
        write_file(&s.worktree, "web/package.json", "{}\n", 0o644);
        write_file(&s.worktree, ".gitignore", ".env\n.env.*\n", 0o644);
        fake.expect(
            "cargo",
            [
                "clippy",
                "--locked",
                "--all-targets",
                "--",
                "-D",
                "warnings",
            ],
        )
        .returns("", "src/lib.rs: bad code", 1);
        fake.expect("cargo", ["test", "--locked"])
            .returns("tests failed", "", 1);
        fake.expect("pnpm", ["check"]);
        fake.expect("pnpm", ["build"]);
        fake.expect("git", ["ls-files", "-z"]);
        let error = verify::run(s).unwrap_err().to_string();
        for message in ["clippy", "src/lib.rs: bad code", "tests failed", "Try:"] {
            assert!(error.contains(message), "{error}");
        }
        assert_eq!(fake.calls().len(), 5);
        for call in fake.calls() {
            assert!(call.replace_env);
            assert!(call.env.iter().all(|(key, _)| {
                !["CARGO_ENCODED_RUSTFLAGS", "RUSTFLAGS", "LLVM_PROFILE_FILE"]
                    .contains(&key.as_str())
            }));
        }
    });
}

#[test]
fn cargo_verify_reports_doctor_failure_and_skips_when_requested() {
    with_step(|s, fake| {
        fake.expect("git", ["ls-files", "-z"])
            .returns(".env\0", "", 0);
        let error = verify::run(s).unwrap_err().to_string();
        assert!(error.contains("doctor plaintext-secrets"), "{error}");
        s.options.no_verify = true;
        verify::run(s).unwrap();
        s.options.no_verify = false;
        s.conflicts.push("api/src/lib.rs".into());
        verify::run(s).unwrap();
        assert!(!(phases().fix.unwrap())(s).unwrap());
        assert_eq!(fake.calls().len(), 1);
    });
}

#[test]
fn cargo_migrations_record_partial_success_and_answer_changes() {
    use spinup_migrate::contract::{Change, Ctx, Migration, Stage};
    fn plan(_: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
        Ok(vec![Change {
            path: "api/src/lib.rs".into(),
            desc: "test".into(),
        }])
    }
    fn apply(ctx: &mut Ctx<'_>) -> anyhow::Result<()> {
        ctx.answers.insert("changed".into(), true.into());
        Ok(())
    }
    fn fail(_: &mut Ctx<'_>) -> anyhow::Result<()> {
        anyhow::bail!("cannot migrate")
    }
    let first = Migration {
        id: "test-first",
        since: "0.2.0",
        desc: "test",
        stage: Stage::Before,
        plan,
        apply,
    };
    let second = Migration {
        id: "test-fail",
        apply: fail,
        ..first
    };
    with_step(|s, _| {
        let error = migrations::apply(s, &[&first, &second])
            .unwrap_err()
            .to_string();
        assert!(error.contains("cannot migrate"));
        assert_eq!(s.lock.applied_migrations, ["test-first"]);
        assert_eq!(s.answers["changed"], true);
        let owners = migrations::provenance(s);
        let message = migrations::attribute("src/lib.rs: failed".into(), &owners);
        assert!(message.contains("migration test-first"), "{message}");
    });
    with_step(|s, _| {
        s.cancel.cancel();
        let error = migrations::apply(s, &[&first]).unwrap_err().to_string();
        assert!(error.contains("context canceled"));
        assert!(s.lock.applied_migrations.is_empty());
    });
}

#[test]
fn cargo_plan_keeps_new_render_and_never_text_merges_the_lock() {
    use crate::plan::testing::{Fixture, spec};
    use spinup_manifest::lock::FileEntry;
    let dir = tempfile::tempdir().unwrap();
    let files = ["api/Cargo.toml", "api/Cargo.lock"];
    let old = version("0.1.0", &files.map(|p| spec(p, "old\n")));
    let new = version("0.2.0", &files.map(|p| spec(p, "new\n")));
    let mut lock = Lock::new();
    lock.template_version = "0.1.0".into();
    lock.packs = vec!["base".into()];
    for path in files {
        write_file(dir.path(), path, "user\n", 0o644);
        lock.files.insert(
            path.into(),
            FileEntry {
                class: Class::Scaffold,
                template: format!("base/{path}.tmpl"),
                hash: spinup_manifest::hash::hash(b"old\n"),
                mode: "0644".into(),
            },
        );
    }
    let fixture = Fixture::default();
    let plan =
        crate::plan::build_plan(&fixture.input(dir.path(), &lock, Some(&old), &new)).unwrap();
    assert!(fixture.merger.calls().is_empty());
    for file in plan.files {
        assert_eq!(file.action, Action::Keep);
        assert_eq!(file.data, b"new\n");
        assert_eq!(Some(file.reason.as_str()), kept_manifest(&file.path));
    }
}

#[test]
fn cargo_upgrade_with_autocrlf_and_base_attributes_stays_clean() {
    use crate::plan::testing::{FakeRenderer, spec};
    use spinup_exec::os_runner::OsRunner;
    use spinup_tplstore::source::{Source, SourceError, Version};
    struct Versions([Arc<Version>; 2]);
    impl Source for Versions {
        fn versions(&self) -> Result<Vec<String>, SourceError> {
            Ok(self.0.iter().map(|v| v.version.clone()).collect())
        }
        fn load(&self, name: &str) -> Result<Arc<Version>, SourceError> {
            Ok(self.0.iter().find(|v| v.version == name).unwrap().clone())
        }
    }
    fn git(root: &std::path::Path, args: &[&str]) -> String {
        let cmd = Cmd::new("git")
            .args(args.iter().copied())
            .dir(root)
            .env("GIT_CONFIG_NOSYSTEM", "1")
            .env(
                "GIT_CONFIG_GLOBAL",
                if cfg!(windows) { "NUL" } else { "/dev/null" },
            )
            .env("GIT_AUTHOR_NAME", "Spinup Test")
            .env("GIT_AUTHOR_EMAIL", "test@example.com")
            .env("GIT_COMMITTER_NAME", "Spinup Test")
            .env("GIT_COMMITTER_EMAIL", "test@example.com");
        String::from_utf8(OsRunner.output(&cmd, &Cancellation::new()).unwrap().stdout).unwrap()
    }
    let dir = tempfile::tempdir().unwrap();
    let root = dir.path();
    git(root, &["init"]);
    git(root, &["config", "core.autocrlf", "true"]);
    git(root, &["config", "user.name", "Spinup Test"]);
    git(root, &["config", "user.email", "test@example.com"]);
    let attributes = include_str!("../../../spinup-packs/packs/base/files/.gitattributes");
    assert!(attributes.lines().any(|line| line == "* text=auto eol=lf"));
    write_file(root, ".gitattributes", attributes, 0o644);
    write_file(root, ".gitignore", ".spinup/\n.env\n.env.*\n", 0o644);
    write_file(root, "file.txt", "old\n", 0o644);
    let lock = crate::plan::testing::lock("old\n", Some(Class::Scaffold));
    lock.save(root.join("spinup.lock")).unwrap();
    git(root, &["add", "."]);
    git(root, &["commit", "-m", "initial"]);
    let versions = Versions([
        Arc::new(version("0.1.0", &[spec("file.txt", "old\n")])),
        Arc::new(version("0.2.0", &[spec("file.txt", "new\n")])),
    ]);
    let cancel = Cancellation::new();
    let merger = FakeMerger::default();
    let mut output = Vec::new();
    let mut upgrade =
        crate::run::Runner::new(root, &versions, &OsRunner, &merger, &cancel, &mut output);
    upgrade.renderer = &FakeRenderer;
    upgrade
        .run(Options {
            to: Some("0.2.0".into()),
            yes: true,
            ..Options::default()
        })
        .unwrap();
    let work = root.join(".spinup/upgrade/0.2.0");
    assert_eq!(std::fs::read(work.join("file.txt")).unwrap(), b"new\n");
    assert_eq!(git(&work, &["status", "--porcelain"]), "");
    assert_eq!(git(root, &["status", "--porcelain"]), "");
    git(root, &["merge", "--ff-only", "spinup/upgrade-0.2.0"]);
    assert_eq!(git(root, &["status", "--porcelain"]), "");
    assert_eq!(std::fs::read(root.join("file.txt")).unwrap(), b"new\n");
}

#[test]
fn cargo_deps_selects_the_resolved_old_version_from_the_lock() {
    with_step(|s, fake| {
        write_file(
            &s.worktree,
            "api/Cargo.toml",
            "[dependencies]\naxum = '^0.8.0'\n",
            0o644,
        );
        write_file(
            &s.worktree,
            "api/Cargo.lock",
            "version = 4\n[[package]]\nname = 'axum'\nversion = '0.8.4'\n",
            0o644,
        );
        Arc::make_mut(&mut Arc::make_mut(&mut s.to).stack)
            .crates
            .insert("axum".into(), "0.8.9".into());
        fake.expect(
            "cargo",
            ["update", "-p", "axum@0.8.4", "--precise", "0.8.9"],
        );
        deps::run(s).unwrap();
    });
}

#[test]
fn cargo_deps_resolves_new_dependencies_when_no_existing_pin_increases() {
    with_step(|s, fake| {
        write_file(
            &s.worktree,
            "api/Cargo.toml",
            "[dependencies]\naxum = '0.8.9'\n",
            0o644,
        );
        let stack = Arc::make_mut(&mut Arc::make_mut(&mut s.to).stack);
        stack.crates.insert("axum".into(), "0.8.9".into());
        stack.crates.insert("serde".into(), "1.0.0".into());
        s.plan = Some(Plan {
            files: vec![decision(
                "api/Cargo.toml",
                "[dependencies]\naxum = '0.8.9'\nserde = { version = '1.0.0', features = ['derive'] }\n",
            )],
            ..Plan::default()
        });
        fake.expect("cargo", ["metadata", "--format-version", "1"]);
        fake.expect("cargo", ["update", "-p", "serde", "--precise", "1.0.0"]);
        deps::run(s).unwrap();
        let doc = read(&s.worktree.join("api/Cargo.toml")).unwrap();
        assert_eq!(
            doc["dependencies"]["serde"]["features"][0].as_str(),
            Some("derive")
        );
    });
}

#[test]
fn cargo_migrations_run_registered_stages_once_and_rebuild_provenance() {
    with_step(|s, fake| {
        write_file(&s.worktree, "web/package.json", "{}\n", 0o644);
        Arc::make_mut(&mut Arc::make_mut(&mut s.from).stack)
            .npm
            .insert("@sveltejs/kit".into(), "2.0.0".into());
        let stack = Arc::make_mut(&mut Arc::make_mut(&mut s.to).stack);
        stack.npm.insert("@sveltejs/kit".into(), "3.0.0".into());
        stack.npm.insert("sv".into(), "1.0.0".into());
        fake.expect("pnpm", ["dlx", "sv@1.0.0", "migrate", "sveltekit-3"]);
        (phases().before.unwrap())(s).unwrap();
        assert!(s.lock.applied_migrations.is_empty());
        (phases().after.unwrap())(s).unwrap();
        (phases().after.unwrap())(s).unwrap();
        assert_eq!(s.lock.applied_migrations, ["0001_web_svmigrate"]);
        assert_eq!(
            migrations::provenance(s).get("web").unwrap(),
            "0001_web_svmigrate"
        );
    });
    // A resumed process has only the recorded migration IDs.
    with_step(|s, _| {
        write_file(&s.worktree, "web/package.json", "{}\n", 0o644);
        Arc::make_mut(&mut Arc::make_mut(&mut s.from).stack)
            .npm
            .insert("@sveltejs/kit".into(), "2.0.0".into());
        let stack = Arc::make_mut(&mut Arc::make_mut(&mut s.to).stack);
        stack.npm.insert("@sveltejs/kit".into(), "3.0.0".into());
        stack.npm.insert("sv".into(), "1.0.0".into());
        s.lock.applied_migrations.push("0001_web_svmigrate".into());
        let message = migrations::attribute(
            "web/src/app.svelte: failed".into(),
            &migrations::provenance(s),
        );
        assert!(
            message.contains("migration 0001_web_svmigrate"),
            "{message}"
        );
    });
}

#[test]
fn cargo_deps_invalid_structures_return_errors_without_panics() {
    for text in [
        "features = 1",
        "dependencies = 1",
        "[features]\ndefault = 1",
        "[dependencies]\naxum = false",
        "package = false",
    ] {
        let mut disk = parse(text, "disk").unwrap();
        let error = deps::edit(
            &mut disk,
            None,
            None,
            &Default::default(),
            &mut Default::default(),
        )
        .unwrap_err()
        .to_string();
        assert!(error.contains("Try:"), "{error}");
    }
}

#[test]
fn cargo_deps_reads_old_default_features_from_the_disk_lock_answers() {
    with_step(|s, fake| {
        let mut tree = spinup_vfs::tree::Tree::new();
        tree.insert("api/pack.toml", b"name = 'api'\n[[files]]\npath = 'api/Cargo.toml'\nsource = 'Cargo.toml.tmpl'\nclass = 'scaffold'\n".to_vec(), 0o644).unwrap();
        tree.insert(
            "api/Cargo.toml.tmpl",
            b"[features]\ndefault = ['[% if data.enabled %]new[% else %]old[% endif %]']\n"
                .to_vec(),
            0o644,
        )
        .unwrap();
        Arc::make_mut(&mut s.from).packs = Arc::new(tree);
        let mut old_lock = Lock::new();
        old_lock.answers.insert(
            "enabled".into(),
            serde_json::value::to_raw_value(&false).unwrap(),
        );
        old_lock.save(s.worktree.join("spinup.lock")).unwrap();
        s.lock.answers.insert(
            "enabled".into(),
            serde_json::value::to_raw_value(&true).unwrap(),
        );
        write_file(
            &s.worktree,
            "api/Cargo.toml",
            "[features]\ndefault = ['old', 'custom']\ncustom = []\n",
            0o644,
        );
        s.plan = Some(Plan {
            files: vec![decision(
                "api/Cargo.toml",
                "[features]\ndefault = ['new']\nnew = []\n",
            )],
            ..Plan::default()
        });
        fake.expect("cargo", ["metadata", "--format-version", "1"]);
        deps::run(s).unwrap();
        let manifest = read(&s.worktree.join("api/Cargo.toml")).unwrap();
        let defaults: Vec<_> = manifest["features"]["default"]
            .as_array()
            .unwrap()
            .iter()
            .filter_map(toml_edit::Value::as_str)
            .collect();
        assert_eq!(defaults, ["new", "custom"]);
    });
}
