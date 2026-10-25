use super::*;

fn module_fixture(collision: bool) -> (tempfile::TempDir, Versions) {
    let (dir, mut source) = fixture(false);
    let version = Arc::make_mut(source.0.get_mut("0.1.0").unwrap());
    let tree = Arc::make_mut(&mut version.packs);
    for (path, data) in [
        (
            "base/pack.toml",
            "name='base'\n[[questions]]\nkey='modules'\nprompt='Modules'\ntype='string'\ndefault=''\n[[files]]\npath='spinup.toml'\nsource='config.tmpl'\nclass='scaffold'\n[[files]]\npath='file.txt'\nsource='file.tmpl'\nclass='scaffold'\n",
        ),
        ("base/file.tmpl", "old\n"),
        ("api/pack.toml", "name='api'\nrequires=['base']"),
        (
            "jobs/pack.toml",
            "name='jobs'\nrequires=['api']\n[[questions]]\nkey='queue_name'\nprompt='Queue name'\ntype='string'\ndefault='normal'\n[[files]]\npath='job.txt'\nsource='job.tmpl'\nclass='managed'\n",
        ),
        ("jobs/job.tmpl", "queue=[[ data.queue_name ]]\n"),
    ] {
        tree.insert(path, data.as_bytes().to_vec(), 0o644).unwrap();
    }
    // Separate the TOML opening bracket from the template statement delimiter.
    tree.insert("base/config.tmpl", b"modules = [ [% for m in data.modules %][[ quote(m) ]], [% endfor %]]\n[app]\nname = 'demo'\nmodule = 'example.com/demo'\n".to_vec(),0o644).unwrap();
    let mut lock = Lock::load(dir.path().join("spinup.lock")).unwrap();
    lock.packs = vec!["base".into(), "api".into()];
    lock.answers.insert(
        "modules".into(),
        serde_json::value::to_raw_value(&Vec::<String>::new()).unwrap(),
    );
    let rendered = Packs
        .render(version, &lock.packs, &answers(&lock).unwrap())
        .unwrap();
    let file = rendered.plan.get("spinup.toml").unwrap();
    lock.files.insert(
        "spinup.toml".into(),
        spinup_manifest::lock::FileEntry {
            class: Class::Scaffold,
            template: file.template,
            hash: spinup_manifest::hash::hash(&file.data),
            mode: "0644".into(),
        },
    );
    lock.save(dir.path().join("spinup.lock")).unwrap();
    write(
        dir.path(),
        "spinup.toml",
        "# retained heading\nmodules = [] # retained modules\n[app]\nname = 'demo'\nmodule = 'example.com/demo'\n# retained app\n",
        0o644,
    );
    if collision {
        write(dir.path(), "job.txt", "user job\n", 0o644);
    }
    git(dir.path(), &["add", "."]);
    git(dir.path(), &["commit", "-m", "module fixture"]);
    (dir, source)
}

#[test]
fn module_run_edits_config_and_lock_at_same_version() {
    use crate::module::ModuleOptions;
    let (dir, source) = module_fixture(false);
    git(dir.path(), &["branch", "spinup/upgrade-0.1.0"]);
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
    runner
        .add_modules(ModuleOptions {
            names: vec!["jobs".into(), "jobs".into()],
            answers: serde_json::json!({"queue_name":"priority"})
                .as_object()
                .unwrap()
                .clone(),
            options: Options {
                yes: true,
                ..Options::default()
            },
            ..ModuleOptions::default()
        })
        .unwrap();
    let work = dir.path().join(".spinup/upgrade/0.1.0");
    let lock = Lock::load(work.join("spinup.lock")).unwrap();
    assert_eq!(lock.template_version, "0.1.0");
    assert!(lock.packs.contains(&"jobs".into()));
    assert_eq!(answers(&lock).unwrap()["queue_name"], "priority");
    let config = std::fs::read_to_string(work.join("spinup.toml")).unwrap();
    for comment in ["# retained heading", "# retained modules", "# retained app"] {
        assert!(config.contains(comment));
    }
    assert!(
        spinup_project::config::load(&work)
            .unwrap()
            .modules
            .contains(&"jobs".into())
    );
    assert_eq!(git(&work, &["status", "--porcelain"]), "");
    assert_eq!(git(dir.path(), &["status", "--porcelain"]), "");
    assert!(
        String::from_utf8(output)
            .unwrap()
            .contains("git merge spinup/module-jobs")
    );
}

