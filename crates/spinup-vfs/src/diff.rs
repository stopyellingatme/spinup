//! Direct port of internal/vfs/diff.go. Lines remain bytes, including invalid UTF-8.
use std::collections::HashMap;

#[derive(Clone, Copy)]
struct Line<'a> {
    kind: u8,
    text: &'a [u8],
}

/// Produce Go's patience diff and three-line context hunks.
pub fn file_diff(name: &str, old: &[u8], new: &[u8]) -> Vec<u8> {
    let size = old.len().max(new.len());
    if size > 1 << 20 {
        return format!("File too large to diff ({size} bytes)").into_bytes();
    }
    if crate::binary::is_binary(old) || crate::binary::is_binary(new) {
        return b"Binary files differ".to_vec();
    }
    let old: Vec<_> = old.split_inclusive(|v| *v == b'\n').collect();
    let new: Vec<_> = new.split_inclusive(|v| *v == b'\n').collect();
    let lines = patience(&old, &new);
    let mut hunks: Vec<(usize, usize)> = Vec::new();
    for (i, line) in lines.iter().enumerate() {
        if line.kind == b' ' {
            continue;
        }
        let start = i.saturating_sub(3);
        let end = lines.len().min(i + 4);
        if let Some(last) = hunks.last_mut()
            && start <= last.1
        {
            last.1 = last.1.max(end);
        } else {
            hunks.push((start, end));
        }
    }
    if hunks.is_empty() {
        return Vec::new();
    }
    let mut out = format!("--- a/{name}\n+++ b/{name}\n").into_bytes();
    for (start, end) in hunks {
        let (mut os, mut ns) = (1, 1);
        for line in &lines[..start] {
            os += usize::from(line.kind != b'+');
            ns += usize::from(line.kind != b'-');
        }
        let oc = lines[start..end].iter().filter(|v| v.kind != b'+').count();
        let nc = lines[start..end].iter().filter(|v| v.kind != b'-').count();
        if oc == 0 {
            os -= 1;
        }
        if nc == 0 {
            ns -= 1;
        }
        out.extend_from_slice(format!("@@ -{} +{} @@\n", range(os, oc), range(ns, nc)).as_bytes());
        for line in &lines[start..end] {
            out.push(line.kind);
            out.extend_from_slice(line.text.strip_suffix(b"\n").unwrap_or(line.text));
            out.push(b'\n');
            if !line.text.ends_with(b"\n") {
                out.extend_from_slice(b"\\ No newline at end of file\n");
            }
        }
    }
    out
}
fn range(start: usize, count: usize) -> String {
    if count == 1 {
        start.to_string()
    } else {
        format!("{start},{count}")
    }
}
fn patience<'a>(old: &[&'a [u8]], new: &[&'a [u8]]) -> Vec<Line<'a>> {
    let mut out = Vec::with_capacity(old.len() + new.len());
    let mut stack = vec![(old, new, false)];
    while let Some((mut old, mut new, equal)) = stack.pop() {
        if equal {
            out.extend(old.iter().map(|text| Line { kind: b' ', text }));
            continue;
        }
        let prefix = old.iter().zip(new).take_while(|(a, b)| a == b).count();
        out.extend(old[..prefix].iter().map(|text| Line { kind: b' ', text }));
        old = &old[prefix..];
        new = &new[prefix..];
        let suffix = old
            .iter()
            .rev()
            .zip(new.iter().rev())
            .take_while(|(a, b)| a == b)
            .count();
        if suffix > 0 {
            stack.push((&old[old.len() - suffix..], &[], true));
            old = &old[..old.len() - suffix];
            new = &new[..new.len() - suffix];
        }
        let anchors = anchors(old, new);
        if anchors.is_empty() {
            out.extend(old.iter().map(|text| Line { kind: b'-', text }));
            out.extend(new.iter().map(|text| Line { kind: b'+', text }));
            continue;
        }
        let (mut oe, mut ne) = (old.len(), new.len());
        for &(o, n) in anchors.iter().rev() {
            stack.push((&old[o + 1..oe], &new[n + 1..ne], false));
            stack.push((&old[o..o + 1], &[], true));
            (oe, ne) = (o, n);
        }
        stack.push((&old[..oe], &new[..ne], false));
    }
    out
}
fn occurrences<'a>(lines: &[&'a [u8]]) -> HashMap<&'a [u8], (usize, usize)> {
    let mut map = HashMap::with_capacity(lines.len());
    for (i, line) in lines.iter().enumerate() {
        let entry = map.entry(*line).or_insert((0, 0));
        entry.0 = i;
        entry.1 += 1;
    }
    map
}
fn anchors(old: &[&[u8]], new: &[&[u8]]) -> Vec<(usize, usize)> {
    let (oc, nc) = (occurrences(old), occurrences(new));
    let anchors: Vec<_> = old
        .iter()
        .enumerate()
        .filter_map(|(i, line)| {
            if oc[line].1 == 1 {
                nc.get(line).filter(|v| v.1 == 1).map(|v| (i, v.0))
            } else {
                None
            }
        })
        .collect();
    let mut tails: Vec<usize> = Vec::new();
    let mut previous = vec![None; anchors.len()];
    for (i, anchor) in anchors.iter().enumerate() {
        let pos = tails.partition_point(|j| anchors[*j].1 < anchor.1);
        if pos > 0 {
            previous[i] = Some(tails[pos - 1]);
        }
        if pos == tails.len() {
            tails.push(i);
        } else {
            tails[pos] = i;
        }
    }
    let mut selected = Vec::with_capacity(tails.len());
    let mut index = tails.last().copied();
    while let Some(i) = index {
        selected.push(anchors[i]);
        index = previous[i];
    }
    selected.reverse();
    selected
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::{Engine, engine::general_purpose::STANDARD};
    use serde::Deserialize;
    fn fixture(name: &str) -> Vec<u8> {
        std::fs::read(
            std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/oracle")
                .join(name),
        )
        .unwrap()
    }
    #[derive(Deserialize)]
    struct DiffCase {
        #[serde(default)]
        encoding: String,
        name: String,
        old: String,
        new: String,
        expected: String,
    }
    #[test]
    fn diff_bytes_equal_go() {
        let cases: Vec<DiffCase> = serde_json::from_slice(&fixture("diffs.json")).unwrap();
        assert!(cases.len() >= 200);
        for case in cases {
            let decode = |data: &str| {
                let raw = STANDARD.decode(data).unwrap();
                if case.encoding == "zstd" {
                    zstd::stream::decode_all(raw.as_slice()).unwrap()
                } else {
                    raw
                }
            };
            let old = decode(&case.old);
            let new = decode(&case.new);
            // vfs.Diff does not call fileDiff for unchanged files.
            let got = if old == new {
                Vec::new()
            } else {
                file_diff(&case.name, &old, &new)
            };
            assert_eq!(got, decode(&case.expected), "{}", case.name);
        }
    }
}

