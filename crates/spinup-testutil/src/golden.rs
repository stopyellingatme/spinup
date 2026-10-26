//! Compare generated files with the Go golden trees.

use anyhow::{Result, bail};
use similar::TextDiff;
use spinup_vfs::tree::Tree;
use std::{collections::BTreeSet, path::Path};

/// Reports whether this golden group matches `SPINUP_GOLDEN_PACK`.
pub fn selected(name: &str) -> bool {
    let selected = std::env::var("SPINUP_GOLDEN_PACK").unwrap_or_default();
    selected.is_empty() || selected == name.split('/').next().unwrap_or(name)
}

/// Loads packs and renders with the same stack pins and version as Go tests.
pub fn render_pack(
    packs: &[String],
    answers: &serde_json::Map<String, serde_json::Value>,
) -> Result<Tree> {
    let embedded = spinup_packs::embed::embedded()?;
    let loaded = packs
        .iter()
        .map(|name| spinup_packs::loader::load(&embedded, name))
        .collect::<Result<Vec<_>>>()?;
    spinup_render::render::render(spinup_render::render::Input {
        packs: &loaded,
        answers,
        stack: Some(spinup_stack::load::embedded()),
        template_version: "0.0.0-test",
    })?
    .fs()
}

/// Runs a contract case and returns its selected output.
/// An unselected group returns `None` without rendering.
pub fn run(name: &str) -> Result<Option<Tree>> {
    if !selected(name) {
        return Ok(None);
    }
    let config = crate::filter::load()?;
    let case = config.case(name)?;
    let render = || config.select(name, &render_pack(&case.packs, &case.answers)?);
    let got = render()?;
    deterministic(render)?;
    let go = case.origin == crate::filter::Origin::Go;
    let dir = golden_directory(name, case.origin)?;
    if std::env::var("SPINUP_GOLDEN_UPDATE").as_deref() == Ok("1") {
        // R1 does not format Go files. Preserve their frozen oracle bytes on update.
        let mut updated = got.clone();
        if go && dir.exists() {
            for (path, file) in read_golden(&dir)?
                .iter()
                .filter(|(path, _)| config.skips(path))
            {
                updated.insert(path, file.data.clone(), file.mode)?;
            }
        }
        write_golden(&dir, &updated)?;
    } else {
        let mut want = read_golden(&dir).map_err(|error| {
            anyhow::anyhow!(
                "read golden {name}: {error:#}\nTry: run make rs-golden PACK={} UPDATE=1",
                name.split('/').next().unwrap_or(name)
            )
        })?;
        let skipped = want
            .iter()
            .filter(|(path, _)| go && config.skips(path))
            .map(|(path, _)| path.to_owned())
            .collect::<Vec<_>>();
        for path in skipped {
            want.remove(&path);
        }
        let reports = compare_trees(&want, &got);
        if !reports.is_empty() {
            bail!(
                "golden {name}: {}\nTry: inspect the render or run make rs-golden PACK={} UPDATE=1",
                reports.join("\n"),
                name.split('/').next().unwrap_or(name)
            );
        }
    }
    Ok(Some(got))
}

fn golden_directory(name: &str, origin: crate::filter::Origin) -> Result<std::path::PathBuf> {
    let mut check = Tree::new();
    check.insert(name, Vec::new(), 0o644).map_err(|_| {
        anyhow::anyhow!("compare golden: invalid name {name:?}\nTry: use a relative pack path")
    })?;
    let base = match origin {
        crate::filter::Origin::Go => "testdata/golden",
        crate::filter::Origin::Rs => "testdata/golden-rs",
    };
    Ok(crate::repo::repo_root()?.join(base).join(name))
}

