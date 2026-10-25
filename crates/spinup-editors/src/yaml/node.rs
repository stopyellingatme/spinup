//! The YAML node model (a subset of the go-yaml `yaml.Node`) and its decoding.
//!
//! A node keeps the presentation that matters for writing: the scalar style, the flow style
//! of a collection, the anchor, and an explicit tag. Comments are not part of the model:
//! the editor keeps them in the source text.

use std::collections::BTreeMap;

use tree_sitter::{Node as TsNode, Parser, Tree};

use super::Error;

/// The presentation style of a node (go-yaml `yaml.Style`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Style {
    /// No preference: the writer selects the style.
    #[default]
    Any,
    /// A single-quoted scalar.
    Single,
    /// A double-quoted scalar.
    Double,
    /// A literal block scalar (`|`).
    Literal,
    /// A folded block scalar (`>`).
    Folded,
    /// A flow collection (`[...]` or `{...}`).
    Flow,
}

/// The tag of a node.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub enum Tag {
    /// The tag follows from the plain text (go-yaml `resolve`). The writer never quotes a
    /// scalar because of its tag.
    #[default]
    Implicit,
    /// A string. The writer quotes a plain scalar that would read as another type.
    Str,
    /// An explicit tag that the writer writes, such as `!!binary` or `!Ref`.
    Explicit(String),
}

/// The content of a node.
#[derive(Debug, Clone, PartialEq)]
pub enum Kind {
    /// A scalar with its decoded text. A null scalar has an empty text.
    Scalar(String),
    /// A sequence.
    Seq(Vec<Node>),
    /// A mapping, in source order.
    Map(Vec<(Node, Node)>),
    /// An alias to the anchor with this name.
    Alias(String),
}

/// One YAML node.
#[derive(Debug, Clone, PartialEq)]
pub struct Node {
    /// The content.
    pub kind: Kind,
    /// The presentation style.
    pub style: Style,
    /// The tag.
    pub tag: Tag,
    /// The anchor name, or an empty string.
    pub anchor: String,
}

impl Node {
    /// Returns a node of `kind` with no style, tag, or anchor.
    pub fn new(kind: Kind) -> Self {
        Self {
            kind,
            style: Style::Any,
            tag: Tag::Implicit,
            anchor: String::new(),
        }
    }

    /// Returns a string scalar. The writer quotes it when it would read as another type.
    pub fn str(value: impl Into<String>) -> Self {
        Self {
            tag: Tag::Str,
            ..Self::new(Kind::Scalar(value.into()))
        }
    }

    /// Returns a scalar whose type follows from its plain text, such as `true` or `5m`.
    pub fn plain(value: impl Into<String>) -> Self {
        Self::new(Kind::Scalar(value.into()))
    }

    /// Returns a null scalar.
    pub fn null() -> Self {
        Self::plain("")
    }

    /// Returns a block sequence.
    pub fn seq(items: Vec<Node>) -> Self {
        Self::new(Kind::Seq(items))
    }

    /// Returns a block mapping with string keys, in the given order.
    pub fn map<K: Into<String>>(entries: impl IntoIterator<Item = (K, Node)>) -> Self {
        Self::new(Kind::Map(
            entries
                .into_iter()
                .map(|(key, value)| (Self::str(key), value))
                .collect(),
        ))
    }

    /// Returns the node with another style.
    pub fn with_style(mut self, style: Style) -> Self {
        self.style = style;
        self
    }

    /// Parses one YAML document into a node. An empty document is a null scalar.
    pub fn parse(src: &str) -> Result<Self, Error> {
        let tree = parse_tree(src)?;
        let root = document_root(&tree)?;
        let mut anchors = BTreeMap::new();
        Ok(match root {
            Some(root) => decode(root, src, 0, &mut anchors)?,
            None => Self::null(),
        })
    }

    /// Returns the scalar text, or `None` for a collection or an alias.
    pub fn as_str(&self) -> Option<&str> {
        match &self.kind {
            Kind::Scalar(value) => Some(value),
            _ => None,
        }
    }

