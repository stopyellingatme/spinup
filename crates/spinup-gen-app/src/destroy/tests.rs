#[test]
fn destroy_is_not_a_generator() {
    assert!(
        !spinup_gen::contract::GENERATORS
            .iter()
            .any(|g| g.name == "destroy")
    );
}

use super::*;
use spinup_exec::fake::Fake;

fn write(root: &Path, path: &str, source: &str) {
    let mut plan = FilePlan::new();
    plan.add(File {
        path: path.into(),
        data: source.as_bytes().to_vec(),
        ..File::default()
    })
    .unwrap();
    spinup_vfs::apply::apply(root, &plan, Default::default()).unwrap();
}

fn track(lock: &mut Lock, path: &str, source: &str) {
    lock.set_file(
        path,
        FileEntry {
            class: Class::Scaffold,
            template: "gen/resource".into(),
            hash: spinup_manifest::hash::hash(source.as_bytes()),
            mode: "0644".into(),
        },
    )
    .unwrap();
}

fn fixture(root: &Path) -> Lock {
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "web".into()];
    let name = Name::parse("todo").unwrap();
    for path in crate::resource_core::paths(&name).unwrap().iter().chain(
        [
            "api/migrations/00001_create_todos.up.sql".into(),
            "api/migrations/00001_create_todos.down.sql".into(),
        ]
        .iter(),
    ) {
        write(root, path, "original");
        track(&mut lock, path, "original");
    }
    lock.save(root.join("spinup.lock")).unwrap();
    lock
}

fn options(root: &Path) -> Options<'_> {
    Options {
        root: root.into(),
        kind: "resource".into(),
        name: "todo".into(),
        ..Default::default()
    }
}

fn expect_format(fake: &Fake) {
    fake.expect("rustfmt", ["--edition", "2024"])
        .returns("", "", 0);
    fake.expect("rustfmt", ["--version"])
        .returns("rustfmt 1.99.1\n", "", 0);
}

