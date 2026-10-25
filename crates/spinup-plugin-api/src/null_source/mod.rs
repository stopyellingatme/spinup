//! The null [`PluginSource`] of R1: a build without plugin support (Go `plugin.Resolve`
//! without a resolver, `ErrNoResolver`).
//!
//! A project without plugins works as usual: [`NullSource::fetch`] returns no plugins, and
//! [`NullSource::trust`] accepts the empty list. A project that declares plugins fails with
//! [`PluginError::Unsupported`], which names the plugins and ends with a `Try:` line.

use std::path::Path;

use spinup_manifest::lock::Lock;
use spinup_vfs::plan::Plan;

use crate::{FetchRequest, Loaded, PluginError, PluginSource};

/// The plugin source of a build without plugin support.
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub struct NullSource;

impl NullSource {
    /// Returns the null source.
    pub const fn new() -> Self {
        Self
    }
}

/// The error for the named plugins.
fn unsupported<'a>(names: impl IntoIterator<Item = &'a str>) -> PluginError {
    PluginError::Unsupported {
        names: names.into_iter().collect::<Vec<_>>().join(", "),
    }
}

impl PluginSource for NullSource {
    fn fetch(&self, request: &mut FetchRequest<'_>) -> Result<Vec<Loaded>, PluginError> {
        if request.declared.is_empty() {
            return Ok(Vec::new());
        }
        Err(unsupported(
            request.declared.iter().map(|d| d.name.as_str()),
        ))
    }

    fn trust(&self, _root: &Path, plugins: &[Loaded], _lock: &Lock) -> Result<(), PluginError> {
        if plugins.is_empty() {
            return Ok(());
        }
        Err(unsupported(plugins.iter().map(|p| p.meta.name.as_str())))
    }

    fn render(
        &self,
        plugin: &Loaded,
        _answers: &serde_json::Map<String, serde_json::Value>,
    ) -> Result<Plan, PluginError> {
        Err(unsupported([plugin.meta.name.as_str()]))
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::sync::Arc;

    use super::*;
    use crate::{Declared, Meta};
    use spinup_vfs::tree::Tree;

    fn declared(name: &str) -> Declared {
        Declared {
            name: name.into(),
            source: format!("path:./plugins/{name}"),
            version: "v1.0.0".into(),
        }
    }

    fn fetch(declared: &[Declared]) -> (Result<Vec<Loaded>, PluginError>, Vec<u8>) {
        let versions = BTreeMap::new();
        let mut out = Vec::new();
        let result = NullSource::new().fetch(&mut FetchRequest {
            root: Path::new("."),
            declared,
            offline: true,
            versions: &versions,
            out: &mut out,
        });
        (result, out)
    }

    fn loaded(name: &str) -> Loaded {
        Loaded {
            meta: Meta {
                name: name.into(),
                ..Meta::default()
            },
            source: format!("path:./plugins/{name}"),
            sha256: String::new(),
            tree: Arc::new(Tree::new()),
        }
    }

    #[test]
    fn a_project_without_plugins_works() {
        let (result, out) = fetch(&[]);
        assert!(result.unwrap().is_empty());
        assert!(out.is_empty());
        NullSource.trust(Path::new("."), &[], &Lock::new()).unwrap();
    }

    #[test]
    fn declared_plugins_are_unsupported() {
        let (result, _) = fetch(&[declared("audit"), declared("billing")]);
        let err = result.unwrap_err();
        assert!(matches!(&err, PluginError::Unsupported { names } if names == "audit, billing"));
        let text = err.to_string();
        assert!(
            text.starts_with(
                "plugin support is not in this build: the project declares audit, billing\n"
            ),
            "{text}"
        );
        assert_eq!(text.matches("Try:").count(), 1);
    }

    #[test]
    fn trust_and_render_refuse_plugins() {
        let plugin = loaded("audit");
        let err = NullSource
            .trust(Path::new("."), std::slice::from_ref(&plugin), &Lock::new())
            .unwrap_err();
        assert!(matches!(err, PluginError::Unsupported { names } if names == "audit"));
        let err = NullSource
            .render(&plugin, &serde_json::Map::new())
            .unwrap_err();
        assert!(matches!(err, PluginError::Unsupported { names } if names == "audit"));
    }

    #[test]
    fn works_as_a_shared_plugin_source() {
        let source: Arc<dyn PluginSource> = Arc::new(NullSource);
        let plugin = loaded("audit");
        assert!(source.render(&plugin, &serde_json::Map::new()).is_err());
    }
}