    /// Reports whether the node is a null scalar (`~`, `null`, or nothing).
    pub fn is_null(&self) -> bool {
        match &self.kind {
            Kind::Scalar(value) => {
                self.tag == Tag::Implicit
                    && self.style == Style::Any
                    && resolve(value) == Resolved::Null
            }
            _ => false,
        }
    }

    /// Returns the items of a sequence.
    pub fn items(&self) -> Option<&[Node]> {
        match &self.kind {
            Kind::Seq(items) => Some(items),
            _ => None,
        }
    }

    /// Returns the items of a sequence, for changes.
    pub fn items_mut(&mut self) -> Option<&mut Vec<Node>> {
        match &mut self.kind {
            Kind::Seq(items) => Some(items),
            _ => None,
        }
    }

    /// Returns the entries of a mapping.
    pub fn entries(&self) -> Option<&[(Node, Node)]> {
        match &self.kind {
            Kind::Map(entries) => Some(entries),
            _ => None,
        }
    }

    /// Returns the value of a mapping key.
    pub fn get(&self, key: &str) -> Option<&Node> {
        self.entries()?
            .iter()
            .find(|(k, _)| k.as_str() == Some(key))
            .map(|(_, v)| v)
    }

    /// Returns the value of a mapping key, for changes.
    pub fn get_mut(&mut self, key: &str) -> Option<&mut Node> {
        match &mut self.kind {
            Kind::Map(entries) => entries
                .iter_mut()
                .find(|(k, _)| k.as_str() == Some(key))
                .map(|(_, v)| v),
            _ => None,
        }
    }

    /// Sets a mapping key and keeps the order of the other keys (a new key goes last).
    ///
    /// It returns `false` when the node is not a mapping.
    pub fn insert(&mut self, key: &str, value: Node) -> bool {
        let Kind::Map(entries) = &mut self.kind else {
            return false;
        };
        match entries.iter_mut().find(|(k, _)| k.as_str() == Some(key)) {
            Some((_, old)) => *old = value,
            None => entries.push((Self::str(key), value)),
        }
        true
    }

    /// Returns the node at a path. A segment indexes a sequence when the parent is one.
    pub fn at(&self, path: &[&str]) -> Option<&Node> {
        let mut node = self;
        for segment in path {
            node = match &node.kind {
                Kind::Seq(items) => items.get(segment.parse::<usize>().ok()?)?,
                Kind::Map(_) => node.get(segment)?,
                _ => return None,
            };
        }
        Some(node)
    }

    /// Returns the node at a path, for changes.
    pub fn at_mut(&mut self, path: &[&str]) -> Option<&mut Node> {
        let mut node = self;
        for segment in path {
            node = match node.kind {
                Kind::Seq(ref mut items) => items.get_mut(segment.parse::<usize>().ok()?)?,
                Kind::Map(_) => node.get_mut(segment)?,
                _ => return None,
            };
        }
        Some(node)
    }
}

/// The type that a plain scalar reads as (go-yaml `resolve`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Resolved {
    /// `~`, `null`, or an empty text.
    Null,
    /// `true` or `false` in three spellings.
    Bool,
    /// An integer in decimal, `0x`, `0o`, `0b`, or a leading `0` (octal).
    Int,
    /// A float, `.inf`, or `.nan`.
    Float,
    /// A date or a date-time.
    Timestamp,
    /// A string.
    Str,
}

