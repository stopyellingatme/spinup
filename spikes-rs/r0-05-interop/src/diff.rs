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
    if [old, new]
        .iter()
        .any(|v| v[..v.len().min(8000)].contains(&0))
    {
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
