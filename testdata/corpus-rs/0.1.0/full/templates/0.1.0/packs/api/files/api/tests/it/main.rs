//! The API smoke tests. These tests need no database or socket.

use kit::testkit::http::{Req, send};

#[tokio::test]
async fn health_and_version() {
    let app = kit::app::App::new(&kit::config::Base::default())
        .name(api::openapi::openapi().info.title)
        .version("test")
        .router();
    let health = send(&app, Req::get("/healthz")).await;
    assert_eq!(health.status, 200);
    assert_eq!(health.json()["status"], "ok");
    let version = send(&app, Req::get("/version")).await;
    assert_eq!(version.status, 200);
    assert_eq!(version.json()["version"], "test");
}

#[test]
fn openapi_without_configuration() {
    let doc = api::openapi::openapi();
    assert!(!doc.info.title.is_empty());
    assert_eq!(doc.servers.unwrap()[0].url, "/");
    assert!(doc.info.description.is_none());
    assert!(doc.info.license.is_none());
}