#[test]
fn module_recovery_keeps_saved_answers() {
    use crate::module::ModuleOptions;
    for abort in [false, true] {
        let (dir, source) = module_fixture(true);
        let mut output = Vec::new();
        let cancel = Cancellation::new();
        let merger = FakeMerger::default();
        {
            let mut runner = Runner::new(
                dir.path(),
                &source,
                &OsRunner,
                &merger,
                &cancel,
                &mut output,
            );
            let err = runner
                .add_modules(ModuleOptions {
                    names: vec!["jobs".into()],
                    options: Options {
                        yes: true,
                        ..Options::default()
                    },
                    ..ModuleOptions::default()
                })
                .unwrap_err();
            assert_eq!(spinup_ui::exit::exit_code(&err), 3);
        }
        let mut resumed = Runner::new(
            dir.path(),
            &source,
            &OsRunner,
            &merger,
            &cancel,
            &mut output,
        );
        let work = dir.path().join(".spinup/upgrade/0.1.0");
        if abort {
            resumed
                .abort(Options {
                    yes: true,
                    ..Options::default()
                })
                .unwrap();
            assert!(!work.exists());
        } else {
            assert_eq!(
                spinup_ui::exit::exit_code(
                    &resumed
                        .continue_upgrade(Options {
                            yes: true,
                            ..Options::default()
                        })
                        .unwrap_err()
                ),
                3
            );
            write(&work, "job.txt", "resolved\n", 0o644);
            resumed
                .continue_upgrade(Options {
                    yes: true,
                    ..Options::default()
                })
                .unwrap();
            assert_eq!(
                answers(&Lock::load(work.join("spinup.lock")).unwrap()).unwrap()["queue_name"],
                "normal"
            );
        }
        assert!(!dir.path().join(".spinup/upgrade.json").exists());
    }
}

#[test]
fn module_preconditions_and_preview() {
    use crate::module::ModuleOptions;
    for (dirty, yes, dry, want) in [
        (false, false, false, "--yes"),
        (true, true, false, "uncommitted"),
        (true, true, true, ""),
    ] {
        let (dir, source) = module_fixture(false);
        if dirty {
            write(dir.path(), "user.txt", "user", 0o644);
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
        let result = runner.add_modules(ModuleOptions {
            names: vec!["jobs".into()],
            options: Options {
                yes,
                dry_run: dry,
                allow_dirty: true,
                ..Options::default()
            },
            ..ModuleOptions::default()
        });
        if want.is_empty() {
            result.unwrap();
        } else {
            assert!(format!("{:#}", result.unwrap_err()).contains(want));
        }
        assert!(!dir.path().join(".spinup/upgrade.json").exists());
        assert!(!dir.path().join(".spinup/upgrade/0.1.0").exists());
    }
}

#[test]
fn module_state_rejects_unsafe_names_and_core_packs() {
    for (names, valid) in [
        (vec!["jobs"], true),
        (vec!["ci", "jobs"], true),
        (vec!["bogus"], false),
        (vec!["../jobs"], false),
        (vec!["web"], false),
        (vec!["jobs", "jobs"], false),
        (vec!["jobs", "ci"], false),
    ] {
        let change = ModuleChange {
            names: names.into_iter().map(str::to_owned).collect(),
            packs: vec!["base".into(), "jobs".into()],
            added: vec![],
            answers: serde_json::Map::new(),
        };
        assert_eq!(change.valid(), valid);
    }
}

#[test]
fn module_dependency_profile_and_disabled_configuration() {
    use crate::module::ModuleOptions;
    for already_locked in [false, true] {
        let (dir, mut source) = module_fixture(false);
        let tree = Arc::make_mut(&mut Arc::make_mut(source.0.get_mut("0.1.0").unwrap()).packs);
        let jobs = String::from_utf8(tree.get("jobs/pack.toml").unwrap().data.to_vec())
            .unwrap()
            .replace("requires=['api']", "requires=['api','monitoring']");
        tree.insert("jobs/pack.toml", jobs.into_bytes(), 0o644)
            .unwrap();
        for (path, data) in [
            ("deploy/pack.toml", "name='deploy'\nrequires=['api']\n"),
            (
                "monitoring/pack.toml",
                "name='monitoring'\nrequires=['deploy']\n[[questions]]\nkey='monitoring_profile'\nprompt='Monitoring profile'\ntype='enum'\nchoices=['lite','full','none']\ndefault='lite'\n",
            ),
        ] {
            tree.insert(path, data.as_bytes().to_vec(), 0o644).unwrap();
        }
        if already_locked {
            let mut lock = Lock::load(dir.path().join("spinup.lock")).unwrap();
            lock.packs
                .extend(["deploy".into(), "monitoring".into(), "jobs".into()]);
            lock.answers.insert(
                "modules".into(),
                serde_json::value::to_raw_value(&["deploy", "jobs", "monitoring"]).unwrap(),
            );
            lock.answers.insert(
                "queue_name".into(),
                serde_json::value::to_raw_value("normal").unwrap(),
            );
            lock.save(dir.path().join("spinup.lock")).unwrap();
        }
        let config = std::fs::read_to_string(dir.path().join("spinup.toml")).unwrap()
            + "\n[monitoring]\nprofile = 'none' # retain profile comment\n";
        write(dir.path(), "spinup.toml", config, 0o644);
        git(dir.path(), &["commit", "-am", "disabled configuration"]);
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
        runner
            .add_modules(ModuleOptions {
                names: vec!["jobs".into()],
                options: Options {
                    yes: true,
                    ..Options::default()
                },
                ..ModuleOptions::default()
            })
            .unwrap();
        let work = dir.path().join(".spinup/upgrade/0.1.0");
        let config = spinup_project::config::load(&work).unwrap();
        assert!(config.modules.contains(&"jobs".into()));
        assert_eq!(config.monitoring.profile, "none");
        assert!(
            std::fs::read_to_string(work.join("spinup.toml"))
                .unwrap()
                .contains("# retain profile comment")
        );
        if !already_locked {
            assert!(
                String::from_utf8(output)
                    .unwrap()
                    .contains("Adding required modules: deploy, monitoring")
            );
        }
    }
}
