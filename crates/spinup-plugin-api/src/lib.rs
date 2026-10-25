//! The plugin contract: plugin metadata, [`Loaded`], and the [`PluginSource`] seam with its
//! three methods `fetch`, `trust`, and `render` (Go `internal/plugin/contract.go`, ADR 0013
//! §4, ADR 0020 §1 and §4).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it. This file is
//! also the module list of the crate.
//!
//! In R1, `spinup-newapp` and `spinup-upgrade` use the null source of unit R1.10
//! (`null_source`). In R4, `spinup-plugin` binds the real source (fetch with git, cache,
//! hash, allowlist, and the minijinja render of plugin packs).

use std::collections::BTreeMap;
use std::io::Write;
use std::path::Path;
use std::sync::Arc;

use serde::Deserialize;
use spinup_manifest::lock::Lock;
use spinup_vfs::plan::Plan;
use spinup_vfs::tree::Tree;

pub mod null_source;

/// The plugin API version that this CLI supports.
pub const API: u32 = 1;

/// The committed allowlist, relative to the project root.
pub const ALLOWLIST_FILE: &str = ".spinup/plugins.allow";

/// The decoded `plugin.toml` of a plugin.
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
pub struct Meta {
    /// The plugin name.
    pub name: String,
    /// The plugin version.
    pub version: String,
    /// The plugin API version.
    pub api: u32,
    /// The range of CLI versions that the plugin supports.
    pub spinup: String,
    /// One line about the plugin.
    #[serde(default)]
    pub description: String,
    /// The declarative generators of the plugin.
    #[serde(default)]
    pub generators: Vec<GeneratorSpec>,
}

/// A declarative generator of a plugin.
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
pub struct GeneratorSpec {
    /// The generator kind.
    pub name: String,
    /// One help line.
    #[serde(default)]
    pub short: String,
    /// The argument synopsis.
    #[serde(default)]
    pub usage: String,
    /// The names of the positional arguments.
    #[serde(default)]
    pub args: Vec<String>,
    /// The files that the generator writes.
    #[serde(default)]
    pub files: Vec<GenFile>,
}

/// One file of a declarative generator. `path` and `source` are templates over the
/// generator arguments. `class` is `managed`, `scaffold`, or `seed`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
pub struct GenFile {
    /// The output path template.
    pub path: String,
    /// The source template, relative to the plugin directory.
    pub source: String,
    /// The file class.
    pub class: String,
}

/// One plugin that `spinup.toml` declares (`[[plugins]]`).
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
pub struct Declared {
    /// The plugin name.
    pub name: String,
    /// The source, such as `path:./plugins/foo` or `git:https://host/owner/repo`.
    pub source: String,
    /// The version (the git tag for a git source).
    pub version: String,
}

/// One fetched plugin.
#[derive(Debug, Clone)]
pub struct Loaded {
    /// The decoded `plugin.toml`.
    pub meta: Meta,
    /// The declared source.
    pub source: String,
    /// The hex SHA-256 of the normalized plugin tree.
    pub sha256: String,
    /// The plugin tree (`plugin.toml`, `pack.toml`, `pack.d/`, `files/`).
    pub tree: Arc<Tree>,
}

/// The input of [`PluginSource::fetch`].
pub struct FetchRequest<'a> {
    /// The project root (the directory of `spinup.toml`).
    pub root: &'a Path,
    /// The plugins that `spinup.toml` declares, in declaration order.
    pub declared: &'a [Declared],
    /// Forbid network access. A plugin that is missing from the cache is an error.
    pub offline: bool,
    /// Version overrides by plugin name (`plugin update`, the OLD render of an upgrade).
    pub versions: &'a BTreeMap<String, String>,
    /// Progress lines.
    pub out: &'a mut (dyn Write + Send),
}

/// The errors of a [`PluginSource`].
#[derive(Debug, thiserror::Error)]
pub enum PluginError {
    /// This build has no plugin support, and the project declares plugins (Go
    /// `ErrNoResolver`).
    #[error(
        "plugin support is not in this build: the project declares {names}\nTry: use a spinup build with plugin support, or remove the plugins from spinup.toml"
    )]
    Unsupported {
        /// The declared plugin names, comma-separated.
        names: String,
    },
    /// A plugin is missing from the allowlist, or its hash differs from the allowlist or
    /// the lock.
    #[error("plugin {name} is not trusted: {reason}\nTry: {hint}")]
    Untrusted {
        /// The plugin name.
        name: String,
        /// Why the trust check failed.
        reason: String,
        /// The next step for the user, such as `spinup plugin add`.
        hint: String,
    },
    /// Any other failure: fetch, cache, decode, or render.
    #[error(transparent)]
    Other(#[from] anyhow::Error),
}

/// The plugin seam of `spinup new`, `spinup upgrade`, and `spinup generate`.
///
/// spinup never runs code from a tier-1 plugin: a plugin is data (templates and metadata).
pub trait PluginSource: Send + Sync {
    /// Fetches (or reads from the cache) every declared plugin, decodes its metadata, and
    /// hashes its tree. It does not decide trust. A project without plugins returns an
    /// empty list.
    fn fetch(&self, request: &mut FetchRequest<'_>) -> Result<Vec<Loaded>, PluginError>;

    /// Checks each plugin against the allowlist (`.spinup/plugins.allow`) and the plugin
    /// entries of `lock`. A missing entry or a different hash is
    /// [`PluginError::Untrusted`].
    fn trust(&self, root: &Path, plugins: &[Loaded], lock: &Lock) -> Result<(), PluginError>;

    /// Renders the pack of one trusted plugin with the answers. The plan paths use the lock
    /// template prefix `plugin/<name>/`.
    fn render(
        &self,
        plugin: &Loaded,
        answers: &serde_json::Map<String, serde_json::Value>,
    ) -> Result<Plan, PluginError>;
}
