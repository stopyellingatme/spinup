//! Cache warming for explicit versions and the nearest project lock.

use crate::{
    source::{Source, SourceError},
    store::{Store, validate_version},
};
use std::path::Path;

impl Store {
    /// Validates the whole request, then loads versions in lexical order.
    pub fn warm(&self, versions: &[String]) -> Result<(), SourceError> {
        if self.options.offline {
            return Err(anyhow::anyhow!("cannot warm templates: offline mode disables downloads\nTry: remove --offline and unset SPINUP_OFFLINE").into());
        }
        for version in versions {
            validate_version(version)?;
        }
        let mut versions = versions.to_vec();
        versions.sort();
        for version in versions {
            self.load(&version)?;
        }
        Ok(())
    }

    /// Warms the version recorded by the nearest project lock.
    pub fn warm_project(&self, start: impl AsRef<Path>) -> Result<String, SourceError> {
        let start = start.as_ref();
        let root = spinup_project::root::find_root(if start.as_os_str().is_empty() { Path::new(".") } else { start })
            .map_err(|error| anyhow::anyhow!("cannot locate project templates: {error}\nTry: run inside a spinup project or pass --dir <directory>"))?;
        let lock = spinup_manifest::save::load(root.join(spinup_project::config::LOCK_FILE))
            .map_err(anyhow::Error::from)?;
        self.warm(std::slice::from_ref(&lock.template_version))?;
        Ok(lock.template_version)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        source::Source,
        store::{Options, current},
    };

    #[test]
    fn warms_nearest_project_and_rejects_missing_project() {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().join("project");
        let child = root.join("child");
        std::fs::create_dir_all(&child).unwrap();
        std::fs::write(
            root.join("spinup.toml"),
            "[app]\nname = 'test'\nmodule = 'example.com/test'\n",
        )
        .unwrap();
        let mut lock = spinup_manifest::lock::Lock::new();
        lock.template_version = "1.2.3".into();
        lock.save(root.join("spinup.lock")).unwrap();
        let store = Store::new(Options {
            current: Some(current("v1.2.3").unwrap()),
            cache_dir: dir.path().join("cache"),
            ..Options::default()
        })
        .unwrap();
        assert_eq!(store.warm_project(&child).unwrap(), "1.2.3");
        assert!(
            store
                .warm_project(dir.path())
                .unwrap_err()
                .to_string()
                .ends_with("Try: run inside a spinup project or pass --dir <directory>")
        );
        assert_eq!(store.versions().unwrap(), ["1.2.3"]);
        let offline = Store::new(Options {
            offline: true,
            cache_dir: dir.path().join("cache"),
            ..Options::default()
        })
        .unwrap();
        assert!(
            offline
                .warm(&[])
                .unwrap_err()
                .to_string()
                .contains("offline mode disables downloads")
        );
        assert!(
            offline
                .load("2.0.0")
                .unwrap_err()
                .to_string()
                .ends_with("Try: run once without --offline")
        );
    }
}
