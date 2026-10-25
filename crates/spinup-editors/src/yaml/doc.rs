//! The splice editor: each edit replaces only the source bytes of the edited node.
//!
//! The editor reads the tree-sitter syntax tree for the byte ranges and writes new values
//! with the go-yaml rules ([`super::emit`]). Text outside the edited range stays as it is,
//! with its comments, quotes, and blank lines.

use std::collections::BTreeMap;

use tree_sitter::Node as TsNode;

use super::Error;
use super::emit;
use super::node::{Kind, Node, Style, decode, document_root, parse_tree, props};

/// One YAML document that the editor changes in place.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Document {
    src: String,
}

/// A syntax node with its byte ranges.
#[derive(Debug)]
struct Syn {
    /// The start of the node, with its anchor and tag.
    start: usize,
    /// The end of the last content token (not of trailing comments or line breaks).
    end: usize,
    /// Whether a flow collection holds the node.
    in_flow: bool,
    kind: SynKind,
    node: Node,
}

#[derive(Debug)]
enum SynKind {
    Scalar,
    Alias,
    BlockMap(Vec<Pair>),
    FlowMap(Vec<Pair>),
    BlockSeq(Vec<Item>),
    FlowSeq(Vec<Item>),
}

#[derive(Debug)]
struct Pair {
    key: String,
    start: usize,
    colon_end: usize,
    end: usize,
    value: Option<Syn>,
}

#[derive(Debug)]
struct Item {
    start: usize,
    end: usize,
    value: Option<Syn>,
}

/// The place of a value: a mapping entry, a sequence item, or the document root.
#[derive(Clone, Copy)]
enum Slot<'a> {
    Root,
    Pair { parent: &'a Syn, pair: &'a Pair },
    Item { parent: &'a Syn, item: &'a Item },
}

