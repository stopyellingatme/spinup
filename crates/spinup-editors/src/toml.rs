//! The TOML editor (Go `codemod.SetTOML` and `codemod.DeleteTOML`).
//!
//! The editor changes only the bytes of the edited entry. Comments, blank lines, and the
//! order of all other entries stay as they are. `toml_edit` checks that the document is
//! valid. A small scanner finds the byte ranges of the table headers and the key-value
//! expressions in source order, as the Go editor does with the `go-toml` parser.
//!
//! New values use the `go-toml` encoding: a string is a literal string (`'v'`) when it can
//! be one, else a basic string. A new table gets one blank line before its header
//! (ADR 0018 §6), but never two, and none at the start of a file.

use std::fmt;

use toml_edit::{Document, Item, Value as TomlItemValue};

/// A TOML edit that failed.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("edit TOML: {0}\nTry: check the document and the migration input")]
pub struct Error(pub String);

/// A value that [`set`] writes. These are the types that Go `SetTOML` supports.
#[derive(Debug, Clone, PartialEq)]
pub enum Value {
    /// A string.
    String(String),
    /// A boolean.
    Bool(bool),
    /// A 64-bit integer.
    Int(i64),
    /// A 64-bit float.
    Float(f64),
    /// An array of strings.
    Strings(Vec<String>),
}

impl From<&str> for Value {
    fn from(value: &str) -> Self {
        Self::String(value.to_string())
    }
}

impl From<String> for Value {
    fn from(value: String) -> Self {
        Self::String(value)
    }
}

impl From<bool> for Value {
    fn from(value: bool) -> Self {
        Self::Bool(value)
    }
}

impl From<i64> for Value {
    fn from(value: i64) -> Self {
        Self::Int(value)
    }
}

impl From<f64> for Value {
    fn from(value: f64) -> Self {
        Self::Float(value)
    }
}

impl From<Vec<String>> for Value {
    fn from(value: Vec<String>) -> Self {
        Self::Strings(value)
    }
}

impl From<&[&str]> for Value {
    fn from(value: &[&str]) -> Self {
        Self::Strings(value.iter().map(|s| s.to_string()).collect())
    }
}

impl fmt::Display for Value {
    /// Writes the value in the `go-toml` encoding.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::String(s) => f.write_str(&encode_string(s)),
            Self::Bool(b) => write!(f, "{b}"),
            Self::Int(i) => write!(f, "{i}"),
            Self::Float(x) => f.write_str(&encode_float(*x)),
            Self::Strings(items) => {
                let items: Vec<String> = items.iter().map(|s| encode_string(s)).collect();
                write!(f, "[{}]", items.join(", "))
            }
        }
    }
}

/// Sets `key` in `table` and returns the new document.
///
/// An existing value is replaced in place. Comments inside a replaced array become
/// standalone comments before its key. A missing key is appended to its table. A missing
/// table is appended to the file. An equal value returns `src` unchanged.
pub fn set(src: &str, table: &[&str], key: &str, value: impl Into<Value>) -> Result<String, Error> {
    let value = value.into();
    let encoded = value.to_string();
    let (doc, location) = locate(src, table, key)?;
    let mut path: Vec<&str> = table.to_vec();
    path.push(key);
    if lookup(&doc, &path).is_some_and(|existing| equal(existing, &value)) {
        return Ok(src.to_string());
    }
    let nl = newline(src);
    let out = if let Some(found) = &location.found {
        if found.comments.is_empty() {
            splice(src, found.value_start, found.value_end, &encoded)
        } else {
            let mut replacement = found.comments.join(nl);
            replacement.push_str(nl);
            replacement.push_str(&src[found.key_start..found.value_start]);
            replacement.push_str(&encoded);
            splice(src, found.key_start, found.value_end, &replacement)
        }
    } else {
        let mut insertion = format!("{} = {encoded}{nl}", key_text(key));
        let mut offset = location.table_end;
        if !location.table_found {
            offset = src.len();
            let names: Vec<String> = table.iter().map(|name| key_text(name)).collect();
            insertion = format!("[{}]{nl}{insertion}", names.join("."));
        }
        if offset > 0 && src.as_bytes()[offset - 1] != b'\n' {
            insertion.insert_str(0, nl);
        }
        // A new table header gets one blank line before it, but never two,
        // and none at the start of a file (ADR 0018 §6).
        if !location.table_found
            && offset > 0
            && !src.trim().is_empty()
            && !ends_with_blank_line(src)
        {
            insertion.insert_str(0, nl);
        }
        splice(src, offset, offset, &insertion)
    };
    locate(&out, table, key)?;
    Ok(out)
}

