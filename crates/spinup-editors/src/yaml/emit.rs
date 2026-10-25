//! A YAML writer with the go-yaml v3 output rules (the libyaml emitter of go-yaml).
//!
//! It ports the parts of `emitterc.go` and `encode.go` that spinup output reaches: block
//! and flow collections, the indentation rules, scalar analysis, style selection, and the
//! quoting and escaping of scalars. The line width is unlimited, as in go-yaml v3.
//! It does not write comments: the editor keeps comments in the source text.

use std::cmp::Ordering;
use std::collections::BTreeMap;

use super::Error;
use super::node::{Kind, Node, Resolved, Style, Tag, go_parse_float, go_parse_int, resolve};

/// Writes one document like go-yaml `Encoder.Encode` with `SetIndent(indent)`.
///
/// `yaml.Marshal` uses an indentation of 4. The codemods use 2.
pub fn marshal(node: &Node, indent: usize) -> String {
    let mut e = Emitter::new(indent);
    e.node(node, -1, Top::Root, false);
    // The document end writes a final line break when the line is open.
    e.write_indent(0);
    e.out
}

/// Writes a document like go-yaml `yaml.Marshal` of the value that `yaml.Unmarshal`
/// decodes into `any`: aliases and merge keys are expanded, mapping keys are sorted,
/// scalars are typed again, and all styles and comments are dropped.
///
/// This is the Go Compose writer (`internal/maintain/pg`). It removes comments, as Go does.
pub fn marshal_value(node: &Node, indent: usize) -> Result<String, Error> {
    let mut anchors = BTreeMap::new();
    let value = go_value(node, &mut anchors, 0)?;
    Ok(marshal(&value, indent))
}

/// Returns the text without its final line break.
fn strip_break(mut text: String) -> String {
    if text.ends_with('\n') {
        text.pop();
    }
    text
}

/// The text after `key:` for a value of a block mapping whose keys are at `key_indent`,
/// or of a flow pair when `in_flow` is set. A block value starts with a line break.
pub(crate) fn value_text(node: &Node, key_indent: usize, in_flow: bool) -> String {
    let indent = isize::try_from(key_indent).unwrap_or(0);
    let mut e = Emitter::new(2);
    e.column = indent + 2;
    e.whitespace = false;
    e.indention = false;
    if in_flow {
        e.flow_level = 1;
        e.node(node, indent, Top::Flow, false);
    } else {
        e.node(node, indent, Top::Map, false);
    }
    strip_break(e.out)
}

/// The text after the dash of a block sequence item whose dash is at `dash`.
pub(crate) fn item_text(node: &Node, dash: usize) -> String {
    let indent = isize::try_from(dash).unwrap_or(0);
    let mut e = Emitter::new(2);
    e.column = indent + 1;
    e.whitespace = false;
    e.indention = true;
    e.node(node, indent, Top::SeqItem, false);
    strip_break(e.out)
}

/// The lines of new block mapping entries whose keys are at `indent`.
pub(crate) fn entries_text(entries: &[(Node, Node)], indent: usize) -> String {
    let mut e = Emitter::new(2);
    e.block_map(entries, isize::try_from(indent).unwrap_or(0));
    strip_break(e.out)
}

/// The lines of new block sequence items whose dashes are at `dash`.
pub(crate) fn items_text(items: &[Node], dash: usize) -> String {
    let mut e = Emitter::new(2);
    e.block_seq(items, isize::try_from(dash).unwrap_or(0));
    strip_break(e.out)
}

/// The text of a node inside a flow collection.
pub(crate) fn flow_text(node: &Node) -> String {
    let mut e = Emitter::new(2);
    e.flow_level = 1;
    e.node(node, 0, Top::Flow, false);
    e.out
}

/// The text of a `key: value` entry inside a flow mapping.
pub(crate) fn flow_entry_text(key: &Node, value: &Node) -> String {
    let mut e = Emitter::new(2);
    e.flow_level = 1;
    e.node(key, 0, Top::Flow, true);
    e.indicator(":", false, false, false);
    e.node(value, 0, Top::Flow, false);
    e.out
}

/// The state that the parent pushed before a node (libyaml `emitter.states` top).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Top {
    /// The document root.
    Root,
    /// An item of a block sequence.
    SeqItem,
    /// A key or a value of a block mapping.
    Map,
    /// An item of a flow collection.
    Flow,
}

