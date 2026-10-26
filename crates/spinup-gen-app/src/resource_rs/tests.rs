use super::*;
use spinup_gen::fields::{self, Name};
use spinup_manifest::lock::Lock;

fn spec<'a>(root: &'a std::path::Path, lock: &'a Lock, owned: bool) -> Spec<'a> {
    Spec {
        root,
        lock,
        name: Name::parse("todo").unwrap(),
        fields: fields::parse(&["title:string", "done:bool", "due:date?"]).unwrap(),
        owned,
        shared: false,
    }
}

#[test]
fn api_paths_and_no_tests() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    let spec = spec(root.path(), &lock, true);
    let plan = plan_api(&spec, true).unwrap();
    assert_eq!(
        plan.paths(),
        [
            "api/src/handlers/todos.rs",
            "api/src/handlers/todos_tests.rs",
            "api/src/models/todo.rs"
        ]
    );
    let without = plan_api(&spec, false).unwrap();
    assert!(without.get("api/src/handlers/todos_tests.rs").is_none());
    let handlers =
        String::from_utf8(without.get("api/src/handlers/todos.rs").unwrap().data).unwrap();
    assert!(!handlers.contains("mod tests;"));
    assert!(handlers.contains("AuthUser"));
    assert!(plan.files.iter().all(|file| file.class == "scaffold"));
}

#[test]
fn reference_sql_todo() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    let plan = plan_api(&spec(root.path(), &lock, true), false).unwrap();
    let reference = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/reference/todo-rs/.sqlx");
    let queries: Vec<String> = std::fs::read_dir(reference)
        .unwrap()
        .map(|entry| {
            let value: serde_json::Value =
                serde_json::from_slice(&std::fs::read(entry.unwrap().path()).unwrap()).unwrap();
            value["query"].as_str().unwrap().to_string()
        })
        .collect();
    let pattern = regex::Regex::new(r##"sqlx::query(?:_(?:as|scalar))?!\(\s*(?:\w+,\s*)?(?:r#"([\s\S]*?)"#|"((?:[^"\\]|\\.)*)")"##).unwrap();
    let mut count = 0;
    for file in plan.files {
        let text = String::from_utf8(file.data).unwrap();
        for captures in pattern.captures_iter(&text) {
            let query = captures.iter().skip(1).flatten().next().unwrap().as_str();
            assert!(
                queries.iter().any(|expected| expected == query),
                "SQL does not match the reference: {query}"
            );
            count += 1;
        }
    }
    assert_eq!(count, 6);
}

#[test]
fn shared_resources_require_auth_without_owner_queries() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    let mut spec = spec(root.path(), &lock, false);
    for shared in [false, true] {
        spec.shared = shared;
        let plan = plan_api(&spec, true).unwrap();
        let handlers =
            String::from_utf8(plan.get("api/src/handlers/todos.rs").unwrap().data).unwrap();
        let model = String::from_utf8(plan.get("api/src/models/todo.rs").unwrap().data).unwrap();
        assert_eq!(handlers.contains("AuthUser"), shared);
        assert_eq!(handlers.contains("status = 401"), shared);
        assert!(!model.contains("user_id"));
        assert!(model.contains("FROM todos ORDER BY id LIMIT $1 OFFSET $2"));
        assert!(model.contains("SET title = $2, done = $3, due = $4"));
    }
}

#[test]
fn required_references_seed_parents_before_requests() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    let mut setup = Plan::new();
    setup.add(File {
        path: "api/migrations/00001_create_tags.up.sql".into(),
        data: b"CREATE TABLE tags (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);".to_vec(),
        ..File::default()
    }).unwrap();
    spinup_vfs::apply::apply(root.path(), &setup, Default::default()).unwrap();
    let mut spec = spec(root.path(), &lock, false);
    spec.fields = fields::parse(&["tag:ref:tag", "parent:ref:todo?"]).unwrap();
    let plan = plan_api(&spec, true).unwrap();
    let tests =
        String::from_utf8(plan.get("api/src/handlers/todos_tests.rs").unwrap().data).unwrap();
    let insert = tests
        .find("INSERT INTO tags (name) VALUES ('sample')")
        .unwrap();
    let request = tests.find("Req::post").unwrap();
    assert!(insert < request);
    assert!(tests.contains("SELECT max(id) FROM tags"));
    assert!(!tests.contains("INSERT INTO todos"));
    for absent in ["tower::", "reqwest::", "http_body_util::"] {
        assert!(!tests.contains(absent));
    }
}

#[test]
fn fields_preserve_nullable_types_and_sql_bind_order() {
    let root = tempfile::tempdir().unwrap();
    let lock = Lock::new();
    let mut spec = spec(root.path(), &lock, true);
    spec.fields = fields::parse(&[
        "amount:decimal",
        "extra:json?",
        "stamp:time?",
        "code:uuid",
        "state:enum:in_progress|done?",
        "input:enum:draft|sent",
    ])
    .unwrap();
    let plan = plan_api(&spec, false).unwrap();
    let model = String::from_utf8(plan.get("api/src/models/todo.rs").unwrap().data).unwrap();
    for expected in [
        "amount::text AS \"amount!\"",
        "VALUES ($1::text::numeric, $2, $3, $4, $5, $6, $7)",
        "WHERE id = $1 AND user_id = $8",
        "pub extra: Option<serde_json::Value>",
        "#[schema(value_type = Option<Object>)]",
        "time::serde::rfc3339::option",
        "pub state: Option<TodoStateValue>",
        "pub enum TodoInputValue",
        "map(TodoStateValue::parse).transpose()?",
        "must be a JSON object",
        "must be a decimal number",
    ] {
        assert!(model.contains(expected), "missing {expected}");
    }
}
