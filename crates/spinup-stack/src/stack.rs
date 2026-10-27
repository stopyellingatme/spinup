//! The stack versions model, schema 2 (ADR 0020 §3), and its validation and lookups.
//!
//! Seam (ADR 0020 §1): the contract files name [`Stack`]. The path stays
//! `spinup_stack::stack::Stack`, with the derives `Debug` and `Clone`.
//!
//! Schema 2 shares its sections with schema 1, except the Go sections. The R2 gate removed
//! the Go sections (`go`, `go_modules`, `go_tools`, rust-plan.md decision 11): no pack reads
//! them. Schema 2 adds the Rust sections `rust`, `crates`, and `cargo_tools`.
//!
//! The decode is strict: an unknown field is an error, as with Go `KnownFields(true)`.
//! A missing field takes its zero value, as in Go.
//!
//! The serialized form is the template data. It uses the Go field names (`Images`, `Tag`,
//! `SHA256`), because the converted templates read `data.Stack.Images` with these names.
//! Maps are sorted, so every walk is deterministic.

use std::collections::BTreeMap;
use std::fmt;

use serde::{Deserialize, Serialize};

use crate::pins;

/// The schema version that this crate reads.
pub const SCHEMA: u32 = 2;

/// A pinned container image.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Image {
    /// The image name, such as `postgres` or `grafana/loki`.
    #[serde(rename(serialize = "Image", deserialize = "image"))]
    pub image: String,
    /// The readable tag.
    #[serde(rename(serialize = "Tag", deserialize = "tag"))]
    pub tag: String,
    /// The multi-arch index digest, `sha256:<64 hex>`.
    #[serde(rename(serialize = "Digest", deserialize = "digest"))]
    pub digest: String,
}

impl Image {
    /// Returns the immutable image reference `<image>:<tag>@<digest>` (Go `Image.Ref`).
    pub fn reference(&self) -> String {
        format!("{}:{}@{}", self.image, self.tag, self.digest)
    }
}

/// A binary release asset and its SHA-256 digest.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Asset {
    /// The asset file name.
    #[serde(rename(serialize = "Name", deserialize = "name"))]
    pub name: String,
    /// The SHA-256 digest, 64 lowercase hex characters.
    #[serde(rename(serialize = "SHA256", deserialize = "sha256"))]
    pub sha256: String,
}

/// A downloadable binary release.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Binary {
    /// The release version.
    #[serde(rename(serialize = "Version", deserialize = "version"))]
    pub version: String,
    /// The GitHub repository, `owner/name`.
    #[serde(rename(serialize = "Repo", deserialize = "repo"))]
    pub repo: String,
    /// The assets, by platform (`darwin-arm64`, `linux-amd64`, and others).
    #[serde(rename(serialize = "Assets", deserialize = "assets"))]
    pub assets: BTreeMap<String, Asset>,
    /// The checksum file of the release, when it has one.
    #[serde(rename(serialize = "Checksums", deserialize = "checksums"))]
    pub checksums: BTreeMap<String, String>,
}

/// A GitHub Action, pinned by its commit SHA.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Action {
    /// The readable tag.
    #[serde(rename(serialize = "Tag", deserialize = "tag"))]
    pub tag: String,
    /// The full commit SHA, 40 lowercase hex characters.
    #[serde(rename(serialize = "SHA", deserialize = "sha"))]
    pub sha: String,
}

impl Action {
    /// Returns the `uses:` expression with a readable tag comment (Go `Action.Uses`).
    pub fn uses(&self, name: &str) -> String {
        format!("{name}@{} # {}", self.sha, self.tag)
    }
}

/// The Rust toolchain section (new in schema 2).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Rust {
    /// The pinned toolchain of `rust-toolchain.toml`, such as `1.99.1`.
    #[serde(rename(serialize = "Toolchain", deserialize = "toolchain"))]
    pub toolchain: String,
    /// The edition of generated crates, such as `2024`.
    #[serde(rename(serialize = "Edition", deserialize = "edition"))]
    pub edition: String,
    /// The `rust-version` of generated crates, such as `1.99`.
    #[serde(rename(serialize = "Min", deserialize = "min"))]
    pub min: String,
    /// The rustup components.
    #[serde(rename(serialize = "Components", deserialize = "components"))]
    pub components: Vec<String>,
}

/// The droplet defaults of `spinup deploy digital-ocean` (ADR 0017).
///
/// Validation does not require this section: template archives made before ADR 0017 do
/// not have it. Provisioning reads the stack versions compiled into the CLI.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct DigitalOcean {
    /// The droplet image slug.
    #[serde(rename(serialize = "Image", deserialize = "image"))]
    pub image: String,
    /// The region slug.
    #[serde(rename(serialize = "Region", deserialize = "region"))]
    pub region: String,
    /// The droplet size for production.
    #[serde(rename(serialize = "SizeProduction", deserialize = "size_production"))]
    pub size_production: String,
    /// The droplet size for other environments.
    #[serde(rename(serialize = "SizeDefault", deserialize = "size_default"))]
    pub size_default: String,
    /// The wildcard DNS service for hosts without `--domain`.
    #[serde(rename(serialize = "IPDNS", deserialize = "ip_dns"))]
    pub ip_dns: String,
}

