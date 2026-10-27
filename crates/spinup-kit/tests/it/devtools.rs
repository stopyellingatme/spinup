//! Tests for the development page and its request recorder.

use std::sync::Arc;

use axum::{Router, body::Body, http::Request, routing::post};
use spinup_kit::{
    app::{App, Migration, MigrationStatusFn},
    config::Base,
};

use crate::httpx::{get_req, send};

fn dev_app() -> App {
    App::new(&Base {
        env: "dev".into(),
        database_url: "postgres://app:password@db/todo".into(),
        ..Base::default()
    })
}

#[tokio::test]
async fn devtools_mounts_only_in_development() {
    for env in ["dev", "production", ""] {
        let app = App::new(&Base {
            env: env.into(),
            ..Base::default()
        })
        .with_devtools(None);
        for path in ["/_dev", "/_dev/", "/_dev/requests.json"] {
            let res = send(app.router(), get_req(path)).await;
            assert_eq!(res.status, if env == "dev" { 200 } else { 404 });
            if env != "dev" {
                continue;
            }
            assert_eq!(res.headers["cache-control"], "no-store");
            assert!(
                res.headers["content-security-policy"]
                    .to_str()
                    .unwrap()
                    .contains("default-src 'none'")
            );
            if path.ends_with(".json") {
                let values: serde_json::Value = serde_json::from_str(&res.body).unwrap();
                assert_eq!(values.as_array().unwrap().len(), 2);
                assert_eq!(values[0]["path"], "/_dev");
                assert_eq!(values[1]["path"], "/_dev/");
            } else {
                assert!(res.body.contains("Development"));
            }
        }
    }
}

#[tokio::test]
async fn devtools_masks_escapes_and_lists_routes() {
    let app = dev_app()
        .name("<script>alert(1)</script>")
        .version("1.0")
        .with_devtools(None)
        .routes(Router::new().route("/items", post(|| async { axum::http::StatusCode::CREATED })));
    send(
        app.router(),
        Request::post("/items").body(Body::empty()).unwrap(),
    )
    .await;
    let res = send(app.router(), get_req("/_dev")).await;
    for needle in [
        "postgres://app:****@db/todo",
        "&lt;script&gt;alert(1)&lt;/script&gt;",
        "POST /items",
        "GET /healthz",
        "not configured",
        "DATABASE_URL",
    ] {
        assert!(res.body.contains(needle), "missing {needle}: {}", res.body);
    }
    assert!(!res.body.contains("password@"));
    assert!(!res.body.contains("<script>"));
}

#[tokio::test]
async fn devtools_records_concurrent_requests() {
    let router = dev_app()
        .with_devtools(None)
        .routes(Router::new().route("/items", post(|| async { axum::http::StatusCode::CREATED })))
        .router();
    let mut tasks = tokio::task::JoinSet::new();
    for _ in 0..80 {
        let router = router.clone();
        tasks.spawn(async move {
            send(
                router,
                Request::post("/items?secret=hidden")
                    .header("x-request-id", "request-1")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .status
        });
    }
    while let Some(result) = tasks.join_next().await {
        assert_eq!(result.unwrap(), 201);
    }
    let res = send(router, get_req("/_dev/requests.json")).await;
    let values: serde_json::Value = serde_json::from_str(&res.body).unwrap();
    assert_eq!(values.as_array().unwrap().len(), 50);
    for value in values.as_array().unwrap() {
        assert_eq!(value["path"], "/items");
        assert_eq!(value["method"], "POST");
        assert_eq!(value["status"], 201);
        assert_eq!(value["request_id"], "request-1");
        assert!(value["duration_ms"].is_number());
    }
}

#[tokio::test]
async fn devtools_pending_migrations() {
    let status: MigrationStatusFn = Arc::new(|| {
        Box::pin(async {
            Ok(vec![
                Migration {
                    name: "applied-secret".into(),
                    applied: true,
                },
                Migration {
                    name: "pending<script>".into(),
                    applied: false,
                },
            ])
        })
    });
    let res = send(
        dev_app().with_devtools(Some(status)).router(),
        get_req("/_dev"),
    )
    .await;
    assert!(res.body.contains("pending&lt;script&gt;"));
    assert!(!res.body.contains("applied-secret"));
    let status: MigrationStatusFn =
        Arc::new(|| Box::pin(async { anyhow::bail!("database unavailable") }));
    let res = send(
        dev_app().with_devtools(Some(status)).router(),
        get_req("/_dev"),
    )
    .await;
    assert!(res.body.contains("unavailable: database unavailable"));
    assert!(res.body.contains("Try:"));
}
