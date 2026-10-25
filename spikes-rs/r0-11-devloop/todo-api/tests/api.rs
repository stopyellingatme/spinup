//! Contract tests of the todo API. They follow the Go golden tests in
//! `testdata/golden/gen-resource/api/internal/handlers/todos_test.go`.

use axum::Router;
use axum::body::Body;
use axum::http::{Request, StatusCode};
use http_body_util::BodyExt;
use kit::Problem;
use serde_json::Value;
use sqlx::PgPool;
use sqlx::postgres::{PgListener, PgPoolOptions};
use todo_api::models::todo::{Todo, TodoList};
use tower::ServiceExt;

struct Answer {
    status: StatusCode,
    content_type: String,
    body: Vec<u8>,
}

impl Answer {
    fn json<T: serde::de::DeserializeOwned>(&self) -> T {
        serde_json::from_slice(&self.body).unwrap_or_else(|e| {
            panic!("{e}: {}", String::from_utf8_lossy(&self.body));
        })
    }
}

async fn send(app: &Router, method: &str, path: &str, body: Option<&str>) -> Answer {
    let req = Request::builder()
        .method(method)
        .uri(path)
        .header("content-type", "application/json")
        .body(body.map_or_else(Body::empty, |b| Body::from(b.to_owned())))
        .unwrap();
    let res = app.clone().oneshot(req).await.unwrap();
    let status = res.status();
    let content_type = res
        .headers()
        .get("content-type")
        .map(|v| v.to_str().unwrap().to_owned())
        .unwrap_or_default();
    let body = res.into_body().collect().await.unwrap().to_bytes().to_vec();
    Answer {
        status,
        content_type,
        body,
    }
}

fn app(pool: PgPool) -> Router {
    kit::app::router(todo_api::router(pool.clone()), Some(pool))
}

/// A router whose pool never connects: for requests that fail before a query.
fn app_without_db() -> Router {
    let pool = PgPoolOptions::new()
        .connect_lazy("postgres://nobody@127.0.0.1:1/none")
        .unwrap();
    kit::app::router(todo_api::router(pool), None)
}