impl<'a> Slot<'a> {
    fn value(self, root: Option<&'a Syn>) -> Option<&'a Syn> {
        match self {
            Slot::Root => root,
            Slot::Pair { pair, .. } => pair.value.as_ref(),
            Slot::Item { item, .. } => item.value.as_ref(),
        }
    }
}

struct Parsed {
    root: Option<Syn>,
    comments: Vec<(usize, usize)>,
}

fn build(ts: TsNode<'_>, src: &str, indent: usize, in_flow: bool) -> Result<Syn, Error> {
    let node = decode(ts, src, indent, &mut BTreeMap::new())?;
    let parts = props(ts, src);
    let start = ts.start_byte();
    let Some(content) = parts.content else {
        return Ok(Syn {
            start,
            end: ts.end_byte(),
            in_flow,
            kind: SynKind::Scalar,
            node,
        });
    };
    let mut cursor = content.walk();
    let children: Vec<TsNode<'_>> = content.named_children(&mut cursor).collect();
    let (kind, end) = match content.kind() {
        "block_mapping" => {
            let mut pairs = Vec::new();
            for pair in children.iter().filter(|c| c.kind() == "block_mapping_pair") {
                pairs.push(build_pair(
                    *pair,
                    src,
                    column(src, pair.start_byte()),
                    in_flow,
                )?);
            }
            let end = pairs.last().map_or(content.end_byte(), |p: &Pair| p.end);
            (SynKind::BlockMap(pairs), end)
        }
        "flow_mapping" => {
            let mut pairs = Vec::new();
            for child in &children {
                match child.kind() {
                    "flow_pair" => pairs.push(build_pair(*child, src, indent, true)?),
                    "flow_node" => pairs.push(Pair {
                        key: decode(*child, src, indent, &mut BTreeMap::new())?
                            .as_str()
                            .unwrap_or_default()
                            .to_string(),
                        start: child.start_byte(),
                        colon_end: child.end_byte(),
                        end: child.end_byte(),
                        value: None,
                    }),
                    _ => {}
                }
            }
            (SynKind::FlowMap(pairs), content.end_byte())
        }
        "block_sequence" => {
            let mut items = Vec::new();
            for item in children
                .iter()
                .filter(|c| c.kind() == "block_sequence_item")
            {
                let mut c = item.walk();
                let value = item
                    .named_children(&mut c)
                    .find(|n| matches!(n.kind(), "block_node" | "flow_node"));
                let value = match value {
                    Some(v) => Some(build(v, src, column(src, item.start_byte()), in_flow)?),
                    None => None,
                };
                let end = value.as_ref().map_or(item.start_byte() + 1, |v| v.end);
                items.push(Item {
                    start: item.start_byte(),
                    end,
                    value,
                });
            }
            let end = items.last().map_or(content.end_byte(), |i| i.end);
            (SynKind::BlockSeq(items), end)
        }
        "flow_sequence" => {
            let mut items = Vec::new();
            for child in &children {
                if matches!(child.kind(), "flow_node" | "flow_pair") {
                    let value = build(*child, src, indent, true)?;
                    items.push(Item {
                        start: child.start_byte(),
                        end: child.end_byte(),
                        value: Some(value),
                    });
                }
            }
            (SynKind::FlowSeq(items), content.end_byte())
        }
        "flow_pair" => {
            let pair = build_pair(content, src, indent, true)?;
            (SynKind::FlowMap(vec![pair]), content.end_byte())
        }
        "alias" => (SynKind::Alias, content.end_byte()),
        _ => (SynKind::Scalar, content.end_byte()),
    };
    // A span can hold the line break that ends the document. The end is the last token.
    let end = start.max(src[..end].trim_end_matches([' ', '\t', '\r', '\n']).len());
    Ok(Syn {
        start,
        end,
        in_flow,
        kind,
        node,
    })
}

fn build_pair(pair: TsNode<'_>, src: &str, indent: usize, in_flow: bool) -> Result<Pair, Error> {
    let key = match pair.child_by_field_name("key") {
        Some(k) => decode(k, src, indent, &mut BTreeMap::new())?
            .as_str()
            .unwrap_or_default()
            .to_string(),
        None => String::new(),
    };
    let mut cursor = pair.walk();
    let colon_end = pair
        .children(&mut cursor)
        .find(|c| c.kind() == ":")
        .map_or(pair.start_byte(), |c| c.end_byte());
    let value = match pair.child_by_field_name("value") {
        Some(v) => Some(build(v, src, indent, in_flow)?),
        None => None,
    };
    let end = value.as_ref().map_or(colon_end, |v| v.end);
    Ok(Pair {
        key,
        start: pair.start_byte(),
        colon_end,
        end,
        value,
    })
}

fn collect_comments(node: TsNode<'_>, out: &mut Vec<(usize, usize)>) {
    if node.kind() == "comment" {
        out.push((node.start_byte(), node.end_byte()));
        return;
    }
    let mut cursor = node.walk();
    for child in node.children(&mut cursor) {
        collect_comments(child, out);
    }
}

fn column(src: &str, offset: usize) -> usize {
    offset - line_start(src, offset)
}

fn line_start(src: &str, offset: usize) -> usize {
    src[..offset].rfind('\n').map_or(0, |i| i + 1)
}

/// The offset after the line break of the line that holds `offset`.
fn line_end(src: &str, offset: usize) -> usize {
    src[offset..]
        .find('\n')
        .map_or(src.len(), |i| offset + i + 1)
}

/// The offset of the line break (or `\r\n`) of the line that holds `offset`.
fn line_content_end(src: &str, offset: usize) -> usize {
    let end = src[offset..].find('\n').map_or(src.len(), |i| offset + i);
    if end > offset && src.as_bytes()[end - 1] == b'\r' {
        end - 1
    } else {
        end
    }
}

fn is_null(syn: &Syn) -> bool {
    matches!(syn.kind, SynKind::Scalar) && (syn.node.is_null() || syn.node.as_str() == Some(""))
}

/// Builds the nested mappings of a missing path suffix.
fn nest(path: &[&str], value: &Node) -> Node {
    match path.split_first() {
        None => value.clone(),
        Some((first, rest)) => Node::map([(*first, nest(rest, value))]),
    }
}

/// A new value keeps the style of the value that it replaces when it has none (go-yaml).
fn inherit(value: &Node, old: &Node) -> Node {
    let mut value = value.clone();
    let same = matches!(
        (&value.kind, &old.kind),
        (Kind::Scalar(_), Kind::Scalar(_))
            | (Kind::Seq(_), Kind::Seq(_))
            | (Kind::Map(_), Kind::Map(_))
    );
    if value.style == Style::Any && same {
        value.style = old.style;
    }
    value
}

impl Document {
    /// Parses one YAML document. It rejects invalid YAML and more than one document.
    pub fn parse(src: &str) -> Result<Self, Error> {
        let doc = Self {
            src: src.to_string(),
        };
        doc.parsed()?;
        Ok(doc)
    }

