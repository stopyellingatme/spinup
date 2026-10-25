//! Template source resolution in priority order.

use crate::{
    archive::{self, MAX_ARCHIVE, MAX_UNPACKED},
    cache, keys,
    source::{Source, SourceError, Version},
};
use anyhow::{Result, bail};
use ed25519_dalek::VerifyingKey;
use spinup_compat::version::parse_version;
use std::{
    collections::{BTreeMap, BTreeSet},
    fs, io,
    path::{Path, PathBuf},
    sync::Arc,
    time::Duration,
};

/// Validates a semantic version without a `v` prefix.
pub fn validate_version(version: &str) -> Result<()> {
    let (core, metadata) = version
        .split_once('+')
        .map_or((version, None), |(a, b)| (a, Some(b)));
    let valid_metadata = metadata.is_none_or(|text| {
        text.split('.').all(|part| {
            !part.is_empty() && part.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
        })
    });
    if version.starts_with('v') || parse_version(core).is_err() || !valid_metadata {
        bail!(
            "invalid template version {version:?}: expected a semantic version without v\nTry: use a version such as 0.2.0"
        );
    }
    Ok(())
}

/// Returns the template version of this build, or `0.0.0-dev` for a development build.
pub fn current_version(cli_version: &str) -> String {
    let version = cli_version.strip_prefix('v').unwrap_or(cli_version);
    if validate_version(version).is_ok() {
        version.into()
    } else {
        "0.0.0-dev".into()
    }
}

/// A bounded download transport. Tests can use an in-memory implementation.
pub trait Downloader: Send + Sync {
    /// Downloads one asset. The caller also checks the returned length.
    fn get(&self, url: &str, limit: usize) -> Result<Vec<u8>>;
}

struct HttpDownloader(ureq::Agent);
impl Downloader for HttpDownloader {
    fn get(&self, url: &str, limit: usize) -> Result<Vec<u8>> {
        let response = self.0.get(url).call()?;
        if response.status().as_u16() != 200 {
            bail!("download {url}: HTTP {}", response.status());
        }
        archive::read_limited(response.into_body().into_reader(), limit)
    }
}

/// Configuration for the local sources, cache, and downloads.
#[derive(Clone, Default)]
pub struct Options {
    /// The packs in this build, selected before all other sources.
    pub current: Option<Arc<Version>>,
    /// Embedded release archives, keyed by version. The release set starts empty.
    pub embedded: BTreeMap<String, Vec<u8>>,
    /// The cache directory. An empty path selects the user cache directory.
    pub cache_dir: PathBuf,
    /// The directory of unpacked versions from `SPINUP_TEMPLATES_DIR`.
    pub extra_dir: PathBuf,
    /// Disables downloads.
    pub offline: bool,
    /// The release URL prefix. An empty string selects GitHub releases.
    pub base_url: String,
    /// Trusted signing keys. `None` selects the embedded keys.
    pub keys: Option<Vec<VerifyingKey>>,
    /// An optional transport for downloads.
    pub downloader: Option<Arc<dyn Downloader>>,
}

/// A locally available version and the selected source.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Entry {
    /// The semantic version without `v`.
    pub version: String,
    /// One of `current`, `extra`, `embedded`, or `cache`.
    pub source: String,
}

/// Resolves current, extra, embedded, cached, and downloaded template versions.
pub struct Store {
    pub(crate) options: Options,
}

/// Returns the packs and stack versions compiled into this build.
pub fn current(cli_version: &str) -> Result<Arc<Version>> {
    Ok(Arc::new(Version {
        version: current_version(cli_version),
        packs: Arc::new(spinup_packs::embed::embedded()?),
        stack: Arc::new(spinup_stack::load::load()?),
        kit_migration: None,
    }))
}

/// Creates a store with the environment settings and this build's packs.
pub fn default(cli_version: &str, offline: bool) -> Result<Store> {
    Store::new(Options {
        current: Some(current(cli_version)?),
        extra_dir: std::env::var_os("SPINUP_TEMPLATES_DIR")
            .map(PathBuf::from)
            .unwrap_or_default(),
        offline: offline || std::env::var("SPINUP_OFFLINE").is_ok_and(|value| value == "1"),
        ..Options::default()
    })
}

