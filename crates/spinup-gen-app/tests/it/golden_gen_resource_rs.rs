//! Golden output and schema contracts for the Rust resource generator.

use spinup_exec::{os_runner::OsRunner, runner::Cancellation};
use spinup_gen::{contract::Input, fields, part::Spec};
use spinup_manifest::lock::Lock;
use spinup_vfs::plan::Plan;

fn fixture() -> (tempfile::TempDir, Lock) {
    let root = tempfile::tempdir().unwrap();
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "auth".into()];
    // The API pack is not available yet. This fixture has only the generator inputs.
    let mut plan = Plan::new();
    plan.add(spinup_vfs::plan::File {
        path: "spinup.lock".into(),
        data: serde_json::to_vec(&lock).unwrap(),
        ..Default::default()
    })
    .unwrap();
    spinup_vfs::apply::apply(root.path(), &plan, Default::default()).unwrap();
    (root, lock)
}

fn assert_names(plan: &Plan, singular: &str, plural: &str) {
    let text = plan
        .files
        .iter()
        .map(|file| String::from_utf8(file.data.clone()).unwrap())
        .collect::<Vec<_>>()
        .join("\n");
    for name in [
        singular.to_string(),
        format!("{singular}Input"),
        format!("{singular}List"),
    ] {
        assert!(
            text.contains(&format!("#[schema(as = {name})]")),
            "missing schema {name}"
        );
    }
    for name in [
        format!("list{plural}"),
        format!("create{singular}"),
        format!("get{singular}"),
        format!("update{singular}"),
        format!("delete{singular}"),
    ] {
        assert!(
            text.contains(&format!("operation_id = \"{name}\"")),
            "missing operation {name}"
        );
    }
    assert!(text.contains("body = Problem"));
}

#[test]
fn golden_gen_resource_rs_todo_owned() {
    if !spinup_testutil::golden::selected("gen-resource-rs") {
        return;
    }
    let (root, lock) = fixture();
    let generator = spinup_gen::contract::GENERATORS
        .iter()
        .find(|generator| generator.name == "resource")
        .unwrap();
    let matches = (generator.args)(clap::Command::new("resource"))
        .try_get_matches_from(["resource", "--api-only"])
        .unwrap();
    let args = ["todo", "title:string", "done:bool", "due:date?"].map(String::from);
    let input = Input {
        root: root.path(),
        lock: &lock,
        args: &args,
        matches: &matches,
        runner: &OsRunner,
        cancel: &Cancellation::new(),
    };
    let mut plan = (generator.run)(&input).unwrap().plan;
    assert_names(&plan, "Todo", "Todos");
    let again = (generator.run)(&input).unwrap().plan;
    assert_eq!(plan, again, "the raw plan must be deterministic");
    spinup_gen::rustfmt::format(root.path(), &mut plan, &OsRunner, &Cancellation::new()).unwrap();
    let formatted = plan.clone();
    spinup_gen::rustfmt::format(root.path(), &mut plan, &OsRunner, &Cancellation::new()).unwrap();
    assert_eq!(plan, formatted, "formatting must be stable");
    let files = plan
        .files
        .iter()
        .map(|file| (file.path.clone(), file.data.clone(), 0o644))
        .collect::<Vec<_>>();
    spinup_testutil::golden::compare_rs("gen-resource-rs", &files).unwrap();
}

#[test]
fn golden_gen_resource_rs_every_field_type() {
    let (root, lock) = fixture();
    let mut args = vec!["widget".to_string()];
    for (index, ty) in [
        "string",
        "text",
        "int",
        "bigint",
        "bool",
        "float",
        "decimal",
        "time",
        "date",
        "uuid",
        "json",
        "enum:small|large",
        "ref:widget?",
    ]
    .iter()
    .enumerate()
    {
        args.push(format!("value_{index}:{ty}"));
        if !ty.ends_with('?') {
            args.push(format!("optional_{index}:{ty}?"));
        }
    }
    let spec = Spec {
        root: root.path(),
        lock: &lock,
        name: fields::Name::parse("widget").unwrap(),
        fields: fields::parse(&args[1..]).unwrap(),
        owned: true,
        shared: false,
    };
    let mut plan = spinup_gen_app::resource_rs::plan_api(&spec, true).unwrap();
    assert_names(&plan, "Widget", "Widgets");
    spinup_gen::rustfmt::format(root.path(), &mut plan, &OsRunner, &Cancellation::new()).unwrap();
    let model = String::from_utf8(plan.get("api/src/models/widget.rs").unwrap().data).unwrap();
    for expected in [
        "serde_json::Value",
        "uuid::Uuid",
        "::text::numeric",
        "time::serde::rfc3339::option",
        "date_format::option",
        "value_12_id",
        "#[schema(inline)]",
    ] {
        assert!(model.contains(expected), "missing {expected}");
    }
}