/// The pinned stack versions that ship with one template version.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields, default)]
pub struct Stack {
    /// The schema version. This crate reads schema 2.
    #[serde(rename(serialize = "Schema", deserialize = "schema"))]
    pub schema: u32,
    /// The date of the last verification of the pins, `YYYY-MM-DD`.
    #[serde(rename(serialize = "Verified", deserialize = "verified"))]
    pub verified: String,
    /// npm package versions.
    #[serde(rename(serialize = "NPM", deserialize = "npm"))]
    pub npm: BTreeMap<String, String>,
    /// Debian package versions.
    #[serde(rename(serialize = "Apt", deserialize = "apt"))]
    pub apt: BTreeMap<String, String>,
    /// Host tool versions.
    #[serde(rename(serialize = "Tools", deserialize = "tools"))]
    pub tools: BTreeMap<String, String>,
    /// Container images.
    #[serde(rename(serialize = "Images", deserialize = "images"))]
    pub images: BTreeMap<String, Image>,
    /// Binary releases.
    #[serde(rename(serialize = "Binaries", deserialize = "binaries"))]
    pub binaries: BTreeMap<String, Binary>,
    /// GitHub Actions.
    #[serde(rename(serialize = "Actions", deserialize = "actions"))]
    pub actions: BTreeMap<String, Action>,
    /// The droplet defaults (ADR 0017).
    #[serde(rename(serialize = "DigitalOcean", deserialize = "digitalocean"))]
    pub digitalocean: DigitalOcean,
    /// The Rust toolchain (schema 2).
    #[serde(rename(serialize = "Rust", deserialize = "rust"))]
    pub rust: Rust,
    /// Crate versions of generated apps (schema 2).
    #[serde(rename(serialize = "Crates", deserialize = "crates"))]
    pub crates: BTreeMap<String, String>,
    /// Cargo host tool versions (schema 2).
    #[serde(rename(serialize = "CargoTools", deserialize = "cargo_tools"))]
    pub cargo_tools: BTreeMap<String, String>,
}

/// The validation problems of one stack versions file (Go `errors.Join`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Invalid(pub Vec<String>);

impl fmt::Display for Invalid {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0.join("\n"))
    }
}

/// A lookup of a pin that the stack versions do not have.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum NotFound {
    /// No image has this key.
    #[error("stack image {0:?} was not found")]
    Image(String),
    /// No npm package has this name.
    #[error("stack NPM package {0:?} was not found")]
    Npm(String),
    /// No crate has this name.
    #[error("stack crate {0:?} was not found")]
    Crate(String),
    /// No action has this name.
    #[error("stack action {0:?} was not found")]
    Action(String),
    /// No binary has this name.
    #[error("stack binary {0:?} was not found")]
    Binary(String),
    /// The binary has no asset for the platform.
    #[error("stack binary {binary:?} has no asset for platform {platform:?}")]
    Platform {
        /// The binary name.
        binary: String,
        /// The platform, such as `darwin-arm64`.
        platform: String,
    },
}

impl Stack {
    /// Reports every invalid field (Go `Stack.Validate`), in a stable order.
    pub fn validate(&self) -> Result<(), Invalid> {
        let mut problems = Vec::new();
        if self.schema != SCHEMA {
            problems.push(format!("schema must be {SCHEMA}, got {}", self.schema));
        }
        if self.verified.trim().is_empty() {
            problems.push("verified must not be empty".to_string());
        }
        if self.rust.toolchain.trim().is_empty() {
            problems.push("rust.toolchain must not be empty".to_string());
        }
        if self.rust.edition.trim().is_empty() {
            problems.push("rust.edition must not be empty".to_string());
        }
        if self.rust.min.trim().is_empty() {
            problems.push("rust.min must not be empty".to_string());
        }
        for (group, versions) in [
            ("npm", &self.npm),
            ("apt", &self.apt),
            ("tools", &self.tools),
            ("crates", &self.crates),
            ("cargo_tools", &self.cargo_tools),
        ] {
            for (name, version) in versions {
                if version.trim().is_empty() {
                    problems.push(format!("{group}.{name} must not be empty"));
                }
            }
        }
        for (name, image) in &self.images {
            if image.image.trim().is_empty() {
                problems.push(format!("images.{name}.image must not be empty"));
            }
            if image.tag.trim().is_empty() {
                problems.push(format!("images.{name}.tag must not be empty"));
            }
            if !pins::is_digest(&image.digest) {
                problems.push(format!("images.{name}.digest is invalid"));
            }
        }
        for (name, binary) in &self.binaries {
            if binary.version.trim().is_empty() {
                problems.push(format!("binaries.{name}.version must not be empty"));
            }
            if binary.repo.trim().is_empty() {
                problems.push(format!("binaries.{name}.repo must not be empty"));
            }
            for (platform, asset) in &binary.assets {
                if asset.name.trim().is_empty() {
                    problems.push(format!(
                        "binaries.{name}.assets.{platform}.name must not be empty"
                    ));
                }
                if !pins::is_sha256(&asset.sha256) {
                    problems.push(format!(
                        "binaries.{name}.assets.{platform}.sha256 is invalid"
                    ));
                }
            }
        }
        for (name, action) in &self.actions {
            if action.tag.trim().is_empty() {
                problems.push(format!("actions.{name}.tag must not be empty"));
            }
            if !pins::is_commit_sha(&action.sha) {
                problems.push(format!("actions.{name}.sha is invalid"));
            }
        }
        if problems.is_empty() {
            Ok(())
        } else {
            Err(Invalid(problems))
        }
    }