    /// Returns the current source text.
    pub fn as_str(&self) -> &str {
        &self.src
    }

    /// Returns the current source text.
    pub fn into_string(self) -> String {
        self.src
    }

    fn parsed(&self) -> Result<Parsed, Error> {
        let tree = parse_tree(&self.src)?;
        let mut comments = Vec::new();
        collect_comments(tree.root_node(), &mut comments);
        let root = match document_root(&tree)? {
            Some(root) => Some(build(root, &self.src, 0, false)?),
            None => None,
        };
        Ok(Parsed { root, comments })
    }

    /// Returns the node at `path`, or `None` when the path is absent. A segment indexes a
    /// sequence when the parent is one. An entry without a value is a null node.
    pub fn get(&self, path: &[&str]) -> Option<Node> {
        let parsed = self.parsed().ok()?;
        let root = parsed.root.as_ref()?;
        let mut current = root;
        for (i, segment) in path.iter().enumerate() {
            match child(current, segment)?.value(None) {
                Some(value) => current = value,
                None if i + 1 == path.len() => return Some(Node::null()),
                None => return None,
            }
        }
        Some(current.node.clone())
    }

    /// Sets the value at `path` and creates missing mappings (Go `codemod.YAMLSet`).
    ///
    /// A null value on the path becomes a mapping. A replaced value keeps its scalar or
    /// collection style when the new value has none. Comments inside a replaced value move
    /// above its key. A new key goes after the last entry of its mapping.
    pub fn set(&mut self, path: &[&str], value: &Node) -> Result<(), Error> {
        if path.is_empty() {
            return Err(Error::new(
                "a document, value, and nonempty path are required",
            ));
        }
        let parsed = self.parsed()?;
        let Some(root) = parsed.root.as_ref() else {
            let text = emit::entries_text(&[(Node::str(path[0]), nest(&path[1..], value))], 0);
            let mut out = self.src.clone();
            if !out.is_empty() && !out.ends_with('\n') {
                out.push('\n');
            }
            out.push_str(&text);
            out.push('\n');
            return self.commit(out);
        };
        let mut current = root;
        for (i, segment) in path.iter().enumerate() {
            let last = i + 1 == path.len();
            let found = child(current, segment);
            match (&current.kind, found) {
                (SynKind::BlockMap(_) | SynKind::FlowMap(_), None) => {
                    let nested = nest(&path[i + 1..], value);
                    return self.insert_pair(current, segment, &nested);
                }
                (_, Some(slot)) => {
                    let target = slot.value(None);
                    if last {
                        let value = match target {
                            Some(old) => inherit(value, &old.node),
                            None => value.clone(),
                        };
                        return self.replace(&parsed, slot, &value);
                    }
                    match target {
                        Some(next) if !is_null(next) => current = next,
                        _ => {
                            let nested = nest(&path[i + 1..], value);
                            return self.replace(&parsed, slot, &nested);
                        }
                    }
                }
                _ => {
                    return Err(Error::new(format!(
                        "path {:?} crosses a non-mapping value",
                        &path[..i]
                    )));
                }
            }
        }
        Ok(())
    }