/// Returns the type that a plain scalar reads as, with the go-yaml v3 rules.
pub fn resolve(value: &str) -> Resolved {
    match value {
        "" | "~" | "null" | "Null" | "NULL" => return Resolved::Null,
        "true" | "True" | "TRUE" | "false" | "False" | "FALSE" => return Resolved::Bool,
        ".nan" | ".NaN" | ".NAN" | ".inf" | ".Inf" | ".INF" | "+.inf" | "+.Inf" | "+.INF"
        | "-.inf" | "-.Inf" | "-.INF" => return Resolved::Float,
        _ => {}
    }
    match value.as_bytes()[0] {
        b'.' => {
            if go_parse_float(value).is_some() {
                return Resolved::Float;
            }
        }
        b'+' | b'-' | b'0'..=b'9' => {
            if is_timestamp(value) {
                return Resolved::Timestamp;
            }
            let plain = value.replace('_', "");
            if go_parse_int(&plain).is_some() {
                return Resolved::Int;
            }
            if is_yaml_float(&plain) && go_parse_float(&plain).is_some() {
                return Resolved::Float;
            }
            if let Some(rest) = plain
                .strip_prefix("0b")
                .or_else(|| plain.strip_prefix("-0b"))
                && !rest.is_empty()
                && rest.bytes().all(|b| b == b'0' || b == b'1')
            {
                return Resolved::Int;
            }
            if let Some(rest) = plain
                .strip_prefix("0o")
                .or_else(|| plain.strip_prefix("-0o"))
                && !rest.is_empty()
                && rest.bytes().all(|b| (b'0'..=b'7').contains(&b))
            {
                return Resolved::Int;
            }
        }
        _ => {}
    }
    Resolved::Str
}

/// Parses an integer like Go `strconv.ParseInt(s, 0, 64)` or `ParseUint`, as i128.
pub(crate) fn go_parse_int(s: &str) -> Option<i128> {
    let (negative, digits) = match s.as_bytes().first()? {
        b'-' => (true, &s[1..]),
        b'+' => (false, &s[1..]),
        _ => (false, s),
    };
    let (radix, body) = if let Some(rest) = digits
        .strip_prefix("0x")
        .or_else(|| digits.strip_prefix("0X"))
    {
        (16, rest)
    } else if let Some(rest) = digits
        .strip_prefix("0o")
        .or_else(|| digits.strip_prefix("0O"))
    {
        (8, rest)
    } else if let Some(rest) = digits
        .strip_prefix("0b")
        .or_else(|| digits.strip_prefix("0B"))
    {
        (2, rest)
    } else if digits.len() > 1 && digits.starts_with('0') {
        (8, &digits[1..])
    } else {
        (10, digits)
    };
    if body.is_empty() || !body.chars().all(|c| c.is_digit(radix)) {
        return None;
    }
    let magnitude = u64::from_str_radix(body, radix).ok()?;
    let value = if negative {
        -i128::from(magnitude)
    } else {
        i128::from(magnitude)
    };
    if negative && value < i128::from(i64::MIN) {
        return None;
    }
    Some(value)
}

/// Parses a float like Go `strconv.ParseFloat(s, 64)` for the forms that YAML reaches.
pub(crate) fn go_parse_float(s: &str) -> Option<f64> {
    let lower = s.to_ascii_lowercase();
    let body = lower.trim_start_matches(['+', '-']);
    if matches!(body, "inf" | "infinity" | "nan") {
        return lower.parse().ok();
    }
    if body.starts_with("0x") || body.contains('_') {
        return None;
    }
    s.parse::<f64>().ok().filter(|f| f.is_finite())
}

/// The YAML float pattern of go-yaml: `^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`.
fn is_yaml_float(s: &str) -> bool {
    let b = s.as_bytes();
    let mut i = usize::from(matches!(b.first(), Some(b'+' | b'-')));
    let digits = |i: &mut usize| {
        let start = *i;
        while *i < b.len() && b[*i].is_ascii_digit() {
            *i += 1;
        }
        *i - start
    };
    if b.get(i) == Some(&b'.') {
        i += 1;
        if digits(&mut i) == 0 {
            return false;
        }
    } else {
        if digits(&mut i) == 0 {
            return false;
        }
        if b.get(i) == Some(&b'.') {
            i += 1;
            digits(&mut i);
        }
    }
    if matches!(b.get(i), Some(b'e' | b'E')) {
        i += 1;
        if matches!(b.get(i), Some(b'+' | b'-')) {
            i += 1;
        }
        if digits(&mut i) == 0 {
            return false;
        }
    }
    i == b.len()
}

