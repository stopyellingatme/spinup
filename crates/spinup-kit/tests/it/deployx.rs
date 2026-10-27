//! The `deployx` tests of the kit (Go `kit/deployx/deployx_test.go`).

use std::net::SocketAddr;

use axum::Router;
use axum::body::Body;
use axum::extract::ConnectInfo;
use axum::http::Request;
use spinup_kit::deployx::{self, Info, Options};

use crate::httpx::send;

fn request(peer: Option<&str>, token: &str, header: Option<&str>) -> Request<Body> {
    let mut req = Request::get(deployx::PATH)
        .header("authorization", format!("Bearer {token}"))
        .body(Body::empty())
        .unwrap();
    if let Some(peer) = peer {
        let addr: SocketAddr = peer.parse().unwrap();
        req.extensions_mut().insert(ConnectInfo(addr));
    }
    if let Some(h) = header {
        req.headers_mut().insert(
            axum::http::HeaderName::from_bytes(h.as_bytes()).unwrap(),
            "127.0.0.1".parse().unwrap(),
        );
    }
    req
}

fn token_file(dir: &tempfile::TempDir, text: &str) -> std::path::PathBuf {
    let path = dir.path().join("token");
    std::fs::write(&path, text).unwrap();
    path
}

#[tokio::test]
async fn deployx_info_access() {
    let dir = tempfile::tempdir().unwrap();
    let options = Options {
        version: "v1".into(),
        git_sha: "abc123".into(),
        ..Options::default()
    }
    .token_file(token_file(&dir, "shared-secret\n"))
    .migration_version(|| async { Ok("42".to_string()) });
    let router: Router = deployx::routes(options);
    for (peer, token, status) in [
        (Some("8.8.8.8:20"), "", 403),
        (Some("10.0.0.2:20"), "", 200),
        (Some("192.168.1.2:20"), "", 200),
        (Some("172.16.0.1:20"), "", 200),
        (Some("[::1]:20"), "", 200),
        (Some("[fd00::1]:20"), "", 200),
        (Some("[::ffff:10.0.0.2]:20"), "", 200),
        (Some("8.8.8.8:20"), "shared-secret", 200),
        (Some("8.8.8.8:20"), "wrong", 403),
        (None, "", 403),
    ] {
        let a = send(router.clone(), request(peer, token, None)).await;
        assert_eq!(a.status, status, "{peer:?} {token:?}");
        if status == 200 {
            assert_eq!(a.headers["cache-control"], "no-store");
            assert_eq!(a.headers["content-type"], "application/json");
            let info: Info = serde_json::from_str(&a.body).unwrap();
            assert_eq!(info.migration_version, "42");
            assert_eq!(info.version, "v1");
            assert_eq!(info.git_sha, "abc123");
            assert!(a.body.ends_with('\n'));
        } else {
            assert_eq!(a.body, "forbidden\n");
        }
    }
}

#[tokio::test]
async fn deployx_info_fails_closed() {
    let dir = tempfile::tempdir().unwrap();
    for file in [dir.path().join("missing"), token_file(&dir, "")] {
        let router = deployx::routes(Options::default().token_file(file));
        let a = send(router, request(Some("8.8.8.8:1"), "", None)).await;
        assert_eq!(a.status, 403);
    }
}

#[tokio::test]
async fn deployx_info_migration_failure() {
    let router = deployx::routes(
        Options::default()
            .migration_version(|| async { anyhow::bail!("sensitive database error") }),
    );
    let a = send(router, request(Some("127.0.0.1:20"), "", None)).await;
    assert_eq!(a.status, 503);
    assert!(!a.body.contains("sensitive"), "{}", a.body);
}

#[tokio::test]
async fn deployx_forwarded_requests_require_token() {
    let dir = tempfile::tempdir().unwrap();
    let router = deployx::routes(Options::default().token_file(token_file(&dir, "token")));
    for header in ["forwarded", "x-forwarded-for", "x-real-ip"] {
        for token in ["", "token"] {
            let a = send(
                router.clone(),
                request(Some("10.0.0.2:123"), token, Some(header)),
            )
            .await;
            let want = if token.is_empty() { 403 } else { 200 };
            assert_eq!(a.status, want, "{header} {token:?}");
        }
    }
}
