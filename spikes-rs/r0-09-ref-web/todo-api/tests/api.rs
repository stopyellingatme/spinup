//! Contract tests of the todo API. They follow the Go tests that
//! `spinup g resource todo title:string done:bool 'due:date?'` writes in an
//! app with the auth module (`api/internal/handlers/todos_test.go`): every
//! operation needs a signed-in user, and a user sees only their own todos.

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use axum::Router;
use axum::body::Body;
use axum::extract::ConnectInfo;
use axum::http::{Request, StatusCode};
use http_body_util::BodyExt;
use kit::Problem;
use kit::auth::password::Params;
use kit::auth::{Auth, Options};
use serde_json::Value;
use sqlx::PgPool;
use sqlx::postgres::{PgListener, PgPoolOptions};
use todo_api::todos::{Todo, TodoList};
use tower::ServiceExt;

struct Answer {
    status: StatusCode,
    content_type: String,
    set_cookie: String,
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
    send_as(app, None, method, path, body).await
}

/// One request with the session cookie `cookie`. The peer is a loopback
/// address, so the auth rate limit does not apply.
async fn send_as(
    app: &Router,
    cookie: Option<&str>,
    method: &str,
    path: &str,
    body: Option<&str>,
) -> Answer {
    let mut b = Request::builder()
        .method(method)
        .uri(path)
        .header("content-type", "application/json");
    if let Some(c) = cookie {
        b = b.header("cookie", format!("session={c}"));
    }
    let mut req = b
        .body(body.map_or_else(Body::empty, |b| Body::from(b.to_owned())))
        .unwrap();
    req.extensions_mut()
        .insert(ConnectInfo(SocketAddr::from(([127, 0, 0, 1], 40000))));
    let res = app.clone().oneshot(req).await.unwrap();
    let status = res.status();
    let header = |name: &str| {
        res.headers()
            .get(name)
            .map(|v| v.to_str().unwrap().to_owned())
            .unwrap_or_default()
    };
    let content_type = header("content-type");
    let set_cookie = header("set-cookie");
    let body = res.into_body().collect().await.unwrap().to_bytes().to_vec();
    Answer {
        status,
        content_type,
        set_cookie,
        body,
    }
}

/// Auth options for tests: no Secure cookie, cheap argon2id parameters, an
/// empty environment, and no cleanup task.
fn auth_options() -> Options {
    Options {
        secure: false,
        params: Params {
            memory: 64,
            time: 1,
            threads: 1,
            salt_len: 16,
            key_len: 32,
        },
        lookup: Arc::new(|_| None),
        cleanup: Duration::ZERO,
        ..Options::default()
    }
}

/// The app of `serve`: the todo and auth routes behind the auth layer and
/// the kit middleware.
fn app(pool: PgPool) -> Router {
    let auth = Auth::new(pool.clone(), auth_options()).unwrap();
    let api = todo_api::router_with_auth(pool.clone(), &auth, None).unwrap();
    kit::app::router(api, Some(pool))
}

/// A router whose pool never connects: for requests that fail before a query.
fn app_without_db() -> Router {
    let pool = PgPoolOptions::new()
        .connect_lazy("postgres://nobody@127.0.0.1:1/none")
        .unwrap();
    let auth = Auth::new(pool.clone(), auth_options()).unwrap();
    kit::app::router(todo_api::router_with_auth(pool, &auth, None).unwrap(), None)
}

/// Registers `email` and returns the value of its session cookie.
async fn register(app: &Router, email: &str) -> String {
    let body = serde_json::json!({"email": email, "password": "correct horse battery"});
    let res = send(app, "POST", "/api/auth/register", Some(&body.to_string())).await;
    assert_eq!(
        res.status,
        StatusCode::CREATED,
        "{}",
        String::from_utf8_lossy(&res.body)
    );
    let value = res.set_cookie.split(';').next().unwrap();
    value.strip_prefix("session=").unwrap().to_owned()
}

