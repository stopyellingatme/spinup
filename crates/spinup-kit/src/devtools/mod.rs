//! The development page and a recorder for the last 50 requests.

use std::collections::VecDeque;
use std::fmt::Write;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Instant;

use axum::extract::{Request, State};
use axum::http::{HeaderValue, StatusCode, header};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::{Json, Router};
use serde::Serialize;
use serde_json::Value;

use crate::app::{App, MigrationStatusFn, RoutePatterns};
use crate::httpx::{Problem, RequestId};

const CSP: &str = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";

#[derive(Clone, Serialize)]
struct RecordedRequest {
    method: String,
    path: String,
    status: u16,
    duration_ms: u128,
    request_id: String,
}

struct Devtools {
    name: String,
    version: String,
    config: Value,
    patterns: RoutePatterns,
    migrations: Option<MigrationStatusFn>,
    requests: Mutex<VecDeque<RecordedRequest>>,
}

impl App {
    /// Mounts `/_dev` and records the last 50 requests in development.
    /// The optional callback supplies the migration state without a database dependency.
    #[must_use]
    pub fn with_devtools(mut self, migrations: Option<MigrationStatusFn>) -> Self {
        if !self.config().is_dev() {
            return self;
        }
        let dev = Arc::new(Devtools {
            name: self.app_name().into(),
            version: self.app_version().into(),
            config: serde_json::to_value(self.config()).expect("Base serializes to JSON"),
            patterns: self.route_patterns(),
            migrations,
            requests: Mutex::new(VecDeque::with_capacity(50)),
        });
        for path in ["/_dev", "/_dev/", "/_dev/requests.json"] {
            self.add_route_pattern(format!("GET {path}"));
        }
        self.record_route_patterns();
        self.mount_internal(
            Router::new()
                .route("/_dev", any(page))
                .route("/_dev/", any(page))
                .route("/_dev/{*path}", any(page))
                .with_state(dev.clone()),
        );
        self.push_outer_layer(move |router| {
            router.layer(middleware::from_fn_with_state(dev.clone(), record))
        });
        self
    }
}

async fn record(State(dev): State<Arc<Devtools>>, req: Request, next: Next) -> Response {
    let start = Instant::now();
    let method = req.method().to_string();
    let path = req.uri().path().to_string();
    let request_id = req
        .extensions()
        .get::<RequestId>()
        .map(|id| id.0.clone())
        .or_else(|| {
            req.headers()
                .get("x-request-id")
                .and_then(|v| v.to_str().ok())
                .map(str::to_string)
        })
        .unwrap_or_default();
    let res = next.run(req).await;
    let value = RecordedRequest {
        method,
        path,
        status: res.status().as_u16(),
        duration_ms: start.elapsed().as_millis(),
        request_id,
    };
    let mut requests = dev.requests.lock().unwrap_or_else(PoisonError::into_inner);
    if requests.len() == 50 {
        requests.pop_front();
    }
    requests.push_back(value);
    res
}