    /// Appends an item to the sequence at `path`. An empty path names the root.
    ///
    /// A null value becomes a sequence. A flow sequence stays a flow sequence, unless
    /// `block` asks for a block sequence outside a flow collection.
    pub fn append(&mut self, path: &[&str], value: &Node, block: bool) -> Result<(), Error> {
        let parsed = self.parsed()?;
        let (slot, target) = locate(&parsed, path)
            .ok_or_else(|| Error::new(format!("missing sequence: {path:?}")))?;
        let Some(target) = target.filter(|t| !is_null(t)) else {
            return self.replace(&parsed, slot, &Node::seq(vec![value.clone()]));
        };
        match &target.kind {
            SynKind::BlockSeq(items) => {
                let dash = items.first().map_or(0, |i| column(&self.src, i.start));
                let text = emit::items_text(std::slice::from_ref(value), dash);
                let at = line_end(&self.src, target.end);
                Ok(self.insert_lines(at, &text)?)
            }
            SynKind::FlowSeq(items) if block && !target.in_flow => {
                let mut nodes: Vec<Node> = items
                    .iter()
                    .filter_map(|i| i.value.as_ref().map(|v| v.node.clone()))
                    .collect();
                nodes.push(value.clone());
                self.replace(&parsed, slot, &Node::seq(nodes))
            }
            SynKind::FlowSeq(items) => {
                let entry = emit::flow_text(value);
                let out = match items.last() {
                    None => splice(&self.src, target.start, target.end, &format!("[{entry}]")),
                    Some(last) => splice(&self.src, last.end, last.end, &format!(", {entry}")),
                };
                self.commit(out)
            }
            _ => Err(Error::new(format!(
                "the value at {path:?} is not a sequence"
            ))),
        }
    }

    /// Removes the mapping entry at `path` and keeps its comments (Go `codemod.YAMLDelete`).
    ///
    /// It returns whether the document changed. A mapping without entries becomes `{}`.
    pub fn delete(&mut self, path: &[&str]) -> Result<bool, Error> {
        let Some((key, parent_path)) = path.split_last() else {
            return Ok(false);
        };
        let parsed = self.parsed()?;
        let Some((parent_slot, Some(parent))) = locate(&parsed, parent_path) else {
            return Ok(false);
        };
        let pairs = match &parent.kind {
            SynKind::BlockMap(pairs) | SynKind::FlowMap(pairs) => pairs,
            _ => return Ok(false),
        };
        let Some(index) = pairs.iter().position(|p| p.key == *key) else {
            return Ok(false);
        };
        let pair = &pairs[index];
        if pairs.len() == 1 {
            let empty = Node::new(Kind::Map(Vec::new())).with_style(Style::Flow);
            self.replace(&parsed, parent_slot, &empty)?;
            return Ok(true);
        }
        let src = &self.src;
        let out = if matches!(parent.kind, SynKind::FlowMap(_)) {
            let (start, end) = if index + 1 < pairs.len() {
                (pair.start, pairs[index + 1].start)
            } else {
                (pairs[index - 1].end, pair.end)
            };
            splice(src, start, end, "")
        } else {
            let first_on_line = line_start(src, pair.start);
            let region_end = line_end(src, pair.end);
            let comments = comments_in(src, &parsed.comments, pair.start, region_end);
            if src[first_on_line..pair.start].trim().is_empty() {
                let indent = " ".repeat(column(src, pair.start));
                let lines: String = comments.iter().map(|c| format!("{indent}{c}\n")).collect();
                splice(src, first_on_line, region_end, &lines)
            } else {
                // The first entry of a mapping on a sequence item line: `- key: value`.
                let next = &pairs[index + 1];
                let indent = " ".repeat(column(
                    src,
                    first_on_line + leading_spaces(&src[first_on_line..]),
                ));
                let lines: String = comments.iter().map(|c| format!("{indent}{c}\n")).collect();
                let out = splice(src, pair.start, next.start, "");
                splice(&out, first_on_line, first_on_line, &lines)
            }
        };
        self.commit(out)?;
        Ok(true)
    }