    /// Returns the named container image.
    pub fn image(&self, name: &str) -> Result<&Image, NotFound> {
        self.images
            .get(name)
            .ok_or_else(|| NotFound::Image(name.to_string()))
    }

    /// Returns the version of the named npm package.
    pub fn npm_version(&self, package: &str) -> Result<&str, NotFound> {
        self.npm
            .get(package)
            .map(String::as_str)
            .ok_or_else(|| NotFound::Npm(package.to_string()))
    }

    /// Returns the version of the named crate of generated apps.
    pub fn crate_version(&self, name: &str) -> Result<&str, NotFound> {
        self.crates
            .get(name)
            .map(String::as_str)
            .ok_or_else(|| NotFound::Crate(name.to_string()))
    }

    /// Returns the named GitHub Action.
    pub fn action(&self, name: &str) -> Result<&Action, NotFound> {
        self.actions
            .get(name)
            .ok_or_else(|| NotFound::Action(name.to_string()))
    }

    /// Returns the asset of a binary release for one platform.
    pub fn asset(&self, binary: &str, platform: &str) -> Result<&Asset, NotFound> {
        let release = self
            .binaries
            .get(binary)
            .ok_or_else(|| NotFound::Binary(binary.to_string()))?;
        release
            .assets
            .get(platform)
            .ok_or_else(|| NotFound::Platform {
                binary: binary.to_string(),
                platform: platform.to_string(),
            })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn valid() -> Stack {
        let a64 = "a".repeat(64);
        Stack {
            schema: 2,
            verified: "2026-10-13".into(),
            npm: [("example".into(), "1.0.0".into())].into(),
            apt: [("example".into(), "1.0.0".into())].into(),
            tools: [("example".into(), "1.0.0".into())].into(),
            images: [(
                "example".into(),
                Image {
                    image: "example".into(),
                    tag: "1.0.0".into(),
                    digest: format!("sha256:{a64}"),
                },
            )]
            .into(),
            binaries: [(
                "example".into(),
                Binary {
                    version: "1.0.0".into(),
                    repo: "example/repo".into(),
                    assets: [(
                        "darwin-arm64".into(),
                        Asset {
                            name: "example".into(),
                            sha256: a64.clone(),
                        },
                    )]
                    .into(),
                    checksums: BTreeMap::new(),
                },
            )]
            .into(),
            actions: [(
                "example/action".into(),
                Action {
                    tag: "v1".into(),
                    sha: "a".repeat(40),
                },
            )]
            .into(),
            digitalocean: DigitalOcean::default(),
            rust: Rust {
                toolchain: "1.99.1".into(),
                edition: "2024".into(),
                min: "1.99".into(),
                components: vec!["rustfmt".into()],
            },
            crates: [("axum".into(), "0.8.9".into())].into(),
            cargo_tools: [("cargo-nextest".into(), "0.9.146".into())].into(),
        }
    }

    #[test]
    fn validate_accepts_a_valid_stack() {
        assert_eq!(valid().validate(), Ok(()));
    }

    #[test]
    fn validate_reports_each_field() {
        type Edit = fn(&mut Stack);
        let cases: &[(&str, Edit, &str)] = &[
            ("schema", |s| s.schema = 1, "schema must be 2"),
            (
                "verified",
                |s| s.verified.clear(),
                "verified must not be empty",
            ),
            (
                "npm-version",
                |s| {
                    s.npm.insert("example".into(), String::new());
                },
                "npm.example",
            ),
            (
                "rust-toolchain",
                |s| s.rust.toolchain.clear(),
                "rust.toolchain",
            ),
            ("rust-edition", |s| s.rust.edition.clear(), "rust.edition"),
            ("rust-min", |s| s.rust.min.clear(), "rust.min"),
            (
                "crate-version",
                |s| {
                    s.crates.insert("axum".into(), " ".into());
                },
                "crates.axum",
            ),
            (
                "cargo-tool-version",
                |s| {
                    s.cargo_tools.insert("cargo-deny".into(), String::new());
                },
                "cargo_tools.cargo-deny",
            ),
            (
                "image-name",
                |s| s.images.get_mut("example").unwrap().image.clear(),
                "images.example.image",
            ),
            (
                "image-tag",
                |s| s.images.get_mut("example").unwrap().tag.clear(),
                "images.example.tag",
            ),
            (
                "image-digest",
                |s| s.images.get_mut("example").unwrap().digest = "sha256:bad".into(),
                "images.example.digest",
            ),
            (
                "binary-version",
                |s| s.binaries.get_mut("example").unwrap().version.clear(),
                "binaries.example.version",
            ),
            (
                "binary-repo",
                |s| s.binaries.get_mut("example").unwrap().repo.clear(),
                "binaries.example.repo",
            ),
            (
                "asset-name",
                |s| {
                    let binary = s.binaries.get_mut("example").unwrap();
                    binary.assets.get_mut("darwin-arm64").unwrap().name.clear();
                },
                "assets.darwin-arm64.name",
            ),
            (
                "asset-sha",
                |s| {
                    let binary = s.binaries.get_mut("example").unwrap();
                    binary.assets.get_mut("darwin-arm64").unwrap().sha256 = "bad".into();
                },
                "assets.darwin-arm64.sha256",
            ),
            (
                "action-tag",
                |s| s.actions.get_mut("example/action").unwrap().tag.clear(),
                "actions.example/action.tag",
            ),
            (
                "action-sha",
                |s| s.actions.get_mut("example/action").unwrap().sha = "bad".into(),
                "actions.example/action.sha",
            ),
        ];
        for (name, edit, want) in cases {
            let mut stack = valid();
            edit(&mut stack);
            let err = stack.validate().expect_err(name).to_string();
            assert!(err.contains(want), "{name}: error {err:?}, want {want:?}");
        }
    }

    #[test]
    fn serialize_has_no_go_sections() {
        let value = yaml_serde::to_value(valid()).unwrap();
        for name in ["Go", "GoModules", "GoTools", "go", "go_modules", "go_tools"] {
            assert!(value.get(name).is_none(), "the template data has {name}");
        }
    }

    #[test]
    fn validate_joins_every_problem() {
        let mut stack = valid();
        stack.verified.clear();
        stack.rust.toolchain.clear();
        let err = stack.validate().unwrap_err();
        assert_eq!(err.0.len(), 2);
        assert_eq!(
            err.to_string(),
            "verified must not be empty\nrust.toolchain must not be empty"
        );
    }

    #[test]
    fn serialize_uses_the_go_template_names() {
        let value = yaml_serde::to_value(valid()).unwrap();
        let image = &value["Images"]["example"];
        assert_eq!(image["Tag"].as_str(), Some("1.0.0"));
        let asset = &value["Binaries"]["example"]["Assets"]["darwin-arm64"];
        assert_eq!(asset["SHA256"].as_str(), Some("a".repeat(64).as_str()));
        assert_eq!(value["Rust"]["Toolchain"].as_str(), Some("1.99.1"));
        assert!(value.get("images").is_none());
    }

    #[test]
    fn reference_and_uses_format_like_go() {
        let image = Image {
            image: "postgres".into(),
            tag: "18".into(),
            digest: "sha256:abc".into(),
        };
        assert_eq!(image.reference(), "postgres:18@sha256:abc");
        let action = Action {
            tag: "v7.0.1".into(),
            sha: "3d3c".into(),
        };
        assert_eq!(
            action.uses("actions/checkout"),
            "actions/checkout@3d3c # v7.0.1"
        );
    }

    #[test]
    fn lookups_name_the_missing_key() {
        let stack = valid();
        assert!(stack.image("example").is_ok());
        assert!(stack.npm_version("example").is_ok());
        assert!(stack.crate_version("axum").is_ok());
        assert!(stack.action("example/action").is_ok());
        assert!(stack.asset("example", "darwin-arm64").is_ok());
        let errors = [
            stack.image("missing").unwrap_err(),
            stack.npm_version("missing").unwrap_err(),
            stack.crate_version("missing").unwrap_err(),
            stack.action("missing").unwrap_err(),
            stack.asset("missing", "darwin-arm64").unwrap_err(),
            stack.asset("example", "missing").unwrap_err(),
        ];
        for err in errors {
            assert!(err.to_string().contains("\"missing\""), "{err}");
        }
    }
}