impl Store {
    /// Creates a store without downloads or writes.
    pub fn new(mut options: Options) -> Result<Self> {
        if let Some(current) = &options.current {
            validate_version(&current.version)?;
            archive::check_engine(&current.packs, &current.version)?;
        }
        for version in options.embedded.keys() {
            validate_version(version)?;
        }
        if options.cache_dir.as_os_str().is_empty() {
            options.cache_dir = dirs::cache_dir().ok_or_else(|| anyhow::anyhow!(
                "cannot locate template cache: user cache directory is unavailable\nTry: configure the user cache directory"
            ))?.join("spinup/templates");
        }
        if options.base_url.is_empty() {
            options.base_url = "https://github.com/stopyellingatme/spinup/releases/download".into();
        }
        if options.keys.is_none() {
            options.keys = Some(keys::trusted_keys()?);
        }
        if options.downloader.is_none() {
            let config = ureq::Agent::config_builder()
                .timeout_global(Some(Duration::from_secs(60)))
                .build();
            options.downloader = Some(Arc::new(HttpDownloader(config.into())));
        }
        Ok(Self { options })
    }

    /// Returns the directory of downloaded archives and signatures.
    pub fn cache_dir(&self) -> &Path {
        &self.options.cache_dir
    }

    /// Lists loadable local versions and their sources without downloads.
    pub fn list(&self) -> std::result::Result<Vec<Entry>, SourceError> {
        let mut candidates = BTreeSet::new();
        if let Some(current) = &self.options.current {
            candidates.insert(current.version.clone());
        }
        candidates.extend(self.options.embedded.keys().cloned());
        for (directory, extra) in [
            (&self.options.cache_dir, false),
            (&self.options.extra_dir, true),
        ] {
            if directory.as_os_str().is_empty() {
                continue;
            }
            for (name, is_dir) in
                cache::directory_names(directory).map_err(|e| self.unavailable("", e))?
            {
                if extra && is_dir && validate_version(&name).is_ok() {
                    candidates.insert(name);
                } else if !extra
                    && !is_dir
                    && let Some(version) = cache::version_from_name(&name)
                {
                    candidates.insert(version.into());
                }
            }
        }
        let mut versions: Vec<_> = candidates
            .into_iter()
            .map(|v| {
                let core = v.split('+').next().unwrap_or(&v);
                (parse_version(core).expect("validated candidate"), v)
            })
            .collect();
        versions.sort();
        Ok(versions
            .into_iter()
            .filter_map(|(_, version)| {
                self.local(&version).ok().map(|(_, source)| Entry {
                    version,
                    source: source.into(),
                })
            })
            .collect())
    }

    /// Removes cached archives and signatures.
    pub fn clean(&self) -> Result<()> {
        cache::clean(&self.options.cache_dir).map_err(|error| {
            anyhow::anyhow!(
                "cannot remove template cache: {error}\nTry: check the cache directory permissions"
            )
        })
    }

    fn local(
        &self,
        version: &str,
    ) -> std::result::Result<(Arc<Version>, &'static str), (&'static str, SourceError)> {
        if let Some(current) = &self.options.current
            && current.version == version
        {
            archive::check_engine(&current.packs, version).map_err(|e| ("current", e))?;
            return Ok((current.clone(), "current"));
        }
        if !self.options.extra_dir.as_os_str().is_empty() {
            let path = self.options.extra_dir.join(version);
            match fs::symlink_metadata(&path) {
                Ok(_) => {
                    return read_extra(&path, version)
                        .map(|v| (Arc::new(v), "extra"))
                        .map_err(|e| ("extra", e));
                }
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(error) => return Err(("extra", SourceError::Other(error.into()))),
            }
        }
        if let Some(data) = self.options.embedded.get(version) {
            return archive::read_archive(data, version)
                .map(|v| (Arc::new(v), "embedded"))
                .map_err(|e| ("embedded", e));
        }
        let result = (|| -> Result<_> {
            let name = cache::archive_name(version)?;
            let data = cache::read(&self.options.cache_dir.join(&name), MAX_ARCHIVE)?;
            let signature = cache::read(&self.options.cache_dir.join(format!("{name}.sig")), 64)?;
            keys::verify(
                &data,
                &signature,
                self.options.keys.as_deref().unwrap_or_default(),
            )?;
            Ok(data)
        })();
        match result {
            Ok(data) => archive::read_archive(&data, version)
                .map(|v| (Arc::new(v), "cache"))
                .map_err(|e| ("cache", e)),
            Err(error) => {
                // A missing signature is an invalid cache, not an absent version.
                let archive_exists = cache::archive_name(version)
                    .is_ok_and(|name| self.options.cache_dir.join(name).symlink_metadata().is_ok());
                let source = if archive_exists
                    || !error
                        .downcast_ref::<io::Error>()
                        .is_some_and(|e| e.kind() == io::ErrorKind::NotFound)
                {
                    "cache"
                } else {
                    ""
                };
                Err((source, SourceError::Other(error)))
            }
        }
    }

