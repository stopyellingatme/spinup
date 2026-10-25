//! The YAML splice editor on tree-sitter source spans (R0.1, ADR 0020 §7).
//!
//! The editor replaces Go `codemod.EditYAML`, `YAMLGet`, `YAMLSet`, and `YAMLDelete`.
//! Go decodes the whole document and encodes it again. This editor changes only the bytes
//! of the edited node, so the rest of the file keeps its comments, quotes, and layout.
//! New values use the go-yaml output rules, with an indentation of 2. For a document in
//! go-yaml style, the output equals the Go output. Comments stay where Go keeps them, but
//! a comment of a removed or replaced node can land on another line than in Go.
//!
//! [`marshal_value`] is the Go Compose writer (`yaml.Unmarshal` into `any`, then
//! `yaml.Marshal`). It sorts keys and removes comments, as Go does (ADR 0020 §7).

mod doc;
mod emit;
mod node;

#[cfg(test)]
mod tests;

pub use doc::Document;
pub use emit::{marshal, marshal_value};
pub use node::{Kind, Node, Resolved, Style, Tag, resolve};

/// A YAML edit that failed.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("edit YAML: {reason}\nTry: check the document and the migration input")]
pub struct Error {
    reason: String,
}

impl Error {
    /// Returns an error with the reason why the edit failed.
    pub fn new(reason: impl Into<String>) -> Self {
        Self {
            reason: reason.into(),
        }
    }

    /// Returns the reason why the edit failed.
    pub fn reason(&self) -> &str {
        &self.reason
    }
}

/// Edits one YAML document (Go `codemod.EditYAML`).
///
/// `change` returns whether it changed the document. An unchanged document returns the
/// original text and `false`. The source must hold one valid document.
pub fn edit<F>(src: &str, change: F) -> Result<(String, bool), Error>
where
    F: FnOnce(&mut Document) -> Result<bool, Error>,
{
    let mut doc = Document::parse(src)?;
    let changed = change(&mut doc)?;
    if !changed || doc.as_str() == src {
        return Ok((src.to_string(), false));
    }
    Ok((doc.into_string(), true))
}
