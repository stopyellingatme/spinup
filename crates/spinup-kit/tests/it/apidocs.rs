//! Parity tests for the Scalar reference routes.

use std::io::Read;

use axum::body::Body;
use axum::http::Request;
use http_body_util::BodyExt;
use spinup_kit::{
    apidocs::{self, Options},
    app::App,
    config::Base,
};
use tower::ServiceExt;

use crate::httpx::{get_req, send};

#[tokio::test]
async fn apidocs_routes() {
    let router = apidocs::routes(Options {
        spec_yaml: Some("openapi: 3.1.0".into()),
        ..Options::default()
    });
    for (path, content, body) in [
        ("/api/openapi.yaml", "application/yaml", "openapi: 3.1.0"),
        (
            "/api/docs",
            "text/html; charset=utf-8",
            "<title>API reference</title>",
        ),
        (
            "/api/docs/boot.js",
            "text/javascript; charset=utf-8",
            "Scalar.createApiReference('#app',",
        ),
    ] {
        let res = send(router.clone(), get_req(path)).await;
        assert_eq!(res.status, 200);
        assert_eq!(res.headers["content-type"], content);
        assert_eq!(res.headers["cache-control"], "no-cache");
        assert_eq!(
            res.headers["content-security-policy"],
            apidocs::CONTENT_SECURITY_POLICY
        );
        assert!(res.body.contains(body));
        if path == "/api/docs" {
            for needle in [
                "id=\"app\"",
                "src=\"/api/docs/scalar.js\"",
                "src=\"/api/docs/boot.js\"",
            ] {
                assert!(res.body.contains(needle));
            }
            assert!(!res.body.contains("<script>"));
        }
        if path.ends_with("boot.js") {
            for needle in [
                "url: '/api/openapi.yaml'",
                "withDefaultFonts: false",
                "telemetry: false",
                "hideClientButton: false",
            ] {
                assert!(res.body.contains(needle));
            }
            assert!(!res.body.contains("proxyUrl"));
        }
    }
}

#[tokio::test]
async fn apidocs_scalar_encoding() {
    let compressed = include_bytes!("../../assets/scalar.js.gz");
    let mut plain = Vec::new();
    flate2::read::GzDecoder::new(&compressed[..])
        .read_to_end(&mut plain)
        .unwrap();
    assert!(String::from_utf8_lossy(&plain).contains("window.Scalar={createApiReference:"));
    let router = apidocs::routes(Options::default());
    for (accept, gzip) in [
        ("", false),
        ("gzip", true),
        ("br, gzip;q=0.5", true),
        ("gzip;q=0", false),
        ("gzip;q=0.0, *;q=1", false),
        ("*;q=1", true),
        ("gzip;q=invalid", false),
        ("xgzip", false),
        ("GZIP;Q=0.3", true),
        ("gzip;q=NaN", false),
        ("gzip;q=2", false),
    ] {
        let req = Request::get("/api/docs/scalar.js")
            .header("accept-encoding", accept)
            .body(Body::empty())
            .unwrap();
        let res = router.clone().oneshot(req).await.unwrap();
        assert_eq!(res.status(), 200);
        assert_eq!(
            res.headers()
                .get("content-encoding")
                .map(|v| v.to_str().unwrap()),
            gzip.then_some("gzip")
        );
        assert_eq!(res.headers()["vary"], "Accept-Encoding");
        assert_eq!(res.headers()["cache-control"], "public, max-age=86400");
        assert_eq!(
            res.headers()["content-security-policy"],
            apidocs::CONTENT_SECURITY_POLICY
        );
        assert_eq!(
            res.headers()["content-type"],
            "text/javascript; charset=utf-8"
        );
        let bytes = res.into_body().collect().await.unwrap().to_bytes();
        assert_eq!(&bytes[..], if gzip { &compressed[..] } else { &plain[..] });
    }
}

#[tokio::test]
async fn apidocs_spec_sources() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("bundle.gen.yaml");
    let router = apidocs::routes(Options {
        spec_yaml: Some("embedded".into()),
        dir: Some(dir.path().into()),
        ..Options::default()
    });
    for value in [None, Some("disk first"), Some("disk changed"), None] {
        if let Some(value) = value {
            std::fs::write(&path, value).unwrap();
        } else if path.exists() {
            std::fs::remove_file(&path).unwrap();
        }
        let res = send(router.clone(), get_req("/api/openapi.yaml")).await;
        assert_eq!(res.status, 200);
        assert_eq!(res.body, value.unwrap_or("embedded"));
    }
}

#[tokio::test]
async fn apidocs_missing_spec_and_title_escape() {
    let router = apidocs::routes(Options {
        title: "Example </title><script>alert(\"x\")</script>".into(),
        ..Options::default()
    });
    let res = send(router.clone(), get_req("/api/docs")).await;
    assert!(!res.body.contains("<script>alert"));
    assert!(res.body.contains("Example &lt;/title&gt;&lt;script&gt;"));
    let res = send(router, get_req("/api/openapi.yaml")).await;
    assert_eq!(res.status, 404);
    assert_eq!(res.headers["content-type"], "application/problem+json");
    assert_eq!(res.headers["cache-control"], "no-cache");
    assert_eq!(
        res.headers["content-security-policy"],
        apidocs::CONTENT_SECURITY_POLICY
    );
    assert!(res.body.contains("spinup g resource"));
    assert!(res.body.contains("spinup g openapi"));
}

#[tokio::test]
async fn apidocs_app_mount() {
    for (env, flag, option, mounted) in [
        ("production", false, true, false),
        ("production", true, true, true),
        ("dev", false, true, true),
        ("production", true, false, false),
        ("dev", false, false, false),
        ("", false, true, false),
    ] {
        let mut app = App::new(&Base {
            env: env.into(),
            api_docs: flag,
            ..Base::default()
        });
        if option {
            app = app.with_api_docs("embedded");
        }
        app = app.with_devtools(None);
        for path in [
            "/api/docs",
            "/api/openapi.yaml",
            "/api/docs/boot.js",
            "/api/docs/scalar.js",
        ] {
            let res = send(app.router(), get_req(path)).await;
            assert_eq!(
                res.status,
                if mounted { 200 } else { 404 },
                "{env} {flag} {option} {path}"
            );
            assert_eq!(res.headers["x-content-type-options"], "nosniff");
        }
        if env == "dev" {
            let res = send(app.router(), get_req("/_dev")).await;
            assert_eq!(res.body.contains("href=\"/api/docs\""), mounted);
            assert!(!res.body.contains("<script"));
        }
    }
}
