//! Static files and the SvelteKit SPA fallback.

use axum::body::Body;
use axum::extract::Request;
use axum::http::{Method, StatusCode, header};
use axum::response::{IntoResponse, Response};

use crate::app::App;
use crate::httpx::Problem;

/// The file table that the app build script generates from `api/webdist/`.
/// Paths are relative to that directory and use `/` separators.
pub type EmbeddedFiles = &'static [(&'static str, &'static [u8])];

const CSP: &str = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'";

impl App {
    /// Serves embedded files and uses `index.html` for client routes.
    /// Reserved kit routes never receive the SPA fallback.
    #[must_use]
    pub fn with_spa(mut self, files: &'static EmbeddedFiles) -> Self {
        self.set_fallback(move |router| router.fallback(move |req| serve(files, req)));
        self
    }
}

async fn serve(files: &'static EmbeddedFiles, req: Request) -> Response {
    // Match Go path.Clean before checking reserved paths or selecting a file.
    let mut components = Vec::new();
    for part in req.uri().path().split('/') {
        match part {
            "" | "." => {}
            ".." => {
                components.pop();
            }
            part => components.push(part),
        }
    }
    let name = components.join("/");
    if [
        "api", "healthz", "readyz", "metrics", "version", "_deploy", "_dev",
    ]
    .contains(&name.split('/').next().unwrap_or_default())
    {
        return missing();
    }
    if req.method() != Method::GET && req.method() != Method::HEAD {
        let mut res = Problem::new(
            StatusCode::METHOD_NOT_ALLOWED,
            "The static application accepts GET and HEAD only.\nTry: use GET or HEAD",
        )
        .into_response();
        res.headers_mut()
            .insert(header::ALLOW, "GET, HEAD".parse().expect("valid header"));
        return res;
    }
    let file = files.iter().find(|(path, _)| *path == name);
    let (name, data) = match file {
        Some(file) => *file,
        None if name.starts_with("_app/") => return missing(),
        None => match files.iter().find(|(path, _)| *path == "index.html") {
            Some(file) => *file,
            None => return missing(),
        },
    };
    let mut res = Body::from(data).into_response();
    let headers = res.headers_mut();
    headers.insert(
        header::CONTENT_TYPE,
        mime_guess::from_path(name)
            .first_or_octet_stream()
            .as_ref()
            .parse()
            .expect("valid MIME type"),
    );
    headers.insert(header::CONTENT_LENGTH, data.len().into());
    if name == "index.html" {
        headers.insert(
            header::CACHE_CONTROL,
            "no-cache".parse().expect("valid header"),
        );
        headers.insert(
            header::CONTENT_SECURITY_POLICY,
            CSP.parse().expect("valid header"),
        );
    } else if name.starts_with("_app/immutable/") {
        headers.insert(
            header::CACHE_CONTROL,
            "public, max-age=31536000, immutable"
                .parse()
                .expect("valid header"),
        );
    }
    if req.method() == Method::HEAD {
        *res.body_mut() = Body::empty();
    }
    res
}

fn missing() -> Response {
    Problem::new(StatusCode::NOT_FOUND, "The requested static file does not exist.\nTry: check the path and rebuild the web application").into_response()
}
