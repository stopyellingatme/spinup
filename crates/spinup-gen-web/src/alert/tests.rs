//! Validation must precede project writes.
use super::*;
use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_manifest::lock::Lock;
use std::path::Path;

#[test]
fn prometheus_duration_bounds() {
    for value in [
        "0",
        "0s",
        "5m",
        "1y2w3d4h5m6s7ms",
        "250ms",
        "9223372036854ms",
    ] {
        assert!(validation::valid_duration(value), "{value}");
    }
    for value in [
        "",
        "banana",
        "-1m",
        "1.5m",
        "1m1h",
        "1h1h",
        "5",
        "1us",
        " 5m",
        "5m ",
        "999999999999999999999999y",
        "293y",
        "9223372036855ms",
        "9223372036s855ms",
    ] {
        assert!(!validation::valid_duration(value), "{value}");
    }
}
#[test]
fn yaml_comments_order_and_duplicates() {
    let source = "# Keep this comment\ngroups:\n  - name: custom\n    rules: []\n";
    let rule = rule("HighErrors", "up == 0", "5m", "warning", "Too many errors").unwrap();
    let data = edit(source, "HighErrors", rule.clone()).unwrap();
    assert_eq!(
        data,
        "# Keep this comment\ngroups:\n  - name: custom\n    rules:\n      - alert: HighErrors\n        expr: up == 0\n        for: 5m\n        labels:\n          severity: warning\n        annotations:\n          summary: Too many errors\n"
    );
    assert!(
        edit(&data, "HighErrors", rule.clone())
            .unwrap_err()
            .to_string()
            .contains("already exists")
    );
    for source in [
        "groups: {}\n",
        "groups:\n  - name: custom\n    rules: {}\n",
        "groups:\n  - name: custom\n",
    ] {
        assert!(
            edit(source, "HighErrors", rule.clone())
                .unwrap_err()
                .to_string()
                .contains("Try:")
        );
    }
    let data = edit("# Keep\ngroups: []\n", "HighErrors", rule.clone()).unwrap();
    assert!(data.starts_with("# Keep\n"));
    assert!(data.contains("alert: HighErrors"));
    assert!(
        edit("{}\n", "HighErrors", rule)
            .unwrap()
            .contains("name: custom")
    );
}
#[test]
#[allow(
    clippy::disallowed_methods,
    reason = "Temporary test fixture, never a project write from a planner."
)]
fn validation_failure_and_skip() -> Result<()> {
    let root = tempfile::tempdir()?;
    std::fs::create_dir_all(root.path().join("monitoring/alerts"))?;
    let original = "# Keep\ngroups: []\n";
    std::fs::write(root.path().join(CUSTOM), original)?;
    let mut lock = Lock::new();
    lock.packs.push("monitoring".into());
    let matches =
        args(clap::Command::new("alert")).try_get_matches_from(["alert", "--expr", "up == 0"])?;
    let runner = Fake::new();
    let cancel = Cancellation::new();
    let names = ["HighErrors".into()];
    let input = Input {
        root: root.path(),
        lock: &lock,
        args: &names,
        matches: &matches,
        runner: &runner,
        cancel: &cancel,
    };
    let skipped = plan_with_tool(&input, None)?;
    assert!(skipped.commands.is_empty());
    assert_eq!(skipped.edits, [CUSTOM]);
    assert!(skipped.next[1].contains("promtool is unavailable"));
    runner
        .expect("promtool", ["check", "rules"])
        .returns("", "bad expression", 1);
    let error = plan_with_tool(&input, Some(Path::new("promtool")))
        .unwrap_err()
        .to_string();
    assert!(
        error.contains("before writing")
            && error.contains("bad expression")
            && error.contains("Try:")
    );
    let path = &runner.calls()[0].args[2];
    assert!(!Path::new(path).exists(), "staged rules leaked");
    assert_eq!(std::fs::read_to_string(root.path().join(CUSTOM))?, original);
    runner
        .expect("promtool", ["check", "rules"])
        .returns("", "", 0);
    let result = plan_with_tool(&input, Some(Path::new("promtool")))?;
    assert_eq!(
        result.commands[0].argv,
        ["promtool", "check", "rules", CUSTOM]
    );
    runner.assert_done()?;
    Ok(())
}
#[test]
fn invalid_rule_flags() {
    for (expr, duration, severity) in [
        ("", "5m", "warning"),
        (" ", "5m", "warning"),
        ("up", "banana", "warning"),
        ("up", "5m", "low"),
    ] {
        assert!(
            rule("Example", expr, duration, severity, "")
                .unwrap_err()
                .to_string()
                .contains("Try:")
        );
    }
}

#[test]
#[allow(
    clippy::disallowed_methods,
    reason = "Temporary rules fixture for the installed promtool."
)]
fn installed_promtool_checks_planned_rules() -> Result<()> {
    let Ok(tool) = spinup_exec::lookup::look_path("promtool") else {
        eprintln!("skip: promtool is unavailable");
        return Ok(());
    };
    let root = tempfile::tempdir()?;
    std::fs::create_dir_all(root.path().join("monitoring/alerts"))?;
    let original = "# Keep\ngroups: []\n";
    std::fs::write(root.path().join(CUSTOM), original)?;
    let mut lock = Lock::new();
    lock.packs.push("monitoring".into());
    let names = ["HighErrors".into()];
    let runner = spinup_exec::os_runner::OsRunner;
    let cancel = Cancellation::new();
    for (expr, valid) in [
        ("up == 0", true),
        ("up ==", false),
        ("not_a_function(up)", false),
    ] {
        let matches =
            args(clap::Command::new("alert")).try_get_matches_from(["alert", "--expr", expr])?;
        let input = Input {
            root: root.path(),
            lock: &lock,
            args: &names,
            matches: &matches,
            runner: &runner,
            cancel: &cancel,
        };
        let result = plan_with_tool(&input, Some(&tool));
        assert_eq!(result.is_ok(), valid, "{expr}: {result:?}");
        if let Err(error) = result {
            assert!(error.to_string().contains("before writing"));
        }
        assert_eq!(std::fs::read_to_string(root.path().join(CUSTOM))?, original);
    }
    Ok(())
}