/// The scalar analysis of libyaml (`yaml_emitter_analyze_scalar`).
#[derive(Debug, Clone, Copy)]
struct Analysis {
    multiline: bool,
    flow_plain: bool,
    block_plain: bool,
    single_quoted: bool,
    block: bool,
}

pub(crate) struct Emitter {
    pub(crate) out: String,
    best: isize,
    pub(crate) column: isize,
    pub(crate) whitespace: bool,
    pub(crate) indention: bool,
    pub(crate) flow_level: usize,
}

impl Emitter {
    pub(crate) fn new(indent: usize) -> Self {
        let indent = isize::try_from(indent).unwrap_or(2);
        Self {
            out: String::new(),
            best: if (2..10).contains(&indent) { indent } else { 2 },
            column: 0,
            whitespace: true,
            indention: true,
            flow_level: 0,
        }
    }

    fn put(&mut self, c: char) {
        self.out.push(c);
        self.column += 1;
    }

    fn put_break(&mut self) {
        self.out.push('\n');
        self.column = 0;
        self.indention = true;
    }

    /// libyaml `write_break`: a line feed starts a new line. Another break character is
    /// written as it is and also starts a new line.
    fn write_break(&mut self, c: char) {
        if c == '\n' {
            self.put_break();
        } else {
            self.out.push(c);
            self.column = 0;
            self.indention = true;
        }
    }

    fn write(&mut self, text: &str) {
        for c in text.chars() {
            self.put(c);
        }
    }

    pub(crate) fn write_indent(&mut self, indent: isize) {
        let indent = indent.max(0);
        if !self.indention || self.column > indent || (self.column == indent && !self.whitespace) {
            self.put_break();
        }
        while self.column < indent {
            self.put(' ');
        }
        self.whitespace = true;
    }

    fn indicator(
        &mut self,
        text: &str,
        need_whitespace: bool,
        is_whitespace: bool,
        is_indention: bool,
    ) {
        if need_whitespace && !self.whitespace {
            self.put(' ');
        }
        self.write(text);
        self.whitespace = is_whitespace;
        self.indention = self.indention && is_indention;
    }

    fn increase(&self, indent: isize, flow: bool, top: Top) -> isize {
        if indent < 0 {
            if flow { self.best } else { 0 }
        } else if top == Top::SeqItem {
            indent + 2
        } else {
            self.best * ((indent + self.best) / self.best)
        }
    }

    fn props(&mut self, node: &Node) {
        if !node.anchor.is_empty() {
            self.indicator("&", true, false, false);
            self.write(&node.anchor);
        }
        if let Tag::Explicit(tag) = &node.tag {
            self.indicator(tag, true, false, false);
        }
    }

    /// Writes a node whose container has the indentation `indent`.
    pub(crate) fn node(&mut self, node: &Node, indent: isize, top: Top, simple_key: bool) {
        match &node.kind {
            Kind::Alias(name) => {
                self.indicator("*", true, false, false);
                self.write(name);
            }
            Kind::Scalar(value) => self.scalar(node, value, indent, top, simple_key),
            Kind::Seq(items) => {
                self.props(node);
                if self.flow_level > 0 || node.style == Style::Flow || items.is_empty() {
                    self.flow_seq(items, indent, top);
                } else {
                    let indent = self.increase(indent, false, top);
                    self.block_seq(items, indent);
                }
            }
            Kind::Map(entries) => {
                self.props(node);
                if self.flow_level > 0 || node.style == Style::Flow || entries.is_empty() {
                    self.flow_map(entries, indent, top);
                } else {
                    let indent = self.increase(indent, false, top);
                    self.block_map(entries, indent);
                }
            }
        }
    }

    /// Writes the items of a block sequence whose dashes are at `indent`.
    pub(crate) fn block_seq(&mut self, items: &[Node], indent: isize) {
        for item in items {
            self.write_indent(indent);
            self.indicator("-", true, false, true);
            self.node(item, indent, Top::SeqItem, false);
        }
    }

    /// Writes the entries of a block mapping whose keys are at `indent`.
    pub(crate) fn block_map(&mut self, entries: &[(Node, Node)], indent: isize) {
        for (key, value) in entries {
            self.write_indent(indent);
            if simple_key(key) {
                self.node(key, indent, Top::Map, true);
                self.indicator(":", false, false, false);
            } else {
                self.indicator("?", true, false, true);
                self.node(key, indent, Top::Map, false);
                self.write_indent(indent);
                self.indicator(":", true, false, true);
            }
            self.node(value, indent, Top::Map, false);
        }
    }