#[test]
fn destroy_preview_keeps_changed_files_and_every_migration() {
    let root = tempfile::tempdir().unwrap();
    let lock = fixture(root.path());
    write(root.path(), "api/src/handlers/todos.rs", "changed");
    let before = std::fs::read(root.path().join("spinup.lock")).unwrap();
    let plan = build(&options(root.path()), &lock).unwrap();
    assert_eq!(plan.kept, ["api/src/handlers/todos.rs"]);
    assert!(
        plan.files
            .files
            .iter()
            .all(|f| !f.path.starts_with("api/migrations/"))
    );
    assert!(plan.dirs.contains(&"web/src/routes/todos".into()));
    assert!(!plan.dirs.contains(&"api/src/handlers".into()));
    assert!(
        String::from_utf8(
            plan.files
                .get(crate::resource_core::paths::ROUTES_GEN)
                .unwrap()
                .data
        )
        .unwrap()
        .contains("handlers::todos::routes()")
    );
    let out = Mutex::new(Vec::<u8>::new());
    let fake = Fake::new();
    run(
        &Options {
            dry_run: true,
            out: Some(&out),
            ..options(root.path())
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap();
    assert!(fake.calls().is_empty());
    let text = String::from_utf8(out.into_inner().unwrap()).unwrap();
    assert!(
        text.contains("would remove web/src/routes/todos/\n"),
        "{text}"
    );
    assert!(text.contains("Next: spinup db new drop_todos"));
    assert_eq!(
        std::fs::read(root.path().join("spinup.lock")).unwrap(),
        before
    );
    assert!(root.path().join("api/src/models/todo.rs").exists());
}

#[test]
fn destruction_updates_the_lock_navigation_and_directories() {
    let root = tempfile::tempdir().unwrap();
    fixture(root.path());
    let fake = Fake::new();
    expect_format(&fake);
    let out = Mutex::new(Vec::<u8>::new());
    run(
        &Options {
            out: Some(&out),
            database_url: Some(None),
            ..options(root.path())
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap();
    fake.assert_done().unwrap();
    let text = String::from_utf8(out.into_inner().unwrap()).unwrap();
    assert!(
        text.contains("Skipped post-steps: DATABASE_URL is not set"),
        "{text}"
    );
    let lock = Lock::load(root.path().join("spinup.lock")).unwrap();
    for path in crate::resource_core::paths(&Name::parse("todo").unwrap()).unwrap() {
        assert!(!root.path().join(&path).exists(), "{path}");
        assert!(!lock.files.contains_key(&path), "{path}");
    }
    assert!(!root.path().join("web/src/routes/todos").exists());
    for suffix in ["up", "down"] {
        let path = format!("api/migrations/00001_create_todos.{suffix}.sql");
        assert!(root.path().join(&path).exists());
        assert!(lock.files.contains_key(&path));
    }
    for path in [
        crate::resource_core::paths::HANDLERS_MOD,
        crate::resource_core::paths::MODELS_MOD,
        crate::resource_core::paths::ROUTES_GEN,
        "web/src/lib/nav.gen.ts",
    ] {
        let data = std::fs::read(root.path().join(path)).unwrap();
        assert_eq!(lock.files[path].hash, spinup_manifest::hash::hash(&data));
        assert_eq!(lock.files[path].class, Class::Managed);
    }
    assert_eq!(lock.formatter, "rustfmt 1.99.1");
}

#[test]
fn model_handler_untracked_missing_and_bad_kind() {
    for (kind, value, path) in [
        ("model", "todo", "api/src/models/todo.rs"),
        (
            "handler",
            "healthReports",
            "api/src/handlers/health_reports.rs",
        ),
    ] {
        let root = tempfile::tempdir().unwrap();
        let options = Options {
            kind: kind.into(),
            name: value.into(),
            ..options(root.path())
        };
        let mut lock = Lock::new();
        assert!(build(&options, &lock).unwrap().files.files.is_empty());
        write(root.path(), path, "original");
        assert_eq!(build(&options, &lock).unwrap().kept, [path]);
        track(&mut lock, path, "original");
        assert_eq!(
            deleted_paths(&build(&options, &lock).unwrap().files),
            [path]
        );
        write(root.path(), path, "changed");
        assert_eq!(build(&options, &lock).unwrap().kept, [path]);
    }
    let root = tempfile::tempdir().unwrap();
    for (kind, name) in [("job", "todo"), ("model", "../escape"), ("handler", "mod")] {
        let error = build(
            &Options {
                kind: kind.into(),
                name: name.into(),
                ..options(root.path())
            },
            &Lock::new(),
        )
        .unwrap_err();
        assert!(error.to_string().contains("\nTry:"), "{error}");
    }
}

#[test]
fn destroy_failure_after_apply_leaves_a_valid_lock() {
    let root = tempfile::tempdir().unwrap();
    fixture(root.path());
    write(root.path(), "spinup.toml", "[app]\nname='todo'\n");
    write(
        root.path(),
        "api/.env",
        "DATABASE_URL=postgres://fixture-secret\n",
    );
    let fake = Fake::new();
    expect_format(&fake);
    fake.expect("cargo", ["metadata"])
        .returns("", "metadata failed", 1);
    let error = run(&options(root.path()), &fake, &Cancellation::new())
        .unwrap_err()
        .to_string();
    assert!(error.contains("\nTry:"), "{error}");
    assert!(!root.path().join("api/src/models/todo.rs").exists());
    assert!(
        !Lock::load(root.path().join("spinup.lock"))
            .unwrap()
            .files
            .contains_key("api/src/models/todo.rs")
    );
    fake.assert_done().unwrap();
}

#[cfg(unix)]
#[test]
fn linked_file_and_linked_parent_are_kept() {
    for parent in [false, true] {
        let root = tempfile::tempdir().unwrap();
        let outside = tempfile::tempdir().unwrap();
        write(outside.path(), "todo.rs", "original");
        let mut lock = Lock::new();
        let path = "api/src/models/todo.rs";
        track(&mut lock, path, "original");
        // Create parents through VFS, then retain them with an unrelated file.
        write(root.path(), "api/src/.keep", "");
        if parent {
            std::os::unix::fs::symlink(outside.path(), root.path().join("api/src/models")).unwrap();
        } else {
            write(root.path(), "api/src/models/.keep", "");
            std::os::unix::fs::symlink(outside.path().join("todo.rs"), root.path().join(path))
                .unwrap();
        }
        let plan = build(
            &Options {
                kind: "model".into(),
                ..options(root.path())
            },
            &lock,
        )
        .unwrap();
        assert_eq!(plan.kept, [path]);
        assert!(plan.files.files.is_empty());
        assert_eq!(
            std::fs::read(outside.path().join("todo.rs")).unwrap(),
            b"original"
        );
    }
}

struct ChangeDuringPreview {
    root: PathBuf,
    changed: bool,
}
impl Write for ChangeDuringPreview {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        if !self.changed {
            self.changed = true;
            write(
                &self.root,
                "api/src/models/todo.rs",
                "changed after preview",
            );
        }
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn stale_preview_never_authorizes_deletion() {
    let root = tempfile::tempdir().unwrap();
    fixture(root.path());
    let out = Mutex::new(ChangeDuringPreview {
        root: root.path().into(),
        changed: false,
    });
    let fake = Fake::new();
    let error = run(
        &Options {
            out: Some(&out),
            ..options(root.path())
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap_err()
    .to_string();
    assert!(
        error.contains("files changed during destruction"),
        "{error}"
    );
    assert_eq!(
        std::fs::read_to_string(root.path().join("api/src/models/todo.rs")).unwrap(),
        "changed after preview"
    );
    assert!(fake.calls().is_empty());
}