/// Compares generator output without a pack filter with `testdata/golden-rs/<name>`, with the
/// diff and `SPINUP_GOLDEN_UPDATE=1` rules of [`run`] (R2.14). `files` holds (path, bytes,
/// mode). A group that `SPINUP_GOLDEN_PACK` does not select passes without a compare.
///
/// # Errors
/// Returns an error with a `Try:` line when the trees differ or a file cannot be read.
pub fn compare_rs(name: &str, files: &[(String, Vec<u8>, u32)]) -> Result<()> {
    if !selected(name) {
        return Ok(());
    }
    let mut got = Tree::new();
    for (path, data, mode) in files {
        got.insert(path.as_str(), data.clone(), *mode)?;
    }
    let dir = golden_directory(name, crate::filter::Origin::Rs)?;
    let group = name.split('/').next().unwrap_or(name);
    if std::env::var("SPINUP_GOLDEN_UPDATE").as_deref() == Ok("1") {
        return write_golden(&dir, &got);
    }
    let want = read_golden(&dir).map_err(|error| {
        anyhow::anyhow!(
            "read golden {name}: {error:#}\nTry: run make rs-golden PACK={group} UPDATE=1"
        )
    })?;
    let reports = compare_trees(&want, &got);
    if !reports.is_empty() {
        bail!(
            "golden {name}: {}\nTry: inspect the generator or run make rs-golden PACK={group} UPDATE=1",
            reports.join("\n")
        );
    }
    Ok(())
}

/// Renders twice and compares bytes, paths, and permissions.
pub fn deterministic(mut render: impl FnMut() -> Result<Tree>) -> Result<()> {
    let first = render()?;
    let second = render()?;
    let reports = compare_trees(&first, &second);
    if !reports.is_empty() {
        bail!(
            "render is not deterministic: {}\nTry: remove variable inputs and sort output keys",
            reports.join("\n")
        );
    }
    Ok(())
}

/// Reads regular files and applies the authoritative `.modes` manifest.
/// A missing manifest gives every file mode `0644`.
pub fn read_golden(dir: &Path) -> Result<Tree> {
    let mut files = Tree::new();
    let mut manifest = String::new();
    for entry in walkdir::WalkDir::new(dir).sort_by_file_name() {
        let entry = entry?;
        if entry.file_type().is_dir() {
            continue;
        }
        if !entry.file_type().is_file() {
            bail!(
                "read golden: {} is not a regular file\nTry: use regular golden files",
                entry.path().display()
            );
        }
        let relative = entry.path().strip_prefix(dir)?;
        let path = relative
            .iter()
            .map(|segment| {
                segment.to_str().ok_or_else(|| {
                    anyhow::anyhow!("read golden: path is not UTF-8\nTry: use UTF-8 file names")
                })
            })
            .collect::<Result<Vec<_>>>()?
            .join("/");
        let data = std::fs::read(entry.path())?;
        if path == ".modes" {
            manifest = String::from_utf8(data)?;
        } else {
            files.insert(path, data, 0o644)?;
        }
    }
    crate::modes::apply(&files, &manifest)
}

/// Replaces a golden tree, removes stale files, and writes sorted permissions.
pub fn write_golden(dir: &Path, files: &Tree) -> Result<()> {
    let manifest = crate::modes::write(files)?;
    if dir.exists() {
        std::fs::remove_dir_all(dir)?;
    }
    std::fs::create_dir_all(dir)?;
    for (path, file) in files.iter() {
        let target = dir.join(path);
        std::fs::create_dir_all(target.parent().unwrap())?;
        std::fs::write(&target, &file.data)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&target, std::fs::Permissions::from_mode(file.mode))?;
        }
    }
    std::fs::write(dir.join(".modes"), manifest)?;
    Ok(())
}

