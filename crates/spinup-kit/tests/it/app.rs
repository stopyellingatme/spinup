//! The `app` tests of the kit (Go `kit/app/app_test.go`, `healthcheck_test.go`, and
//! `deployx_test.go`).

use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Router;
use axum::body::Body;
use axum::extract::{ConnectInfo, Request};
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::Response;
use axum::routing::get;
use spinup_kit::app::{self, App, Command, CommandInput};
use spinup_kit::config::Base;
use spinup_kit::deployx;
use spinup_kit::httpx::{Problem, RequestId};
use tokio::net::TcpListener;
use utoipa::openapi::path::{OperationBuilder, ParameterBuilder, ParameterIn, PathItemBuilder};
use utoipa::openapi::{HttpMethod, OpenApiBuilder, PathsBuilder};

use crate::httpx::{capture, get_req, send};

fn new_app() -> App {
    App::new(&Base::default())
}

#[tokio::test]
async fn app_builtin_routes() {
    let app = new_app()
        .name("todo")
        .version("1.2.3")
        .rust_version("1.99.1");
    for (path, want) in [
        ("/healthz", serde_json::json!({"status": "ok"})),
        ("/readyz", serde_json::json!({"status": "ok", "checks": {}})),
        (
            "/version",
            serde_json::json!({"name": "todo", "version": "1.2.3", "rust": "1.99.1"}),
        ),
    ] {
        let a = send(app.router(), get_req(path)).await;
        assert_eq!(a.status, 200, "{path}");
        assert_eq!(a.headers["content-type"], "application/json", "{path}");
        let got: serde_json::Value = serde_json::from_str(&a.body).unwrap();
        assert_eq!(got, want, "{path}");
        assert!(a.headers.contains_key("x-request-id"));
        assert_eq!(a.headers["x-content-type-options"], "nosniff");
    }
    // The key order of /version equals Go: name, version, then the language.
    let a = send(app.router(), get_req("/version")).await;
    assert_eq!(
        a.body,
        r#"{"name":"todo","version":"1.2.3","rust":"1.99.1"}"#
    );
}

#[tokio::test]
async fn app_unknown_path_and_method_are_problems() {
    let app = new_app();
    let req = Request::get("/nope")
        .header("x-request-id", "abc")
        .body(Body::empty())
        .unwrap();
    let a = send(app.router(), req).await;
    assert_eq!(a.status, 404);
    assert_eq!(a.headers["content-type"], "application/problem+json");
    let p: Problem = serde_json::from_str(&a.body).unwrap();
    assert_eq!(p.title, "Not Found");
    assert_eq!(p.request_id.as_deref(), Some("abc"));
    let req = Request::post("/healthz").body(Body::empty()).unwrap();
    let a = send(app.router(), req).await;
    assert_eq!(a.status, 405);
    assert_eq!(a.headers["content-type"], "application/problem+json");
}

#[tokio::test]
async fn app_readiness() {
    for fail in [false, true] {
        let (_guard, logs) = capture();
        let order = Arc::new(Mutex::new(Vec::<&str>::new()));
        let (o1, o2) = (order.clone(), order.clone());
        let check_err = format!(
            "database unavailable: user=private-user host=private-host port=5432 {}",
            "details".repeat(40)
        );
        let err_text = check_err.clone();
        let app = new_app()
            .readiness("zeta", move || {
                let o = o1.clone();
                async move {
                    o.lock().unwrap().push("zeta");
                    Ok(())
                }
            })
            .readiness("alpha", move || {
                let o = o2.clone();
                let e = err_text.clone();
                async move {
                    o.lock().unwrap().push("alpha");
                    if fail {
                        Err(anyhow::anyhow!(e))
                    } else {
                        Ok(())
                    }
                }
            });
        send(app.router(), get_req("/healthz")).await;
        assert!(
            order.lock().unwrap().is_empty(),
            "healthz checked dependencies"
        );
        let a = send(app.router(), get_req("/readyz")).await;
        assert_eq!(a.status, if fail { 503 } else { 200 });
        assert_eq!(*order.lock().unwrap(), ["alpha", "zeta"]);
        let want = if fail { "fail" } else { "ok" };
        let body: serde_json::Value = serde_json::from_str(&a.body).unwrap();
        assert_eq!(
            body,
            serde_json::json!({"status": want, "checks": {"alpha": want, "zeta": "ok"}})
        );
        let warnings: Vec<_> = logs
            .records()
            .into_iter()
            .filter(|r| r["level"] == "WARN")
            .collect();
        assert_eq!(warnings.len(), usize::from(fail));
        if let Some(w) = warnings.first() {
            assert_eq!(w["check"], "alpha");
            assert_eq!(w["error"], check_err);
            assert_eq!(w["request_id"], a.headers["x-request-id"].to_str().unwrap());
        }
    }
}

