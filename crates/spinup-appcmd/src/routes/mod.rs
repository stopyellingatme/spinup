//! List routes from the kit, the OpenAPI bundle, and literal Rust registrations.

use std::collections::BTreeMap;
use std::io::Write;
use std::path::Path;

use anyhow::Context;
use serde::{Deserialize, Serialize};

mod rust;

/// An HTTP registration and its source.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Route {
    /// The HTTP method, or `*` for all methods.
    pub method: String,
    /// The registered URL path.
    pub path: String,
    /// The kit, operation, or file and line that registered the route.
    pub source: String,
}

/// Read the application files and return the routes sorted as Go sorts them.
/// Missing files contribute no routes. The three kit routes are always present.
///
/// # Errors
/// Return a repair hint when a file cannot be read or parsed.
pub fn list(root: &Path) -> anyhow::Result<Vec<Route>> {
    list_inner(root).map_err(route_error)
}

fn list_inner(root: &Path) -> anyhow::Result<Vec<Route>> {
    let mut result: Vec<_> = ["/healthz", "/readyz", "/version"]
        .into_iter()
        .map(|path| Route {
            method: "GET".into(),
            path: path.into(),
            source: "kit".into(),
        })
        .collect();
    if let Some(bytes) =
        read_optional(&root.join("api/openapi/bundle.gen.yaml")).context("read OpenAPI bundle")?
    {
        result.extend(openapi(&bytes)?);
    }
    if let Some(bytes) =
        read_optional(&root.join("api/src/routes.rs")).context("read route file")?
    {
        let source = std::str::from_utf8(&bytes).context("parse route file")?;
        result.extend(rust::routes(source).context("parse route file")?);
    }
    result.sort_by(|a, b| (&a.path, &a.method, &a.source).cmp(&(&b.path, &b.method, &b.source)));
    Ok(result)
}

fn read_optional(path: &Path) -> std::io::Result<Option<Vec<u8>>> {
    match std::fs::read(path) {
        Ok(bytes) => Ok(Some(bytes)),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(error),
    }
}

fn openapi(bytes: &[u8]) -> anyhow::Result<Vec<Route>> {
    #[derive(Default, Deserialize)]
    struct Document {
        #[serde(default)]
        paths: BTreeMap<String, BTreeMap<String, yaml_serde::Value>>,
    }
    #[derive(Default, Deserialize)]
    struct Operation {
        #[serde(default, rename = "operationId")]
        id: String,
    }
    let doc: Option<Document> = yaml_serde::from_slice(bytes).context("parse OpenAPI bundle")?;
    let mut result = Vec::new();
    for (path, item) in doc.unwrap_or_default().paths {
        for method in [
            "get", "put", "post", "delete", "options", "head", "patch", "trace",
        ] {
            if let Some(value) = item.get(method) {
                let operation: Option<Operation> =
                    yaml_serde::from_value(value.clone()).context("parse OpenAPI operation")?;
                result.push(Route {
                    method: method.to_uppercase(),
                    path: path.clone(),
                    source: format!("openapi:{}", operation.unwrap_or_default().id),
                });
            }
        }
    }
    Ok(result)
}

/// Write the routes as compact JSON or the Go table format.
/// The input order is preserved, as in Go `routes.Print`.
///
/// # Errors
/// Return a repair hint when serialization or a write fails.
pub fn print(out: &mut dyn Write, routes: &[Route], json: bool) -> anyhow::Result<()> {
    print_inner(out, routes, json).map_err(route_error)
}

fn print_inner(out: &mut dyn Write, routes: &[Route], json: bool) -> anyhow::Result<()> {
    if json {
        // Go encoding/json escapes these characters by default.
        let text = serde_json::to_string(routes)
            .context("write routes JSON")?
            .replace('&', "\\u0026")
            .replace('<', "\\u003c")
            .replace('>', "\\u003e")
            .replace('\u{2028}', "\\u2028")
            .replace('\u{2029}', "\\u2029");
        writeln!(out, "{text}").context("write routes JSON")?;
    } else {
        let method_width = routes
            .iter()
            .map(|r| r.method.chars().count())
            .max()
            .unwrap_or(0)
            .max(6)
            + 2;
        let path_width = routes
            .iter()
            .map(|r| r.path.chars().count())
            .max()
            .unwrap_or(0)
            .max(4)
            + 2;
        writeln!(
            out,
            "{:<method_width$}{:<path_width$}SOURCE",
            "METHOD", "PATH"
        )
        .context("write routes table")?;
        for route in routes {
            writeln!(
                out,
                "{:<method_width$}{:<path_width$}{}",
                route.method, route.path, route.source
            )
            .context("write routes table")?;
        }
    }
    Ok(())
}

fn route_error(error: anyhow::Error) -> anyhow::Error {
    anyhow::anyhow!(
        "{error:#}\nTry: check the application files and permissions, then run spinup routes"
    )
}

#[cfg(test)]
mod tests;
