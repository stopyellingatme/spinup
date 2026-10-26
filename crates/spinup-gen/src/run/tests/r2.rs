//! R2 formatter and database rules.

use super::*;

#[test]
fn no_database_skips_each_database_step_and_dependent_client() {
    for requested in [
        vec![PostStep::Migrate],
        vec![PostStep::SqlxPrepare],
        vec![PostStep::Build],
        vec![PostStep::OpenApiEmit],
        vec![
            PostStep::Client,
            PostStep::Build,
            PostStep::Migrate,
            PostStep::SqlxPrepare,
            PostStep::OpenApiEmit,
            PostStep::Migrate,
        ],
        vec![PostStep::OpenApiEmit, PostStep::Client],
    ] {
        for json in [false, true] {
            let root = Root::new();
            let fake = Fake::new();
            let cancel = Cancellation::new();
            let mut out = Vec::new();
            let mut command_out = Vec::new();
            let mut output = plan_output(vec![file("api/new.txt", "new", "managed", 0)]);
            output.post_steps = requested.clone();
            // Duplicate Next commands must not be added.
            output.next = vec!["spinup db prepare".into()];
            set_output(Ok(output));
            let mut options = Options::new(root.path(), &mut out, &fake, &cancel);
            options.database_url = None;
            options.post_steps = &[];
            options.json = json;
            options.command_out = Some(&mut command_out);
            let result = run(&EXAMPLE, &[], &matches(), options).unwrap();
            assert!(result.post_steps.is_empty());
            assert_eq!(result.next, ["spinup db prepare", "spinup g openapi"]);
            let text = String::from_utf8(if json { command_out } else { out }).unwrap();
            let skipped = steps::ordered(&requested)
                .iter()
                .map(|s| step_name(*s))
                .collect::<Vec<_>>()
                .join(", ");
            assert!(
                text.contains(&format!(
                    "Skipped post-steps: DATABASE_URL is not set ({skipped})"
                )),
                "{text}"
            );
            assert_eq!(root.read("api/new.txt").unwrap(), b"new");
            assert_eq!(root.lock().files["api/new.txt"].template, "gen/example");
            assert!(fake.calls().is_empty());
        }
    }
}

#[test]
fn client_runs_without_database_unless_openapi_was_skipped() {
    for requested in [
        vec![PostStep::Client],
        vec![PostStep::Migrate, PostStep::Client],
    ] {
        let root = Root::new();
        let fake = Fake::new();
        fake.expect("client-tool", ["web/new.txt"])
            .returns("", "", 0);
        set_output(Ok(Output {
            post_steps: requested,
            ..plan_output(vec![file("web/new.txt", "new", "managed", 0)])
        }));
        let mut out = Vec::new();
        let cancel = Cancellation::new();
        let runners = step_runners();
        let mut options = Options::new(root.path(), &mut out, &fake, &cancel);
        options.database_url = None;
        options.post_steps = &runners;
        let result = run(&EXAMPLE, &[], &matches(), options).unwrap();
        assert_eq!(result.post_steps, [PostStep::Client]);
        fake.assert_done().unwrap();
    }
}

#[test]
fn database_runs_all_five_steps_in_order() {
    let root = Root::new();
    let fake = Fake::new();
    let runners = [
        step(PostStep::Migrate, |i| tool(i, "migrate")),
        step(PostStep::SqlxPrepare, |i| tool(i, "prepare")),
        step(PostStep::Build, |i| tool(i, "build")),
        step(PostStep::OpenApiEmit, |i| tool(i, "openapi")),
        step(PostStep::Client, |i| tool(i, "client")),
    ];
    for name in ["migrate", "prepare", "build", "openapi", "client"] {
        fake.expect(name, ["api/new.txt"]).returns("", "", 0);
    }
    set_output(Ok(Output {
        post_steps: vec![
            PostStep::Client,
            PostStep::Build,
            PostStep::OpenApiEmit,
            PostStep::SqlxPrepare,
            PostStep::Migrate,
        ],
        ..plan_output(vec![file("api/new.txt", "new", "managed", 0)])
    }));
    let result = run_with(&EXAMPLE, &root, Flags::default(), &fake, &runners)
        .0
        .unwrap();
    assert_eq!(result.post_steps.len(), 5);
    fake.assert_done().unwrap();
}

#[test]
fn runner_formats_every_rust_file_and_hashes_the_formatted_bytes() {
    let root = Root::new();
    let runner = spinup_exec::os_runner::OsRunner;
    for preview in [true, false, false] {
        set_output(Ok(plan_output(vec![
            file(
                "api/src/lib.rs",
                "mod missing; pub fn entry( ){}",
                "managed",
                0,
            ),
            file("scripts/check.rs", "fn main( ){let x=1;}", "scaffold", 0),
        ])));
        let mut out = Vec::new();
        let cancel = Cancellation::new();
        let mut options = Options::new(root.path(), &mut out, &runner, &cancel);
        options.database_url = None;
        options.dry_run = preview;
        let result = run(&EXAMPLE, &[], &matches(), options).unwrap();
        if preview {
            assert!(root.lock().formatter.is_empty());
            assert!(root.read("api/src/lib.rs").is_none());
        } else {
            let lock = root.lock();
            assert!(lock.formatter.starts_with("rustfmt "));
            for path in ["api/src/lib.rs", "scripts/check.rs"] {
                let data = root.read(path).unwrap();
                assert_eq!(lock.files[path].hash, spinup_manifest::hash::hash(&data));
                assert!(data.ends_with(b"\n"));
                assert!(!String::from_utf8_lossy(&data).contains("( )"));
            }
            assert!(!root.path().join("api/src/missing.rs").exists());
            if result.files.iter().any(|f| f.action == Action::Create) {
                // The next no-op run must refresh this value.
                let mut lock = root.lock();
                lock.formatter = "old".into();
                lock.save(root.lock_path()).unwrap();
            } else {
                assert!(result.files.iter().all(|f| f.action == Action::Unchanged));
                assert_ne!(lock.formatter, "old");
            }
        }
    }
    // A non-Rust plan retains the existing formatter value.
    let formatter = root.lock().formatter;
    set_output(Ok(plan_output(vec![file(
        "api/file.txt",
        "plain",
        "managed",
        0,
    )])));
    run_example(&root, Flags::default()).0.unwrap();
    assert_eq!(root.lock().formatter, formatter);
}

#[test]
fn formatter_failure_prevents_apply_and_lock_changes() {
    let root = Root::new();
    let original = std::fs::read(root.lock_path()).unwrap();
    let fake = Fake::new();
    fake.expect("rustfmt", ["--edition", "2024"])
        .returns("", "invalid Rust source", 1);
    set_output(Ok(plan_output(vec![file(
        "api/src/lib.rs",
        "invalid",
        "managed",
        0,
    )])));
    let error = err_text(run_with(&EXAMPLE, &root, Flags::default(), &fake, &[]).0);
    assert!(error.contains("invalid Rust source"), "{error}");
    assert!(root.read("api/src/lib.rs").is_none());
    assert_eq!(std::fs::read(root.lock_path()).unwrap(), original);
    fake.assert_done().unwrap();
}