/// Reports whether a plain scalar reads as a timestamp (go-yaml `parseTimestamp`).
fn is_timestamp(s: &str) -> bool {
    let b = s.as_bytes();
    if b.len() < 5 || !b[..4].iter().all(u8::is_ascii_digit) || b[4] != b'-' {
        return false;
    }
    // 2006-1-2, with an optional time: "T"/"t"/" " 15:4:5[.frac][Z|±07:00].
    let mut parts = s.splitn(2, ['T', 't', ' ']);
    let date = parts.next().unwrap_or("");
    let mut fields = date.split('-');
    let ok_date = fields.next().is_some_and(|y| y.len() == 4)
        && fields
            .next()
            .is_some_and(|m| (1..=2).contains(&m.len()) && m.bytes().all(|c| c.is_ascii_digit()))
        && fields
            .next()
            .is_some_and(|d| (1..=2).contains(&d.len()) && d.bytes().all(|c| c.is_ascii_digit()))
        && fields.next().is_none();
    if !ok_date {
        return false;
    }
    match parts.next() {
        None => true,
        Some(time) => {
            let time = time.trim_end_matches('Z');
            let time = time
                .rsplit_once(['+', '-'])
                .filter(|(_, zone)| zone.contains(':'))
                .map_or(time, |(t, _)| t);
            let mut hms = time.split(':');
            let num = |p: Option<&str>| {
                p.is_some_and(|p| {
                    let p = p.split('.').next().unwrap_or("");
                    (1..=2).contains(&p.len()) && p.bytes().all(|c| c.is_ascii_digit())
                })
            };
            num(hms.next()) && num(hms.next()) && num(hms.next()) && hms.next().is_none()
        }
    }
}

/// Parses YAML source text with the tree-sitter grammar.
pub(crate) fn parse_tree(src: &str) -> Result<Tree, Error> {
    let mut parser = Parser::new();
    parser
        .set_language(&tree_sitter_yaml::LANGUAGE.into())
        .map_err(|e| Error::new(format!("load the YAML grammar: {e}")))?;
    let tree = parser
        .parse(src, None)
        .ok_or_else(|| Error::new("the YAML parser stopped"))?;
    if tree.root_node().has_error() {
        return Err(Error::new(format!(
            "invalid YAML at line {}",
            first_error(tree.root_node()).map_or(1, |n| n.start_position().row + 1)
        )));
    }
    // tree-sitter accepts some documents that YAML forbids. The libyaml decoder checks them.
    yaml_serde::from_str::<yaml_serde::Value>(src)
        .map_err(|e| Error::new(format!("invalid YAML: {e}")))?;
    Ok(tree)
}

fn first_error(node: TsNode<'_>) -> Option<TsNode<'_>> {
    if node.is_error() || node.is_missing() {
        return Some(node);
    }
    let mut cursor = node.walk();
    node.children(&mut cursor)
        .find(|c| c.has_error())
        .and_then(first_error)
}

/// Returns the content node of the only document, or `None` for an empty stream.
pub(crate) fn document_root(tree: &Tree) -> Result<Option<TsNode<'_>>, Error> {
    let stream = tree.root_node();
    let mut cursor = stream.walk();
    let documents: Vec<_> = stream
        .named_children(&mut cursor)
        .filter(|n| n.kind() == "document")
        .collect();
    if documents.len() > 1 {
        return Err(Error::new("expected one document"));
    }
    let Some(document) = documents.first() else {
        return Ok(None);
    };
    let mut cursor = document.walk();
    let root = document
        .named_children(&mut cursor)
        .find(|n| matches!(n.kind(), "block_node" | "flow_node"));
    Ok(root)
}

/// The parts of a `block_node` or `flow_node`: properties and content.
pub(crate) struct Props<'t> {
    pub anchor: String,
    pub tag: String,
    pub content: Option<TsNode<'t>>,
}

pub(crate) fn props<'t>(node: TsNode<'t>, src: &str) -> Props<'t> {
    if !matches!(node.kind(), "block_node" | "flow_node") {
        return Props {
            anchor: String::new(),
            tag: String::new(),
            content: Some(node),
        };
    }
    let mut out = Props {
        anchor: String::new(),
        tag: String::new(),
        content: None,
    };
    let mut cursor = node.walk();
    for child in node.named_children(&mut cursor) {
        match child.kind() {
            "anchor" => out.anchor = src[child.byte_range()].trim_start_matches('&').to_string(),
            "tag" => out.tag = src[child.byte_range()].to_string(),
            "comment" => {}
            _ => {
                if out.content.is_none() {
                    out.content = Some(child);
                }
            }
        }
    }
    out
}

