use super::*;
use spinup_exec::fake::Fake;
use spinup_exec::runner::{Cancellation, ExecError};

#[test]
fn build_is_registered_and_runs_once() {
    let root = tempfile::tempdir().unwrap();
    let fake = Fake::new();
    fake.expect("cargo", ["build", "--locked"])
        .returns("", "", 0);
    run_steps(
        &[PostStep::Build, PostStep::Build],
        &StepInput {
            root: root.path(),
            files: &[],
            runner: &fake,
            cancel: &Cancellation::new(),
        },
    )
    .unwrap();
    fake.assert_done().unwrap();
    assert_eq!(fake.calls()[0].dir, Some(root.path().join("api")));
    assert_eq!(fake.calls()[0].args, ["build", "--locked"]);
}

#[test]
fn build_failures_keep_output_and_the_correct_retry() {
    for (output, hint) in [
        (SQLX_NO_CACHE, SQLX_PREPARE_TRY),
        (
            "syntax error",
            "Try: run cargo build --locked in api/ by hand",
        ),
    ] {
        for stdout in [false, true] {
            let fake = Fake::new();
            fake.expect("cargo", ["build", "--locked"]).returns(
                if stdout { output } else { "compiler stdout" },
                if stdout { "compiler stderr" } else { output },
                1,
            );
            let error = run_steps(
                &[PostStep::Build],
                &StepInput {
                    root: std::path::Path::new("app"),
                    files: &[],
                    runner: &fake,
                    cancel: &Cancellation::new(),
                },
            )
            .unwrap_err()
            .to_string();
            assert!(error.contains(output), "{error}");
            assert!(error.ends_with(hint), "{error}");
            assert_eq!(error.matches("Try:").count(), 1, "{error}");
            fake.assert_done().unwrap();
        }
    }
    let fake = Fake::new();
    fake.expect("cargo", ["build", "--locked"])
        .fail(ExecError::NotFound {
            name: "cargo".into(),
        });
    let error = run_steps(
        &[PostStep::Build],
        &StepInput {
            root: std::path::Path::new("app"),
            files: &[],
            runner: &fake,
            cancel: &Cancellation::new(),
        },
    )
    .unwrap_err()
    .to_string();
    assert!(error.contains("was not found"), "{error}");
    assert!(
        error.ends_with("Try: run cargo build --locked in api/ by hand"),
        "{error}"
    );
}

#[test]
fn missing_runner_is_checked_before_build() {
    let fake = Fake::new();
    let error = run_steps(
        &[PostStep::Build, PostStep::Client],
        &StepInput {
            root: std::path::Path::new("app"),
            files: &[],
            runner: &fake,
            cancel: &Cancellation::new(),
        },
    )
    .unwrap_err()
    .to_string();
    assert!(error.contains("no runner is registered"), "{error}");
    assert!(fake.calls().is_empty());
}