#[tokio::test]
async fn app_readiness_panic_is_a_500_without_the_text() {
    let app = new_app().readiness("panic", || async {
        if true {
            panic!("private failure");
        }
        Ok(())
    });
    let a = send(app.router(), get_req("/readyz")).await;
    assert_eq!(a.status, 500);
    assert!(!a.body.contains("private failure"), "{}", a.body);
}

#[tokio::test]
async fn app_readiness_timeout() {
    let app = new_app().readiness("slow", || async {
        tokio::time::sleep(Duration::from_secs(30)).await;
        Ok(())
    });
    let start = std::time::Instant::now();
    let a = send(app.router(), get_req("/readyz")).await;
    assert_eq!(a.status, 503);
    assert!(a.body.contains(r#""slow":"fail""#), "{}", a.body);
    assert!(start.elapsed() < Duration::from_secs(10));
}

#[tokio::test]
async fn app_routes_and_layers() {
    let route = |name: &'static str| {
        Router::new().route(&format!("/{name}"), get(move || async move { name }))
    };
    let seen = Arc::new(Mutex::new(Vec::<String>::new()));
    let (s1, s2) = (seen.clone(), seen.clone());
    let layer = |tag: &'static str, seen: Arc<Mutex<Vec<String>>>| {
        axum::middleware::from_fn(move |req: Request, next: Next| {
            let seen = seen.clone();
            async move {
                // The kit layers run outside the app layers.
                let id = req
                    .extensions()
                    .get::<RequestId>()
                    .cloned()
                    .unwrap_or_default();
                seen.lock()
                    .unwrap()
                    .push(format!("{tag}:{}", !id.0.is_empty()));
                let res: Response = next.run(req).await;
                res
            }
        })
    };
    let app = new_app()
        .routes(route("first"))
        .routes(route("second"))
        .layer(layer("outer", s1))
        .layer(layer("inner", s2));
    for path in ["first", "second"] {
        let a = send(app.router(), get_req(&format!("/{path}"))).await;
        assert_eq!(a.body, path);
        assert_eq!(a.headers["x-frame-options"], "DENY");
    }
    assert_eq!(
        *seen.lock().unwrap(),
        ["outer:true", "inner:true", "outer:true", "inner:true"]
    );
}

#[tokio::test]
async fn app_commands() {
    let got = Arc::new(Mutex::new(Vec::<String>::new()));
    let g = got.clone();
    let app = new_app()
        .command("zeta", Command::new(|_| async { Ok(()) }))
        .command(
            "migrate",
            Command::new(move |input: CommandInput| async move {
                *g.lock().unwrap() = input.args;
                anyhow::bail!("command failure")
            }),
        );
    let err = app
        .run(vec!["migrate".into(), "up".into()], std::future::pending())
        .await
        .unwrap_err();
    assert_eq!(err.to_string(), "command failure");
    assert_eq!(*got.lock().unwrap(), ["up"]);

    let app = new_app()
        .command("zeta", Command::new(|_| async { Ok(()) }))
        .command("migrate", Command::new(|_| async { Ok(()) }));
    let err = app
        .run(vec!["unknown".into()], std::future::pending())
        .await
        .unwrap_err()
        .to_string();
    assert_eq!(
        err,
        "cannot run command \"unknown\": unknown command. Available commands: healthcheck, migrate, openapi, serve, version, zeta.\nTry: use a listed command, or omit the command to serve HTTP"
    );
    let err = new_app()
        .run(vec!["unknown".into()], std::future::pending())
        .await
        .unwrap_err()
        .to_string();
    assert!(
        err.contains("Available commands: healthcheck, openapi, serve, version."),
        "{err}"
    );
    assert!(err.contains("\nTry: "), "{err}");
}

