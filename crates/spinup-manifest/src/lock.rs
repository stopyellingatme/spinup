//! The `spinup.lock` model, schema 3 (ADR 0020 §2).
//!
//! Seam (ADR 0020 §1): the contract files name [`Lock`] and [`Class`]. R1.4 keeps the paths
//! `spinup_manifest::lock::{Lock, Class}`, the derives `Debug` and `Clone` on both, and the
//! three `Class` variants with their lock names.

use serde::{Deserialize, Serialize};

/// The schema that the Rust CLI writes. It rejects schema 1 and 2 (Go-era) locks.
pub const SCHEMA_VERSION: u32 = 3;

/// How an upgrade treats a generated file.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Class {
    /// The CLI maintains the file.
    Managed,
    /// An upgrade merges the file with the user's changes.
    Scaffold,
    /// The CLI writes the file once and never compares it again.
    Seed,
}

/// The runtime package and its version.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct KitRef {
    /// The runtime package name.
    #[serde(rename = "crate")]
    pub crate_name: String,
    /// The runtime package version.
    pub version: String,
}

/// A locked plugin and its protocol version.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize)]
#[serde(default)]
pub struct Plugin {
    /// The registered plugin name.
    pub name: String,
    /// The artifact version.
    pub version: String,
    /// The artifact digest.
    pub sha256: String,
    /// The protocol version.
    pub api: i64,
}

/// The source, content hash, and permissions of a tracked file.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct FileEntry {
    /// The upgrade policy.
    pub class: Class,
    /// The source path, relative to the pack tree.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub template: String,
    /// The normalized content digest.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub hash: String,
    /// Four octal permission digits.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub mode: String,
}

/// The inputs and file state that reproduce and upgrade an app.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct Lock {
    /// The lock format version.
    pub schema: u32,
    /// The CLI version that wrote the lock.
    pub cli_version: String,
    /// The source template version.
    pub template_version: String,
    /// Pack names in render order.
    pub packs: Vec<String>,
    /// The minimum supported CLI version.
    pub min_cli: String,
    /// The runtime package reference.
    pub kit: KitRef,
    /// The selected runtime behavior.
    pub behavior_version: i64,
    /// The formatter name and version, or an empty string.
    pub formatter: String,
    /// The template engine name.
    pub engine: String,
    /// The render inputs. Raw values preserve number precision and exponent spelling.
    pub answers: std::collections::BTreeMap<String, Box<serde_json::value::RawValue>>,
    /// The locked stack versions.
    pub stack_versions: std::collections::BTreeMap<String, String>,
    /// The locked plugins.
    pub plugins: Vec<Plugin>,
    /// Tracked files by clean relative path.
    pub files: std::collections::BTreeMap<String, FileEntry>,
    /// Deleted paths that upgrades must not recreate.
    pub tombstones: Vec<String>,
    /// Retained files whose templates were removed.
    pub orphans: Vec<String>,
    /// Completed migration identifiers.
    pub applied_migrations: Vec<String>,
}

impl Default for Lock {
    fn default() -> Self {
        Self {
            schema: SCHEMA_VERSION,
            cli_version: String::new(),
            template_version: String::new(),
            packs: Vec::new(),
            min_cli: String::new(),
            kit: KitRef::default(),
            behavior_version: 0,
            formatter: String::new(),
            engine: String::new(),
            answers: Default::default(),
            stack_versions: Default::default(),
            plugins: Vec::new(),
            files: Default::default(),
            tombstones: Vec::new(),
            orphans: Vec::new(),
            applied_migrations: Vec::new(),
        }
    }
}

/// A failure to read, validate, compare, or save a lock.
#[derive(Debug, thiserror::Error)]
#[error("{message}\nTry: {hint}")]
pub struct Error {
    message: String,
    hint: &'static str,
}

impl Error {
    pub(crate) fn new(message: impl Into<String>, hint: &'static str) -> Self {
        Self {
            message: message.into(),
            hint,
        }
    }
}

impl Lock {
    /// Create an empty schema 3 lock.
    pub fn new() -> Self {
        Self::default()
    }

