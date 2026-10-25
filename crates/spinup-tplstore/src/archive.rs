//! Deterministic tar and zstd archives with bounded expansion.

use crate::source::{ENGINE, SourceError, Version};
use anyhow::{Result, bail, ensure};
use spinup_vfs::tree::Tree;
use std::{
    collections::BTreeMap,
    io::{Cursor, Read, Write},
    sync::Arc,
};

/// Maximum number of tar entries, including directories.
pub const MAX_ENTRIES: usize = 10_000;
/// Maximum sum of uncompressed entry sizes.
pub const MAX_UNPACKED: usize = 50 << 20;
/// Maximum compressed size and expanded tar size, including headers and padding.
pub const MAX_ARCHIVE: usize = MAX_UNPACKED + MAX_ENTRIES * 1024;

pub(crate) fn archive_error(error: impl std::fmt::Display) -> anyhow::Error {
    anyhow::anyhow!("cannot process template archive: {error}\nTry: use a valid templates archive")
}

pub(crate) fn valid_path(path: &str) -> bool {
    !path.is_empty()
        && !path.contains(['\\', ':', '\0'])
        && path.split('/').all(|part| !matches!(part, "" | "." | ".."))
}

/// Reads at most `limit` bytes and rejects an oversized input.
pub(crate) fn read_limited(reader: impl Read, limit: usize) -> Result<Vec<u8>> {
    let mut data = Vec::new();
    reader.take(limit as u64 + 1).read_to_end(&mut data)?;
    ensure!(data.len() <= limit, "template data exceeds the size limit");
    Ok(data)
}

/// Writes sorted files and implicit directories with stable tar metadata.
pub fn write_archive(
    writer: impl Write,
    packs: &Tree,
    stack_yaml: &[u8],
    notes: Option<&[u8]>,
) -> Result<()> {
    write_inner(writer, packs, stack_yaml, notes).map_err(archive_error)
}

fn write_inner(writer: impl Write, packs: &Tree, stack: &[u8], notes: Option<&[u8]>) -> Result<()> {
    // A tree records files only. Implicit directories use mode 0o755.
    let mut entries: BTreeMap<String, (u32, bool, &[u8])> = BTreeMap::new();
    entries.insert("packs".into(), (0o755, true, &[]));
    entries.insert("stackversions.yaml".into(), (0o644, false, stack));
    if let Some(notes) = notes {
        entries.insert("MIGRATION.json".into(), (0o644, false, notes));
    }
    for (path, file) in packs.iter() {
        ensure!(valid_path(path), "unsafe archive path {path:?}");
        entries.insert(format!("packs/{path}"), (file.mode, false, &file.data));
        let mut parent = format!("packs/{path}");
        while let Some((directory, _)) = parent.rsplit_once('/') {
            let directory = directory.to_owned();
            if let Some((_, is_dir, _)) = entries.get(&directory) {
                ensure!(*is_dir, "archive parent {directory:?} is a file");
            } else {
                entries.insert(directory.clone(), (0o755, true, &[]));
            }
            parent = directory;
        }
    }
    ensure!(
        entries.len() <= MAX_ENTRIES
            && entries
                .values()
                .map(|(_, _, data)| data.len())
                .sum::<usize>()
                <= MAX_UNPACKED,
        "archive exceeds the entry or size limit"
    );
    let encoder = zstd::stream::Encoder::new(writer, 3)?;
    let mut tar = tar::Builder::new(encoder);
    for (name, (mode, directory, data)) in entries {
        let mut header = tar::Header::new_ustar();
        header.set_size(data.len() as u64);
        header.set_mode(mode);
        header.set_uid(0);
        header.set_gid(0);
        header.set_mtime(0);
        header.set_entry_type(if directory {
            tar::EntryType::Directory
        } else {
            tar::EntryType::Regular
        });
        tar.append_data(&mut header, name, data)?;
    }
    tar.into_inner()?.finish()?;
    Ok(())
}