/// Removes `key` from `table` and keeps its comments.
///
/// It returns the document and whether it changed. An absent key returns `src` unchanged.
pub fn delete(src: &str, table: &[&str], key: &str) -> Result<(String, bool), Error> {
    let (_, location) = locate(src, table, key)?;
    let Some(found) = location.found else {
        return Ok((src.to_string(), false));
    };
    let nl = newline(src);
    let mut replacement = String::new();
    if !found.comments.is_empty() {
        replacement = found.comments.join(nl);
        replacement.push_str(nl);
    }
    let (mut start, mut end) = (found.key_start, found.value_end);
    // Remove an empty assignment line. An inline comment stays on its own line.
    let line_end = src[end..].find('\n').map_or(src.len(), |i| end + i + 1);
    if src[end..line_end].trim().is_empty() {
        end = line_end;
        start = line_start(src, start);
    }
    Ok((splice(src, start, end, &replacement), true))
}

/// The byte ranges of one key-value expression.
#[derive(Debug)]
struct Found {
    key_start: usize,
    value_start: usize,
    value_end: usize,
    comments: Vec<String>,
}

/// Where a key is, or where a new key goes.
#[derive(Debug)]
struct Location {
    found: Option<Found>,
    table_found: bool,
    table_end: usize,
}

fn locate(src: &str, table: &[&str], key: &str) -> Result<(Document<String>, Location), Error> {
    let doc = Document::parse(src.to_string())
        .map_err(|e| Error(e.to_string().trim_end().to_string()))?;
    let expressions = Scanner::new(src).expressions()?;
    let mut location = Location {
        found: None,
        table_found: table.is_empty(),
        table_end: src.len(),
    };
    let mut current: Vec<String> = Vec::new();
    let mut current_array = false;
    let mut active = table.is_empty();
    let mut target: Vec<&str> = table.to_vec();
    target.push(key);
    for expression in expressions {
        match expression {
            Expression::Table {
                keys,
                array,
                line_start,
            } => {
                if active && location.table_end == src.len() {
                    location.table_end = line_start;
                }
                active = false;
                current = keys;
                current_array = array;
                if current == table {
                    if current_array {
                        return Err(Error(format!("table {table:?} is an array of tables")));
                    }
                    location.table_found = true;
                    active = true;
                    location.table_end = src.len();
                }
            }
            Expression::KeyValue {
                keys,
                key_start,
                value_start,
                value_end,
                comments,
            } => {
                if current_array {
                    continue;
                }
                if active && current == table {
                    location.table_end = line_end(src, value_end);
                }
                let full: Vec<&str> = current.iter().chain(&keys).map(String::as_str).collect();
                if full != target {
                    continue;
                }
                location.found = Some(Found {
                    key_start,
                    value_start,
                    value_end,
                    comments,
                });
            }
        }
    }
    Ok((doc, location))
}

fn lookup<'a>(doc: &'a Document<String>, path: &[&str]) -> Option<&'a Item> {
    let (first, rest) = path.split_first()?;
    let mut item = doc.as_table().get(first)?;
    for key in rest {
        item = item.get(key)?;
    }
    Some(item)
}

