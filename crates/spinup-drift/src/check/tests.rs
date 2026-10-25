//! The tests of `check` and `doctor` (Go `drift_test.go`).

use std::sync::Arc;

use spinup_doctor::registry::{Check, Context, Outcome, Scope, Status};
use spinup_exec::fake::Fake;
use spinup_ui::exit::exit_code;

use super::*;
use crate::diff::testing::{Fixture, printer};

fn run(o: &Options, allow_behind: bool) -> (Result<()>, String) {
    let (p, out) = printer();
    let result = check(o, allow_behind, &p);
    (result, out.contents())
}

fn code(result: &Result<()>) -> i32 {
    result.as_ref().err().map_or(0, exit_code)
}

#[test]
fn check_conflict_markers() {
    for (data, want) in [
        (
            "<<<<<<< ours\nours\n=======\ntheirs\n>>>>>>> theirs\n",
            true,
        ),
        (
            "<<<<<<< ours\nours\n||||||| base\nbase\n=======\ntheirs\n>>>>>>> theirs\n",
            true,
        ),
        (
            "<<<<<<< ours\r\nours\r\n=======\r\ntheirs\r\n>>>>>>> theirs\r\n",
            true,
        ),
        ("<<<<<<< ours\n", false),
        ("=======\n", false),
        (">>>>>>> theirs\n", false),
        (">>>>>>> theirs\n<<<<<<< ours\n", false),
        ("Title\n=======\n", false),
        (" =======\n", false),
        ("========\n", false),
        ("normal\n", false),
    ] {
        assert_eq!(conflict_markers(data.as_bytes()), want, "{data:?}");
    }
}

#[test]
fn check_behind_and_allow_behind() {
    let f = Fixture::new();
    let (result, out) = run(&f.options, false);
    assert_eq!(code(&result), exit::DRIFT, "{out}");
    assert_eq!(
        out,
        "spinup.lock: newer template available: 0.2.0 (behind)\nTry: spinup upgrade\n"
    );
    let ui = spinup_ui::error::find_ui_error(result.as_ref().unwrap_err()).unwrap();
    assert_eq!(ui.what, "Application check failed");
    let (result, out) = run(&f.options, true);
    result.unwrap();
    assert_eq!(out, "");
}

#[test]
fn check_reports_unavailable_template_with_drift() {
    let f = Fixture::new();
    f.write("file.txt", "edited\n");
    let o = Options {
        json: true,
        ..f.unavailable()
    };
    let (result, out) = run(&o, false);
    assert_eq!(code(&result), exit::DRIFT, "{out}");
    let json: serde_json::Value = serde_json::from_str(&out).unwrap();
    let states: Vec<&str> = json["problems"]
        .as_array()
        .unwrap()
        .iter()
        .map(|p| p["state"].as_str().unwrap())
        .collect();
    assert_eq!(states, ["drift", "template-unavailable"]);
    assert!(out.contains("\"try\": \"spinup eject file.txt or spinup restore file.txt\""));
    let (result, out) = run(&o, true);
    assert_eq!(code(&result), exit::DRIFT, "{out}");
    assert!(!out.contains("template-unavailable"), "{out}");
    assert!(out.contains("\"state\": \"drift\""), "{out}");
}

#[test]
fn check_missing_managed_and_conflict() {
    let mut f = Fixture::new();
    f.lock.files.insert(
        "notes.txt".into(),
        spinup_manifest::lock::FileEntry {
            class: Class::Scaffold,
            template: "base/notes.txt".into(),
            hash: hash(b"x"),
            mode: "0644".into(),
        },
    );
    f.save();
    f.write("notes.txt", "<<<<<<< ours\na\n=======\nb\n>>>>>>> theirs\n");
    std::fs::remove_file(f.root().join("file.txt")).unwrap();
    let (result, out) = run(&f.options, true);
    assert_eq!(code(&result), exit::DRIFT);
    assert_eq!(
        out,
        "file.txt: managed file is missing (missing)\nTry: spinup restore file.txt\n\
         notes.txt: file contains conflict markers (conflict)\nTry: resolve the conflict markers in notes.txt\n"
    );
}

