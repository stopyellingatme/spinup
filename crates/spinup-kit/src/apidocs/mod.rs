//! The self-hosted Scalar API reference.

use std::io::Read;
use std::sync::Arc;

use axum::Router;
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::get;

use crate::{app::App, httpx::Problem};
use std::path::PathBuf;

/// The Content-Security-Policy of the Go `kit/apidocs`.
pub const CONTENT_SECURITY_POLICY: &str = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'";

static SCALAR_GZIP: &[u8] = include_bytes!("../../assets/scalar.js.gz");

const BOOT_JS: &str = include_str!("static/boot.js");

/// The sources and title of the API reference.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// The embedded fallback document. `None` reports a missing document.
    pub spec_yaml: Option<String>,
    /// The development directory. Each request first reads `bundle.gen.yaml` here.
    pub dir: Option<PathBuf>,
    /// The page title. An empty title uses `API reference`.
    pub title: String,
}

impl App {
    /// Mounts the API reference in development, or when `SPINUP_API_DOCS` is true.
    /// Development requests first read `openapi/bundle.gen.yaml` from disk.
    #[must_use]
    pub fn with_api_docs(mut self, spec_yaml: impl Into<String>) -> Self {
        if self.config().is_dev() || self.config().api_docs {
            let options = Options {
                spec_yaml: Some(spec_yaml.into()),
                dir: self.config().is_dev().then(|| PathBuf::from("openapi")),
                ..Options::default()
            };
            for path in [
                "/api/openapi.yaml",
                "/api/docs",
                "/api/docs/boot.js",
                "/api/docs/scalar.js",
            ] {
                self.add_route_pattern(format!("GET {path}"));
            }
            self.mount_internal(routes(options));
        }
        self
    }
}

/// Returns the self-hosted Scalar routes and the OpenAPI document route.
pub fn routes(options: Options) -> Router {
    let title = if options.title.is_empty() {
        "API reference"
    } else {
        &options.title
    };
    let page = Arc::new(index_html(title));
    let options = Arc::new(options);
    Router::new()
        .route("/api/openapi.yaml", get(move || {
            let options = options.clone();
            async move {
                let disk = match &options.dir {
                    Some(dir) => tokio::fs::read(dir.join("bundle.gen.yaml")).await.ok(),
                    None => None,
                };
                match disk.or_else(|| options.spec_yaml.as_ref().map(|s| s.as_bytes().to_vec())) {
                    Some(data) => answer("application/yaml", "no-cache", data),
                    None => {
                        let mut res = Problem::new(StatusCode::NOT_FOUND, "The OpenAPI document is missing.\nTry: run spinup g resource or spinup g openapi").into_response();
                        res.headers_mut().insert(header::CACHE_CONTROL, HeaderValue::from_static("no-cache"));
                        res.headers_mut().insert(header::CONTENT_SECURITY_POLICY, HeaderValue::from_static(CONTENT_SECURITY_POLICY));
                        res
                    }
                }
            }
        }))
        .route("/api/docs", get(move || {
            let page = page.clone();
            async move { answer("text/html; charset=utf-8", "no-cache", page.as_bytes().to_vec()) }
        }))
        .route("/api/docs/boot.js", get(|| async {
            answer("text/javascript; charset=utf-8", "no-cache", BOOT_JS.as_bytes().to_vec())
        }))
        .route("/api/docs/scalar.js", get(scalar))
}

async fn scalar(headers: HeaderMap) -> Response {
    let accept = headers
        .get_all(header::ACCEPT_ENCODING)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .collect::<Vec<_>>()
        .join(",");
    let gzip = accepts_gzip(&accept);
    let body = if gzip {
        SCALAR_GZIP.to_vec()
    } else {
        let mut plain = Vec::new();
        if flate2::read::GzDecoder::new(SCALAR_GZIP)
            .read_to_end(&mut plain)
            .is_err()
        {
            return Problem::new(
                StatusCode::INTERNAL_SERVER_ERROR,
                "Cannot read the API reference asset.\nTry: rebuild the application with a valid Scalar bundle",
            )
            .into_response();
        }
        plain
    };
    let mut res = answer(
        "text/javascript; charset=utf-8",
        "public, max-age=86400",
        body,
    );
    let h = res.headers_mut();
    h.insert(header::VARY, HeaderValue::from_static("Accept-Encoding"));
    if gzip {
        h.insert(header::CONTENT_ENCODING, HeaderValue::from_static("gzip"));
    }
    res
}

fn answer(content_type: &'static str, cache: &'static str, body: Vec<u8>) -> Response {
    let mut res = body.into_response();
    let h = res.headers_mut();
    h.insert(
        header::CONTENT_SECURITY_POLICY,
        HeaderValue::from_static(CONTENT_SECURITY_POLICY),
    );
    h.insert(header::CONTENT_TYPE, HeaderValue::from_static(content_type));
    h.insert(header::CACHE_CONTROL, HeaderValue::from_static(cache));
    res
}

fn index_html(title: &str) -> String {
    let title = title
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&#34;")
        .replace('\'', "&#39;");
    include_str!("static/index.html").replace("{{.}}", &title)
}

/// The Accept-Encoding rule of the Go `acceptsGzip`.
fn accepts_gzip(value: &str) -> bool {
    let mut wildcard = false;
    for token in value.split(',') {
        let mut parts = token.split(';');
        let name = parts.next().unwrap_or("").trim();
        let is_gzip = name.eq_ignore_ascii_case("gzip");
        if !is_gzip && name != "*" {
            continue;
        }
        let mut quality = 1.0_f64;
        for p in parts {
            if let Some((k, raw)) = p.trim().split_once('=')
                && k.eq_ignore_ascii_case("q")
            {
                quality = match raw.parse::<f64>() {
                    Ok(q) if q > 0.0 && q <= 1.0 => q,
                    _ => 0.0,
                };
            }
        }
        if is_gzip {
            return quality > 0.0;
        }
        wildcard = quality > 0.0;
    }
    wildcard
}
