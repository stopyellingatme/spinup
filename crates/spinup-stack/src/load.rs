//! The embedded `stackversions.yaml`, strict decode, and the sync check with the Go file.
//!
//! [`parse`] decodes and validates one stack versions file (Go `stackversions.Parse`).
//! Schema 2 has no Go sections ([`GO_SECTIONS`]). A file with a Go section is an error.
//! [`load`] parses the file that is compiled into the binary. [`embedded`] returns it once
//! and panics when it is invalid (Go `MustLoad`): a test guards the file.

use std::sync::OnceLock;

use crate::stack::{Invalid, Stack};

/// The stack versions file compiled into the binary (schema 2).
pub const EMBEDDED: &str = include_str!("../stackversions.yaml");

/// The marker line between the lines copied from the Go file and the Rust sections
/// (`scripts/rust/gen-stack.sh`).
pub const MARKER: &str =
    "# ---- Rust sections (schema 2). The lines above are generated. Edit the lines below. ----";

/// The schema 1 sections that schema 2 does not have. `scripts/rust/gen-stack.sh` removes
/// them when it copies the Go file (rust-plan.md decision 11).
pub const GO_SECTIONS: [&str; 3] = ["go", "go_modules", "go_tools"];

/// A stack versions file that does not decode or is not valid.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// The YAML does not decode into the schema, or has an unknown field.
    #[error(
        "parse stack versions: {0}\nTry: check stackversions.yaml of the template version against schema 2"
    )]
    Parse(String),
    /// A field has an invalid value.
    #[error("{0}\nTry: fix these fields of stackversions.yaml")]
    Invalid(Invalid),
}

/// Decodes and validates one stack versions file.
///
/// The decode is strict: an unknown field is an error. A Go section of schema 1 is an
/// error that names the section.
pub fn parse(data: &[u8]) -> Result<Stack, Error> {
    // The generic decode rejects duplicate keys, as go-yaml does. The typed decode then
    // rejects unknown fields and reports the location.
    let value = yaml_serde::from_slice::<yaml_serde::Value>(data)
        .map_err(|e| Error::Parse(e.to_string()))?;
    if let Some(section) = GO_SECTIONS.iter().find(|s| value.get(**s).is_some()) {
        return Err(Error::Parse(format!(
            "the Go section {section:?} is not part of schema 2 (the Rust stack has no go, go_modules, or go_tools sections)"
        )));
    }
    let stack: Stack = yaml_serde::from_slice(data).map_err(|e| Error::Parse(e.to_string()))?;
    stack.validate().map_err(Error::Invalid)?;
    Ok(stack)
}

/// Parses the stack versions file compiled into the binary.
pub fn load() -> Result<Stack, Error> {
    parse(EMBEDDED.as_bytes())
}

/// Returns the stack versions compiled into the binary.
///
/// # Panics
///
/// Panics when the embedded file is invalid. The tests of this crate guard the file.
pub fn embedded() -> &'static Stack {
    static STACK: OnceLock<Stack> = OnceLock::new();
    STACK.get_or_init(|| match load() {
        Ok(stack) => stack,
        Err(err) => panic!("the embedded stackversions.yaml is invalid: {err}"),
    })
}

#[cfg(test)]
mod tests {
    use std::path::Path;

    use super::*;

