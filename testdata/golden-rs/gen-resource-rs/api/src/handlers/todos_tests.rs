//! The HTTP tests of todos.

use kit::testkit::http::{Req, send};
use serde_json::{Value, json};

#[tokio::test]
async fn pg_todos_crud() {
    let Some(db) = kit::testkit::db::db().await else {
        return;
    };
    kit::db::kit_migrator().run(db.pool()).await.unwrap();
    sqlx::migrate!("./migrations").run(db.pool()).await.unwrap();
    let user_id: i64 = sqlx::query_scalar(
        "INSERT INTO users (email, password_hash) VALUES ('resource@example.test', 'unused') RETURNING id",
    )
    .fetch_one(db.pool())
    .await
    .unwrap();
    let app = super::routes()
        .split_for_parts()
        .0
        .with_state(db.pool().clone());
    let auth = kit::auth::Auth::new(
        db.pool().clone(),
        kit::auth::Options {
            secure: false,
            ..Default::default()
        },
    )
    .unwrap();
    let answer = send(&auth.layer(app.clone()), Req::get("/todos")).await;
    assert_eq!(answer.status, 401);
    let anonymous = app.clone();
    let app = auth.layer(app.layer(axum::Extension(kit::auth::User {
        id: user_id,
        email: "resource@example.test".into(),
    })));
    let input: Value =
        serde_json::from_str(r#"{"done":true,"due":"2026-01-02","title":"sample"}"#).unwrap();
    let answer = send(&app, Req::post("/todos").json_value(&input)).await;
    assert_eq!(answer.status, 201, "{}", answer.body);
    let created = answer.json();
    let id = created["id"].as_i64().unwrap();
    let path = format!("/todos/{id}");
    let answer = send(&app, Req::get(&path)).await;
    assert_eq!(answer.status, 200);
    assert_eq!(answer.json(), created);
    let answer = send(&app, Req::get("/todos?limit=1&offset=0")).await;
    assert_eq!(answer.status, 200);
    assert_eq!(answer.json()["total"], 1);
    assert_eq!(answer.json()["items"], json!([created]));
    let other_id: i64 = sqlx::query_scalar(
        "INSERT INTO users (email, password_hash) VALUES ('other@example.test', 'unused') RETURNING id",
    )
    .fetch_one(db.pool())
    .await
    .unwrap();
    let other = auth.layer(anonymous.layer(axum::Extension(kit::auth::User {
        id: other_id,
        email: "other@example.test".into(),
    })));
    let answer = send(&other, Req::get("/todos")).await;
    assert_eq!(answer.status, 200);
    assert_eq!(answer.json()["total"], 0);
    let answer = send(&other, Req::get(&path)).await;
    assert_eq!(answer.status, 404);
    let answer = send(&other, Req::put(&path).json_value(&input)).await;
    assert_eq!(answer.status, 404);
    let answer = send(&other, Req::delete(&path)).await;
    assert_eq!(answer.status, 404);
    let answer = send(&app, Req::put(&path).json_value(&input)).await;
    assert_eq!(answer.status, 200, "{}", answer.body);
    for (field, value) in input.as_object().unwrap() {
        assert_eq!(&answer.json()[field], value);
    }
    let answer = send(&app, Req::get("/todos?offset=1")).await;
    assert_eq!(answer.status, 200);
    assert_eq!(answer.json()["total"], 1);
    assert_eq!(answer.json()["items"], json!([]));
    let mut nullable = input.clone();
    nullable["due"] = Value::Null;
    let answer = send(&app, Req::put(&path).json_value(&nullable)).await;
    assert_eq!(answer.status, 200, "{}", answer.body);
    assert_eq!(answer.json()["due"], Value::Null);
    let mut invalid = input.clone();
    invalid["due"] = json!("invalid-value");
    let answer = send(&app, Req::post("/todos").json_value(&invalid)).await;
    assert_eq!(answer.status, 422, "{}", answer.body);
    let answer = send(&app, Req::get("/todos?limit=0")).await;
    assert_eq!(answer.status, 422);
    assert!(!answer.problem().errors.is_empty());
    let answer = send(&app, Req::get("/todos/invalid")).await;
    assert_eq!(answer.status, 400);
    let answer = send(&app, Req::post("/todos").json("{")).await;
    assert_eq!(answer.status, 400);
    let mut missing = input.clone();
    missing.as_object_mut().unwrap().remove("title");
    let answer = send(&app, Req::post("/todos").json_value(&missing)).await;
    assert_eq!(answer.status, 422, "{}", answer.body);
    let mut missing = input.clone();
    missing.as_object_mut().unwrap().remove("done");
    let answer = send(&app, Req::post("/todos").json_value(&missing)).await;
    assert_eq!(answer.status, 422, "{}", answer.body);
    let answer = send(&app, Req::delete(&path)).await;
    assert_eq!(answer.status, 204);
    let answer = send(&app, Req::get(&path)).await;
    assert_eq!(answer.status, 404);
    let answer = send(&app, Req::delete(&path)).await;
    assert_eq!(answer.status, 404);
}