    fn flow_seq(&mut self, items: &[Node], indent: isize, top: Top) {
        self.indicator("[", true, true, false);
        let inner = self.increase(indent, true, top);
        self.flow_level += 1;
        for (i, item) in items.iter().enumerate() {
            if i > 0 {
                self.indicator(",", false, false, false);
            }
            if self.column == 0 {
                self.write_indent(inner);
            }
            self.node(item, inner, Top::Flow, false);
        }
        self.flow_level -= 1;
        if self.column == 0 {
            self.write_indent(indent);
        }
        self.indicator("]", false, false, false);
    }

    fn flow_map(&mut self, entries: &[(Node, Node)], indent: isize, top: Top) {
        self.indicator("{", true, true, false);
        let inner = self.increase(indent, true, top);
        self.flow_level += 1;
        for (i, (key, value)) in entries.iter().enumerate() {
            if i > 0 {
                self.indicator(",", false, false, false);
            }
            if self.column == 0 {
                self.write_indent(inner);
            }
            if simple_key(key) {
                self.node(key, inner, Top::Flow, true);
                self.indicator(":", false, false, false);
            } else {
                self.indicator("?", true, false, false);
                self.node(key, inner, Top::Flow, false);
                self.indicator(":", true, false, false);
            }
            self.node(value, inner, Top::Flow, false);
        }
        self.flow_level -= 1;
        self.indicator("}", false, false, false);
    }

    fn scalar(&mut self, node: &Node, value: &str, indent: isize, top: Top, simple_key: bool) {
        // go-yaml `encoder.node`: a string that would read as another type is quoted.
        let force_quoting =
            node.tag == Tag::Str && node.style == Style::Any && resolve(value) != Resolved::Str;
        let requested = match node.style {
            Style::Double => ScalarStyle::Double,
            Style::Single => ScalarStyle::Single,
            Style::Literal => ScalarStyle::Literal,
            Style::Folded => ScalarStyle::Folded,
            Style::Any | Style::Flow if value.contains('\n') => ScalarStyle::Literal,
            _ if force_quoting => ScalarStyle::Double,
            _ => ScalarStyle::Plain,
        };
        let analysis = analyze(value);
        let style = self.select(requested, &analysis, value, simple_key);
        self.props(node);
        let indent = self.increase(indent, true, top);
        match style {
            ScalarStyle::Plain => self.plain(value),
            ScalarStyle::Single => self.single(value, indent),
            ScalarStyle::Double => self.double(value),
            ScalarStyle::Literal => self.literal(value, indent),
            ScalarStyle::Folded => self.folded(value, indent),
        }
    }

    /// libyaml `yaml_emitter_select_scalar_style`.
    fn select(
        &self,
        mut style: ScalarStyle,
        a: &Analysis,
        value: &str,
        simple_key: bool,
    ) -> ScalarStyle {
        if simple_key && a.multiline {
            style = ScalarStyle::Double;
        }
        if style == ScalarStyle::Plain {
            if (self.flow_level > 0 && !a.flow_plain) || (self.flow_level == 0 && !a.block_plain) {
                style = ScalarStyle::Single;
            }
            if value.is_empty() && (self.flow_level > 0 || simple_key) {
                style = ScalarStyle::Single;
            }
        }
        if style == ScalarStyle::Single && !a.single_quoted {
            style = ScalarStyle::Double;
        }
        if matches!(style, ScalarStyle::Literal | ScalarStyle::Folded)
            && (!a.block || self.flow_level > 0 || simple_key)
        {
            style = ScalarStyle::Double;
        }
        style
    }

    fn plain(&mut self, value: &str) {
        if !value.is_empty() && !self.whitespace {
            self.put(' ');
        }
        self.write(value);
        if !value.is_empty() {
            self.whitespace = false;
        }
        self.indention = false;
    }