/// A difference between a planned file and the disk.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ChangeKind {
    /// The content and mode are equal.
    Unchanged,
    /// The file does not exist.
    Create,
    /// The content differs.
    Update,
    /// The plan removes an existing file.
    Remove,
    /// Only the permission bits differ.
    ModeOnly,
}

impl std::fmt::Display for ChangeKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Self::Unchanged => "unchanged",
            Self::Create => "create",
            Self::Update => "update",
            Self::Remove => "remove",
            Self::ModeOnly => "mode-only",
        })
    }
}

/// One comparison result. Diff output remains bytes to preserve invalid UTF-8.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Change {
    /// The relative path.
    pub path: String,
    /// The change category.
    pub kind: ChangeKind,
    /// The old content, or empty for a missing file.
    pub old: Vec<u8>,
    /// The new content, or empty for removal.
    pub new: Vec<u8>,
    /// The old permission bits.
    pub old_mode: u32,
    /// The new permission bits.
    pub new_mode: u32,
    /// The unified diff, binary marker, or size marker.
    pub unified_diff: Vec<u8>,
}

/// Compares a plan with files below an existing root directory.
pub fn diff(
    root: impl AsRef<std::path::Path>,
    plan: &crate::plan::Plan,
) -> anyhow::Result<Vec<Change>> {
    let (root, dir) = crate::path::open_root(root.as_ref())?;
    compare(&dir, &root, plan, true)
}

