//! The null plugin source for R1 application creation.

use crate::select::Options;
use anyhow::Result;
use spinup_manifest::lock::Lock;
use spinup_plugin_api::{Declared, FetchRequest, PluginSource, null_source::NullSource};
use std::{collections::BTreeMap, path::Path};

/// Checks requested sources through the R1 seam before any app file is written.
pub fn check_plugins(options: &Options, dir: &Path, lock: &Lock) -> Result<()> {
    let declared: Vec<_> = options
        .plugins
        .iter()
        .map(|value| {
            let (source, version) = split_plugin(value);
            Declared {
                name: source.into(),
                source: source.into(),
                version: version.into(),
            }
        })
        .collect();
    let mut out = Vec::new();
    let versions = BTreeMap::new();
    let source = NullSource::new();
    let loaded = source.fetch(&mut FetchRequest {
        root: dir,
        declared: &declared,
        offline: options.offline,
        versions: &versions,
        out: &mut out,
    })?;
    source.trust(dir, &loaded, lock)?;
    Ok(())
}

/// Splits an optional version suffix without splitting source paths or SSH users.
pub fn split_plugin(value: &str) -> (&str, &str) {
    if let Some(index) = value.rfind('@')
        && value.find(':').is_none_or(|colon| index > colon)
        && !value[index + 1..].contains('/')
    {
        return (&value[..index], &value[index + 1..]);
    }
    (value, "")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn version_suffixes_match_go() {
        for (value, expected) in [
            ("path:../plugin@v1", ("path:../plugin", "v1")),
            (
                "git:ssh://git@host/org/repo",
                ("git:ssh://git@host/org/repo", ""),
            ),
            (
                "git:ssh://git@host/org/repo@v2",
                ("git:ssh://git@host/org/repo", "v2"),
            ),
            ("path:../foo@bar/plugin", ("path:../foo@bar/plugin", "")),
        ] {
            assert_eq!(split_plugin(value), expected);
        }
    }

    #[test]
    fn null_source_rejects_before_writes() {
        let dir = tempfile::tempdir().unwrap();
        let mut options = Options::default();
        check_plugins(&options, dir.path(), &Lock::new()).unwrap();
        options.plugins.push("path:../demo@v1".into());
        options.trust = true;
        let error = check_plugins(&options, dir.path(), &Lock::new())
            .unwrap_err()
            .to_string();
        assert!(error.contains("plugin support is not in this build") && error.contains("Try:"));
        assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 0);
    }
}