fn equal(existing: &Item, value: &Value) -> bool {
    let Some(existing) = existing.as_value() else {
        return false;
    };
    match (existing, value) {
        (TomlItemValue::String(s), Value::String(v)) => s.value() == v,
        (TomlItemValue::Boolean(b), Value::Bool(v)) => b.value() == v,
        (TomlItemValue::Integer(i), Value::Int(v)) => i.value() == v,
        (TomlItemValue::Float(x), Value::Float(v)) => x.value() == v,
        (TomlItemValue::Array(items), Value::Strings(v)) => {
            items.len() == v.len()
                && items
                    .iter()
                    .zip(v)
                    .all(|(item, want)| item.as_str() == Some(want.as_str()))
        }
        _ => false,
    }
}

/// Encodes a string like `go-toml`: a literal string when it can be one, else a basic string.
fn encode_string(s: &str) -> String {
    if !s.chars().any(|c| c == '\'' || c == '\u{7f}' || c < ' ') {
        return format!("'{s}'");
    }
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c < ' ' || c == '\u{7f}' => out.push_str(&format!("\\u{:04X}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Encodes a float like `go-toml`: the shortest decimal form, with `.0` for whole numbers.
fn encode_float(x: f64) -> String {
    if x.is_nan() {
        return "nan".to_string();
    }
    if x.is_infinite() {
        return if x > 0.0 { "inf" } else { "-inf" }.to_string();
    }
    let mut out = x.to_string();
    if !out.contains(['.', 'e', 'E']) {
        out.push_str(".0");
    }
    out
}

/// Writes a bare key when TOML allows one, else a quoted key.
fn key_text(key: &str) -> String {
    if !key.is_empty()
        && key
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
    {
        return key.to_string();
    }
    let mut out = String::from("\"");
    for c in key.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c < ' ' => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

fn newline(src: &str) -> &'static str {
    if src.contains("\r\n") { "\r\n" } else { "\n" }
}

fn line_start(src: &str, offset: usize) -> usize {
    src[..offset].rfind('\n').map_or(0, |i| i + 1)
}

fn line_end(src: &str, offset: usize) -> usize {
    src[offset..]
        .find('\n')
        .map_or(src.len(), |i| offset + i + 1)
}

fn splice(src: &str, start: usize, end: usize, value: &str) -> String {
    let mut out = String::with_capacity(src.len() - (end - start) + value.len());
    out.push_str(&src[..start]);
    out.push_str(value);
    out.push_str(&src[end..]);
    out
}

fn ends_with_blank_line(src: &str) -> bool {
    src.ends_with("\n\n") || src.ends_with("\n\r\n")
}

/// One top-level TOML expression, in source order.
#[derive(Debug)]
enum Expression {
    /// A `[table]` or `[[array]]` header.
    Table {
        keys: Vec<String>,
        array: bool,
        line_start: usize,
    },
    /// A `key = value` line.
    KeyValue {
        keys: Vec<String>,
        key_start: usize,
        value_start: usize,
        value_end: usize,
        comments: Vec<String>,
    },
}

/// A scanner for the expression ranges of a document that `toml_edit` accepted.
struct Scanner<'a> {
    src: &'a str,
    bytes: &'a [u8],
    pos: usize,
}

impl<'a> Scanner<'a> {
    fn new(src: &'a str) -> Self {
        Self {
            src,
            bytes: src.as_bytes(),
            pos: 0,
        }
    }

    fn fail<T>(&self, what: &str) -> Result<T, Error> {
        Err(Error(format!("{what} at byte {}", self.pos)))
    }

    fn peek(&self) -> Option<u8> {
        self.bytes.get(self.pos).copied()
    }

    fn starts_with(&self, text: &str) -> bool {
        self.src[self.pos..].starts_with(text)
    }

    fn expressions(mut self) -> Result<Vec<Expression>, Error> {
        let mut out = Vec::new();
        loop {
            self.skip_blank(true, &mut Vec::new());
            let Some(byte) = self.peek() else {
                return Ok(out);
            };
            if byte == b'[' {
                let line_start = line_start(self.src, self.pos);
                let array = self.starts_with("[[");
                self.pos += if array { 2 } else { 1 };
                self.skip_spaces();
                let keys = self.keys()?;
                self.skip_spaces();
                let close = if array { "]]" } else { "]" };
                if !self.starts_with(close) {
                    return self.fail("unclosed table header");
                }
                self.pos += close.len();
                out.push(Expression::Table {
                    keys,
                    array,
                    line_start,
                });
            } else {
                let key_start = self.pos;
                let keys = self.keys()?;
                self.skip_spaces();
                if self.peek() != Some(b'=') {
                    return self.fail("missing '='");
                }
                self.pos += 1;
                self.skip_spaces();
                let value_start = self.pos;
                let mut comments = Vec::new();
                self.value(&mut comments)?;
                out.push(Expression::KeyValue {
                    keys,
                    key_start,
                    value_start,
                    value_end: self.pos,
                    comments,
                });
            }
        }
    }

    fn skip_spaces(&mut self) {
        while matches!(self.peek(), Some(b' ' | b'\t')) {
            self.pos += 1;
        }
    }

    /// Skips spaces, comments, and, when `lines` is set, line breaks. It collects comments.
    fn skip_blank(&mut self, lines: bool, comments: &mut Vec<String>) {
        loop {
            match self.peek() {
                Some(b' ' | b'\t') => self.pos += 1,
                Some(b'\r' | b'\n') if lines => self.pos += 1,
                Some(b'#') => {
                    let end = self.src[self.pos..]
                        .find('\n')
                        .map_or(self.src.len(), |i| self.pos + i);
                    comments.push(self.src[self.pos..end].trim_end_matches('\r').to_string());
                    self.pos = end;
                }
                _ => return,
            }
        }
    }

    fn keys(&mut self) -> Result<Vec<String>, Error> {
        let mut keys = vec![self.key()?];
        loop {
            let save = self.pos;
            self.skip_spaces();
            if self.peek() == Some(b'.') {
                self.pos += 1;
                self.skip_spaces();
                keys.push(self.key()?);
            } else {
                self.pos = save;
                return Ok(keys);
            }
        }
    }

    fn key(&mut self) -> Result<String, Error> {
        match self.peek() {
            Some(b'"') => self.basic_string(),
            Some(b'\'') => {
                let start = self.pos + 1;
                let Some(len) = self.src[start..].find('\'') else {
                    return self.fail("unclosed key");
                };
                self.pos = start + len + 1;
                Ok(self.src[start..start + len].to_string())
            }
            _ => {
                let start = self.pos;
                while self
                    .peek()
                    .is_some_and(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
                {
                    self.pos += 1;
                }
                if start == self.pos {
                    return self.fail("missing key");
                }
                Ok(self.src[start..self.pos].to_string())
            }
        }
    }

    /// Reads a basic string and returns its decoded value.
    fn basic_string(&mut self) -> Result<String, Error> {
        self.pos += 1;
        let mut out = String::new();
        loop {
            let Some(c) = self.src[self.pos..].chars().next() else {
                return self.fail("unclosed string");
            };
            self.pos += c.len_utf8();
            match c {
                '"' => return Ok(out),
                '\\' => {
                    let Some(e) = self.src[self.pos..].chars().next() else {
                        return self.fail("unclosed string");
                    };
                    self.pos += e.len_utf8();
                    match e {
                        'b' => out.push('\u{8}'),
                        't' => out.push('\t'),
                        'n' => out.push('\n'),
                        'f' => out.push('\u{c}'),
                        'r' => out.push('\r'),
                        'e' => out.push('\u{1b}'),
                        '"' => out.push('"'),
                        '\\' => out.push('\\'),
                        'x' | 'u' | 'U' => {
                            let len = match e {
                                'x' => 2,
                                'u' => 4,
                                _ => 8,
                            };
                            let hex = self.src.get(self.pos..self.pos + len).unwrap_or("");
                            let code = u32::from_str_radix(hex, 16).ok().and_then(char::from_u32);
                            let Some(code) = code else {
                                return self.fail("invalid escape");
                            };
                            out.push(code);
                            self.pos += len;
                        }
                        _ => return self.fail("invalid escape"),
                    }
                }
                c => out.push(c),
            }
        }
    }

    fn value(&mut self, comments: &mut Vec<String>) -> Result<(), Error> {
        if self.starts_with("\"\"\"") || self.starts_with("'''") {
            let quote = &self.src[self.pos..self.pos + 3];
            let escapes = quote == "\"\"\"";
            self.pos += 3;
            loop {
                if self.pos >= self.bytes.len() {
                    return self.fail("unclosed multi-line string");
                }
                if escapes && self.peek() == Some(b'\\') {
                    self.pos += 2;
                    continue;
                }
                if self.starts_with(quote) {
                    self.pos += 3;
                    // Up to two more quotes belong to the content.
                    let mut extra = 0;
                    while extra < 2 && self.starts_with(&quote[..1]) {
                        self.pos += 1;
                        extra += 1;
                    }
                    return Ok(());
                }
                self.pos += self.src[self.pos..]
                    .chars()
                    .next()
                    .map_or(1, char::len_utf8);
            }
        }
        match self.peek() {
            Some(b'"') => self.basic_string().map(|_| ()),
            Some(b'\'') => self.key().map(|_| ()),
            Some(b'[') => {
                self.pos += 1;
                loop {
                    self.skip_blank(true, comments);
                    match self.peek() {
                        Some(b']') => {
                            self.pos += 1;
                            return Ok(());
                        }
                        Some(b',') => self.pos += 1,
                        Some(_) => self.value(comments)?,
                        None => return self.fail("unclosed array"),
                    }
                }
            }
            Some(b'{') => {
                self.pos += 1;
                loop {
                    self.skip_blank(true, comments);
                    match self.peek() {
                        Some(b'}') => {
                            self.pos += 1;
                            return Ok(());
                        }
                        Some(b',') => self.pos += 1,
                        Some(_) => {
                            self.keys()?;
                            self.skip_spaces();
                            if self.peek() != Some(b'=') {
                                return self.fail("missing '='");
                            }
                            self.pos += 1;
                            self.skip_spaces();
                            self.value(comments)?;
                        }
                        None => return self.fail("unclosed inline table"),
                    }
                }
            }
            Some(_) => {
                let start = self.pos;
                while self.peek().is_some_and(|b| {
                    !matches!(b, b' ' | b'\t' | b'\r' | b'\n' | b',' | b']' | b'}' | b'#')
                }) {
                    self.pos += 1;
                }
                // A date-time may use one space between the date and the time.
                let token = &self.src[start..self.pos];
                let rest = &self.bytes[self.pos..];
                if token.len() == 10
                    && token.as_bytes()[4] == b'-'
                    && rest.len() > 3
                    && rest[0] == b' '
                    && rest[1].is_ascii_digit()
                    && rest[2].is_ascii_digit()
                    && rest[3] == b':'
                {
                    self.pos += 1;
                    while self.peek().is_some_and(|b| {
                        !matches!(b, b' ' | b'\t' | b'\r' | b'\n' | b',' | b']' | b'}' | b'#')
                    }) {
                        self.pos += 1;
                    }
                }
                if start == self.pos {
                    return self.fail("missing value");
                }
                Ok(())
            }
            None => self.fail("missing value"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const EXAMPLE: &str = include_str!("testdata/example.toml");

    fn parses(src: &str) {
        src.parse::<toml_edit::DocumentMut>()
            .unwrap_or_else(|e| panic!("invalid TOML: {e}\n{src}"));
    }

    #[test]
    fn set_keeps_comments_and_is_idempotent() {
        let cases: Vec<(&str, &[&str], &str, Value)> = vec![
            ("string", &["app"], "name", "new".into()),
            ("array", &["app"], "items", (&["b", "c"][..]).into()),
            ("insert", &["app"], "new", 2i64.into()),
            ("new table", &["a.b", "space name"], "key", false.into()),
            ("root", &[], "root", false.into()),
            ("float", &[], "float", 1.25.into()),
        ];
        for (name, table, key, value) in cases {
            let out =
                set(EXAMPLE, table, key, value.clone()).unwrap_or_else(|e| panic!("{name}: {e}"));
            for comment in [
                "# document",
                "# root",
                "# table",
                "# name",
                "# array",
                "# element",
                "# items",
                "# keep",
            ] {
                assert!(out.contains(comment), "{name}: lost {comment}:\n{out}");
            }
            let again = set(&out, table, key, value).unwrap();
            assert_eq!(again, out, "{name}: not idempotent");
            parses(&out);
        }
    }

    #[test]
    fn set_replaces_values_in_place() {
        let out = set(EXAMPLE, &["app"], "name", "new").unwrap();
        assert_eq!(out, EXAMPLE.replace("name  = 'old'", "name  = 'new'"));
        let out = set(EXAMPLE, &["app"], "items", &["b", "c"][..]).unwrap();
        assert!(
            out.contains("[app] # table\nname  = 'old' # name\n# array\n# element\nitems = ['b', 'c'] # items\n[other]"),
            "{out}"
        );
        let out = set(EXAMPLE, &[], "root", false).unwrap();
        assert!(out.contains("root = false # root\n"), "{out}");
        let out = set(EXAMPLE, &[], "float", 1.25).unwrap();
        assert!(
            out.contains("root = true # root\nfloat = 1.25\n[app]"),
            "{out}"
        );
        let out = set(EXAMPLE, &["a.b", "space name"], "key", false).unwrap();
        assert!(
            out.ends_with("keep = 1 # keep\n\n[\"a.b\".\"space name\"]\nkey = false\n"),
            "{out}"
        );
    }

    #[test]
    fn set_returns_equal_values_unchanged() {
        assert_eq!(set(EXAMPLE, &["app"], "name", "old").unwrap(), EXAMPLE);
        assert_eq!(
            set(EXAMPLE, &["app"], "items", &["a"][..]).unwrap(),
            EXAMPLE
        );
        assert_eq!(set(EXAMPLE, &["other"], "keep", 1i64).unwrap(), EXAMPLE);
    }

    #[test]
    fn delete_keeps_comments_and_is_idempotent() {
        for key in ["name", "items", "missing"] {
            let (out, changed) = delete(EXAMPLE, &["app"], key).unwrap();
            assert_eq!(changed, key != "missing", "{key}");
            if key == "missing" {
                assert_eq!(out, EXAMPLE);
            }
            for comment in ["# name", "# array", "# element", "# items"] {
                assert!(out.contains(comment), "{key}: lost {comment}:\n{out}");
            }
            let (again, changed) = delete(&out, &["app"], key).unwrap();
            assert!(!changed && again == out, "{key}: not idempotent");
            parses(&out);
        }
        let (out, _) = delete(EXAMPLE, &["other"], "keep").unwrap();
        assert!(
            out.ends_with("[other]\n# keep\n") || out.ends_with("[other]\n # keep\n"),
            "{out:?}"
        );
        let (out, _) = delete("a = 1\nb = 2\n", &[], "a").unwrap();
        assert_eq!(out, "b = 2\n");
    }

    #[test]
    fn errors_name_the_problem_and_end_with_try() {
        for src in ["a = [", "a=1\na=2"] {
            let err = set(src, &[], "a", "x").unwrap_err().to_string();
            assert!(err.starts_with("edit TOML: "), "{err}");
            assert!(
                err.ends_with("\nTry: check the document and the migration input"),
                "{err}"
            );
            assert!(delete(src, &[], "a").is_err());
        }
        let err = set("[[t]]\na = 1\n", &["t"], "a", 2i64).unwrap_err();
        assert!(err.to_string().contains("array of tables"), "{err}");
    }

    #[test]
    fn insert_keeps_the_comment_of_the_next_table() {
        let src = "[x]\nb = 2\n\n# about y\n[y]\nc = 3\n";
        let out = set(src, &["x"], "n", "v").unwrap();
        assert_eq!(out, "[x]\nb = 2\nn = 'v'\n\n# about y\n[y]\nc = 3\n");
    }

    /// ADR 0018 §6: a new table gets one blank line before it, but never two.
    #[test]
    fn new_table_blank_line_rule() {
        for (name, src, want) in [
            ("empty file", "", "[a]\nk = 1\n"),
            ("after a key", "x = 1\n", "x = 1\n\n[a]\nk = 1\n"),
            ("blank line exists", "x = 1\n\n", "x = 1\n\n[a]\nk = 1\n"),
            ("no final newline", "x = 1", "x = 1\n\n[a]\nk = 1\n"),
            (
                "after a table",
                "[b]\ny = 2\n",
                "[b]\ny = 2\n\n[a]\nk = 1\n",
            ),
            ("crlf", "x = 1\r\n", "x = 1\r\n\r\n[a]\r\nk = 1\r\n"),
        ] {
            let got = set(src, &["a"], "k", 1i64).unwrap();
            assert_eq!(got, want, "{name}");
        }
    }

    #[test]
    fn new_table_uses_bare_keys() {
        let got = set(
            "name = \"app\"\n",
            &["env", "production", "digital ocean"],
            "droplet_id",
            7i64,
        )
        .unwrap();
        assert_eq!(
            got,
            "name = \"app\"\n\n[env.production.\"digital ocean\"]\ndroplet_id = 7\n"
        );
    }

    #[test]
    fn insert_into_an_empty_table_goes_before_the_next_header() {
        // Go parity: the new key goes to the start of the next header line.
        let src = "[a]\n\n[b]\nx = 1\n";
        assert_eq!(
            set(src, &["a"], "k", true).unwrap(),
            "[a]\n\nk = true\n[b]\nx = 1\n"
        );
        let src = "[a.sub]\nx = 1\n[a]\ny = 2\n";
        assert_eq!(
            set(src, &["a"], "k", "v").unwrap(),
            "[a.sub]\nx = 1\n[a]\ny = 2\nk = 'v'\n"
        );
    }

    #[test]
    fn dotted_keys_and_multi_line_values_are_located() {
        let src = "app.name = 'old' # c\n[t]\ns = \"\"\"\nline \"\" x\"\"\"\"\"\nd = 1979-05-27 07:32:00Z\nn = 1\n";
        let out = set(src, &["app"], "name", "new").unwrap();
        assert!(out.starts_with("app.name = 'new' # c\n"), "{out}");
        let out = set(src, &["t"], "n", 2i64).unwrap();
        assert!(out.ends_with("d = 1979-05-27 07:32:00Z\nn = 2\n"), "{out}");
        let out = set(src, &["t"], "new", "x").unwrap();
        assert!(out.ends_with("n = 1\nnew = 'x'\n"), "{out}");
        let (out, changed) = delete(src, &["t"], "s").unwrap();
        assert!(changed);
        assert_eq!(
            out,
            "app.name = 'old' # c\n[t]\nd = 1979-05-27 07:32:00Z\nn = 1\n"
        );
    }

    #[test]
    fn values_use_the_go_toml_encoding() {
        assert_eq!(Value::from("plain").to_string(), "'plain'");
        assert_eq!(Value::from("it's").to_string(), "\"it's\"");
        assert_eq!(
            Value::from("a\tb\n\u{1}\"\\").to_string(),
            "\"a\\tb\\n\\u0001\\\"\\\\\""
        );
        assert_eq!(Value::from(2.0).to_string(), "2.0");
        assert_eq!(Value::from(1.25).to_string(), "1.25");
        assert_eq!(Value::from(1e21).to_string(), "1000000000000000000000.0");
        assert_eq!(Value::from(f64::NEG_INFINITY).to_string(), "-inf");
        assert_eq!(Value::from(-3i64).to_string(), "-3");
        assert_eq!(Value::from(vec![]).to_string(), "[]");
        assert_eq!(key_text("a.b"), "\"a.b\"");
        assert_eq!(key_text("a-b_1"), "a-b_1");
    }
}