/// Validates and expands an archive. Go-era archives return [`SourceError::Engine`].
pub fn read_archive(data: &[u8], version: &str) -> std::result::Result<Version, SourceError> {
    crate::store::validate_version(version)?;
    let contents = decode(data).map_err(archive_error)?;
    make_version(version, contents.packs, &contents.stack, contents.notes)
}

pub(crate) fn make_version(
    version: &str,
    packs: Tree,
    stack: &[u8],
    notes: Option<Vec<u8>>,
) -> std::result::Result<Version, SourceError> {
    check_engine(&packs, version)?;
    let stack = spinup_stack::load::parse(stack).map_err(archive_error)?;
    Ok(Version {
        version: version.into(),
        packs: Arc::new(packs),
        stack: Arc::new(stack),
        kit_migration: notes.map(Arc::from),
    })
}

pub(crate) fn check_engine(packs: &Tree, version: &str) -> std::result::Result<(), SourceError> {
    let found = packs
        .get("ENGINE")
        .map(|f| String::from_utf8_lossy(&f.data).trim().to_owned())
        .unwrap_or_default();
    if found != ENGINE {
        return Err(SourceError::Engine {
            version: version.into(),
            found,
        });
    }
    Ok(())
}

struct Contents {
    packs: Tree,
    stack: Vec<u8>,
    notes: Option<Vec<u8>>,
}