    /// Validate all schema, entry, and path constraints.
    pub fn validate(&self) -> Result<(), Error> {
        let mut problems = Vec::new();
        if self.schema != SCHEMA_VERSION {
            problems.push(format!(
                "schema {} is unsupported (expected {SCHEMA_VERSION})",
                self.schema
            ));
        }
        let mut seen = std::collections::BTreeSet::new();
        for pack in &self.packs {
            if pack.is_empty() || !seen.insert(pack) {
                problems.push(format!("packs: {pack:?} is empty or repeated"));
            }
        }
        for (path, entry) in &self.files {
            entry_problems(path, entry, &mut problems);
        }
        for path in sorted_unique(&self.tombstones) {
            if !clean_path(&path) {
                problems.push(format!(
                    "tombstone path {path:?} must be a clean relative path"
                ));
            }
            if self.files.contains_key(&path) {
                problems.push(format!("tombstone {path:?} is also a live entry"));
            }
        }
        for path in sorted_unique(&self.orphans) {
            if !clean_path(&path) {
                problems.push(format!(
                    "orphan path {path:?} must be a clean relative path"
                ));
            }
        }
        validation_result(
            problems,
            "validate spinup.lock",
            "repair the listed entries in spinup.lock",
        )
    }

    /// Remove a live entry and record its tombstone once.
    pub fn tombstone(&mut self, path: &str) {
        self.files.remove(path);
        self.tombstones.push(path.into());
        self.tombstones = sorted_unique(&self.tombstones);
    }

    /// Remove a tombstone without creating a live entry.
    pub fn restore(&mut self, path: &str) {
        self.tombstones.retain(|p| p != path);
    }

    /// Validate and record an entry, then remove its tombstone.
    pub fn set_file(&mut self, path: &str, entry: FileEntry) -> Result<(), Error> {
        let mut problems = Vec::new();
        entry_problems(path, &entry, &mut problems);
        validation_result(
            problems,
            &format!("set tracked file {path:?}"),
            "provide a valid relative path and file entry",
        )?;
        self.files.insert(path.into(), entry);
        self.restore(path);
        Ok(())
    }
}

pub(crate) fn clean_path(path: &str) -> bool {
    !path.is_empty()
        && !path.contains(['\\', ':', '\0'])
        && path.split('/').all(|part| !matches!(part, "" | "." | ".."))
}

pub(crate) fn sorted_unique(values: &[String]) -> Vec<String> {
    let mut values = values.to_vec();
    values.sort();
    values.dedup();
    values
}

pub(crate) fn entry_problems(path: &str, entry: &FileEntry, problems: &mut Vec<String>) {
    if !clean_path(path) {
        problems.push(format!("file path {path:?} must be a clean relative path"));
    }
    let seed = entry.class == Class::Seed;
    if (!entry.hash.is_empty() || !seed)
        && !(entry.hash.len() == 71
            && entry.hash.starts_with("sha256:")
            && entry.hash.as_bytes()[7..]
                .iter()
                .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(c)))
    {
        problems.push(format!(
            "file {path:?} has invalid hash {:?} (expected sha256: and 64 lowercase hex digits)",
            entry.hash
        ));
    }
    if (!entry.mode.is_empty() || !seed)
        && !(entry.mode.len() == 4
            && entry.mode.starts_with('0')
            && entry.mode.bytes().all(|c| (b'0'..=b'7').contains(&c)))
    {
        problems.push(format!(
            "file {path:?} has invalid mode {:?} (expected four octal digits, such as 0644)",
            entry.mode
        ));
    }
    if (!entry.template.is_empty() || !seed) && !clean_path(&entry.template) {
        problems.push(format!(
            "file {path:?} has invalid template path {:?} (expected a clean relative path)",
            entry.template
        ));
    }
}