/// Decodes a tree-sitter value node. `indent` is the indentation of the parent block.
pub(crate) fn decode(
    node: TsNode<'_>,
    src: &str,
    indent: usize,
    anchors: &mut BTreeMap<String, Node>,
) -> Result<Node, Error> {
    let parts = props(node, src);
    let mut out = match parts.content {
        None => Node::null(),
        Some(content) => decode_content(content, src, indent, anchors)?,
    };
    if !parts.tag.is_empty() {
        out.tag = Tag::Explicit(parts.tag);
    }
    if !parts.anchor.is_empty() {
        out.anchor = parts.anchor.clone();
        anchors.insert(parts.anchor, out.clone());
    }
    Ok(out)
}

fn column(src: &str, offset: usize) -> usize {
    offset - src[..offset].rfind('\n').map_or(0, |i| i + 1)
}

fn decode_content(
    node: TsNode<'_>,
    src: &str,
    indent: usize,
    anchors: &mut BTreeMap<String, Node>,
) -> Result<Node, Error> {
    let text = &src[node.byte_range()];
    let mut cursor = node.walk();
    Ok(match node.kind() {
        "plain_scalar" => Node::plain(fold_plain(text)),
        "single_quote_scalar" => {
            Node::plain(fold_plain(&text[1..text.len() - 1]).replace("''", "'"))
                .with_style(Style::Single)
        }
        "double_quote_scalar" => {
            Node::plain(decode_double(&text[1..text.len() - 1])?).with_style(Style::Double)
        }
        "block_scalar" => {
            let (value, style) = decode_block(src, node.start_byte(), node.end_byte(), indent)?;
            Node::plain(value).with_style(style)
        }
        "alias" => Node::new(Kind::Alias(text.trim_start_matches('*').to_string())),
        "block_sequence" => {
            let mut items = Vec::new();
            for item in node.named_children(&mut cursor) {
                if item.kind() != "block_sequence_item" {
                    continue;
                }
                let dash = column(src, item.start_byte());
                let mut c = item.walk();
                let value = item
                    .named_children(&mut c)
                    .find(|n| matches!(n.kind(), "block_node" | "flow_node"));
                items.push(match value {
                    Some(v) => decode(v, src, dash, anchors)?,
                    None => Node::null(),
                });
            }
            Node::seq(items)
        }
        "flow_sequence" => {
            let mut items = Vec::new();
            for item in node.named_children(&mut cursor) {
                match item.kind() {
                    "flow_node" => items.push(decode(item, src, indent, anchors)?),
                    "flow_pair" => {
                        let (k, v) = decode_pair(item, src, indent, anchors)?;
                        items.push(Node::new(Kind::Map(vec![(k, v)])).with_style(Style::Flow));
                    }
                    _ => {}
                }
            }
            Node::seq(items).with_style(Style::Flow)
        }
        "block_mapping" => {
            let mut entries: Vec<(Node, Node)> = Vec::new();
            for pair in node.named_children(&mut cursor) {
                if pair.kind() != "block_mapping_pair" {
                    continue;
                }
                let key_indent = column(src, pair.start_byte());
                let (k, v) = decode_pair(pair, src, key_indent, anchors)?;
                check_duplicate(&entries, &k, pair)?;
                entries.push((k, v));
            }
            Node::new(Kind::Map(entries))
        }
        "flow_mapping" => {
            let mut entries: Vec<(Node, Node)> = Vec::new();
            for pair in node.named_children(&mut cursor) {
                let entry = match pair.kind() {
                    "flow_pair" => decode_pair(pair, src, indent, anchors)?,
                    "flow_node" => (decode(pair, src, indent, anchors)?, Node::null()),
                    _ => continue,
                };
                check_duplicate(&entries, &entry.0, pair)?;
                entries.push(entry);
            }
            Node::new(Kind::Map(entries)).with_style(Style::Flow)
        }
        "flow_pair" => {
            let (k, v) = decode_pair(node, src, indent, anchors)?;
            Node::new(Kind::Map(vec![(k, v)])).with_style(Style::Flow)
        }
        other => return Err(Error::new(format!("unsupported YAML node {other}"))),
    })
}

