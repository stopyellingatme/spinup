//! The API reference, as the Go `kit/apidocs`: `/api/openapi.yaml`,
//! `/api/docs` (a page without an inline script), `/api/docs/boot.js`, and
//! `/api/docs/scalar.js`. Every answer has the strict script policy.
//!
//! The spike embeds the vendored Scalar bundle of the Go kit
//! (`kit/apidocs/assets/scalar.js.gz`) from the repository, so the bytes are
//! the bytes that the Go app serves.

use std::io::Read;
use std::sync::Arc;

use axum::Router;
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::get;

use crate::Problem;

/// The Content-Security-Policy of the Go `kit/apidocs`.
pub const CONTENT_SECURITY_POLICY: &str = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'";

static SCALAR_GZIP: &[u8] = include_bytes!("../../../../../kit/apidocs/assets/scalar.js.gz");

const BOOT_JS: &str = "Scalar.createApiReference('#app', {
  url: '/api/openapi.yaml',
  withDefaultFonts: false,
  telemetry: false,
  hideClientButton: false,
});
";

/// The routes of the API reference. `spec` is the OpenAPI document as YAML.
pub fn routes(spec: String, title: &str) -> Router {
    let spec = Arc::new(spec);
    let page = Arc::new(index_html(title));
    Router::new()
        .route(
            "/api/openapi.yaml",
            get(move || {
                let spec = spec.clone();
                async move { answer("application/yaml", "no-cache", spec.as_bytes().to_vec()) }
            }),
        )
        .route(
            "/api/docs",
            get(move || {
                let page = page.clone();
                async move {
                    answer(
                        "text/html; charset=utf-8",
                        "no-cache",
                        page.as_bytes().to_vec(),
                    )
                }
            }),
        )
        .route(
            "/api/docs/boot.js",
            get(|| async {
                answer(
                    "text/javascript; charset=utf-8",
                    "no-cache",
                    BOOT_JS.as_bytes().to_vec(),
                )
            }),
        )
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
    format!(
        r#"<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{title}</title>
  <script src="/api/docs/scalar.js" defer></script>
  <script src="/api/docs/boot.js" defer></script>
</head>
<body>
  <div id="app"></div>
</body>
</html>
"#
    )
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn gzip_negotiation_follows_go() {
        for (accept, want) in [
            ("", false),
            ("gzip", true),
            ("br, gzip;q=0.5", true),
            ("gzip;q=0", false),
            ("gzip;q=0.0, *;q=1", false),
            ("*;q=1", true),
            ("gzip;q=invalid", false),
            ("xgzip", false),
        ] {
            assert_eq!(accepts_gzip(accept), want, "{accept:?}");
        }
    }

    #[test]
    fn bundle_is_the_scalar_standalone_build() {
        let mut plain = Vec::new();
        flate2::read::GzDecoder::new(SCALAR_GZIP)
            .read_to_end(&mut plain)
            .unwrap();
        let needle = b"window.Scalar={createApiReference:";
        assert!(plain.windows(needle.len()).any(|w| w == needle));
    }

    #[test]
    fn title_is_escaped() {
        let page = index_html(r#"Example </title><script>alert("x")</script>"#);
        assert!(!page.contains("<script>alert"));
        assert!(page.contains("Example &lt;/title&gt;&lt;script&gt;"));
        assert!(!page.contains("<script>\n"));
    }
}