#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn todos_crud(pool: PgPool) {
    let app = app(pool);
    let res = send(
        &app,
        "POST",
        "/api/todos",
        Some(r#"{"title": "title 1", "done": true, "due": "2026-01-02", "priority": "low"}"#),
    )
    .await;
    assert_eq!(res.status, StatusCode::CREATED);
    let created: Todo = res.json();
    let path = format!("/api/todos/{}", created.id);

    let res = send(&app, "GET", &path, None).await;
    assert_eq!(res.status, StatusCode::OK);
    assert_eq!(res.json::<Todo>(), created);
    let raw: Value = res.json();
    assert_eq!(raw["due"], "2026-01-02");
    assert_eq!(raw["priority"], "low");

    let res = send(&app, "GET", "/api/todos?limit=10", None).await;
    assert_eq!(res.status, StatusCode::OK);
    let list: TodoList = res.json();
    assert_eq!(list.total, 1);
    assert_eq!(list.items, vec![created.clone()]);

    let res = send(
        &app,
        "PUT",
        &path,
        Some(r#"{"title": "title 2", "done": false, "due": null, "priority": "high"}"#),
    )
    .await;
    assert_eq!(res.status, StatusCode::OK);
    let updated: Todo = res.json();
    assert_eq!(updated.id, created.id);
    assert_ne!(updated.title, created.title);
    assert_eq!(updated.due, None);
    assert_eq!(send(&app, "GET", &path, None).await.json::<Todo>(), updated);

    let res = send(&app, "DELETE", &path, None).await;
    assert_eq!(res.status, StatusCode::NO_CONTENT);
    for method in ["GET", "DELETE"] {
        let res = send(&app, method, &path, None).await;
        assert_eq!(res.status, StatusCode::NOT_FOUND, "{method}");
        assert_eq!(res.content_type, "application/problem+json");
        let p: Problem = res.json();
        assert_eq!(p.detail.as_deref(), Some("The record does not exist."));
        assert!(p.request_id.is_some());
    }
    let res = send(
        &app,
        "PUT",
        &path,
        Some(r#"{"title": "t", "done": false, "priority": "high"}"#),
    )
    .await;
    assert_eq!(res.status, StatusCode::NOT_FOUND);
}

#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn todos_pages(pool: PgPool) {
    let app = app(pool);
    for i in 0..5 {
        let body = format!(r#"{{"title": "t{i}", "done": false, "priority": "normal"}}"#);
        assert_eq!(
            send(&app, "POST", "/api/todos", Some(&body)).await.status,
            StatusCode::CREATED
        );
    }
    let list: TodoList = send(&app, "GET", "/api/todos?limit=2&offset=3", None)
        .await
        .json();
    assert_eq!(list.total, 5);
    let titles: Vec<_> = list.items.iter().map(|t| t.title.as_str()).collect();
    assert_eq!(titles, ["t3", "t4"]);
    let list: TodoList = send(&app, "GET", "/api/todos", None).await.json();
    assert_eq!(list.items.len(), 5);
}

#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn readyz_checks_the_database(pool: PgPool) {
    let res = send(&app(pool), "GET", "/readyz", None).await;
    assert_eq!(res.status, StatusCode::OK);
    assert_eq!(
        res.json::<Value>(),
        serde_json::json!({"status": "ok", "checks": {"db": "ok"}})
    );
}

#[tokio::test]
async fn todos_reject_bad_input() {
    let app = app_without_db();
    let cases = [
        ("wrong type", "POST", "/api/todos", r#"{"title": 42}"#, 422),
        ("not JSON", "POST", "/api/todos", "{", 400),
        ("empty body", "POST", "/api/todos", "", 400),
        ("not an object", "POST", "/api/todos", "[1]", 422),
        ("bad id", "GET", "/api/todos/abc", "", 400),
        ("bad limit", "GET", "/api/todos?limit=abc", "", 400),
        ("limit too large", "GET", "/api/todos?limit=201", "", 422),
        ("negative offset", "GET", "/api/todos?offset=-1", "", 422),
        (
            "bad enum",
            "POST",
            "/api/todos",
            r#"{"title": "a", "done": true, "priority": "urgent"}"#,
            422,
        ),
        (
            "bad date",
            "POST",
            "/api/todos",
            r#"{"title": "a", "done": true, "due": "soon", "priority": "low"}"#,
            422,
        ),
    ];
    for (name, method, path, body, status) in cases {
        let body = (!body.is_empty()).then_some(body);
        let res = send(&app, method, path, body).await;
        assert_eq!(res.status.as_u16(), status, "{name}");
        assert_eq!(res.content_type, "application/problem+json", "{name}");
    }
    let p: Problem = send(&app, "GET", "/api/todos/abc", None).await.json();
    assert_eq!(p.errors[0].field, "id");
    let p: Problem = send(
        &app,
        "POST",
        "/api/todos",
        Some(r#"{"title": "a", "done": true, "priority": "urgent"}"#),
    )
    .await
    .json();
    assert_eq!(p.errors[0].field, "priority");
    assert_eq!(p.errors[0].message, "must be one of: low, normal, high");
}

#[tokio::test]
async fn todos_require_fields() {
    let app = app_without_db();
    let all = vec!["done", "priority", "title"];
    let cases: Vec<(&str, Vec<&str>)> = vec![
        ("{}", all.clone()),
        ("null", all),
        (
            r#"{"title": "title 1", "due": "2026-01-02", "priority": "low"}"#,
            vec!["done"],
        ),
        (
            r#"{"title": "title 1", "done": null, "due": "2026-01-02", "priority": "low"}"#,
            vec!["done"],
        ),
        (
            r#"{"title": "title 1", "done": true, "due": "2026-01-02"}"#,
            vec!["priority"],
        ),
        (
            r#"{"title": null, "done": true, "due": "2026-01-02", "priority": "low"}"#,
            vec!["title"],
        ),
    ];
    for (method, path) in [("POST", "/api/todos"), ("PUT", "/api/todos/1")] {
        for (body, want) in &cases {
            let res = send(&app, method, path, Some(body)).await;
            assert_eq!(
                res.status,
                StatusCode::UNPROCESSABLE_ENTITY,
                "{method} {body}"
            );
            assert_eq!(res.content_type, "application/problem+json");
            let p: Problem = res.json();
            let got: Vec<_> = p.errors.iter().map(|e| e.field.as_str()).collect();
            assert_eq!(&got, want, "{method} {body}");
            assert!(p.errors.iter().all(|e| e.message == "is required"));
        }
    }
}

#[test]
fn openapi_contract() {
    let doc: Value = serde_json::to_value(todo_api::openapi()).unwrap();
    assert_eq!(doc["openapi"], "3.1.0");
    // nest("/api") prefixes the paths, and one path has several methods.
    let ops = [
        ("/api/todos", "get", "listTodos"),
        ("/api/todos", "post", "createTodo"),
        ("/api/todos/{id}", "get", "getTodo"),
        ("/api/todos/{id}", "put", "updateTodo"),
        ("/api/todos/{id}", "delete", "deleteTodo"),
    ];
    for (path, method, id) in ops {
        assert_eq!(doc["paths"][path][method]["operationId"], id);
    }
    assert_eq!(
        doc["paths"]["/api/todos/greeting"]["get"]["operationId"],
        "todoGreeting"
    );
    // 20 generated resources with two paths each, plus the greeting.
    assert_eq!(doc["paths"].as_object().unwrap().len(), 41);
    let schemas = doc["components"]["schemas"].as_object().unwrap();
    for name in ["Problem", "Todo", "TodoInput", "TodoList", "Greeting"] {
        assert!(schemas.contains_key(name), "{name}");
    }
    // Three schemas per resource, plus Problem and Greeting.
    assert_eq!(schemas.len(), 62);
    for (path, method, status) in [
        ("/api/todos/{id}", "get", "404"),
        ("/api/todos", "post", "422"),
        ("/api/todos", "get", "default"),
    ] {
        let content = &doc["paths"][path][method]["responses"][status]["content"];
        assert!(
            content.get("application/problem+json").is_some(),
            "{path} {method} {status}"
        );
    }
}

/// The emitted spec is committed as `openapi.gen.yaml`. This test fails on
/// drift, as the Go golden tests do.
#[test]
fn openapi_matches_the_committed_file() {
    let want = include_str!("../openapi.gen.yaml");
    assert_eq!(todo_api::openapi().to_yaml().unwrap(), want);
}

/// The sqlx migrator owns the kit job-queue tables through its own
/// migrations table, next to the app migrations.
#[sqlx::test(migrations = false)]
async fn migrate_command_owns_kit_tables(pool: PgPool) {
    async fn run(pool: &PgPool, sub: &str) -> String {
        let mut out = Vec::new();
        kit::db::migrate_command(pool, &todo_api::MIGRATOR, sub, &mut out)
            .await
            .unwrap();
        String::from_utf8(out).unwrap()
    }
    // The app migrations: auth (00002 to 00004), then one per resource.
    let mut app: Vec<String> =
        std::fs::read_dir(concat!(env!("CARGO_MANIFEST_DIR"), "/migrations"))
            .unwrap()
            .filter_map(|e| {
                let name = e.unwrap().file_name().into_string().unwrap();
                name.strip_suffix(".up.sql").map(str::to_owned)
            })
            .collect();
    app.sort();
    assert_eq!(app.len(), 23);
    let lines = |state: &str| -> String { app.iter().map(|m| format!("{state}  {m}\n")).collect() };
    assert_eq!(
        run(&pool, "status").await,
        format!("pending  00001_job_queue\n{}", lines("pending"))
    );
    assert_eq!(
        run(&pool, "up").await,
        format!(
            "migrate: 24 pending\napplied  00001_job_queue\n{}",
            lines("applied")
        )
    );
    assert_eq!(run(&pool, "up").await, "migrate: no pending migrations\n");
    assert_eq!(run(&pool, "version").await, "24\n");

    // The trigger of the kit migration works: an insert sends a notification.
    let mut listener = PgListener::connect_with(&pool).await.unwrap();
    listener.listen("kit_insert").await.unwrap();
    sqlx::query("INSERT INTO kit_job (kind, max_attempts) VALUES ('email', 5)")
        .execute(&pool)
        .await
        .unwrap();
    let n = tokio::time::timeout(std::time::Duration::from_secs(5), listener.recv())
        .await
        .unwrap()
        .unwrap();
    assert_eq!(n.payload(), r#"{"queue" : "default"}"#);

    for m in app.iter().rev() {
        assert_eq!(run(&pool, "down").await, format!("rolled back  {m}\n"));
    }
    assert_eq!(run(&pool, "down").await, "migrate: nothing to roll back\n");
    assert_eq!(
        run(&pool, "status").await,
        format!("applied  00001_job_queue\n{}", lines("pending"))
    );
    let kit_rows: i64 = sqlx::query_scalar("SELECT count(*) FROM kit_job")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(kit_rows, 1);

    let mut out = Vec::new();
    let err = kit::db::migrate_command(&pool, &todo_api::MIGRATOR, "sideways", &mut out)
        .await
        .unwrap_err()
        .to_string();
    assert!(err.contains("\nTry: migrate up"), "{err}");
}

/// The greeting that the dev loop measurement edits counts the open todos.
#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn greeting_counts_open_todos(pool: PgPool) {
    let app = app(pool);
    for done in [false, false, true] {
        let body = format!(r#"{{"title": "t", "done": {done}, "priority": "normal"}}"#);
        let res = send(&app, "POST", "/api/todos", Some(&body)).await;
        assert_eq!(res.status, StatusCode::CREATED);
    }
    let res = send(&app, "GET", "/api/todos/greeting", None).await;
    assert_eq!(res.status, StatusCode::OK);
    let v: Value = res.json();
    assert_eq!(v["open"], 2);
    assert!(
        v["message"]
            .as_str()
            .unwrap()
            .starts_with("2 open todos (mark-")
    );
}