#[tokio::test]
async fn app_command_error_unchanged() {
    for message in [
        "command failed",
        "command failed: invalid argument\nTry: use migrate up",
    ] {
        let app = new_app().command(
            "migrate",
            Command::new(move |_| async move { Err(anyhow::anyhow!(message)) }),
        );
        let err = app
            .run(vec!["migrate".into(), "up".into()], std::future::pending())
            .await
            .unwrap_err();
        assert_eq!(format!("{err:#}"), message);
    }
}

#[tokio::test]
async fn app_command_gets_the_shutdown_future() {
    let app = new_app().command(
        "worker",
        Command::new(|input: CommandInput| async move {
            input.shutdown.await;
            Ok(())
        }),
    );
    let (tx, rx) = tokio::sync::oneshot::channel::<()>();
    let run = tokio::spawn(app.run(vec!["worker".into()], async {
        let _ = rx.await;
    }));
    tx.send(()).unwrap();
    tokio::time::timeout(Duration::from_secs(5), run)
        .await
        .expect("the worker stops on shutdown")
        .unwrap()
        .unwrap();
}

#[test]
fn app_defaults() {
    let base = Base {
        port: 9090,
        ..Base::default()
    };
    assert_eq!(App::new(&base).listen_addr(), "0.0.0.0:9090");
    assert_eq!(new_app().listen_addr(), "0.0.0.0:8080");
    let zero = Base {
        port: 0,
        ..Base::default()
    };
    assert_eq!(App::new(&zero).listen_addr(), "0.0.0.0:8080");
    assert_eq!(new_app().addr("127.0.0.1:1").listen_addr(), "127.0.0.1:1");
}

#[tokio::test]
async fn app_graceful_shutdown_hooks_and_healthcheck() {
    let order = Arc::new(Mutex::new(Vec::<i32>::new()));
    let (o1, o2) = (order.clone(), order.clone());
    let base = Base {
        shutdown_timeout: Duration::from_secs(1),
        ..Base::default()
    };
    let app = App::new(&base)
        .shutdown(move || async move {
            o1.lock().unwrap().push(1);
            Ok(())
        })
        .shutdown(move || async move {
            o2.lock().unwrap().push(2);
            anyhow::bail!("hook failure")
        });
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let (stop_tx, stop_rx) = tokio::sync::oneshot::channel::<()>();
    let server = tokio::spawn(app.serve_on(listener, async {
        let _ = stop_rx.await;
    }));
    tokio::task::spawn_blocking(move || app::healthcheck(port))
        .await
        .unwrap()
        .unwrap();
    stop_tx.send(()).unwrap();
    let err = tokio::time::timeout(Duration::from_secs(5), server)
        .await
        .expect("shutdown finishes")
        .unwrap()
        .unwrap_err()
        .to_string();
    assert!(err.contains("shutdown hook failed: hook failure"), "{err}");
    assert!(err.contains("\nTry: "), "{err}");
    assert_eq!(*order.lock().unwrap(), [2, 1]);
    // The server is gone, so the health check fails with a Try: line.
    let err = tokio::task::spawn_blocking(move || app::healthcheck(port))
        .await
        .unwrap()
        .unwrap_err()
        .to_string();
    assert!(err.contains("\nTry: "), "{err}");
}