    /// Replaces the value of a slot with `value` (the style is final).
    fn replace(&mut self, parsed: &Parsed, slot: Slot<'_>, value: &Node) -> Result<(), Error> {
        let src = self.src.as_str();
        match slot {
            Slot::Root => {
                let text = emit::marshal(value, 2);
                let text = text.strip_suffix('\n').unwrap_or(&text);
                let out = match parsed.root.as_ref() {
                    Some(root) => {
                        let end = line_content_end(src, root.end);
                        let moved = comments_in(src, &parsed.comments, root.start, end);
                        let lines: String = moved.iter().map(|c| format!("{c}\n")).collect();
                        let out = splice(src, root.start, end, text);
                        splice(&out, root.start, root.start, &lines)
                    }
                    None => {
                        let mut out = src.to_string();
                        if !out.is_empty() && !out.ends_with('\n') {
                            out.push('\n');
                        }
                        format!("{out}{text}\n")
                    }
                };
                self.commit(out)
            }
            Slot::Pair { parent, pair } => {
                let in_flow = matches!(parent.kind, SynKind::FlowMap(_)) || parent.in_flow;
                let key_indent = column(src, pair.start);
                let text = emit::value_text(value, key_indent, in_flow);
                let out = self.splice_value(
                    parsed,
                    pair.colon_end,
                    pair.start,
                    pair.value.as_ref(),
                    &text,
                    in_flow,
                );
                self.commit(out)
            }
            Slot::Item { parent, item } => {
                let in_flow = matches!(parent.kind, SynKind::FlowSeq(_)) || parent.in_flow;
                if in_flow {
                    let text = emit::flow_text(value);
                    let out = splice(src, item.start, item.end, &text);
                    return self.commit(out);
                }
                let dash = column(src, item.start);
                let text = emit::item_text(value, dash);
                let out = self.splice_value(
                    parsed,
                    item.start + 1,
                    item.start,
                    item.value.as_ref(),
                    &text,
                    false,
                );
                self.commit(out)
            }
        }
    }

    /// Splices the text of a new value after the indicator that ends at `after` (`:` or `-`).
    /// `owner` is the start of the entry or item, for comments that move above it.
    fn splice_value(
        &self,
        parsed: &Parsed,
        after: usize,
        owner: usize,
        old: Option<&Syn>,
        text: &str,
        in_flow: bool,
    ) -> String {
        let src = self.src.as_str();
        // An inline value replaced by an inline value: change only its bytes.
        if let Some(old) = old
            && !text.contains('\n')
            && (in_flow || !src[old.start..old.end].contains('\n'))
            && !matches!(old.kind, SynKind::BlockMap(_) | SynKind::BlockSeq(_))
        {
            return splice(src, old.start, old.end, text.trim_start_matches(' '));
        }
        if in_flow {
            let end = old.map_or(after, |o| o.end);
            return splice(src, after, end, text);
        }
        let end = line_content_end(src, old.map_or(after, |o| o.end));
        let key_row_end = line_content_end(src, after);
        let comments: Vec<(usize, &str)> = parsed
            .comments
            .iter()
            .filter(|(s, _)| *s >= after && *s < end)
            .map(|(s, e)| (*s, &src[*s..*e]))
            .collect();
        let key_comment = comments
            .iter()
            .find(|(s, _)| *s < key_row_end)
            .map(|(_, c)| *c);
        let moved: Vec<&str> = comments
            .iter()
            .filter(|(s, _)| *s >= key_row_end)
            .map(|(_, c)| *c)
            .collect();
        let replacement = match key_comment {
            None => text.to_string(),
            Some(comment) => match text.split_once('\n') {
                None => format!("{text} {comment}"),
                Some((head, rest)) => format!("{head} {comment}\n{rest}"),
            },
        };
        let out = splice(src, after, end, &replacement);
        if moved.is_empty() {
            return out;
        }
        let start = line_start(src, owner);
        let indent = " ".repeat(column(src, owner));
        let lines: String = moved.iter().map(|c| format!("{indent}{c}\n")).collect();
        splice(&out, start, start, &lines)
    }