#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn todos_crud(pool: PgPool) {
    let app = app(pool);
    let me = register(&app, "owner@example.com").await;
    let me = Some(me.as_str());
    let res = send_as(
        &app,
        me,
        "POST",
        "/api/todos",
        Some(r#"{"title": "title 1", "done": true, "due": "2026-01-02"}"#),
    )
    .await;
    assert_eq!(res.status, StatusCode::CREATED);
    let created: Todo = res.json();
    let path = format!("/api/todos/{}", created.id);

    let res = send_as(&app, me, "GET", &path, None).await;
    assert_eq!(res.status, StatusCode::OK);
    assert_eq!(res.json::<Todo>(), created);
    let raw: Value = res.json();
    assert_eq!(raw["due"], "2026-01-02");

    let res = send_as(&app, me, "GET", "/api/todos?limit=10", None).await;
    assert_eq!(res.status, StatusCode::OK);
    let list: TodoList = res.json();
    assert_eq!(list.total, 1);
    assert_eq!(list.items, vec![created.clone()]);

    let res = send_as(
        &app,
        me,
        "PUT",
        &path,
        Some(r#"{"title": "title 2", "done": false, "due": null}"#),
    )
    .await;
    assert_eq!(res.status, StatusCode::OK);
    let updated: Todo = res.json();
    assert_eq!(updated.id, created.id);
    assert_ne!(updated.title, created.title);
    assert_eq!(updated.due, None);
    assert_eq!(
        send_as(&app, me, "GET", &path, None).await.json::<Todo>(),
        updated
    );

    let res = send_as(&app, me, "DELETE", &path, None).await;
    assert_eq!(res.status, StatusCode::NO_CONTENT);
    for method in ["GET", "DELETE"] {
        let res = send_as(&app, me, method, &path, None).await;
        assert_eq!(res.status, StatusCode::NOT_FOUND, "{method}");
        assert_eq!(res.content_type, "application/problem+json");
        let p: Problem = res.json();
        assert_eq!(p.detail.as_deref(), Some("The record does not exist."));
        assert!(p.request_id.is_some());
    }
    let res = send_as(
        &app,
        me,
        "PUT",
        &path,
        Some(r#"{"title": "t", "done": false}"#),
    )
    .await;
    assert_eq!(res.status, StatusCode::NOT_FOUND);
}

#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn todos_belong_to_their_owner(pool: PgPool) {
    let app = app(pool);
    let owner = register(&app, "a@example.com").await;
    let other = register(&app, "b@example.com").await;
    let created: Todo = send_as(
        &app,
        Some(&owner),
        "POST",
        "/api/todos",
        Some(r#"{"title": "private", "done": false}"#),
    )
    .await
    .json();
    let path = format!("/api/todos/{}", created.id);
    let list: TodoList = send_as(&app, Some(&other), "GET", "/api/todos", None)
        .await
        .json();
    assert_eq!(list.total, 0);
    assert!(list.items.is_empty());
    for (method, body) in [
        ("GET", None),
        ("PUT", Some(r#"{"title": "taken", "done": true}"#)),
        ("DELETE", None),
    ] {
        let res = send_as(&app, Some(&other), method, &path, body).await;
        assert_eq!(res.status, StatusCode::NOT_FOUND, "{method}");
    }
    let still: Todo = send_as(&app, Some(&owner), "GET", &path, None).await.json();
    assert_eq!(still, created);
}

#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn todos_need_a_signed_in_user(pool: PgPool) {
    let app = app(pool);
    for (method, path, body) in [
        ("GET", "/api/todos", None),
        (
            "POST",
            "/api/todos",
            Some(r#"{"title": "a", "done": false}"#),
        ),
        ("GET", "/api/todos/1", None),
        (
            "PUT",
            "/api/todos/1",
            Some(r#"{"title": "a", "done": false}"#),
        ),
        ("DELETE", "/api/todos/1", None),
    ] {
        let res = send(&app, method, path, body).await;
        assert_eq!(res.status, StatusCode::UNAUTHORIZED, "{method} {path}");
        assert_eq!(res.content_type, "application/problem+json");
        let p: Problem = res.json();
        assert_eq!(p.detail.as_deref(), Some("A signed-in user is required."));
    }
}

#[sqlx::test(migrator = "todo_api::MIGRATOR")]
async fn todos_pages(pool: PgPool) {
    let app = app(pool);
    let me = register(&app, "pages@example.com").await;
    let me = Some(me.as_str());
    for i in 0..5 {
        let body = format!(r#"{{"title": "t{i}", "done": false}}"#);
        assert_eq!(
            send_as(&app, me, "POST", "/api/todos", Some(&body))
                .await
                .status,
            StatusCode::CREATED
        );
    }
    let list: TodoList = send_as(&app, me, "GET", "/api/todos?limit=2&offset=3", None)
        .await
        .json();
    assert_eq!(list.total, 5);
    let titles: Vec<_> = list.items.iter().map(|t| t.title.as_str()).collect();
    assert_eq!(titles, ["t3", "t4"]);
    let list: TodoList = send_as(&app, me, "GET", "/api/todos", None).await.json();
    assert_eq!(list.items.len(), 5);
    // The range check runs after the owner check (as in Go), so it needs a user.
    for path in ["/api/todos?limit=201", "/api/todos?offset=-1"] {
        let res = send_as(&app, me, "GET", path, None).await;
        assert_eq!(res.status, StatusCode::UNPROCESSABLE_ENTITY, "{path}");
        assert_eq!(res.content_type, "application/problem+json");
    }
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
        (
            "bad date",
            "POST",
            "/api/todos",
            r#"{"title": "a", "done": true, "due": "soon"}"#,
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
}

#[tokio::test]
async fn todos_require_fields() {
    let app = app_without_db();
    let all = vec!["done", "title"];
    let cases: Vec<(&str, Vec<&str>)> = vec![
        ("{}", all.clone()),
        ("null", all),
        (r#"{"title": "title 1", "due": "2026-01-02"}"#, vec!["done"]),
        (
            r#"{"title": "title 1", "done": null, "due": "2026-01-02"}"#,
            vec!["done"],
        ),
        (
            r#"{"title": null, "done": true, "due": "2026-01-02"}"#,
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
    assert_eq!(doc["paths"].as_object().unwrap().len(), 2);
    let schemas: Vec<_> = doc["components"]["schemas"]
        .as_object()
        .unwrap()
        .keys()
        .cloned()
        .collect();
    assert_eq!(schemas, ["Problem", "Todo", "TodoInput", "TodoList"]);
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
    assert_eq!(
        run(&pool, "status").await,
        "pending  00001_job_queue\npending  00003_create_auth\npending  00004_password_resets\npending  00005_oidc_identities\npending  00006_create_todos\n"
    );
    assert_eq!(
        run(&pool, "up").await,
        "migrate: 5 pending\napplied  00001_job_queue\napplied  00003_create_auth\napplied  00004_password_resets\napplied  00005_oidc_identities\napplied  00006_create_todos\n"
    );
    assert_eq!(run(&pool, "up").await, "migrate: no pending migrations\n");
    assert_eq!(run(&pool, "version").await, "6\n");

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

    for m in [
        "00006_create_todos",
        "00005_oidc_identities",
        "00004_password_resets",
        "00003_create_auth",
    ] {
        assert_eq!(run(&pool, "down").await, format!("rolled back  {m}\n"));
    }
    assert_eq!(run(&pool, "down").await, "migrate: nothing to roll back\n");
    assert_eq!(
        run(&pool, "status").await,
        "applied  00001_job_queue\npending  00003_create_auth\npending  00004_password_resets\npending  00005_oidc_identities\npending  00006_create_todos\n"
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