#[tokio::test]
async fn app_serve_gives_connect_info() {
    let app =
        new_app().routes(Router::new().route(
            "/peer",
            get(
                |ConnectInfo(addr): ConnectInfo<std::net::SocketAddr>| async move {
                    addr.ip().to_string()
                },
            ),
        ));
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = listener.local_addr().unwrap().port();
    let (stop_tx, stop_rx) = tokio::sync::oneshot::channel::<()>();
    let server = tokio::spawn(app.serve_on(listener, async {
        let _ = stop_rx.await;
    }));
    let body = tokio::task::spawn_blocking(move || {
        use std::io::{Read, Write};
        let mut s = std::net::TcpStream::connect(("127.0.0.1", port)).unwrap();
        s.write_all(b"GET /peer HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
            .unwrap();
        let mut out = String::new();
        s.read_to_string(&mut out).unwrap();
        out
    })
    .await
    .unwrap();
    assert!(body.starts_with("HTTP/1.1 200"), "{body}");
    assert!(body.ends_with("127.0.0.1"), "{body}");
    stop_tx.send(()).unwrap();
    server.await.unwrap().unwrap();
}

#[tokio::test]
async fn app_healthcheck_command() {
    for (status, ok) in [
        (StatusCode::OK, true),
        (StatusCode::SERVICE_UNAVAILABLE, false),
    ] {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        let router = Router::new().route("/healthz", get(move || async move { status }));
        let server = tokio::spawn(async move { axum::serve(listener, router).await });
        let result = new_app()
            .addr(format!(":{port}"))
            .run(vec!["healthcheck".into()], std::future::pending())
            .await;
        assert_eq!(result.is_ok(), ok, "{result:?}");
        if let Err(e) = result {
            assert!(e.to_string().contains("answered \"503\""), "{e}");
        }
        server.abort();
    }
    let err = new_app()
        .addr("127.0.0.1:1")
        .run(vec!["healthcheck".into()], std::future::pending())
        .await
        .unwrap_err();
    assert!(err.to_string().contains("\nTry: "), "{err}");
    let err = new_app()
        .addr("no-port")
        .run(vec!["healthcheck".into()], std::future::pending())
        .await
        .unwrap_err();
    assert!(err.to_string().contains("cannot read the port"), "{err}");
}

#[tokio::test]
async fn app_deploy_info() {
    for enabled in [false, true] {
        let mut app = new_app().version("v42");
        if enabled {
            app = app.deploy_info(deployx::Options::default());
        }
        let mut req = get_req("/_deploy/info");
        req.extensions_mut()
            .insert(ConnectInfo(std::net::SocketAddr::from((
                [127, 0, 0, 1],
                20,
            ))));
        let a = send(app.router(), req).await;
        if enabled {
            assert_eq!(a.status, 200, "{}", a.body);
            assert!(a.body.contains(r#""version":"v42""#), "{}", a.body);
        } else {
            assert_eq!(a.status, 404);
        }
    }
}

#[test]
fn app_hoist_path_params() {
    let id = ParameterBuilder::new()
        .name("id")
        .parameter_in(ParameterIn::Path)
        .build();
    let limit = ParameterBuilder::new()
        .name("limit")
        .parameter_in(ParameterIn::Query)
        .build();
    let op = |params: Vec<utoipa::openapi::path::Parameter>| {
        let mut b = OperationBuilder::new();
        for p in params {
            b = b.parameter(p);
        }
        b.build()
    };
    let item = PathItemBuilder::new()
        .operation(HttpMethod::Get, op(vec![id.clone(), limit.clone()]))
        .operation(HttpMethod::Delete, op(vec![id.clone()]))
        .build();
    let mut doc = OpenApiBuilder::new()
        .paths(PathsBuilder::new().path("/todos/{id}", item))
        .build();
    app::hoist_path_params(&mut doc);
    app::hoist_path_params(&mut doc);
    let item = &doc.paths.paths["/todos/{id}"];
    assert_eq!(item.parameters.as_ref().map(Vec::len), Some(1));
    let get = item.get.as_ref().unwrap();
    assert_eq!(get.parameters.as_ref().map(Vec::len), Some(1));
    assert!(item.delete.as_ref().unwrap().parameters.is_none());
    let yaml = app::openapi::to_yaml(doc).unwrap();
    assert!(yaml.contains("/todos/{id}:\n    parameters:"), "{yaml}");
    let mut out = Vec::new();
    app::openapi::write_yaml(OpenApiBuilder::new().build(), &mut out).unwrap();
    assert!(String::from_utf8(out).unwrap().starts_with("openapi:"));
}