    fn download(&self, version: &str) -> std::result::Result<Arc<Version>, SourceError> {
        let keys = self.options.keys.as_deref().unwrap_or_default();
        if keys.is_empty() {
            keys::verify(&[], &[], keys)?;
        }
        let downloader = self
            .options
            .downloader
            .as_ref()
            .expect("constructor sets transport");
        let name = cache::archive_name(version)?;
        let url = format!(
            "{}/v{version}/{name}",
            self.options.base_url.trim_end_matches('/')
        );
        let data = downloader.get(&url, MAX_ARCHIVE)?;
        let signature = downloader.get(&format!("{url}.sig"), 64)?;
        if data.len() > MAX_ARCHIVE || signature.len() != 64 {
            return Err(SourceError::Other(archive::archive_error(
                "template data exceeds the size limit",
            )));
        }
        keys::verify(&data, &signature, keys)?;
        let version = Arc::new(archive::read_archive(&data, version)?);
        cache::publish(&self.options.cache_dir, &version.version, &data, &signature)?;
        Ok(version)
    }

    fn unavailable(&self, version: &str, reason: impl std::fmt::Display) -> SourceError {
        SourceError::Unavailable {
            version: version.into(),
            reason: reason.to_string(),
            hint: if self.options.offline {
                "run once without --offline".into()
            } else {
                format!("spinup cache warm {version}").trim().into()
            },
        }
    }
}

impl Source for Store {
    fn versions(&self) -> std::result::Result<Vec<String>, SourceError> {
        Ok(self
            .list()?
            .into_iter()
            .map(|entry| entry.version)
            .collect())
    }

    fn load(&self, version: &str) -> std::result::Result<Arc<Version>, SourceError> {
        validate_version(version)?;
        match self.local(version) {
            Ok((version, _)) => Ok(version),
            Err((_, error @ SourceError::Engine { .. })) => Err(error),
            Err((source, error)) => {
                if self.options.offline {
                    let mut error = self.unavailable(version, error);
                    if source == "cache"
                        && let SourceError::Unavailable { hint, .. } = &mut error
                    {
                        *hint = "spinup cache clean".into();
                    }
                    return Err(error);
                }
                if !matches!(source, "" | "cache") {
                    return Err(self.unavailable(version, error));
                }
                self.download(version).map_err(|error| match error {
                    error @ SourceError::Engine { .. } => error,
                    error => self.unavailable(version, error),
                })
            }
        }
    }
}

fn read_extra(path: &Path, version: &str) -> std::result::Result<Version, SourceError> {
    let read = || -> Result<_> {
        let metadata = fs::symlink_metadata(path)?;
        anyhow::ensure!(
            metadata.is_dir() && !metadata.is_symlink(),
            "extra version is not a regular directory"
        );
        let mut files = spinup_vfs::tree::Tree::new();
        let mut count = 0;
        let mut total = 0;
        read_tree(path, path, &mut files, &mut count, &mut total)?;
        anyhow::ensure!(files.is_dir("packs"), "packs is not a directory");
        let stack = files
            .get("stackversions.yaml")
            .ok_or_else(|| anyhow::anyhow!("missing stackversions.yaml"))?
            .data
            .to_vec();
        let notes = files.get("MIGRATION.json").map(|file| file.data.to_vec());
        Ok((files.sub("packs"), stack, notes))
    };
    let (packs, stack, notes) = read().map_err(archive::archive_error)?;
    archive::make_version(version, packs, &stack, notes)
}