async fn page(State(dev): State<Arc<Devtools>>, req: Request) -> Response {
    let requests: Vec<_> = dev
        .requests
        .lock()
        .unwrap_or_else(PoisonError::into_inner)
        .iter()
        .cloned()
        .collect();
    let mut res = match req.uri().path() {
        "/_dev/requests.json" => {
            let mut res = Json(requests).into_response();
            res.headers_mut().insert(
                header::CONTENT_TYPE,
                HeaderValue::from_static("application/json; charset=utf-8"),
            );
            res
        }
        "/_dev" | "/_dev/" => {
            let mut html = String::from(
                "<!doctype html><html><head><meta charset=\"utf-8\"><title>Development</title><style>body{font:14px system-ui;margin:2rem;color:#222}table{border-collapse:collapse;width:100%;margin:1rem 0}th,td{border:1px solid #ccc;padding:.4rem;text-align:left}th{background:#f3f3f3}code{white-space:pre-wrap}</style></head><body><h1>Development</h1>",
            );
            let _ = write!(
                html,
                "<p><code>{} {}</code></p>",
                escape(&dev.name),
                escape(&dev.version)
            );
            let patterns = dev.patterns.list();
            if patterns.iter().any(|p| p == "GET /api/docs") {
                html.push_str("<p><a href=\"/api/docs\">API reference</a></p>");
            }
            html.push_str("<h2>Routes</h2><table><tr><th>Pattern</th></tr>");
            for pattern in patterns {
                row(&mut html, &[&pattern]);
            }
            html.push_str(
                "</table><h2>Configuration</h2><table><tr><th>Name</th><th>Value</th></tr>",
            );
            let mut values = Vec::new();
            config_values(&dev.config, "", &mut values);
            for (name, value) in values {
                row(&mut html, &[&name, &value]);
            }
            html.push_str("</table><h2>Requests</h2><p><a href=\"/_dev/requests.json\">JSON</a></p><table><tr><th>Method</th><th>Path</th><th>Status</th><th>Duration ms</th><th>Request ID</th></tr>");
            for req in requests {
                row(
                    &mut html,
                    &[
                        &req.method,
                        &req.path,
                        &req.status.to_string(),
                        &req.duration_ms.to_string(),
                        &req.request_id,
                    ],
                );
            }
            html.push_str("</table><h2>Pending migrations</h2>");
            match &dev.migrations {
                None => html.push_str("<p>not configured</p>"),
                Some(status) => match status().await {
                    Ok(migrations) => {
                        html.push_str("<table><tr><th>Name</th><th>Applied</th></tr>");
                        for migration in migrations.into_iter().filter(|m| !m.applied) {
                            row(&mut html, &[&migration.name, "false"]);
                        }
                        html.push_str("</table>");
                    }
                    Err(err) => {
                        let _ = write!(
                            html,
                            "<p>unavailable: {}\nTry: check the database connection and migration status</p>",
                            escape(&err.to_string())
                        );
                    }
                },
            }
            html.push_str("</body></html>");
            axum::response::Html(html).into_response()
        }
        _ => Problem::new(
            StatusCode::NOT_FOUND,
            "The development page does not exist.\nTry: open /_dev",
        )
        .into_response(),
    };
    res.headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    res.headers_mut().insert(
        header::CONTENT_SECURITY_POLICY,
        HeaderValue::from_static(CSP),
    );
    res
}

fn row(html: &mut String, values: &[&str]) {
    html.push_str("<tr>");
    for value in values {
        let _ = write!(html, "<td><code>{}</code></td>", escape(value));
    }
    html.push_str("</tr>");
}

fn escape(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&#34;")
        .replace('\'', "&#39;")
}

fn config_values(value: &Value, prefix: &str, values: &mut Vec<(String, String)>) {
    if let Value::Object(object) = value {
        for (key, value) in object {
            let name = if prefix.is_empty() {
                key.clone()
            } else {
                format!("{prefix}.{key}")
            };
            config_values(value, &name, values);
        }
    } else {
        let text = value
            .as_str()
            .map(str::to_string)
            .unwrap_or_else(|| value.to_string());
        let upper = prefix.to_uppercase();
        let masked = if ["PASSWORD", "SECRET", "TOKEN", "KEY"]
            .iter()
            .any(|key| upper.contains(key))
        {
            "****".into()
        } else if upper.replace('_', "").ends_with("URL") {
            mask_url(&text)
        } else {
            text
        };
        values.push((prefix.into(), masked));
    }
}

fn mask_url(value: &str) -> String {
    // Devtools has no `url` dependency. Only a URL authority can contain user information.
    if let Some((scheme, rest)) = value.split_once("://") {
        let end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
        if let Some((userinfo, host)) = rest[..end].rsplit_once('@') {
            let username = userinfo.split(':').next().unwrap_or_default();
            if username.is_empty() {
                return "****".into();
            }
            return format!("{scheme}://{username}:****@{host}{}", &rest[end..]);
        }
    }
    value.into()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn devtools_masks_nested_config_and_escapes() {
        let mut values = Vec::new();
        config_values(
            &serde_json::json!({"DatabaseURL": "postgres://app:password@db/todo", "Password": "hunter2", "nested": {"TOKEN": "private"}, "Public": "<script>alert(1)</script>"}),
            "",
            &mut values,
        );
        let mut html = String::new();
        for (name, value) in values {
            row(&mut html, &[&name, &value]);
        }
        assert!(html.contains("postgres://app:****@db/todo"));
        assert!(html.contains("&lt;script&gt;alert(1)&lt;/script&gt;"));
        for secret in ["password@", "hunter2", "private", "<script>"] {
            assert!(!html.contains(secret));
        }
        assert_eq!(
            mask_url("postgres://app@db/todo"),
            "postgres://app:****@db/todo"
        );
        assert_eq!(mask_url("postgres://:password@db/todo"), "****");
    }
}