fn decode(data: &[u8]) -> Result<Contents> {
    ensure!(
        data.len() <= MAX_ARCHIVE,
        "template data exceeds the size limit"
    );
    let mut decoder = zstd::stream::read::Decoder::new(data)?;
    decoder.window_log_max(26)?;
    let decoded = read_limited(decoder, MAX_ARCHIVE)?;
    let mut tar = tar::Archive::new(Cursor::new(&decoded));
    let mut seen: BTreeMap<String, bool> = BTreeMap::new();
    let mut tree = Tree::new();
    let mut stack = None;
    let mut notes = None;
    let mut total = 0;
    let mut has_packs = false;
    for (count, entry) in tar.entries()?.enumerate() {
        let mut entry = entry?;
        ensure!(count < MAX_ENTRIES, "archive has more than 10000 entries");
        let path = entry.path_bytes();
        let raw = std::str::from_utf8(&path)?;
        let name = raw.strip_suffix('/').unwrap_or(raw).to_owned();
        ensure!(valid_path(&name), "unsafe archive path {raw:?}");
        let kind = entry.header().entry_type();
        let directory = kind == tar::EntryType::Directory;
        ensure!(
            directory || kind == tar::EntryType::Regular,
            "unsupported archive entry {name:?}"
        );
        let is_pack = name.starts_with("packs/");
        ensure!(
            is_pack
                || matches!(
                    name.as_str(),
                    "packs" | "stackversions.yaml" | "MIGRATION.json"
                ),
            "unexpected archive entry {name:?}"
        );
        ensure!(
            (name != "packs" || directory) && (is_pack || name == "packs" || !directory),
            "invalid entry type for {name:?}"
        );
        ensure!(
            !seen.contains_key(&name),
            "duplicate archive entry {name:?}"
        );
        // Detect both explicit directory conflicts and implicit parents.
        let mut parent = name.as_str();
        while let Some((p, _)) = parent.rsplit_once('/') {
            ensure!(
                seen.get(p) != Some(&false),
                "archive parent {p:?} is a file"
            );
            parent = p;
        }
        if !directory {
            let prefix = format!("{name}/");
            ensure!(
                !seen
                    .range(prefix.clone()..)
                    .next()
                    .is_some_and(|(p, _)| p.starts_with(&prefix)),
                "conflicting archive path {name:?}"
            );
        }
        seen.insert(name.clone(), directory);
        let size = entry.size();
        ensure!(
            size <= (MAX_UNPACKED - total) as u64,
            "archive exceeds 50 MiB unpacked"
        );
        total += size as usize;
        let mode = entry.header().mode()?;
        let content = read_limited(&mut entry, size as usize)?;
        ensure!(
            content.len() == size as usize,
            "truncated archive entry {name:?}"
        );
        match name.as_str() {
            "stackversions.yaml" => stack = Some(content),
            "MIGRATION.json" => notes = Some(content),
            "packs" => has_packs = true,
            _ => {
                has_packs = true;
                if !directory {
                    tree.insert(&name[6..], content, mode)?;
                }
            }
        }
    }
    let offset = tar.into_inner().position() as usize;
    ensure!(
        decoded[offset..].iter().all(|b| *b == 0),
        "unexpected data after the tar archive"
    );
    let Some(stack) = stack.filter(|_| has_packs) else {
        bail!("archive requires packs/ and stackversions.yaml");
    };
    Ok(Contents {
        packs: tree,
        stack,
        notes,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn packs() -> spinup_vfs::tree::Tree {
        let mut tree = spinup_vfs::tree::Tree::new();
        tree.insert("ENGINE", b"minijinja\n".to_vec(), 0o644)
            .unwrap();
        tree.insert("base/.gitignore", b"secret\n".to_vec(), 0o644)
            .unwrap();
        tree.insert("base/run.sh", b"#!/bin/sh\n".to_vec(), 0o4755)
            .unwrap();
        tree
    }

    #[test]
    fn round_trip_and_stable_metadata() {
        let stack = spinup_stack::load::EMBEDDED.as_bytes();
        let mut data = Vec::new();
        write_archive(&mut data, &packs(), stack, Some(b"{\"schema\":1}")).unwrap();
        let mut second = Vec::new();
        write_archive(&mut second, &packs(), stack, Some(b"{\"schema\":1}")).unwrap();
        assert_eq!(data, second);
        let version = read_archive(&data, "1.2.3").unwrap();
        assert_eq!(*version.packs, packs());
        assert_eq!(version.version, "1.2.3");
        assert_eq!(
            version.kit_migration.as_deref(),
            Some(b"{\"schema\":1}".as_slice())
        );
        let decoded = zstd::stream::decode_all(data.as_slice()).unwrap();
        let mut tar = tar::Archive::new(decoded.as_slice());
        let mut previous = String::new();
        for entry in tar.entries().unwrap() {
            let entry = entry.unwrap();
            let name = entry.path().unwrap().to_string_lossy().into_owned();
            assert!(name > previous);
            previous = name;
            let h = entry.header();
            assert_eq!(
                (h.uid().unwrap(), h.gid().unwrap(), h.mtime().unwrap()),
                (0, 0, 0)
            );
            assert_eq!(h.username().unwrap(), Some(""));
            assert_eq!(h.groupname().unwrap(), Some(""));
        }
    }

    #[test]
    fn go_archive_requires_rust_engine() {
        let data = include_bytes!("../../../testdata/oracle/templates-v1.2.3.tar.zst");
        let error = read_archive(data, "1.2.3").unwrap_err();
        assert!(matches!(error, crate::source::SourceError::Engine { .. }));
        assert!(
            error
                .to_string()
                .contains("Try: upgrade to a template version from a Rust release")
        );
    }
}

#[cfg(test)]
mod rejection_tests {
    use super::*;

    fn raw(entries: &[(&str, u8, u64)]) -> Vec<u8> {
        let mut bytes = Vec::new();
        for (path, kind, size) in entries {
            let mut header = tar::Header::new_ustar();
            header.as_mut_bytes()[..path.len()].copy_from_slice(path.as_bytes());
            header.set_entry_type(tar::EntryType::new(*kind));
            header.set_size(*size);
            header.set_mode(0o644);
            header.set_cksum();
            bytes.extend_from_slice(header.as_bytes());
        }
        bytes.extend_from_slice(&[0; 1024]);
        zstd::stream::encode_all(bytes.as_slice(), 3).unwrap()
    }

    #[test]
    fn rejects_go_archive_abuse_cases() {
        for (entries, message) in [
            (vec![("/packs/x", b'0', 0)], "unsafe archive path"),
            (vec![("packs/../x", b'0', 0)], "unsafe archive path"),
            (vec![("packs/a/../b", b'0', 0)], "unsafe archive path"),
            (vec![("packs/..\\escape", b'0', 0)], "unsafe archive path"),
            (vec![("packs/C:x", b'0', 0)], "unsafe archive path"),
            (vec![("packs/x", b'2', 0)], "unsupported archive entry"),
            (vec![("packs/x", b'1', 0)], "unsupported archive entry"),
            (vec![("packs/x", b'3', 0)], "unsupported archive entry"),
            (vec![("other", b'0', 0)], "unexpected archive entry"),
            (vec![("stackversions.yaml/", b'5', 0)], "invalid entry type"),
            (vec![("packs", b'0', 0)], "invalid entry type"),
            (
                vec![("packs/x", b'0', 0), ("packs/x", b'0', 0)],
                "duplicate archive entry",
            ),
            (
                vec![("packs/x", b'0', 0), ("packs/x/y", b'0', 0)],
                "archive parent",
            ),
            (
                vec![("packs/x/y", b'0', 0), ("packs/x", b'0', 0)],
                "conflicting archive path",
            ),
            (vec![("packs/x", b'0', 0)], "archive requires"),
            (vec![("MIGRATION.json", b'0', 0)], "archive requires"),
            (
                vec![("packs/large", b'0', MAX_UNPACKED as u64 + 1)],
                "50 MiB",
            ),
        ] {
            let error = read_archive(&raw(&entries), "1.0.0")
                .unwrap_err()
                .to_string();
            assert!(error.contains(message), "{entries:?}: {error}");
            assert!(error.ends_with("Try: use a valid templates archive"));
        }
    }

    #[test]
    fn rejects_entry_count_and_expanded_stream_limit() {
        let names: Vec<_> = (0..MAX_ENTRIES + 1).map(|i| format!("packs/{i}")).collect();
        let entries: Vec<_> = names.iter().map(|name| (name.as_str(), b'0', 0)).collect();
        assert!(
            read_archive(&raw(&entries), "1.0.0")
                .unwrap_err()
                .to_string()
                .contains("10000")
        );
        let bomb =
            zstd::stream::encode_all(std::io::repeat(0).take(MAX_ARCHIVE as u64 + 1), 1).unwrap();
        assert!(
            read_archive(&bomb, "1.0.0")
                .unwrap_err()
                .to_string()
                .contains("size limit")
        );
    }

    #[test]
    fn rejects_hidden_tar_and_broken_compression() {
        let mut decoded = vec![0; 1024];
        decoded.extend_from_slice(b"hidden archive");
        let data = zstd::stream::encode_all(decoded.as_slice(), 3).unwrap();
        assert!(
            read_archive(&data, "1.0.0")
                .unwrap_err()
                .to_string()
                .contains("unexpected data after")
        );
        let fixture = include_bytes!("../../../testdata/oracle/templates-v1.2.3.tar.zst");
        assert!(read_archive(&fixture[..fixture.len() - 1], "1.0.0").is_err());
        assert!(read_archive(b"not zstd", "1.0.0").is_err());
    }

    #[test]
    fn go_oracle_files_preserve_content_and_permissions() {
        let decoded = decode(include_bytes!(
            "../../../testdata/oracle/templates-v1.2.3.tar.zst"
        ))
        .unwrap();
        let cases = [
            ("base/.gitignore", b"secret\n".as_slice(), 0o644),
            ("base/empty", b"".as_slice(), 0o600),
            (
                "base/nested/data.bin",
                b"\x00\xff\x01\x80".as_slice(),
                0o644,
            ),
            ("base/run.sh", b"#!/bin/sh\necho '<>&'\n".as_slice(), 0o755),
        ];
        for (name, bytes, mode) in cases {
            let file = decoded.packs.get(name).unwrap();
            assert_eq!(file.data.as_ref(), bytes);
            assert_eq!(file.mode, mode);
        }
        assert_eq!(decoded.packs.len(), 4);
        assert_eq!(
            decoded.notes.as_deref(),
            Some(b"{\"schema\":1}\n".as_slice())
        );
        assert_eq!(
            decoded.stack,
            include_bytes!("../../../internal/stackversions/stackversions.yaml")
        );
    }
}