fn check_duplicate(entries: &[(Node, Node)], key: &Node, at: TsNode<'_>) -> Result<(), Error> {
    if let Some(name) = key.as_str()
        && entries.iter().any(|(k, _)| k.as_str() == Some(name))
    {
        return Err(Error::new(format!(
            "mapping key {name:?} already defined (line {})",
            at.start_position().row + 1
        )));
    }
    Ok(())
}

fn decode_pair(
    pair: TsNode<'_>,
    src: &str,
    indent: usize,
    anchors: &mut BTreeMap<String, Node>,
) -> Result<(Node, Node), Error> {
    let key = match pair.child_by_field_name("key") {
        Some(k) => decode(k, src, indent, anchors)?,
        None => Node::null(),
    };
    let value = match pair.child_by_field_name("value") {
        Some(v) => decode(v, src, indent, anchors)?,
        None => Node::null(),
    };
    Ok((key, value))
}

/// Folds a multi-line plain or single-quoted scalar: one line break is a space, and
/// each empty line is a line break.
fn fold_plain(text: &str) -> String {
    if !text.contains('\n') {
        return text.to_string();
    }
    let mut out = String::new();
    let mut blank = 0;
    for (i, line) in text.split('\n').enumerate() {
        let line = line.trim_matches([' ', '\t', '\r']);
        if i == 0 {
            out.push_str(line);
            continue;
        }
        if line.is_empty() {
            blank += 1;
            continue;
        }
        if blank == 0 {
            out.push(' ');
        } else {
            out.push_str(&"\n".repeat(blank));
        }
        blank = 0;
        out.push_str(line);
    }
    out.push_str(&"\n".repeat(blank));
    out
}

/// Decodes the inside of a double-quoted scalar: escapes and line folding.
fn decode_double(text: &str) -> Result<String, Error> {
    let mut out = String::new();
    let mut chars = text.chars().peekable();
    while let Some(c) = chars.next() {
        match c {
            '\\' => {
                let Some(e) = chars.next() else {
                    return Err(Error::new("unclosed escape in a double-quoted scalar"));
                };
                let mut hex = |len: usize| -> Result<char, Error> {
                    let digits: String = (0..len).filter_map(|_| chars.next()).collect();
                    u32::from_str_radix(&digits, 16)
                        .ok()
                        .and_then(char::from_u32)
                        .ok_or_else(|| Error::new(format!("invalid escape \\{e}{digits}")))
                };
                match e {
                    '0' => out.push('\0'),
                    'a' => out.push('\u{7}'),
                    'b' => out.push('\u{8}'),
                    't' | '\t' => out.push('\t'),
                    'n' => out.push('\n'),
                    'v' => out.push('\u{b}'),
                    'f' => out.push('\u{c}'),
                    'r' => out.push('\r'),
                    'e' => out.push('\u{1b}'),
                    ' ' => out.push(' '),
                    '"' => out.push('"'),
                    '/' => out.push('/'),
                    '\\' => out.push('\\'),
                    'N' => out.push('\u{85}'),
                    '_' => out.push('\u{a0}'),
                    'L' => out.push('\u{2028}'),
                    'P' => out.push('\u{2029}'),
                    'x' => out.push(hex(2)?),
                    'u' => out.push(hex(4)?),
                    'U' => out.push(hex(8)?),
                    '\r' | '\n' => {
                        if e == '\r' && chars.peek() == Some(&'\n') {
                            chars.next();
                        }
                        while matches!(chars.peek(), Some(' ' | '\t')) {
                            chars.next();
                        }
                    }
                    other => return Err(Error::new(format!("invalid escape \\{other}"))),
                }
            }
            '\r' | '\n' => {
                if c == '\r' && chars.peek() == Some(&'\n') {
                    chars.next();
                }
                while out.ends_with([' ', '\t']) {
                    out.pop();
                }
                let mut breaks = 0;
                loop {
                    while matches!(chars.peek(), Some(' ' | '\t')) {
                        chars.next();
                    }
                    match chars.peek() {
                        Some('\n') => {
                            chars.next();
                            breaks += 1;
                        }
                        Some('\r') => {
                            chars.next();
                        }
                        _ => break,
                    }
                }
                if breaks == 0 {
                    out.push(' ');
                } else {
                    out.push_str(&"\n".repeat(breaks));
                }
            }
            c => out.push(c),
        }
    }
    Ok(out)
}