/// Reports path, permission, and content changes in path order.
/// Text reports contain at most 40 diff lines. Binary reports contain no file bytes.
pub fn compare_trees(want: &Tree, got: &Tree) -> Vec<String> {
    let paths = want
        .iter()
        .chain(got.iter())
        .map(|(path, _)| path)
        .collect::<BTreeSet<_>>();
    let mut reports = Vec::new();
    for path in paths {
        let old = want.get(path);
        let new = got.get(path);
        let mut changes = Vec::new();
        match (old, new) {
            (None, Some(_)) => changes.push(format!("added: {path}")),
            (Some(_), None) => changes.push(format!("missing: {path}")),
            (Some(old), Some(new)) => {
                if old.mode != new.mode {
                    changes.push(format!(
                        "mode: {path}: want {:04o}, got {:04o}",
                        old.mode, new.mode
                    ));
                }
                if old.data != new.data {
                    changes.push(format!("changed: {path}"));
                }
            }
            (None, None) => unreachable!(),
        }
        let old = old.map_or(&[][..], |f| f.data.as_ref());
        let new = new.map_or(&[][..], |f| f.data.as_ref());
        if old != new {
            match (std::str::from_utf8(old), std::str::from_utf8(new)) {
                (Ok(old), Ok(new)) if !old.contains('\0') && !new.contains('\0') => {
                    let diff = TextDiff::from_lines(old, new)
                        .unified_diff()
                        .header(&format!("want/{path}"), &format!("got/{path}"))
                        .to_string();
                    let mut lines = diff.lines().take(40).collect::<Vec<_>>();
                    if diff.lines().count() > 40 {
                        lines.push("... diff truncated after 40 lines");
                    }
                    changes.push(lines.join("\n"));
                }
                _ => changes.push("binary content differs".into()),
            }
        }
        if !changes.is_empty() {
            reports.push(changes.join("\n"));
        }
    }
    reports
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::{
        path::PathBuf,
        sync::atomic::{AtomicUsize, Ordering},
    };

    pub(crate) fn temp_dir() -> PathBuf {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let dir = std::env::temp_dir().join(format!(
            "spinup-testutil-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn tree(files: &[(&str, &[u8], u32)]) -> spinup_vfs::tree::Tree {
        let mut tree = spinup_vfs::tree::Tree::new();
        for (path, data, mode) in files {
            tree.insert(*path, data.to_vec(), *mode).unwrap();
        }
        tree
    }

    #[test]
    fn reports_all_differences_and_binary_content() {
        let want = tree(&[
            ("changed", b"old\n", 0o644),
            ("missing", b"", 0o644),
            ("mode", b"", 0o755),
            ("binary", b"\0a", 0o644),
        ]);
        let got = tree(&[
            ("added", b"new", 0o644),
            ("changed", b"new\n", 0o644),
            ("mode", b"", 0o644),
            ("binary", b"\0b", 0o644),
        ]);
        let reports = compare_trees(&want, &got).join("\n");
        for fragment in [
            "added: added",
            "changed: changed",
            "-old",
            "+new",
            "missing: missing",
            "mode: mode",
            "binary content differs",
        ] {
            assert!(reports.contains(fragment), "{reports}");
        }
        assert!(compare_trees(&want, &want).is_empty());
    }

    #[test]
    fn rewrites_stale_files_and_uses_manifest_permissions() {
        let dir = temp_dir().join("golden");
        let first = tree(&[("stale/file", b"stale", 0o644)]);
        let next = tree(&[
            ("plain", b"plain", 0o644),
            ("bin/start", b"#!/bin/sh\n", 0o755),
        ]);
        for files in [&first, &next, &next] {
            write_golden(&dir, files).unwrap();
            assert_eq!(read_golden(&dir).unwrap(), *files);
        }
        assert!(!dir.join("stale").exists());
        assert_eq!(
            std::fs::read_to_string(dir.join(".modes")).unwrap(),
            "0755 bin/start\n"
        );
        std::fs::remove_dir_all(dir.parent().unwrap()).unwrap();
    }

    #[test]
    fn limits_diffs_and_renders_twice() {
        let want = tree(&[("a", "old\n".repeat(100).as_bytes(), 0o644)]);
        let got = tree(&[("a", "new\n".repeat(100).as_bytes(), 0o644)]);
        assert!(compare_trees(&want, &got)[0].lines().count() <= 42);
        let mut calls = 0;
        deterministic(|| {
            calls += 1;
            Ok(want.clone())
        })
        .unwrap();
        assert_eq!(calls, 2);
        let mut calls = 0;
        assert!(
            deterministic(|| {
                calls += 1;
                Ok(if calls == 1 {
                    want.clone()
                } else {
                    got.clone()
                })
            })
            .is_err()
        );
    }

    #[cfg(unix)]
    #[test]
    fn rejects_symbolic_links() {
        let dir = temp_dir();
        std::fs::write(dir.join("file"), b"data").unwrap();
        std::os::unix::fs::symlink("file", dir.join("link")).unwrap();
        assert!(read_golden(&dir).is_err());
        std::fs::remove_dir_all(dir).unwrap();
    }
}