#[test]
fn check_invalid_lock() {
    let f = Fixture::new();
    f.write("spinup.lock", "{");
    let (result, out) = run(&f.options, false);
    assert_eq!(code(&result), exit::DRIFT);
    assert!(out.starts_with("spinup.lock: "), "{out}");
    assert!(
        out.contains("(invalid-lock)\nTry: repair spinup.lock\n"),
        "{out}"
    );
}

#[cfg(unix)]
#[test]
fn check_reports_non_regular_path_with_other_drift() {
    let mut f = Fixture::new();
    f.lock.files.insert(
        "other.txt".into(),
        spinup_manifest::lock::FileEntry {
            class: Class::Managed,
            template: "base/other.txt".into(),
            hash: hash(b"original\n"),
            mode: "0644".into(),
        },
    );
    f.save();
    f.write("other.txt", "edited\n");
    std::fs::remove_file(f.root().join("file.txt")).unwrap();
    std::os::unix::fs::symlink(f.root().join("other.txt"), f.root().join("file.txt")).unwrap();
    let (result, out) = run(&f.options, true);
    assert_eq!(code(&result), exit::DRIFT);
    assert!(
        out.contains("tracked path is not a regular file (not-regular)"),
        "{out}"
    );
    assert!(out.contains("managed file differs"), "{out}");
}

fn failing(_: &Context) -> Outcome {
    Outcome::fail("broken", "repair it")
}

fn passing(ctx: &Context) -> Outcome {
    let version = ctx
        .dep::<String>("cli_version")
        .cloned()
        .unwrap_or_default();
    let status = if ctx.quick && ctx.runner.is_some() {
        Status::Ok
    } else {
        Status::Warn
    };
    Outcome::new(status, format!("version {version}"))
}

static CHECKS: [Check; 2] = [
    Check {
        id: "b-fail",
        title: "fails",
        scope: Scope::Local,
        quick: true,
        run: failing,
    },
    Check {
        id: "a-pass",
        title: "passes",
        scope: Scope::Local,
        quick: true,
        run: passing,
    },
];

#[test]
fn check_doctor_output() {
    let dir = tempfile::tempdir().unwrap();
    let o = Options {
        dir: dir.path().into(),
        version: "0.0.0-dev".into(),
        ..Options::default()
    };
    let runner: Arc<dyn Runner> = Arc::new(Fake::new());
    let (p, out) = printer();
    let selected = registry::select(&CHECKS, Scope::Local, false);
    let result = doctor_with(selected.clone(), &o, true, Arc::clone(&runner), &p);
    assert_eq!(code(&result), exit::ERROR);
    assert_eq!(
        out.contents(),
        "ok\ta-pass\tversion 0.0.0-dev\nfail\tb-fail\tbroken\nTry: repair it\n"
    );
    let json = Options { json: true, ..o };
    let (p, out) = printer();
    let result = doctor_with(selected, &json, true, runner, &p);
    assert_eq!(code(&result), exit::ERROR);
    let value: serde_json::Value = serde_json::from_str(&out.contents()).unwrap();
    assert_eq!(value[0]["id"], "a-pass");
    assert!(value[0].get("try").is_none());
    assert_eq!(value[1]["try"], "repair it");
}

#[test]
fn check_doctor_runs_registered_checks_outside_a_project() {
    let dir = tempfile::tempdir().unwrap();
    let o = Options {
        dir: dir.path().into(),
        json: true,
        ..Options::default()
    };
    let (p, out) = printer();
    doctor(&o, true, Arc::new(Fake::new()), &p).unwrap();
    let value: serde_json::Value = serde_json::from_str(&out.contents()).unwrap();
    assert!(value.is_array());
    let missing = Options {
        dir: dir.path().join("missing"),
        ..o
    };
    let text = format!(
        "{:#}",
        doctor(&missing, true, Arc::new(Fake::new()), &p).unwrap_err()
    );
    assert!(
        text.starts_with("cannot inspect application files: "),
        "{text}"
    );
}