/// Decodes a block scalar from its header at `start`. `parent` is the indentation of the
/// parent block, for an indentation indicator.
fn decode_block(
    src: &str,
    start: usize,
    end: usize,
    parent: usize,
) -> Result<(String, Style), Error> {
    let header_end = src[start..].find('\n').map_or(src.len(), |i| start + i);
    let header = src[start..header_end]
        .split(" #")
        .next()
        .unwrap_or("")
        .trim();
    let style = if header.starts_with('>') {
        Style::Folded
    } else {
        Style::Literal
    };
    let mut chomp = ' ';
    let mut explicit = None;
    for c in header.chars().skip(1) {
        match c {
            '-' | '+' => chomp = c,
            '1'..='9' => explicit = c.to_digit(10).map(|d| d as usize),
            _ => {}
        }
    }
    // The content: the lines after the header, up to the node end, plus the blank lines
    // that follow it (they matter for `+`).
    let body_start = (header_end + 1).min(src.len());
    let mut body_end = end.max(body_start);
    let mut rest = &src[body_end..];
    while let Some(i) = rest.find('\n') {
        if !rest[..i].trim().is_empty() {
            break;
        }
        body_end += i + 1;
        rest = &rest[i + 1..];
    }
    let body = &src[body_start..body_end];
    let lines: Vec<&str> = body.split('\n').map(|l| l.trim_end_matches('\r')).collect();
    let indent = match explicit {
        Some(d) => parent + d,
        None => lines
            .iter()
            .find(|l| !l.trim().is_empty())
            .map_or(0, |l| l.len() - l.trim_start_matches(' ').len()),
    };
    let mut content: Vec<String> = Vec::new();
    for line in &lines {
        if line.len() <= indent && line.trim().is_empty() {
            content.push(String::new());
        } else {
            content.push(line.get(indent..).unwrap_or("").to_string());
        }
    }
    // The last split part is the empty rest after the final line break.
    if content.last().is_some_and(String::is_empty) && body.ends_with('\n') {
        content.pop();
    }
    let mut text = if style == Style::Literal {
        content.iter().map(|l| format!("{l}\n")).collect::<String>()
    } else {
        fold_block(&content)
    };
    let trailing = text.len() - text.trim_end_matches('\n').len();
    let body_len = text.len() - trailing;
    match chomp {
        '-' => text.truncate(body_len),
        '+' => {}
        _ => {
            text.truncate(body_len);
            if body_len > 0 {
                text.push('\n');
            }
        }
    }
    Ok((text, style))
}

