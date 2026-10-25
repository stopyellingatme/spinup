//! The tests of `next_lock` helpers and the plan JSON. Each test name holds `plan`, so the
//! unit check `test(plan)` selects it.

use serde_json::value::to_raw_value;
use spinup_manifest::lock::Class;
use tempfile::TempDir;

use super::*;
use crate::contract::FileDecision;
use crate::plan::build_plan;
use crate::plan::testing::{Fixture, lock, spec, version, write};

fn decision(path: &str, action: Action) -> FileDecision {
    FileDecision {
        path: path.into(),
        from: None,
        action,
        class: Class::Scaffold,
        template: "base/x.tmpl".into(),
        data: b"secret content".to_vec(),
        mode: 0o755,
        reason: String::new(),
    }
}

#[test]
fn plan_json_matches_go_marshal_indent() {
    let mut renamed = decision("b<c>.txt", Action::Rename);
    renamed.from = Some("a&b.txt".into());
    renamed.reason = "x".into();
    let mut kept = decision("keep.txt", Action::Keep);
    kept.template = String::new();
    kept.mode = 0;
    let report = PlanReport {
        steps: vec![
            Plan {
                from: "0.1.0".into(),
                to: "0.2.0".into(),
                files: vec![renamed, kept],
                tombstoned: vec!["gone.txt".into()],
                missing_managed: Vec::new(),
            },
            Plan {
                from: "0.2.0".into(),
                to: "0.3.0".into(),
                ..Plan::default()
            },
        ],
    };
    let want = r#"{
  "steps": [
    {
      "from": "0.1.0",
      "to": "0.2.0",
      "files": [
        {
          "path": "b\u003cc\u003e.txt",
          "from": "a\u0026b.txt",
          "action": "rename",
          "class": "scaffold",
          "template": "base/x.tmpl",
          "mode": "0755",
          "reason": "x"
        },
        {
          "path": "keep.txt",
          "action": "keep",
          "class": "scaffold",
          "mode": "0000"
        }
      ],
      "tombstoned": [
        "gone.txt"
      ],
      "missing_managed": null
    },
    {
      "from": "0.2.0",
      "to": "0.3.0",
      "files": [],
      "tombstoned": null,
      "missing_managed": null
    }
  ]
}"#;
    let got = String::from_utf8(report.to_json().unwrap()).unwrap();
    assert_eq!(got, want);
    assert!(!got.contains("secret"), "the plan JSON holds file content");
    let empty = PlanReport::default().to_json().unwrap();
    assert_eq!(empty, b"{\n  \"steps\": []\n}");
}

#[test]
fn plan_lock_answers_round_trip() {
    let mut old = lock("", None);
    old.answers
        .insert("big".into(), RawValue::from_string("1e3".into()).unwrap());
    old.answers
        .insert("name".into(), to_raw_value(&"app").unwrap());
    let decoded = answers(&old).unwrap();
    assert_eq!(decoded["name"], "app");
    let mut completed = decoded.clone();
    completed.insert("name".into(), "other".into());
    completed.insert("added".into(), true.into());
    let raw = raw_answers(&old, &completed).unwrap();
    assert_eq!(raw["big"].get(), "1e3", "the old number token stays");
    assert_eq!(raw["name"].get(), r#""other""#);
    assert_eq!(raw["added"].get(), "true");
}

#[test]
fn plan_next_lock_selects_target_packs() {
    let root = TempDir::new().unwrap();
    write(root.path(), "file.txt", "old", 0o644);
    let old = lock("old", Some(Class::Scaffold));
    let newer = version("0.2.0", &[spec("file.txt", "new")]);
    let mut fixture = Fixture::default();
    fixture.answers.insert("name".into(), "app".into());
    let plan = build_plan(&fixture.input(root.path(), &old, None, &newer)).unwrap();
    let target = PackSet {
        names: Some(vec!["base".into()]),
        plugins: Vec::new(),
    };
    let next = next_lock(
        &fixture.templates(),
        &old,
        &plan,
        &newer,
        &fixture.answers,
        &target,
    )
    .unwrap();
    assert_eq!(next.packs, ["base"]);
    assert!(next.plugins.is_empty());
    assert_eq!(next.answers["name"].get(), r#""app""#);
    assert_eq!(next.files["file.txt"].hash, hash(b"new"));
    assert_eq!(next.files["file.txt"].template, "base/file.txt.tmpl");
    let mut invalid = old.clone();
    invalid.schema = 2;
    let err = next_lock(
        &fixture.templates(),
        &invalid,
        &plan,
        &newer,
        &fixture.answers,
        &target,
    )
    .unwrap_err();
    assert!(format!("{err:#}").contains("Try:"));
}