pub(crate) fn compare(
    dir: &cap_std::fs::Dir,
    root: &std::path::Path,
    plan: &crate::plan::Plan,
    include_diff: bool,
) -> anyhow::Result<Vec<Change>> {
    use crate::path::{rooted_path, user_error};
    let plan = plan.merge(&crate::plan::Plan::default())?;
    plan.files
        .iter()
        .map(|file| {
            let operation = format!("diff {:?}", file.path);
            let result = (|| {
                let path = rooted_path(dir, root, &file.path, false)?;
                let mut change = Change {
                    path: file.path.clone(),
                    kind: ChangeKind::Unchanged,
                    old: Vec::new(),
                    new: file.data.clone(),
                    old_mode: 0,
                    new_mode: crate::plan::file_mode(file.mode),
                    unified_diff: Vec::new(),
                };
                match dir.symlink_metadata(&path) {
                    Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                        if !file.delete {
                            change.kind = ChangeKind::Create;
                        }
                    }
                    Err(e) => return Err(e.into()),
                    Ok(metadata) => {
                        anyhow::ensure!(metadata.is_file(), "target is not a regular file");
                        change.old = dir.read(&path)?;
                        change.old_mode = permission_mode(&metadata.permissions());
                        change.kind = if file.delete {
                            change.new.clear();
                            change.new_mode = 0;
                            ChangeKind::Remove
                        } else if change.old != change.new {
                            ChangeKind::Update
                        } else if !same_mode(change.old_mode, change.new_mode) {
                            ChangeKind::ModeOnly
                        } else {
                            ChangeKind::Unchanged
                        };
                    }
                }
                if include_diff
                    && matches!(
                        change.kind,
                        ChangeKind::Create | ChangeKind::Update | ChangeKind::Remove
                    )
                {
                    change.unified_diff = file_diff(&change.path, &change.old, &change.new);
                }
                Ok(change)
            })();
            result.map_err(|e: anyhow::Error| user_error(&operation, e))
        })
        .collect()
}

/// Reports whether two modes are equal on this platform. Windows stores only the read-only
/// attribute, so there only the owner-write bit counts, and no other mode change is planned.
fn same_mode(old: u32, new: u32) -> bool {
    if cfg!(windows) {
        old & 0o200 == new & 0o200
    } else {
        old == new
    }
}

pub(crate) fn permission_mode(permissions: &cap_std::fs::Permissions) -> u32 {
    #[cfg(unix)]
    {
        use cap_std::fs::PermissionsExt;
        permissions.mode() & 0o777
    }
    #[cfg(not(unix))]
    {
        if permissions.readonly() { 0o444 } else { 0o666 }
    }
}

#[cfg(test)]
mod large_tests {
    use super::*;
    #[test]
    fn patience_diff_handles_twenty_thousand_unrelated_lines() {
        let old: String = (0..20000).map(|i| format!("old {i}\n")).collect();
        let new: String = (0..20000).map(|i| format!("new {i}\n")).collect();
        let output = file_diff("file", old.as_bytes(), new.as_bytes());
        let text = std::str::from_utf8(&output).unwrap();
        assert!(text.contains("@@ -1,20000 +1,20000 @@"));
        assert!(text.contains("-old 19999\n"));
        assert!(text.contains("+new 19999\n"));
    }
}