    fn go_file() -> String {
        let path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../internal/stackversions/stackversions.yaml");
        std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("read {}: {e}", path.display()))
    }

    #[test]
    fn embedded_file_loads() {
        let stack = load().expect("load");
        assert_eq!(stack.schema, 2);
        assert_eq!(embedded(), &stack);
        assert!(!stack.rust.toolchain.is_empty());
        assert!(!stack.crates.is_empty());
        assert!(!stack.cargo_tools.is_empty());
    }

    #[test]
    fn parse_rejects_unknown_fields() {
        let data = "schema: 2\nverified: today\nrust: {toolchain: '1.99.1', edition: '2024', min: '1.99'}\nunknown: value\n";
        let err = parse(data.as_bytes()).unwrap_err().to_string();
        assert!(err.contains("unknown field `unknown`"), "{err}");
        assert!(err.starts_with("parse stack versions: "), "{err}");
        let nested = "schema: 2\nimages: {x: {image: x, tag: '1', digest: d, extra: 1}}\n";
        let err = parse(nested.as_bytes()).unwrap_err().to_string();
        assert!(err.contains("unknown field `extra`"), "{err}");
    }

    #[test]
    fn parse_rejects_invalid_values_and_schema_1() {
        let data = "schema: 1\nverified: today\nrust: {toolchain: '1.99.1', edition: '2024', min: '1.99'}\n";
        let err = parse(data.as_bytes()).unwrap_err();
        assert!(matches!(err, Error::Invalid(_)));
        let text = err.to_string();
        assert!(text.starts_with("schema must be 2, got 1\n"), "{text}");
        assert!(
            text.ends_with("\nTry: fix these fields of stackversions.yaml"),
            "{text}"
        );
    }

    #[test]
    fn parse_rejects_duplicate_keys() {
        let data = "schema: 2\nnpm: {a: '1', a: '2'}\n";
        assert!(matches!(parse(data.as_bytes()), Err(Error::Parse(_))));
    }

    #[test]
    fn lookup_helpers_read_the_embedded_pins() {
        let stack = embedded();
        let postgres = stack.image("postgres").unwrap();
        assert_eq!(
            postgres.reference(),
            "postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336"
        );
        assert!(stack.npm_version("svelte").is_ok());
        assert!(stack.crate_version("axum").is_ok());
        assert_eq!(
            stack
                .action("actions/checkout")
                .unwrap()
                .uses("actions/checkout"),
            "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1"
        );
        assert!(stack.asset("sops", "darwin-arm64").is_ok());
        let digitalocean = &stack.digitalocean;
        for (name, value) in [
            ("image", &digitalocean.image),
            ("region", &digitalocean.region),
            ("size_production", &digitalocean.size_production),
            ("size_default", &digitalocean.size_default),
            ("ip_dns", &digitalocean.ip_dns),
        ] {
            assert!(!value.trim().is_empty(), "digitalocean.{name} is empty");
        }
    }

    #[test]
    fn parse_rejects_a_go_section() {
        for section in GO_SECTIONS {
            let data = format!(
                "schema: 2\nverified: today\nrust: {{toolchain: '1.99.1', edition: '2024', min: '1.99'}}\n{section}: {{}}\n"
            );
            let err = parse(data.as_bytes()).unwrap_err();
            assert!(matches!(err, Error::Parse(_)), "{section}: {err}");
            let text = err.to_string();
            assert!(
                text.contains(&format!("the Go section {section:?}")),
                "{text}"
            );
            assert!(text.contains("\nTry: "), "{text}");
        }
    }

    #[test]
    fn embedded_file_has_no_go_section() {
        let rust: yaml_serde::Value = yaml_serde::from_str(EMBEDDED).expect("Rust file");
        for section in GO_SECTIONS {
            assert!(rust.get(section).is_none(), "the Rust file has {section}");
        }
    }

    /// The lines above the marker are the Go file without its leading comment block and
    /// without the Go sections, with `schema: 2` (a port of `scripts/rust/gen-stack.sh`).
    #[test]
    fn generated_part_matches_the_go_file() {
        let go = go_file();
        let (generated, rust) = EMBEDDED
            .split_once(&format!("\n{MARKER}\n"))
            .expect("the marker line is missing");
        assert!(rust.contains("\nrust:") || rust.starts_with("rust:"));
        assert!(
            go.lines().any(|l| l == "schema: 1"),
            "the Go file is not schema 1"
        );
        let mut want = String::new();
        let mut started = false;
        let mut skip = false;
        for line in go.lines() {
            if !started && (line.starts_with('#') || line.is_empty()) {
                continue;
            }
            started = true;
            // A top-level key starts a section. The Go sections end at the next one.
            if !line.is_empty() && !line.starts_with([' ', '#']) {
                let key = line.split(':').next().unwrap_or_default();
                skip = GO_SECTIONS.contains(&key);
            }
            if skip {
                continue;
            }
            want.push_str(if line == "schema: 1" {
                "schema: 2"
            } else {
                line
            });
            want.push('\n');
        }
        let body = generated
            .lines()
            .skip_while(|l| l.starts_with('#'))
            .map(|l| format!("{l}\n"))
            .collect::<String>();
        // gen-stack.sh writes one empty line before the marker. The split removes it.
        assert_eq!(
            body, want,
            "crates/spinup-stack/stackversions.yaml is stale\nTry: scripts/rust/gen-stack.sh"
        );
    }

    /// The shared sections decode to the same values in both files. The Go sections are
    /// not shared.
    #[test]
    fn shared_sections_decode_equal() {
        let go: yaml_serde::Value = yaml_serde::from_str(&go_file()).expect("Go file");
        let rust: yaml_serde::Value = yaml_serde::from_str(EMBEDDED).expect("Rust file");
        let go_map = go.as_mapping().expect("Go mapping");
        for (key, value) in go_map {
            let name = key.as_str().expect("string key");
            if name == "schema" || GO_SECTIONS.contains(&name) {
                continue;
            }
            assert_eq!(rust.get(name), Some(value), "section {name} differs");
        }
        for name in ["rust", "crates", "cargo_tools"] {
            assert!(
                go.get(name).is_none(),
                "the Go file has the Rust section {name}"
            );
            assert!(
                rust.get(name).is_some(),
                "the Rust file has no section {name}"
            );
        }
    }
}