fn read_tree(
    root: &Path,
    path: &Path,
    tree: &mut spinup_vfs::tree::Tree,
    count: &mut usize,
    total: &mut usize,
) -> Result<()> {
    let mut entries = fs::read_dir(path)?.collect::<std::io::Result<Vec<_>>>()?;
    entries.sort_by_key(|entry| entry.file_name());
    for entry in entries {
        if path == root
            && !matches!(
                entry.file_name().to_str(),
                Some("packs" | "stackversions.yaml" | "MIGRATION.json")
            )
        {
            continue;
        }
        *count += 1;
        anyhow::ensure!(
            *count <= archive::MAX_ENTRIES,
            "archive has more than 10000 entries"
        );
        let kind = entry.file_type()?;
        let path = entry.path();
        let relative = path
            .strip_prefix(root)?
            .to_str()
            .ok_or_else(|| anyhow::anyhow!("extra path is not UTF-8"))?
            .replace(std::path::MAIN_SEPARATOR, "/");
        anyhow::ensure!(
            archive::valid_path(&relative),
            "unsafe archive path {relative:?}"
        );
        anyhow::ensure!(
            !kind.is_symlink() && (kind.is_dir() || kind.is_file()),
            "unsupported extra file {relative:?}"
        );
        if kind.is_dir() {
            read_tree(root, &path, tree, count, total)?;
        } else {
            let data = cache::read(&path, MAX_UNPACKED - *total)?;
            *total += data.len();
            #[cfg(unix)]
            let mode = {
                use std::os::unix::fs::PermissionsExt;
                entry.metadata()?.permissions().mode() & 0o7777
            };
            #[cfg(not(unix))]
            let mode = 0o644;
            tree.insert(relative, data, mode)?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::source::Source;
    use ed25519_dalek::{Signer, SigningKey};
    use std::sync::{Arc, Mutex};

    pub(super) struct Download {
        pub(super) data: Vec<u8>,
        pub(super) signature: Vec<u8>,
        pub(super) calls: Mutex<Vec<String>>,
    }
    impl Downloader for Download {
        fn get(&self, url: &str, _limit: usize) -> Result<Vec<u8>> {
            self.calls.lock().unwrap().push(url.into());
            Ok(if url.ends_with(".sig") {
                self.signature.clone()
            } else {
                self.data.clone()
            })
        }
    }

    fn archive(marker: &str) -> Vec<u8> {
        let mut tree = spinup_vfs::tree::Tree::new();
        tree.insert("ENGINE", b"minijinja\n".to_vec(), 0o644)
            .unwrap();
        tree.insert("base/marker", marker.as_bytes().to_vec(), 0o644)
            .unwrap();
        let mut data = Vec::new();
        crate::archive::write_archive(
            &mut data,
            &tree,
            spinup_stack::load::EMBEDDED.as_bytes(),
            None,
        )
        .unwrap();
        data
    }

    #[test]
    fn download_cache_repair_and_offline_reverification() {
        let dir = tempfile::tempdir().unwrap();
        let cache = dir.path().join("absent/templates");
        let private = SigningKey::from_bytes(&[42; 32]);
        let data = archive("download");
        let download = Arc::new(Download {
            signature: private.sign(&data).to_bytes().to_vec(),
            data,
            calls: Mutex::default(),
        });
        let options = Options {
            cache_dir: cache.clone(),
            keys: Some(vec![private.verifying_key()]),
            downloader: Some(download.clone()),
            ..Options::default()
        };
        let store = Store::new(options.clone()).unwrap();
        store.warm(&["1.2.3".into()]).unwrap();
        assert_eq!(download.calls.lock().unwrap().len(), 2);
        assert_eq!(
            store
                .load("1.2.3")
                .unwrap()
                .packs
                .get("base/marker")
                .unwrap()
                .data
                .as_ref(),
            b"download"
        );
        store.warm(&["1.2.3".into()]).unwrap();
        assert_eq!(download.calls.lock().unwrap().len(), 2);
        std::fs::write(cache.join("templates-v1.2.3.tar.zst"), b"broken").unwrap();
        let offline = Store::new(Options {
            offline: true,
            ..options.clone()
        })
        .unwrap();
        assert!(
            offline
                .load("1.2.3")
                .unwrap_err()
                .to_string()
                .ends_with("Try: spinup cache clean")
        );
        assert!(offline.list().unwrap().is_empty());
        store.load("1.2.3").unwrap();
        assert_eq!(download.calls.lock().unwrap().len(), 4);
        let entries = offline.list().unwrap();
        assert_eq!(entries[0].source, "cache");
        std::fs::write(cache.join("templates-v1.2.3.tar.zst.sig"), [0; 64]).unwrap();
        assert!(offline.load("1.2.3").is_err());
        store.clean().unwrap();
        assert!(store.list().unwrap().is_empty());
    }

    #[test]
    fn source_priority_and_semantic_order() {
        let dir = tempfile::tempdir().unwrap();
        let extra = dir.path().join("extra");
        std::fs::create_dir_all(extra.join("0.2.0/packs/base")).unwrap();
        std::fs::write(extra.join("0.2.0/packs/ENGINE"), b"minijinja\n").unwrap();
        std::fs::write(extra.join("0.2.0/packs/base/marker"), b"extra").unwrap();
        std::fs::write(
            extra.join("0.2.0/stackversions.yaml"),
            spinup_stack::load::EMBEDDED,
        )
        .unwrap();
        std::fs::write(extra.join("0.2.0/MIGRATION.json"), b"notes").unwrap();
        let mut embedded = std::collections::BTreeMap::new();
        for version in [
            "0.2.0",
            "0.10.0",
            "0.2.0-rc.10",
            "0.2.0-rc.2",
            "0.2.0+build.2",
            "0.2.0+build.1",
        ] {
            embedded.insert(version.into(), archive("embedded"));
        }
        let current = Arc::new(crate::archive::read_archive(&archive("current"), "0.2.0").unwrap());
        let options = Options {
            current: Some(current),
            embedded,
            extra_dir: extra,
            cache_dir: dir.path().join("cache"),
            offline: true,
            ..Options::default()
        };
        for (source, options) in [
            ("current", options.clone()),
            (
                "extra",
                Options {
                    current: None,
                    ..options.clone()
                },
            ),
            (
                "embedded",
                Options {
                    current: None,
                    extra_dir: Default::default(),
                    ..options.clone()
                },
            ),
        ] {
            let store = Store::new(options).unwrap();
            let version = store.load("0.2.0").unwrap();
            assert_eq!(
                version.packs.get("base/marker").unwrap().data.as_ref(),
                source.as_bytes()
            );
            assert_eq!(
                store
                    .list()
                    .unwrap()
                    .iter()
                    .find(|entry| entry.version == "0.2.0")
                    .unwrap()
                    .source,
                source
            );
        }
        let store = Store::new(options).unwrap();
        assert_eq!(
            store.versions().unwrap(),
            [
                "0.2.0-rc.2",
                "0.2.0-rc.10",
                "0.2.0",
                "0.2.0+build.1",
                "0.2.0+build.2",
                "0.10.0"
            ]
        );
    }

    #[test]
    fn invalid_requests_and_bad_sources_never_download() {
        let dir = tempfile::tempdir().unwrap();
        let download = Arc::new(Download {
            data: Vec::new(),
            signature: Vec::new(),
            calls: Mutex::default(),
        });
        let store = Store::new(Options {
            downloader: Some(download.clone()),
            cache_dir: dir.path().join("absent"),
            ..Options::default()
        })
        .unwrap();
        assert!(
            store
                .warm(&["1.0.0".into(), "../../escape".into()])
                .is_err()
        );
        for version in [
            "v1.0.0",
            "../bad",
            "1.0",
            "1.0.0-01",
            "1.0.0/x",
            "1.0.0+../x",
            "",
        ] {
            assert!(store.load(version).is_err(), "{version}");
        }
        assert!(download.calls.lock().unwrap().is_empty());
        assert!(!store.cache_dir().exists());
        for input in ["dev", "", "branch-sha", "1.2.3-01", "01.2.3", "1.2.3+"] {
            assert_eq!(current_version(input), "0.0.0-dev");
        }
        assert_eq!(current_version("v1.2.3"), "1.2.3");
        assert_eq!(current_version("1.2.3-rc.1+build.4"), "1.2.3-rc.1+build.4");
    }
}

#[cfg(test)]
mod failure_tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};
    use std::sync::Mutex;

    struct Download {
        data: Vec<u8>,
        signature: Vec<u8>,
        calls: Mutex<Vec<String>>,
    }
    impl Downloader for Download {
        fn get(&self, url: &str, _limit: usize) -> Result<Vec<u8>> {
            self.calls.lock().unwrap().push(url.into());
            Ok(if url.ends_with(".sig") {
                self.signature.clone()
            } else {
                self.data.clone()
            })
        }
    }

    fn archive(engine: Option<&str>) -> Vec<u8> {
        let mut packs = spinup_vfs::tree::Tree::new();
        if let Some(engine) = engine {
            packs
                .insert("ENGINE", engine.as_bytes().to_vec(), 0o644)
                .unwrap();
        }
        let mut data = Vec::new();
        archive::write_archive(
            &mut data,
            &packs,
            spinup_stack::load::EMBEDDED.as_bytes(),
            None,
        )
        .unwrap();
        data
    }

    #[test]
    fn bad_signatures_never_publish_and_missing_keys_never_download() {
        let key = SigningKey::from_bytes(&[42; 32]);
        for (signature, keys, requests) in [
            (vec![0; 64], vec![key.verifying_key()], 2),
            (vec![0; 63], vec![key.verifying_key()], 2),
            (vec![0; 65], vec![key.verifying_key()], 2),
            (vec![0; 64], vec![], 0),
        ] {
            let directory = tempfile::tempdir().unwrap();
            let cache = directory.path().join("absent");
            let transport = Arc::new(Download {
                data: archive(Some("minijinja\n")),
                signature,
                calls: Mutex::default(),
            });
            let store = Store::new(Options {
                cache_dir: cache.clone(),
                downloader: Some(transport.clone()),
                keys: Some(keys),
                ..Options::default()
            })
            .unwrap();
            assert!(store.load("1.0.0").unwrap_err().is_unavailable());
            assert_eq!(transport.calls.lock().unwrap().len(), requests);
            assert!(!cache.exists());
        }
    }

    #[test]
    fn signed_go_and_unknown_engines_stay_typed_and_never_publish() {
        let key = SigningKey::from_bytes(&[42; 32]);
        for engine in [None, Some("go"), Some("unknown")] {
            let directory = tempfile::tempdir().unwrap();
            let cache = directory.path().join("absent");
            let data = archive(engine);
            let transport = Arc::new(Download {
                signature: key.sign(&data).to_bytes().to_vec(),
                data,
                calls: Mutex::default(),
            });
            let store = Store::new(Options {
                cache_dir: cache.clone(),
                downloader: Some(transport),
                keys: Some(vec![key.verifying_key()]),
                ..Options::default()
            })
            .unwrap();
            assert!(matches!(
                store.load("1.0.0"),
                Err(SourceError::Engine { .. })
            ));
            assert!(!cache.exists());
        }
    }

    #[test]
    fn bad_extra_source_never_falls_back_to_downloads() {
        let directory = tempfile::tempdir().unwrap();
        let extra = directory.path().join("1.0.0");
        fs::create_dir_all(extra.join("packs")).unwrap();
        fs::write(
            extra.join("stackversions.yaml"),
            spinup_stack::load::EMBEDDED,
        )
        .unwrap();
        fs::write(extra.join("packs/ENGINE"), b"go\n").unwrap();
        let transport = Arc::new(Download {
            data: Vec::new(),
            signature: Vec::new(),
            calls: Mutex::default(),
        });
        let options = Options {
            extra_dir: directory.path().into(),
            cache_dir: directory.path().join("cache"),
            downloader: Some(transport.clone()),
            ..Options::default()
        };
        let store = Store::new(options).unwrap();
        assert!(matches!(
            store.load("1.0.0"),
            Err(SourceError::Engine { .. })
        ));
        fs::write(extra.join("packs/ENGINE"), b"minijinja\n").unwrap();
        fs::write(extra.join("stackversions.yaml"), b"invalid").unwrap();
        assert!(store.load("1.0.0").unwrap_err().is_unavailable());
        assert!(transport.calls.lock().unwrap().is_empty());
        assert!(store.list().unwrap().is_empty());
    }

    #[test]
    fn missing_signature_is_rejected_on_every_cached_load() {
        let directory = tempfile::tempdir().unwrap();
        let data = archive(Some("minijinja\n"));
        fs::write(directory.path().join("templates-v1.0.0.tar.zst"), data).unwrap();
        let store = Store::new(Options {
            cache_dir: directory.path().into(),
            offline: true,
            ..Options::default()
        })
        .unwrap();
        assert!(
            store
                .load("1.0.0")
                .unwrap_err()
                .to_string()
                .ends_with("Try: spinup cache clean")
        );
        assert!(store.versions().unwrap().is_empty());
    }

    #[cfg(unix)]
    #[test]
    fn extra_symlink_is_rejected() {
        let directory = tempfile::tempdir().unwrap();
        let version = directory.path().join("1.0.0");
        fs::create_dir_all(version.join("packs")).unwrap();
        fs::write(
            version.join("stackversions.yaml"),
            spinup_stack::load::EMBEDDED,
        )
        .unwrap();
        fs::write(version.join("packs/ENGINE"), b"minijinja\n").unwrap();
        std::os::unix::fs::symlink(directory.path(), version.join("packs/escape")).unwrap();
        let store = Store::new(Options {
            extra_dir: directory.path().into(),
            cache_dir: directory.path().join("cache"),
            offline: true,
            ..Options::default()
        })
        .unwrap();
        assert!(
            store
                .load("1.0.0")
                .unwrap_err()
                .to_string()
                .contains("unsupported extra file")
        );
    }
}