    /// Adds an entry after the last entry of a mapping.
    fn insert_pair(&mut self, map: &Syn, key: &str, value: &Node) -> Result<(), Error> {
        match &map.kind {
            SynKind::BlockMap(pairs) => {
                let indent = pairs.first().map_or(0, |p| column(&self.src, p.start));
                let text = emit::entries_text(&[(Node::str(key), value.clone())], indent);
                let at = line_end(&self.src, map.end);
                self.insert_lines(at, &text)
            }
            SynKind::FlowMap(pairs) => {
                let entry = emit::flow_entry_text(&Node::str(key), value);
                let out = match pairs.last() {
                    None => splice(&self.src, map.start, map.end, &format!("{{{entry}}}")),
                    Some(last) => splice(&self.src, last.end, last.end, &format!(", {entry}")),
                };
                self.commit(out)
            }
            _ => Err(Error::new("the parent is not a mapping")),
        }
    }

    /// Inserts whole lines at a line start `at`.
    fn insert_lines(&mut self, at: usize, text: &str) -> Result<(), Error> {
        let mut insertion = String::new();
        if at > 0 && !self.src[..at].ends_with('\n') {
            insertion.push('\n');
        }
        insertion.push_str(text);
        insertion.push('\n');
        let out = splice(&self.src, at, at, &insertion);
        self.commit(out)
    }

    /// Accepts new text when it is valid YAML with one document.
    fn commit(&mut self, out: String) -> Result<(), Error> {
        let candidate = Self { src: out };
        candidate
            .parsed()
            .map_err(|e| Error::new(format!("the edit produced invalid YAML: {}", e.reason())))?;
        *self = candidate;
        Ok(())
    }
}

fn leading_spaces(line: &str) -> usize {
    line.len() - line.trim_start_matches(' ').len()
}

/// Returns the child slot of a mapping key or a sequence index.
fn child<'a>(syn: &'a Syn, segment: &str) -> Option<Slot<'a>> {
    match &syn.kind {
        SynKind::BlockMap(pairs) | SynKind::FlowMap(pairs) => pairs
            .iter()
            .find(|p| p.key == segment)
            .map(|pair| Slot::Pair { parent: syn, pair }),
        SynKind::BlockSeq(items) | SynKind::FlowSeq(items) => {
            let index: usize = segment.parse().ok()?;
            items
                .get(index)
                .map(|item| Slot::Item { parent: syn, item })
        }
        _ => None,
    }
}

/// Returns the slot at `path` and its value.
fn locate<'a>(parsed: &'a Parsed, path: &[&str]) -> Option<(Slot<'a>, Option<&'a Syn>)> {
    let mut slot = Slot::Root;
    let mut value = parsed.root.as_ref();
    for segment in path {
        let current = value?;
        slot = child(current, segment)?;
        value = slot.value(None);
    }
    Some((slot, value))
}

fn comments_in<'a>(
    src: &'a str,
    comments: &[(usize, usize)],
    start: usize,
    end: usize,
) -> Vec<&'a str> {
    comments
        .iter()
        .filter(|(s, _)| *s >= start && *s < end)
        .map(|(s, e)| &src[*s..*e])
        .collect()
}

fn splice(src: &str, start: usize, end: usize, value: &str) -> String {
    let mut out = String::with_capacity(src.len() + value.len());
    out.push_str(&src[..start]);
    out.push_str(value);
    out.push_str(&src[end..]);
    out
}
