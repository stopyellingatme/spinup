//! The test fixture of the drift commands (Go `drift_test.go` `fixture` and `testSource`).

use std::path::Path;
use std::sync::Arc;

use spinup_manifest::hash::hash;
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_tplstore::source::{Source, SourceError, Version};
use spinup_ui::printer::{Options as PrinterOptions, Printer, SharedBuffer};
use spinup_vfs::tree::Tree;

use super::Options;

/// A template source with one version for every request (Go `testSource`).
pub(crate) struct TestSource {
    /// The version that `load` returns. `None` makes `load` fail as unavailable.
    pub version: Option<Arc<Version>>,
}

impl Source for TestSource {
    fn versions(&self) -> Result<Vec<String>, SourceError> {
        Ok(vec!["0.1.0".into(), "0.2.0".into()])
    }

    fn load(&self, version: &str) -> Result<Arc<Version>, SourceError> {
        self.version
            .clone()
            .ok_or_else(|| SourceError::Unavailable {
                version: version.into(),
                reason: "not cached".into(),
                hint: "spinup cache warm".into(),
            })
    }
}

/// Returns version 0.1.0 with the pack `base`: the managed file `file.txt` (`template\n`).
pub(crate) fn version() -> Arc<Version> {
    let mut tree = Tree::new();
    tree.insert("ENGINE", b"minijinja\n".to_vec(), 0o644)
        .unwrap();
    tree.insert(
        "base/pack.toml",
        b"name = 'base'\n[[files]]\npath = 'file.txt'\nsource = 'file.txt'\nclass = 'managed'\n"
            .to_vec(),
        0o644,
    )
    .unwrap();
    tree.insert("base/file.txt", b"template\n".to_vec(), 0o644)
        .unwrap();
    Arc::new(Version {
        version: "0.1.0".into(),
        packs: Arc::new(tree),
        stack: Arc::new(serde_json::from_value(serde_json::json!({})).unwrap()),
        kit_migration: None,
    })
}

/// A project in a temporary directory, rendered from [`version`].
pub(crate) struct Fixture {
    /// Keeps the directory alive.
    pub dir: tempfile::TempDir,
    /// The command options for the project.
    pub options: Options,
    /// The lock that the fixture saved.
    pub lock: Lock,
}

impl Fixture {
    /// Writes `spinup.toml`, `file.txt`, and the lock (Go `fixture`).
    pub fn new() -> Self {
        let dir = tempfile::tempdir().unwrap();
        let mut lock = Lock::new();
        lock.template_version = "0.1.0".into();
        lock.packs = vec!["base".into()];
        lock.files.insert(
            "file.txt".into(),
            FileEntry {
                class: Class::Managed,
                template: "base/file.txt".into(),
                hash: hash(b"template\n"),
                mode: "0644".into(),
            },
        );
        std::fs::write(dir.path().join("spinup.toml"), b"").unwrap();
        std::fs::write(dir.path().join("file.txt"), b"template\n").unwrap();
        let fixture = Self {
            options: Options {
                dir: dir.path().into(),
                source: Some(Arc::new(TestSource {
                    version: Some(version()),
                })),
                ..Options::default()
            },
            dir,
            lock,
        };
        fixture.save();
        fixture
    }

    /// The project root.
    pub fn root(&self) -> &Path {
        self.dir.path()
    }

    /// Saves [`Fixture::lock`].
    pub fn save(&self) {
        self.lock.save(self.root().join("spinup.lock")).unwrap();
    }

    /// Writes a project file.
    pub fn write(&self, name: &str, data: &str) {
        std::fs::write(self.root().join(name), data).unwrap();
    }

    /// Reads the saved lock.
    pub fn saved(&self) -> Lock {
        Lock::load(self.root().join("spinup.lock")).unwrap()
    }

    /// Returns options with an unavailable template version.
    pub fn unavailable(&self) -> Options {
        Options {
            source: Some(Arc::new(TestSource { version: None })),
            ..self.options.clone()
        }
    }
}

/// Returns a printer that writes stdout and stderr to one buffer.
pub(crate) fn printer() -> (Printer, SharedBuffer) {
    let buffer = SharedBuffer::new();
    let printer = Printer::new(
        std::io::empty(),
        buffer.clone(),
        buffer.clone(),
        PrinterOptions::default(),
    );
    (printer, buffer)
}
