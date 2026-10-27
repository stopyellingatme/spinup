//! Tests for the embedded SPA routes.

use axum::body::Body;
use axum::http::Request;
use spinup_kit::{app::App, config::Base, spa::EmbeddedFiles};

use crate::httpx::{get_req, send};

static FILES: EmbeddedFiles = &[
    ("index.html", b"<!doctype html><title>SPA</title>"),
    ("_app/immutable/app.abc123.js", b"console.log('app')"),
    ("plain.txt", b"plain"),
    ("folder/private.txt", b"private"),
];

#[tokio::test]
async fn spa_routes_and_headers() {
    let router = App::new(&Base::default()).with_spa(&FILES).router();
    for (method, path, status, cache, content, body) in [
        ("GET", "/", 200, "no-cache", "text/html", "SPA"),
        ("GET", "/index.html", 200, "no-cache", "text/html", "SPA"),
        ("GET", "/deep/link", 200, "no-cache", "text/html", "SPA"),
        ("HEAD", "/deep/link", 200, "no-cache", "text/html", ""),
        (
            "GET",
            "/_app/immutable/app.abc123.js",
            200,
            "public, max-age=31536000, immutable",
            "javascript",
            "console.log",
        ),
        (
            "HEAD",
            "/_app/immutable/app.abc123.js",
            200,
            "public, max-age=31536000, immutable",
            "javascript",
            "",
        ),
        ("GET", "/plain.txt", 200, "", "text/plain", "plain"),
        ("GET", "/folder/", 200, "no-cache", "text/html", "SPA"),
        ("GET", "/_app/immutable/missing.js", 404, "", "", ""),
        ("POST", "/deep/link", 405, "", "", ""),
        ("GET", "/healthz", 200, "", "application/json", "ok"),
        ("GET", "/readyz", 200, "", "application/json", "ok"),
    ] {
        let res = send(
            router.clone(),
            Request::builder()
                .method(method)
                .uri(path)
                .body(Body::empty())
                .unwrap(),
        )
        .await;
        assert_eq!(res.status, status, "{method} {path}");
        assert_eq!(
            res.headers
                .get("cache-control")
                .map_or("", |v| v.to_str().unwrap()),
            cache
        );
        assert!(
            res.headers
                .get("content-type")
                .map_or("", |v| v.to_str().unwrap())
                .contains(content)
        );
        assert!(res.body.contains(body));
        assert!(!res.headers.contains_key("etag"));
        assert!(!res.headers.contains_key("last-modified"));
        if method == "HEAD" {
            assert!(res.body.is_empty());
        }
        if status == 405 {
            assert_eq!(res.headers["allow"], "GET, HEAD");
        }
        if cache == "no-cache" {
            assert!(
                res.headers["content-security-policy"]
                    .to_str()
                    .unwrap()
                    .contains("script-src 'self' 'unsafe-inline'")
            );
        }
    }
}

#[tokio::test]
async fn spa_reserved_paths() {
    let router = App::new(&Base::default()).with_spa(&FILES).router();
    for prefix in [
        "api", "healthz", "readyz", "metrics", "version", "_deploy", "_dev",
    ] {
        for method in ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"] {
            let res = send(
                router.clone(),
                Request::builder()
                    .method(method)
                    .uri(format!("/{prefix}/missing"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await;
            assert_eq!(res.status, 404, "{method} {prefix}");
            assert!(!res.headers.contains_key("allow"));
            assert!(!res.body.contains("SPA"));
        }
    }
}

#[tokio::test]
async fn spa_empty_files_and_conditional_requests() {
    static EMPTY: EmbeddedFiles = &[];
    let res = send(
        App::new(&Base::default()).with_spa(&EMPTY).router(),
        get_req("/"),
    )
    .await;
    assert_eq!(res.status, 404);
    let req = Request::get("/plain.txt")
        .header("range", "bytes=0-1")
        .header("if-modified-since", "Wed, 21 Oct 2099 07:28:00 GMT")
        .header("if-none-match", "*")
        .body(Body::empty())
        .unwrap();
    let res = send(App::new(&Base::default()).with_spa(&FILES).router(), req).await;
    assert_eq!(res.status, 200);
    assert_eq!(res.body, "plain");
    assert!(!res.headers.contains_key("accept-ranges"));
}
