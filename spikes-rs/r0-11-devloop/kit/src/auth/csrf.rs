//! The cross-origin check of Go's `http.CrossOriginProtection` (Go 1.25),
//! which the Go kit uses for unsafe requests.

use std::collections::HashSet;

use axum::http::{HeaderMap, Method, header};

use super::AuthError;

/// Rejects unsafe cross-origin requests.
#[derive(Debug, Clone, Default)]
pub struct CrossOrigin {
    trusted: HashSet<String>,
}

impl CrossOrigin {
    /// Allows unsafe requests from `origin` (`scheme://host[:port]`).
    pub fn add_trusted_origin(&mut self, origin: &str) -> Result<(), AuthError> {
        let ok = origin.split_once("://").is_some_and(|(scheme, rest)| {
            !scheme.is_empty() && !rest.is_empty() && !rest.contains(['/', '?', '#'])
        });
        if !ok {
            return Err(AuthError(format!(
                "auth: cannot trust origin {origin:?}: it is not of the form scheme://host[:port]\nTry: pass an origin of the form scheme://host[:port] with no path"
            )));
        }
        self.trusted.insert(origin.to_owned());
        Ok(())
    }

    /// Reports whether the request may pass. `host` is the request host
    /// (the Host header or the URI authority).
    pub fn check(&self, method: &Method, headers: &HeaderMap, host: &str) -> bool {
        if matches!(*method, Method::GET | Method::HEAD | Method::OPTIONS) {
            return true;
        }
        let get = |name: &str| {
            headers
                .get(name)
                .and_then(|v| v.to_str().ok())
                .unwrap_or("")
        };
        let origin = get(header::ORIGIN.as_str());
        match get("sec-fetch-site") {
            "" => {}
            "same-origin" | "none" => return true,
            _ => return self.trusted.contains(origin),
        }
        if origin.is_empty() {
            // Neither header: not a browser request, or an old browser.
            return true;
        }
        if origin_host(origin) == Some(host) {
            return true;
        }
        self.trusted.contains(origin)
    }
}

/// The host part of an origin, as Go's `url.Parse(origin).Host`.
fn origin_host(origin: &str) -> Option<&str> {
    let (_, rest) = origin.split_once("://")?;
    let end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
    Some(&rest[..end])
}

#[cfg(test)]
mod tests {
    use super::*;

    // The table of TestCrossOriginProtection in auth_test.go.
    #[test]
    fn cross_origin_table() {
        let mut c = CrossOrigin::default();
        c.add_trusted_origin("http://localhost:5173").unwrap();
        for (name, method, hs, want) in [
            (
                "cross-site fetch",
                Method::POST,
                vec![("sec-fetch-site", "cross-site")],
                false,
            ),
            (
                "same-site fetch",
                Method::POST,
                vec![("sec-fetch-site", "same-site")],
                false,
            ),
            (
                "foreign origin",
                Method::POST,
                vec![("origin", "https://evil.example")],
                false,
            ),
            ("null origin", Method::POST, vec![("origin", "null")], false),
            (
                "cross-site with trusted origin",
                Method::POST,
                vec![
                    ("sec-fetch-site", "cross-site"),
                    ("origin", "http://localhost:5173"),
                ],
                true,
            ),
            (
                "trusted origin",
                Method::POST,
                vec![("origin", "http://localhost:5173")],
                true,
            ),
            (
                "same origin",
                Method::POST,
                vec![
                    ("sec-fetch-site", "same-origin"),
                    ("origin", "http://api.test"),
                ],
                true,
            ),
            (
                "origin equals host",
                Method::POST,
                vec![("origin", "http://api.test")],
                true,
            ),
            ("no browser headers", Method::POST, vec![], true),
            (
                "safe method cross-site",
                Method::GET,
                vec![("sec-fetch-site", "cross-site")],
                true,
            ),
        ] {
            let mut h = HeaderMap::new();
            for (k, v) in hs {
                h.insert(k, v.parse().unwrap());
            }
            assert_eq!(c.check(&method, &h, "api.test"), want, "{name}");
        }
        assert!(c.add_trusted_origin("http://x.test/path").is_err());
    }
}
