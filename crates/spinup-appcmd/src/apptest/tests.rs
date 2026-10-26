use super::*;
use spinup_exec::fake::Fake;
use spinup_ui::exit::exit_code;

#[test]
fn apptest_streams_output_and_preserves_exit_code() {
    for code in [0, 7] {
        let root = tempfile::tempdir().unwrap();
        let fake = Fake::new();
        fake.expect("cargo", ["nextest", "run", "--locked", "Todo"])
            .returns("tests\n", "diagnostic\n", code);
        let mut out = Vec::new();
        let mut err_out = Vec::new();
        let result = run(
            &Cancellation::new(),
            &mut Options {
                root: root.path().into(),
                run: "Todo".into(),
                out: Some(&mut out),
                err_out: Some(&mut err_out),
                ..Options::default()
            },
            &fake,
        );
        assert_eq!(result.as_ref().err().map_or(0, exit_code), code);
        assert_eq!(out, b"tests\n");
        assert_eq!(err_out, b"diagnostic\n");
        assert_eq!(fake.calls()[0].dir, Some(root.path().join("api")));
        assert_eq!(fake.calls()[0].args, ["nextest", "run", "--locked", "Todo"]);
        fake.assert_done().unwrap();
    }
}

#[test]
fn apptest_cover_and_dry_run() {
    let fake = Fake::new();
    fake.expect("cargo", ["llvm-cov", "nextest", "--locked"])
        .returns("", "", 0);
    run(
        &Cancellation::new(),
        &mut Options {
            cover: true,
            ..Options::default()
        },
        &fake,
    )
    .unwrap();
    assert_eq!(fake.calls()[0].args, ["llvm-cov", "nextest", "--locked"]);
    let mut out = Vec::new();
    run(
        &Cancellation::new(),
        &mut Options {
            dry_run: true,
            out: Some(&mut out),
            ..Options::default()
        },
        &fake,
    )
    .unwrap();
    assert_eq!(out, b"cargo nextest run --locked\n");
    assert_eq!(fake.calls().len(), 1);
    fake.assert_done().unwrap();
}

#[test]
fn apptest_missing_nextest_has_install_hint() {
    let fake = Fake::new();
    fake.expect("cargo", ["nextest", "run", "--locked"])
        .returns("", "error: no such command: `nextest`\n", 101);
    let error = run(&Cancellation::new(), &mut Options::default(), &fake).unwrap_err();
    assert!(
        error
            .to_string()
            .contains("Try: cargo install cargo-nextest --locked")
    );
    assert_eq!(exit_code(&error), 101);
    fake.assert_done().unwrap();
}

#[test]
fn apptest_deferred_targets_do_not_run_commands() {
    let fake = Fake::new();
    for target in ["web", "e2e", "contract"] {
        let error = run(
            &Cancellation::new(),
            &mut Options {
                web: target == "web",
                e2e: target == "e2e",
                contract: target == "contract",
                ..Options::default()
            },
            &fake,
        )
        .unwrap_err();
        assert!(error.to_string().contains("M4"));
        assert!(error.to_string().contains("\nTry:"));
    }
    assert!(fake.calls().is_empty());
}

#[test]
fn apptest_missing_cargo_and_coverage_have_repair_hints() {
    for (cover, name, expected) in [
        (false, "cargo", "install Rust and Cargo"),
        (
            false,
            "cargo-nextest",
            "cargo install cargo-nextest --locked",
        ),
        (
            true,
            "cargo-llvm-cov",
            "cargo install cargo-llvm-cov --locked",
        ),
    ] {
        let fake = Fake::new();
        fake.expect("cargo", [] as [&str; 0])
            .fail(ExecError::NotFound { name: name.into() });
        let error = run(
            &Cancellation::new(),
            &mut Options {
                cover,
                ..Options::default()
            },
            &fake,
        )
        .unwrap_err();
        assert!(
            error.to_string().ends_with(&format!(
                "Try: {expected}, then run spinup test{}",
                if cover { " --cover" } else { "" }
            )),
            "{error}"
        );
        assert_eq!(exit_code(&error), 1);
        fake.assert_done().unwrap();
    }
}

#[test]
fn apptest_dry_run_write_error_has_repair_hint() {
    let fake = Fake::new();
    let mut out = std::io::Cursor::new([0u8; 0]);
    let error = run(
        &Cancellation::new(),
        &mut Options {
            dry_run: true,
            out: Some(&mut out),
            ..Options::default()
        },
        &fake,
    )
    .unwrap_err();
    assert!(error.to_string().contains("write test command:"));
    assert!(error.to_string().contains("\nTry:"));
    assert!(fake.calls().is_empty());
}