pub(crate) fn validation_result(
    problems: Vec<String>,
    action: &str,
    hint: &'static str,
) -> Result<(), Error> {
    if problems.is_empty() {
        Ok(())
    } else {
        Err(Error::new(
            format!("{action}: {}", problems.join("\n")),
            hint,
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn new_and_validation() {
        let mut lock = Lock::new();
        assert_eq!(lock.schema, 3);
        assert!(lock.validate().is_ok());
        lock.packs = vec!["base".into(), "base".into()];
        assert!(lock.validate().unwrap_err().to_string().contains("packs"));
    }
}

#[cfg(test)]
mod validation_tests {
    use super::*;

    fn tracked(class: Class) -> FileEntry {
        FileEntry {
            class,
            template: "api/main.tmpl".into(),
            hash: crate::hash::hash(b"original"),
            mode: "0644".into(),
        }
    }

    #[test]
    fn invalid_paths_and_metadata() {
        for path in [
            "", ".", "..", "../a", "/a", "a/../b", "a/./b", "a//b", "a/", "a\\b", "C:/a", "a\0b",
        ] {
            let mut lock = Lock::new();
            assert!(
                lock.set_file(path, tracked(Class::Managed))
                    .unwrap_err()
                    .to_string()
                    .contains("\nTry:")
            );
            lock.files.insert(path.into(), tracked(Class::Managed));
            assert!(lock.validate().unwrap_err().to_string().contains("path"));
        }
        for hash in [
            "",
            "sha256:00",
            &format!("sha256:{}", "A".repeat(64)),
            &format!("sha256:{}", "g".repeat(64)),
        ] {
            let mut entry = tracked(Class::Managed);
            entry.hash = hash.into();
            assert!(
                Lock::new()
                    .set_file("a", entry)
                    .unwrap_err()
                    .to_string()
                    .contains("hash")
            );
        }
        for mode in ["", "644", "0688", "1644"] {
            let mut entry = tracked(Class::Managed);
            entry.mode = mode.into();
            assert!(
                Lock::new()
                    .set_file("a", entry)
                    .unwrap_err()
                    .to_string()
                    .contains("mode")
            );
        }
        for template in ["", "../x"] {
            let mut entry = tracked(Class::Managed);
            entry.template = template.into();
            assert!(
                Lock::new()
                    .set_file("a", entry)
                    .unwrap_err()
                    .to_string()
                    .contains("template")
            );
        }
    }

    #[test]
    fn seed_optional_fields_and_modes() {
        for mode in ["", "0000", "0644", "0755", "0777"] {
            let entry = FileEntry {
                class: Class::Seed,
                template: String::new(),
                hash: String::new(),
                mode: mode.into(),
            };
            Lock::new().set_file("seed.sql", entry).unwrap();
        }
        let mut entry = tracked(Class::Seed);
        entry.hash = "bad".into();
        assert!(Lock::new().set_file("seed.sql", entry).is_err());
    }

    #[test]
    fn tombstone_restore_and_transactional_set_file() {
        let mut lock = Lock::new();
        lock.set_file("a", tracked(Class::Scaffold)).unwrap();
        lock.tombstone("a");
        lock.tombstone("a");
        lock.tombstone("z");
        assert_eq!(lock.tombstones, ["a", "z"]);
        assert!(lock.files.is_empty());
        lock.restore("a");
        lock.restore("a");
        assert_eq!(lock.tombstones, ["z"]);
        assert!(lock.files.is_empty());
        let mut bad = tracked(Class::Managed);
        bad.hash = "bad".into();
        assert!(lock.set_file("z", bad).is_err());
        assert_eq!(lock.tombstones, ["z"]);
        assert!(lock.files.is_empty());
        lock.set_file("z", tracked(Class::Managed)).unwrap();
        assert!(lock.tombstones.is_empty());
        assert!(lock.files.contains_key("z"));
    }

    #[test]
    fn validation_aggregates_and_checks_orphans() {
        let mut lock = Lock::new();
        lock.schema = 0;
        let mut bad = tracked(Class::Managed);
        bad.hash = "bad".into();
        bad.mode = "bad".into();
        lock.files.insert("../a".into(), bad);
        lock.tombstones = vec!["../a".into()];
        lock.orphans = vec!["/a".into()];
        let error = lock.validate().unwrap_err().to_string();
        for field in [
            "schema",
            "path",
            "hash",
            "mode",
            "also a live entry",
            "orphan",
        ] {
            assert!(error.contains(field), "{error}");
        }
    }
}