    fn single(&mut self, value: &str, indent: isize) {
        self.indicator("'", true, false, false);
        let mut breaks = false;
        for c in value.chars() {
            if is_break(c) {
                if !breaks && c == '\n' {
                    self.put_break();
                }
                self.write_break(c);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent(indent);
                }
                if c == '\'' {
                    self.put('\'');
                }
                self.put(c);
                self.indention = false;
                breaks = false;
            }
        }
        self.indicator("'", false, false, false);
        self.whitespace = false;
        self.indention = false;
    }

    fn double(&mut self, value: &str) {
        self.indicator("\"", true, false, false);
        // libyaml `is_bom` looks at the start of the value, not at the current character.
        let bom = value.starts_with('\u{feff}');
        for c in value.chars() {
            if bom || !is_printable(c) || is_break(c) || c == '"' || c == '\\' {
                self.put('\\');
                match c {
                    '\0' => self.put('0'),
                    '\u{7}' => self.put('a'),
                    '\u{8}' => self.put('b'),
                    '\t' => self.put('t'),
                    '\n' => self.put('n'),
                    '\u{b}' => self.put('v'),
                    '\u{c}' => self.put('f'),
                    '\r' => self.put('r'),
                    '\u{1b}' => self.put('e'),
                    '"' => self.put('"'),
                    '\\' => self.put('\\'),
                    '\u{85}' => self.put('N'),
                    '\u{a0}' => self.put('_'),
                    '\u{2028}' => self.put('L'),
                    '\u{2029}' => self.put('P'),
                    c => {
                        let code = c as u32;
                        let (letter, width) = if code <= 0xFF {
                            ('x', 2)
                        } else if code <= 0xFFFF {
                            ('u', 4)
                        } else {
                            ('U', 8)
                        };
                        self.put(letter);
                        self.write(&format!("{code:0width$X}"));
                    }
                }
            } else {
                self.put(c);
            }
        }
        self.indicator("\"", false, false, false);
        self.whitespace = false;
        self.indention = false;
    }

    fn block_hints(&mut self, value: &str) {
        if value.starts_with(' ') || value.starts_with(is_break) {
            self.indicator(&self.best.to_string(), false, false, false);
        }
        let chars: Vec<char> = value.chars().collect();
        let chomp = match chars.as_slice() {
            [] => Some('-'),
            [.., last] if !is_break(*last) => Some('-'),
            [_] => Some('+'),
            [.., before, _] if is_break(*before) => Some('+'),
            _ => None,
        };
        if let Some(c) = chomp {
            self.indicator(&c.to_string(), false, false, false);
        }
    }

    fn literal(&mut self, value: &str, indent: isize) {
        self.indicator("|", true, false, false);
        self.block_hints(value);
        self.put_break();
        self.whitespace = true;
        let mut breaks = true;
        for c in value.chars() {
            if is_break(c) {
                self.write_break(c);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent(indent);
                }
                self.put(c);
                self.indention = false;
                breaks = false;
            }
        }
    }

    fn folded(&mut self, value: &str, indent: isize) {
        self.indicator(">", true, false, false);
        self.block_hints(value);
        self.put_break();
        self.whitespace = true;
        let mut breaks = true;
        let mut leading_spaces = true;
        // libyaml scans from the start of the value here, not from the break.
        let starts_blank = value.trim_start_matches(is_break).starts_with([' ', '\t'])
            || value.trim_start_matches(is_break).is_empty();
        for c in value.chars() {
            if is_break(c) {
                if !breaks && !leading_spaces && c == '\n' && !starts_blank {
                    self.put_break();
                }
                self.write_break(c);
                breaks = true;
            } else {
                if breaks {
                    self.write_indent(indent);
                    leading_spaces = c == ' ' || c == '\t';
                }
                self.put(c);
                self.indention = false;
                breaks = false;
            }
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ScalarStyle {
    Plain,
    Single,
    Double,
    Literal,
    Folded,
}

fn simple_key(key: &Node) -> bool {
    match &key.kind {
        Kind::Alias(name) => name.len() <= 128,
        Kind::Scalar(value) => {
            let tag = match &key.tag {
                Tag::Explicit(tag) => tag.len(),
                _ => 0,
            };
            !analyze(value).multiline && key.anchor.len() + tag + value.len() <= 128
        }
        Kind::Seq(items) => items.is_empty(),
        Kind::Map(entries) => entries.is_empty(),
    }
}

fn is_break(c: char) -> bool {
    matches!(c, '\r' | '\n' | '\u{85}' | '\u{2028}' | '\u{2029}')
}

/// libyaml `is_printable` (go-yaml treats 4-byte characters as not printable).
fn is_printable(c: char) -> bool {
    c == '\n'
        || ('\u{20}'..='\u{7e}').contains(&c)
        || ('\u{a0}'..='\u{d7ff}').contains(&c)
        || (('\u{e000}'..='\u{fffd}').contains(&c) && c != '\u{feff}')
}

/// libyaml `yaml_emitter_analyze_scalar`, with Unicode output enabled.
fn analyze(value: &str) -> Analysis {
    if value.is_empty() {
        return Analysis {
            multiline: false,
            flow_plain: false,
            block_plain: true,
            single_quoted: true,
            block: false,
        };
    }
    let mut block_indicators = value.starts_with("---") || value.starts_with("...");
    let mut flow_indicators = block_indicators;
    let mut line_breaks = false;
    let mut special = false;
    let mut tabs = false;
    let mut leading_space = false;
    let mut leading_break = false;
    let mut trailing_space = false;
    let mut trailing_break = false;
    let mut break_space = false;
    let mut space_break = false;
    let mut previous_space = false;
    let mut previous_break = false;
    let mut preceded_by_whitespace = true;
    let chars: Vec<char> = value.chars().collect();
    for (i, &c) in chars.iter().enumerate() {
        let next = chars.get(i + 1).copied();
        let followed_by_whitespace = next.is_none_or(|n| n == ' ' || n == '\t' || is_break(n));
        if i == 0 {
            match c {
                '#' | ',' | '[' | ']' | '{' | '}' | '&' | '*' | '!' | '|' | '>' | '\'' | '"'
                | '%' | '@' | '`' => {
                    flow_indicators = true;
                    block_indicators = true;
                }
                '?' | ':' => {
                    flow_indicators = true;
                    if followed_by_whitespace {
                        block_indicators = true;
                    }
                }
                '-' if followed_by_whitespace => {
                    flow_indicators = true;
                    block_indicators = true;
                }
                _ => {}
            }
        } else {
            match c {
                ',' | '?' | '[' | ']' | '{' | '}' => flow_indicators = true,
                ':' => {
                    flow_indicators = true;
                    if followed_by_whitespace {
                        block_indicators = true;
                    }
                }
                '#' if preceded_by_whitespace => {
                    flow_indicators = true;
                    block_indicators = true;
                }
                _ => {}
            }
        }
        if c == '\t' {
            tabs = true;
        } else if !is_printable(c) {
            special = true;
        }
        if c == ' ' {
            if i == 0 {
                leading_space = true;
            }
            if i + 1 == chars.len() {
                trailing_space = true;
            }
            if previous_break {
                break_space = true;
            }
            previous_space = true;
            previous_break = false;
        } else if is_break(c) {
            line_breaks = true;
            if i == 0 {
                leading_break = true;
            }
            if i + 1 == chars.len() {
                trailing_break = true;
            }
            if previous_space {
                space_break = true;
            }
            previous_space = false;
            previous_break = true;
        } else {
            previous_space = false;
            previous_break = false;
        }
        preceded_by_whitespace = c == ' ' || c == '\t' || is_break(c) || c == '\0';
    }
    let mut a = Analysis {
        multiline: line_breaks,
        flow_plain: true,
        block_plain: true,
        single_quoted: true,
        block: true,
    };
    if leading_space || leading_break || trailing_space || trailing_break {
        a.flow_plain = false;
        a.block_plain = false;
    }
    if trailing_space {
        a.block = false;
    }
    if break_space {
        a.flow_plain = false;
        a.block_plain = false;
        a.single_quoted = false;
    }
    if space_break || tabs || special {
        a.flow_plain = false;
        a.block_plain = false;
        a.single_quoted = false;
    }
    if space_break || special {
        a.block = false;
    }
    if line_breaks {
        a.flow_plain = false;
        a.block_plain = false;
    }
    if flow_indicators {
        a.flow_plain = false;
    }
    if block_indicators {
        a.block_plain = false;
    }
    a
}

/// Converts a node to the value that go-yaml decodes into `any`, as a node in the style
/// that `yaml.Marshal` writes.
fn go_value(
    node: &Node,
    anchors: &mut BTreeMap<String, Node>,
    depth: usize,
) -> Result<Node, Error> {
    if depth > 64 {
        return Err(Error::new("the aliases nest too deep"));
    }
    if !node.anchor.is_empty() {
        let mut raw = node.clone();
        raw.anchor.clear();
        anchors.insert(node.anchor.clone(), raw);
    }
    Ok(match &node.kind {
        Kind::Alias(name) => {
            let target = anchors
                .get(name)
                .cloned()
                .ok_or_else(|| Error::new(format!("unknown anchor {name:?}")))?;
            go_value(&target, anchors, depth + 1)?
        }
        Kind::Scalar(value) => go_scalar(node, value),
        Kind::Seq(items) => Node::seq(
            items
                .iter()
                .map(|item| go_value(item, anchors, depth + 1))
                .collect::<Result<_, _>>()?,
        ),
        Kind::Map(entries) => {
            let mut explicit: Vec<(String, Node)> = Vec::new();
            let mut merged: Vec<(String, Node)> = Vec::new();
            for (key, value) in entries {
                let name = key.as_str().unwrap_or_default().to_string();
                if name == "<<" && key.tag == Tag::Implicit && key.style == Style::Any {
                    let sources = match &value.kind {
                        Kind::Seq(items) => items.clone(),
                        _ => vec![value.clone()],
                    };
                    for source in sources {
                        let source = go_value(&source, anchors, depth + 1)?;
                        let Kind::Map(pairs) = source.kind else {
                            return Err(Error::new("a merge key needs a mapping"));
                        };
                        for (k, v) in pairs {
                            let k = k.as_str().unwrap_or_default().to_string();
                            if !merged.iter().any(|(m, _)| *m == k) {
                                merged.push((k, v));
                            }
                        }
                    }
                    continue;
                }
                let value = go_value(value, anchors, depth + 1)?;
                match explicit.iter_mut().find(|(k, _)| *k == name) {
                    Some(slot) => slot.1 = value,
                    None => explicit.push((name, value)),
                }
            }
            for (k, v) in merged {
                if !explicit.iter().any(|(e, _)| *e == k) {
                    explicit.push((k, v));
                }
            }
            explicit.sort_by(|(a, _), (b, _)| go_key_order(a, b));
            Node::new(Kind::Map(
                explicit
                    .into_iter()
                    .map(|(k, v)| (go_string(&k), v))
                    .collect(),
            ))
        }
    })
}

fn go_scalar(node: &Node, value: &str) -> Node {
    let typed = node.tag == Tag::Implicit && node.style == Style::Any;
    if !typed {
        return go_string(value);
    }
    match resolve(value) {
        Resolved::Null => Node::plain("null"),
        Resolved::Bool => Node::plain(if value.eq_ignore_ascii_case("true") {
            "true"
        } else {
            "false"
        }),
        Resolved::Int => {
            let plain = value.replace('_', "");
            let parsed = go_parse_int(&plain).or_else(|| {
                let (negative, body) = match plain.strip_prefix('-') {
                    Some(rest) => (true, rest),
                    None => (false, plain.as_str()),
                };
                let (radix, digits) = if let Some(d) = body.strip_prefix("0b") {
                    (2, d)
                } else {
                    (8, body.strip_prefix("0o").unwrap_or(body))
                };
                let magnitude = i128::from(u64::from_str_radix(digits, radix).ok()?);
                Some(if negative { -magnitude } else { magnitude })
            });
            parsed.map_or_else(|| go_string(value), |n| Node::plain(n.to_string()))
        }
        Resolved::Float => Node::plain(go_float(value)),
        Resolved::Timestamp | Resolved::Str => go_string(value),
    }
}

/// Formats a float like go-yaml: `strconv.FormatFloat(f, 'g', -1, 64)`, `.inf`, `.nan`.
fn go_float(text: &str) -> String {
    let lower = text.to_ascii_lowercase();
    let x = match lower.trim_start_matches('+') {
        ".inf" => f64::INFINITY,
        "-.inf" => f64::NEG_INFINITY,
        ".nan" => f64::NAN,
        _ => go_parse_float(&text.replace('_', "")).unwrap_or(f64::NAN),
    };
    if x.is_nan() {
        return ".nan".to_string();
    }
    if x.is_infinite() {
        return if x > 0.0 { ".inf" } else { "-.inf" }.to_string();
    }
    if x == 0.0 {
        return if x.is_sign_negative() { "-0" } else { "0" }.to_string();
    }
    let sci = format!("{x:e}");
    let (mantissa, exponent) = sci.split_once('e').unwrap_or((&sci, "0"));
    let exponent: i32 = exponent.parse().unwrap_or(0);
    let negative = mantissa.starts_with('-');
    let digits: String = mantissa.chars().filter(char::is_ascii_digit).collect();
    let sign = if negative { "-" } else { "" };
    if !(-4..6).contains(&exponent) {
        let (first, rest) = digits.split_at(1);
        let fraction = if rest.is_empty() {
            String::new()
        } else {
            format!(".{rest}")
        };
        let exp_sign = if exponent < 0 { '-' } else { '+' };
        return format!("{sign}{first}{fraction}e{exp_sign}{:02}", exponent.abs());
    }
    if exponent < 0 {
        let zeros = "0".repeat(usize::try_from(-exponent - 1).unwrap_or(0));
        return format!("{sign}0.{zeros}{digits}");
    }
    let point = usize::try_from(exponent + 1).unwrap_or(1);
    if digits.len() <= point {
        format!("{sign}{digits}{}", "0".repeat(point - digits.len()))
    } else {
        format!("{sign}{}.{}", &digits[..point], &digits[point..])
    }
}

/// A string node in the style of go-yaml `encoder.stringv`.
fn go_string(value: &str) -> Node {
    let plain = resolve(value) == Resolved::Str && !is_base60_float(value) && !is_old_bool(value);
    let style = if value.contains('\n') {
        Style::Literal
    } else if plain {
        Style::Any
    } else {
        Style::Double
    };
    Node::plain(value).with_style(style)
}

fn is_old_bool(value: &str) -> bool {
    matches!(
        value,
        "y" | "Y"
            | "yes"
            | "Yes"
            | "YES"
            | "on"
            | "On"
            | "ON"
            | "n"
            | "N"
            | "no"
            | "No"
            | "NO"
            | "off"
            | "Off"
            | "OFF"
    )
}

/// `^[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+(?:\.[0-9_]*)?$`
fn is_base60_float(value: &str) -> bool {
    let b = value.as_bytes();
    let mut i = usize::from(matches!(b.first(), Some(b'+' | b'-')));
    if !b.get(i).is_some_and(u8::is_ascii_digit) || !value.contains(':') {
        return false;
    }
    while b.get(i).is_some_and(|c| c.is_ascii_digit() || *c == b'_') {
        i += 1;
    }
    let mut groups = 0;
    while b.get(i) == Some(&b':') {
        i += 1;
        let start = i;
        while i - start < 2 && b.get(i).is_some_and(u8::is_ascii_digit) {
            i += 1;
        }
        let group = &b[start..i];
        let ok = match group {
            [d] => d.is_ascii_digit(),
            [a, d] => (b'0'..=b'5').contains(a) && d.is_ascii_digit(),
            _ => false,
        };
        if !ok {
            return false;
        }
        groups += 1;
    }
    if groups == 0 {
        return false;
    }
    if b.get(i) == Some(&b'.') {
        i += 1;
        while b.get(i).is_some_and(|c| c.is_ascii_digit() || *c == b'_') {
            i += 1;
        }
    }
    i == b.len()
}

/// The go-yaml order of string map keys (`keyList.Less`): numbers inside keys compare by
/// value, and letters sort before other characters.
pub(crate) fn go_key_order(a: &str, b: &str) -> Ordering {
    let ar: Vec<char> = a.chars().collect();
    let br: Vec<char> = b.chars().collect();
    let less = |x: &[char], y: &[char]| -> bool {
        let mut digits = false;
        let mut i = 0;
        while i < x.len() && i < y.len() {
            if x[i] == y[i] {
                digits = x[i].is_ascii_digit();
                i += 1;
                continue;
            }
            let xl = x[i].is_alphabetic();
            let yl = y[i].is_alphabetic();
            if xl && yl {
                return x[i] < y[i];
            }
            if xl || yl {
                return if digits { xl } else { yl };
            }
            let (mut xn, mut yn) = (0i64, 0i64);
            if x[i] == '0' || y[i] == '0' {
                let mut j = i;
                while j > 0 && x[j - 1].is_ascii_digit() {
                    j -= 1;
                    if x[j] != '0' {
                        xn = 1;
                        yn = 1;
                        break;
                    }
                }
            }
            let mut xi = i;
            while xi < x.len() && x[xi].is_ascii_digit() {
                xn = xn
                    .saturating_mul(10)
                    .saturating_add(i64::from(x[xi] as u8 - b'0'));
                xi += 1;
            }
            let mut yi = i;
            while yi < y.len() && y[yi].is_ascii_digit() {
                yn = yn
                    .saturating_mul(10)
                    .saturating_add(i64::from(y[yi] as u8 - b'0'));
                yi += 1;
            }
            if xn != yn {
                return xn < yn;
            }
            if xi != yi {
                return xi < yi;
            }
            return x[i] < y[i];
        }
        x.len() < y.len()
    };
    if less(&ar, &br) {
        Ordering::Less
    } else if less(&br, &ar) {
        Ordering::Greater
    } else {
        Ordering::Equal
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn m(src: &str) -> String {
        marshal(&Node::parse(src).unwrap(), 2)
    }

    #[test]
    fn block_and_flow_layout_follow_go_yaml() {
        assert_eq!(
            m("a: 1\nb:\n- x\n- y: z\n  w: v\nc: {}\nd: []\n"),
            "a: 1\nb:\n  - x\n  - y: z\n    w: v\nc: {}\nd: []\n"
        );
        assert_eq!(
            m("- - a\n  - b\n- [c, {d: e}]\n"),
            "- - a\n  - b\n- [c, {d: e}]\n"
        );
        assert_eq!(m("a: &x {b: c}\nd: *x\n"), "a: &x {b: c}\nd: *x\n");
        assert_eq!(m("s: |\n  one\n  two\n"), "s: |\n  one\n  two\n");
        assert_eq!(m("s: |-\n  one\n"), "s: |-\n  one\n");
        assert_eq!(m("s: [\"a\\nb\"]\n"), "s: [\"a\\nb\"]\n");
        let node = Node::map([("k", Node::seq(vec![Node::map([("a", Node::str("b"))])]))]);
        assert_eq!(marshal(&node, 4), "k:\n    - a: b\n");
    }

    #[test]
    fn scalars_are_quoted_like_go_yaml() {
        let cases = [
            ("plain", "plain"),
            ("true", "\"true\""),
            ("0755", "\"0755\""),
            ("", "\"\""),
            ("a: b", "'a: b'"),
            ("x #y", "'x #y'"),
            ("- x", "'- x'"),
            ("it's: x", "'it''s: x'"),
            (" lead", "' lead'"),
            ("tab\there", "\"tab\\there\""),
            ("emoji 😀", "\"emoji \\U0001F600\""),
            ("café", "café"),
            ("two\nlines", "|-\n  two\n  lines"),
            ("end\n", "|\n  end"),
            (" lead\n", "|2\n   lead"),
            ("yes", "yes"),
            ("80:80", "80:80"),
        ];
        for (value, want) in cases {
            assert_eq!(
                marshal(&Node::str(value), 2),
                format!("{want}\n"),
                "{value:?}"
            );
        }
        assert_eq!(marshal(&Node::plain("true"), 2), "true\n");
    }

    #[test]
    fn marshal_value_writes_like_go_map_marshal() {
        let src = "# dropped\nb: &x {z: 1, y: 'yes', x: 80:80, w: 1:30}\na:\n  <<: *x\n  z: 2\nc: 0x1F\nd: 1.50\ne: 2026-10-13\nf: ~\ng: True\nh: 1e6\ni: 1234567.5\nk10: 1\nk9: 2\n";
        let out = marshal_value(&Node::parse(src).unwrap(), 4).unwrap();
        assert_eq!(
            out,
            "a:\n    w: \"1:30\"\n    x: 80:80\n    \"y\": \"yes\"\n    z: 2\nb:\n    w: \"1:30\"\n    x: 80:80\n    \"y\": \"yes\"\n    z: 1\nc: 31\nd: 1.5\ne: \"2026-10-13\"\nf: null\ng: true\nh: 1e+06\ni: 1.2345675e+06\nk9: 2\nk10: 1\n"
        );
    }

    #[test]
    fn go_float_formats_like_strconv() {
        for (text, want) in [
            ("1.0", "1"),
            ("0.5", "0.5"),
            ("123456.0", "123456"),
            ("1234567.0", "1.234567e+06"),
            ("0.0001", "0.0001"),
            ("0.00001", "1e-05"),
            ("-2.5e-7", "-2.5e-07"),
            ("0.0", "0"),
        ] {
            assert_eq!(go_float(text), want, "{text}");
        }
    }

    #[test]
    fn key_order_compares_numbers_by_value() {
        let mut keys = vec!["b", "a10", "a9", "a", "A", "_x", "1", "01"];
        keys.sort_by(|a, b| go_key_order(a, b));
        // Go: no digits read as 0, and a shorter number sorts first.
        assert_eq!(keys, vec!["_x", "1", "01", "A", "a", "a9", "a10", "b"]);
    }
}
