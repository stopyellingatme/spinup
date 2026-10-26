use super::*;
use std::fs;

#[test]
fn routes_list_fixture_and_sort_sources() {
    let root = tempfile::tempdir().unwrap();
    assert_eq!(list(root.path()).unwrap().len(), 3);
    fs::create_dir_all(root.path().join("api/openapi")).unwrap();
    fs::create_dir_all(root.path().join("api/src")).unwrap();
    fs::write(
        root.path().join("api/openapi/bundle.gen.yaml"),
        include_str!("fixtures/bundle.yaml"),
    )
    .unwrap();
    fs::write(
        root.path().join("api/src/routes.rs"),
        include_str!("fixtures/routes.rs.txt"),
    )
    .unwrap();
    let routes = list(root.path()).unwrap();
    let triples: Vec<_> = routes
        .iter()
        .map(|r| (r.method.as_str(), r.path.as_str(), r.source.as_str()))
        .collect();
    assert_eq!(
        triples,
        [
            ("GET", "/api/todos", "openapi:listTodos"),
            ("POST", "/api/todos", "openapi:createTodo"),
            ("GET", "/custom", "routes.rs:4"),
            ("POST", "/custom", "routes.rs:4"),
            ("GET", "/healthz", "kit"),
            ("*", "/raw", "routes.rs:8"),
            ("GET", "/readyz", "kit"),
            ("GET", "/version", "kit"),
        ]
    );
}

#[test]
fn routes_formats_equal_go() {
    let root = tempfile::tempdir().unwrap();
    let routes = list(root.path()).unwrap();
    let mut out = Vec::new();
    print(&mut out, &routes, false).unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "METHOD  PATH      SOURCE\nGET     /healthz  kit\nGET     /readyz   kit\nGET     /version  kit\n"
    );
    let mut out = Vec::new();
    print(&mut out, &routes[..1], true).unwrap();
    assert_eq!(
        out,
        b"[{\"method\":\"GET\",\"path\":\"/healthz\",\"source\":\"kit\"}]\n"
    );
    let mut out = Vec::new();
    print(&mut out, &[], true).unwrap();
    assert_eq!(out, b"[]\n");
}

#[test]
fn routes_errors_include_action_and_repair() {
    let root = tempfile::tempdir().unwrap();
    fs::create_dir_all(root.path().join("api/openapi")).unwrap();
    for yaml in ["paths: [bad", "paths: []", "paths: { /bad: { get: 42 } }"] {
        fs::write(root.path().join("api/openapi/bundle.gen.yaml"), yaml).unwrap();
        let error = list(root.path()).unwrap_err().to_string();
        assert!(error.contains("parse OpenAPI"), "{error}");
        assert!(error.contains("\nTry:"), "{error}");
    }
    let mut out = std::io::Cursor::new([0u8; 0]);
    assert!(
        print(&mut out, &[], false)
            .unwrap_err()
            .to_string()
            .contains("\nTry:")
    );
}

#[test]
fn routes_reference_bundle_and_pack_seed() {
    let root = tempfile::tempdir().unwrap();
    fs::create_dir_all(root.path().join("api/openapi")).unwrap();
    fs::write(
        root.path().join("api/openapi/bundle.gen.yaml"),
        include_str!("../../../../testdata/reference/todo-rs/todo-api/openapi.gen.yaml"),
    )
    .unwrap();
    let routes = list(root.path()).unwrap();
    assert!(routes.iter().any(|r| r.method == "DELETE"
        && r.path == "/api/todos/{id}"
        && r.source == "openapi:deleteTodo"));
    assert!(
        routes
            .iter()
            .any(|r| r.method == "PUT" && r.source == "openapi:updateTodo")
    );
    fs::write(
        root.path().join("api/openapi/bundle.gen.yaml"),
        "paths: {}\n",
    )
    .unwrap();
    fs::create_dir_all(root.path().join("api/src")).unwrap();
    fs::write(
        root.path().join("api/src/routes.rs"),
        include_str!("../../../spinup-packs/overlay/api/files/api/src/routes.rs"),
    )
    .unwrap();
    assert_eq!(list(root.path()).unwrap().len(), 3);
}

#[test]
fn routes_decode_literals_and_ignore_handler_calls() {
    let routes = rust::routes(
        r###"
fn routes() {
    let character = '"';
    Router::new()
        .route("/\x63af\u{e9}", get(|| async { post(handler) }).patch(handler))
        .route(r##"/quote"#"##, axum::routing::delete(handler))
        .route(PATH, get(handler))
        .route(concat!("/skip"), get(handler));
}
"###,
    )
    .unwrap();
    assert_eq!(
        routes
            .iter()
            .map(|r| (&*r.method, &*r.path))
            .collect::<Vec<_>>(),
        [
            ("GET", "/café"),
            ("PATCH", "/café"),
            ("DELETE", "/quote\"#")
        ]
    );
    for source in ["/* unclosed", "let s = \"unclosed", "let s = r#\"unclosed"] {
        assert!(rust::routes(source).is_err());
    }
}

#[test]
fn routes_json_uses_go_escaping() {
    let mut out = Vec::new();
    print(
        &mut out,
        &[Route {
            method: "GET".into(),
            path: "/<>&\u{2028}\u{2029}".into(),
            source: "kit".into(),
        }],
        true,
    )
    .unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "[{\"method\":\"GET\",\"path\":\"/\\u003c\\u003e\\u0026\\u2028\\u2029\",\"source\":\"kit\"}]\n"
    );
}