/// Folds the lines of a folded block scalar. A line break between two normal lines is a
/// space. Empty lines are line breaks. Lines with more indentation keep their breaks.
fn fold_block(lines: &[String]) -> String {
    let mut out = String::new();
    let mut previous: Option<bool> = None;
    let mut blank = 0;
    for line in lines {
        if line.is_empty() {
            blank += 1;
            continue;
        }
        let more = line.starts_with([' ', '\t']);
        match previous {
            None => out.push_str(&"\n".repeat(blank)),
            Some(previous_more) if !more && !previous_more => {
                if blank == 0 {
                    out.push(' ');
                } else {
                    out.push_str(&"\n".repeat(blank));
                }
            }
            Some(_) => out.push_str(&"\n".repeat(blank + 1)),
        }
        blank = 0;
        previous = Some(more);
        out.push_str(line);
    }
    out.push('\n');
    out.push_str(&"\n".repeat(blank));
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolve_follows_go_yaml() {
        for (text, want) in [
            ("", Resolved::Null),
            ("~", Resolved::Null),
            ("True", Resolved::Bool),
            ("yes", Resolved::Str),
            ("0755", Resolved::Int),
            ("0x1F", Resolved::Int),
            ("0b101", Resolved::Int),
            ("0o17", Resolved::Int),
            ("1_000", Resolved::Int),
            ("-12", Resolved::Int),
            ("1.5", Resolved::Float),
            (".5", Resolved::Float),
            ("1e3", Resolved::Float),
            (".inf", Resolved::Float),
            ("2026-10-13", Resolved::Timestamp),
            ("2026-10-13T10:00:00Z", Resolved::Timestamp),
            ("2026-10-13 10:00:00", Resolved::Timestamp),
            // go-yaml reads `<<` as a merge key only in the key position.
            ("<<", Resolved::Str),
            ("5m", Resolved::Str),
            ("1.2.3", Resolved::Str),
            ("-", Resolved::Str),
            ("0x", Resolved::Str),
            ("v1", Resolved::Str),
            ("18446744073709551615", Resolved::Int),
            ("99999999999999999999", Resolved::Float),
        ] {
            assert_eq!(resolve(text), want, "{text:?}");
        }
    }

    #[test]
    fn parse_decodes_scalars_and_styles() {
        let src = "a: plain\n  folded\nb: 'it''s'\nc: \"x\\ty\\u00e9\\\n  z\"\nd: |-\n  one\n\n  two\ne: >\n  f g\n  h\n\n  i\nf: [1, {g: h}]\ng: &x !!str v\nh: *x\ni:\nj: |+\n  k\n\n";
        let node = Node::parse(src).unwrap();
        let get = |k: &str| node.get(k).unwrap();
        assert_eq!(get("a").as_str(), Some("plain folded"));
        assert_eq!(get("b").as_str(), Some("it's"));
        assert_eq!(get("b").style, Style::Single);
        assert_eq!(get("c").as_str(), Some("x\tyéz"));
        assert_eq!(get("d").as_str(), Some("one\n\ntwo"));
        assert_eq!(get("d").style, Style::Literal);
        assert_eq!(get("e").as_str(), Some("f g h\ni\n"));
        assert_eq!(get("f").style, Style::Flow);
        assert_eq!(get("f").at(&["1", "g"]).unwrap().as_str(), Some("h"));
        assert_eq!(get("g").anchor, "x");
        assert_eq!(get("g").tag, Tag::Explicit("!!str".into()));
        assert_eq!(get("h").kind, Kind::Alias("x".into()));
        assert!(get("i").is_null());
        let pairs = Node::parse("[a: b, c]\n").unwrap();
        assert_eq!(pairs.at(&["0", "a"]).unwrap().as_str(), Some("b"));
        assert_eq!(get("j").as_str(), Some("k\n\n"));
    }

    #[test]
    fn parse_rejects_invalid_documents() {
        assert!(Node::parse("a: [").is_err());
        assert!(Node::parse("a: 1\n---\nb: 2\n").is_err());
        let err = Node::parse("a: 1\na: 2\n").unwrap_err().to_string();
        assert!(
            err.contains("already defined") || err.contains("duplicate"),
            "{err}"
        );
        assert!(Node::parse("").unwrap().is_null());
        assert!(Node::parse("# only\n").unwrap().is_null());
    }

    #[test]
    fn node_helpers_edit_in_memory() {
        let mut node = Node::parse("a: {b: [x, y]}\n").unwrap();
        assert_eq!(node.at(&["a", "b", "1"]).unwrap().as_str(), Some("y"));
        *node.at_mut(&["a", "b", "0"]).unwrap() = Node::str("z");
        assert!(node.at(&["a", "b", "2"]).is_none());
        assert!(node.at(&["a", "b", "x"]).is_none());
        assert!(node.insert("c", Node::plain("1")));
        assert!(node.insert("a", Node::null()));
        assert!(!Node::str("s").insert("k", Node::null()));
        assert_eq!(node.entries().unwrap().len(), 2);
        assert!(node.get("a").unwrap().is_null());
        node.at_mut(&["c"]).unwrap().style = Style::Double;
        assert!(!node.get("c").unwrap().is_null());
        assert!(Node::seq(vec![]).items().unwrap().is_empty());
    }
}
