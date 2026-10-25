//! The template version contract: [`Version`], [`Source`], and [`SourceError`] (Go
//! `internal/templates/source.go`, ADR 0006 §3, ADR 0020 §1 and §3).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it.
//!
//! Unit R1.12 implements [`Source`] in `store` (the current build, embedded archives, the
//! cache, downloads, and `SPINUP_TEMPLATES_DIR`).

use std::sync::Arc;

use spinup_stack::stack::Stack;
use spinup_vfs::tree::Tree;

/// The engine marker of a converted pack tree: the file `packs/ENGINE` holds this word
/// and a newline. A Go-era template version has no marker, and the Rust CLI rejects it.
pub const ENGINE: &str = "minijinja";

/// One template version: the packs and the stack versions that render it.
///
/// A render is a pure function of the version, the answers, the plugin set, and the stack.
#[derive(Debug, Clone)]
pub struct Version {
    /// The semantic version without a `v` prefix, such as `0.2.0` or `0.0.0-dev`.
    pub version: String,
    /// The pack tree, rooted at the packs directory. It holds `ENGINE` and one directory
    /// for each pack.
    pub packs: Arc<Tree>,
    /// The stack versions that shipped with these packs.
    pub stack: Arc<Stack>,
    /// The kit notes (`MIGRATION.json`) that shipped with this version, if any.
    pub kit_migration: Option<Arc<[u8]>>,
}

/// Loads template versions.
pub trait Source: Send + Sync {
    /// Lists every version that the source can load, in ascending semantic-version order.
    fn versions(&self) -> Result<Vec<String>, SourceError>;

    /// Returns one version. It returns [`SourceError::Unavailable`] when the version is not
    /// embedded, not cached, and cannot be downloaded (for example with `--offline`).
    fn load(&self, version: &str) -> Result<Arc<Version>, SourceError>;
}

/// The errors of a [`Source`].
#[derive(Debug, thiserror::Error)]
pub enum SourceError {
    /// The version cannot be loaded (Go `ErrVersionUnavailable`).
    #[error("template version {version} unavailable: {reason}\nTry: {hint}")]
    Unavailable {
        /// The requested version.
        version: String,
        /// Why it cannot be loaded.
        reason: String,
        /// The next step for the user, such as `spinup cache warm <version>`.
        hint: String,
    },
    /// The version was made for another template engine, such as a Go-era archive
    /// without `packs/ENGINE`.
    #[error(
        "template version {version} uses the template engine {found:?}, but this spinup needs {ENGINE:?}\nTry: upgrade to a template version from a Rust release of spinup"
    )]
    Engine {
        /// The requested version.
        version: String,
        /// The engine that the version declares. It is empty when the marker is missing.
        found: String,
    },
    /// Any other failure: a bad signature, a damaged archive, or a cache error.
    #[error(transparent)]
    Other(#[from] anyhow::Error),
}

impl SourceError {
    /// Returns true for [`SourceError::Unavailable`] (Go `errors.Is(err, ErrVersionUnavailable)`).
    pub fn is_unavailable(&self) -> bool {
        matches!(self, Self::Unavailable { .. })
    }
}
