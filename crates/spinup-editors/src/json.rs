//! The JSON editor (Go `codemod.SetJSON` and `codemod.DeleteJSON`).
//!
//! The editor keeps the key order, the indentation, and the final newline of the source.
//! It changes only the bytes of the edited member. New values use the Go `encoding/json`
//! encoding: sorted object keys, and `<`, `>`, `&`, U+2028, and U+2029 as `\u` escapes.
//! A multi-line object gets new members in the indentation of the source.

use std::collections::BTreeMap;

use serde_json::Value;

/// A JSON edit that failed.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("edit JSON: {0}\nTry: check the document and the migration input")]
pub struct Error(pub String);

/// Sets the member at `path` and creates missing objects.
///
/// An empty path replaces the root value. A path that crosses a value that is not an
/// object is an error. An equal value returns `src` unchanged.
pub fn set(src: &str, path: &[&str], value: &Value) -> Result<String, Error> {
    let root = parse(src)?;
    let mut encoded = encode(value);
    let mut current = &root;
    for (i, key) in path.iter().enumerate() {
        let Some(members) = current.members() else {
            return Err(Error(format!("path {:?} crosses a non-object", &path[..i])));
        };
        if let Some(member) = members.iter().find(|m| m.key == *key) {
            current = &member.value;
            continue;
        }
        // Build the missing suffix from the inside out, so its order is deterministic.
        for name in path[i + 1..].iter().rev() {
            encoded = format!("{{{}:{encoded}}}", encode_string(name));
        }
        return Ok(insert(src, current, key, &encoded));
    }
    if current.value == decode_raw(&encoded) {
        return Ok(src.to_string());
    }
    let encoded = indent(
        &encoded,
        line_indent(src, current.start),
        source_indent(src),
        newline(src),
    );
    Ok(splice(src, current.start, current.end, &encoded))
}

/// Removes the member at `path` and keeps the other members in order.
///
/// It returns the document and whether it changed. An absent path returns `src`
/// unchanged. An empty path is an error.
pub fn delete(src: &str, path: &[&str]) -> Result<(String, bool), Error> {
    let root = parse(src)?;
    let Some((last, parents)) = path.split_last() else {
        return Err(Error("cannot delete the document root".to_string()));
    };
    let mut parent = &root;
    for key in parents {
        let Some(member) = parent
            .members()
            .and_then(|m| m.iter().find(|m| m.key == *key))
        else {
            return Ok((src.to_string(), false));
        };
        parent = &member.value;
    }
    let Some(members) = parent.members() else {
        return Ok((src.to_string(), false));
    };
    let Some(index) = members.iter().position(|m| m.key == *last) else {
        return Ok((src.to_string(), false));
    };
    let entry = &members[index];
    let (start, end) = if members.len() == 1 {
        (parent.start + 1, parent.end - 1)
    } else if index < members.len() - 1 {
        (entry.start, members[index + 1].start)
    } else {
        (members[index - 1].value.end, entry.value.end)
    };
    Ok((splice(src, start, end, ""), true))
}

/// One value of the source, with its byte range.
#[derive(Debug)]
struct Node {
    start: usize,
    end: usize,
    /// The decoded value, for comparison. Numbers keep their source text, as Go
    /// `UseNumber` does.
    value: Plain,
    /// The members of an object, in source order.
    members: Option<Vec<Member>>,
}

impl Node {
    fn members(&self) -> Option<&[Member]> {
        self.members.as_deref()
    }
}

#[derive(Debug)]
struct Member {
    key: String,
    start: usize,
    value: Node,
}

/// A decoded JSON value. Objects compare without order, as Go `reflect.DeepEqual` on maps.
#[derive(Debug, Clone, PartialEq)]
enum Plain {
    Null,
    Bool(bool),
    Number(String),
    String(String),
    Array(Vec<Plain>),
    Object(BTreeMap<String, Plain>),
}

fn parse(src: &str) -> Result<Node, Error> {
    let mut parser = Parser {
        src,
        bytes: src.as_bytes(),
        pos: 0,
    };
    let root = parser
        .value()
        .map_err(|e| Error(format!("invalid JSON document: {e}")))?;
    parser.space();
    if parser.pos != src.len() {
        return Err(Error(format!(
            "invalid JSON document: extra data at byte {}",
            parser.pos
        )));
    }
    Ok(root)
}

fn decode_raw(encoded: &str) -> Plain {
    parse(encoded).map_or(Plain::Null, |node| node.value)
}

/// A strict JSON parser that keeps the byte range of each value.
struct Parser<'a> {
    src: &'a str,
    bytes: &'a [u8],
    pos: usize,
}

impl Parser<'_> {
    fn space(&mut self) {
        while matches!(self.bytes.get(self.pos), Some(b' ' | b'\t' | b'\r' | b'\n')) {
            self.pos += 1;
        }
    }

    fn expect(&mut self, byte: u8) -> Result<(), String> {
        if self.bytes.get(self.pos) == Some(&byte) {
            self.pos += 1;
            Ok(())
        } else {
            Err(format!("expected {:?} at byte {}", byte as char, self.pos))
        }
    }

    fn value(&mut self) -> Result<Node, String> {
        self.space();
        let start = self.pos;
        let mut members = None;
        let value = match self.bytes.get(self.pos) {
            Some(b'{') => {
                self.pos += 1;
                let mut list: Vec<Member> = Vec::new();
                let mut object = BTreeMap::new();
                self.space();
                if self.bytes.get(self.pos) == Some(&b'}') {
                    self.pos += 1;
                } else {
                    loop {
                        self.space();
                        let key_start = self.pos;
                        let key = self.string()?;
                        if object.contains_key(&key) {
                            return Err(format!("duplicate object key {key:?}"));
                        }
                        self.space();
                        self.expect(b':')?;
                        let child = self.value()?;
                        object.insert(key.clone(), child.value.clone());
                        list.push(Member {
                            key,
                            start: key_start,
                            value: child,
                        });
                        self.space();
                        if self.bytes.get(self.pos) == Some(&b',') {
                            self.pos += 1;
                            continue;
                        }
                        self.expect(b'}')?;
                        break;
                    }
                }
                members = Some(list);
                Plain::Object(object)
            }
            Some(b'[') => {
                self.pos += 1;
                let mut items = Vec::new();
                self.space();
                if self.bytes.get(self.pos) == Some(&b']') {
                    self.pos += 1;
                } else {
                    loop {
                        items.push(self.value()?.value);
                        self.space();
                        if self.bytes.get(self.pos) == Some(&b',') {
                            self.pos += 1;
                            continue;
                        }
                        self.expect(b']')?;
                        break;
                    }
                }
                Plain::Array(items)
            }
            Some(b'"') => Plain::String(self.string()?),
            Some(b't') if self.src[self.pos..].starts_with("true") => {
                self.pos += 4;
                Plain::Bool(true)
            }
            Some(b'f') if self.src[self.pos..].starts_with("false") => {
                self.pos += 5;
                Plain::Bool(false)
            }
            Some(b'n') if self.src[self.pos..].starts_with("null") => {
                self.pos += 4;
                Plain::Null
            }
            Some(b'-' | b'0'..=b'9') => Plain::Number(self.number()?),
            _ => return Err(format!("unexpected value at byte {}", self.pos)),
        };
        Ok(Node {
            start,
            end: self.pos,
            value,
            members,
        })
    }

    fn digits(&mut self) -> usize {
        let start = self.pos;
        while self.bytes.get(self.pos).is_some_and(u8::is_ascii_digit) {
            self.pos += 1;
        }
        self.pos - start
    }

    fn number(&mut self) -> Result<String, String> {
        let start = self.pos;
        if self.bytes[self.pos] == b'-' {
            self.pos += 1;
        }
        match self.bytes.get(self.pos) {
            Some(b'0') => self.pos += 1,
            Some(b'1'..=b'9') => {
                self.digits();
            }
            _ => return Err(format!("invalid number at byte {start}")),
        }
        if self.bytes.get(self.pos) == Some(&b'.') {
            self.pos += 1;
            if self.digits() == 0 {
                return Err(format!("invalid number at byte {start}"));
            }
        }
        if matches!(self.bytes.get(self.pos), Some(b'e' | b'E')) {
            self.pos += 1;
            if matches!(self.bytes.get(self.pos), Some(b'+' | b'-')) {
                self.pos += 1;
            }
            if self.digits() == 0 {
                return Err(format!("invalid number at byte {start}"));
            }
        }
        Ok(self.src[start..self.pos].to_string())
    }

    fn string(&mut self) -> Result<String, String> {
        let start = self.pos;
        self.expect(b'"')?;
        let mut out = String::new();
        loop {
            let Some(c) = self.src[self.pos..].chars().next() else {
                return Err(format!("unclosed string at byte {start}"));
            };
            self.pos += c.len_utf8();
            match c {
                '"' => return Ok(out),
                '\\' => {
                    let escape = self.bytes.get(self.pos).copied();
                    self.pos += 1;
                    match escape {
                        Some(b'"') => out.push('"'),
                        Some(b'\\') => out.push('\\'),
                        Some(b'/') => out.push('/'),
                        Some(b'b') => out.push('\u{8}'),
                        Some(b'f') => out.push('\u{c}'),
                        Some(b'n') => out.push('\n'),
                        Some(b'r') => out.push('\r'),
                        Some(b't') => out.push('\t'),
                        Some(b'u') => {
                            let high = self.hex4()?;
                            let code = if (0xD800..0xDC00).contains(&high)
                                && self.src[self.pos..].starts_with("\\u")
                            {
                                let save = self.pos;
                                self.pos += 2;
                                let low = self.hex4()?;
                                if (0xDC00..0xE000).contains(&low) {
                                    0x10000 + ((high - 0xD800) << 10) + (low - 0xDC00)
                                } else {
                                    self.pos = save;
                                    0xFFFD
                                }
                            } else {
                                high
                            };
                            out.push(char::from_u32(code).unwrap_or('\u{FFFD}'));
                        }
                        _ => return Err(format!("invalid escape at byte {}", self.pos - 1)),
                    }
                }
                c if (c as u32) < 0x20 => {
                    return Err(format!(
                        "control character in string at byte {}",
                        self.pos - 1
                    ));
                }
                c => out.push(c),
            }
        }
    }

    fn hex4(&mut self) -> Result<u32, String> {
        let hex = self.src.get(self.pos..self.pos + 4).unwrap_or("");
        let code = u32::from_str_radix(hex, 16)
            .ok()
            .filter(|_| hex.len() == 4 && hex.bytes().all(|b| b.is_ascii_hexdigit()))
            .ok_or_else(|| format!("invalid \\u escape at byte {}", self.pos))?;
        self.pos += 4;
        Ok(code)
    }
}

/// Encodes a value compactly, like Go `json.Marshal`.
pub fn encode(value: &Value) -> String {
    let mut out = String::new();
    encode_into(value, &mut out);
    out
}

fn encode_into(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
        Value::Number(n) => out.push_str(&encode_number(n)),
        Value::String(s) => out.push_str(&encode_string(s)),
        Value::Array(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                encode_into(item, out);
            }
            out.push(']');
        }
        Value::Object(map) => {
            // serde_json without `preserve_order` sorts the keys, as Go sorts map keys.
            out.push('{');
            for (i, (key, item)) in map.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                out.push_str(&encode_string(key));
                out.push(':');
                encode_into(item, out);
            }
            out.push('}');
        }
    }
}

/// Formats a number like Go: integers as they are, floats in the shortest form, with an
/// exponent below 1e-6 and from 1e21.
fn encode_number(n: &serde_json::Number) -> String {
    if n.is_i64() || n.is_u64() {
        return n.to_string();
    }
    let Some(x) = n.as_f64() else {
        return n.to_string();
    };
    let abs = x.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        let text = format!("{x:e}");
        match text.split_once('e') {
            Some((mantissa, exponent)) if !exponent.starts_with('-') => {
                format!("{mantissa}e+{exponent}")
            }
            _ => text,
        }
    } else {
        x.to_string()
    }
}

/// Quotes a string like Go `json.Marshal`, with HTML escaping.
fn encode_string(s: &str) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '<' | '>' | '&' => {
                let b = c as u8;
                out.push_str("\\u00");
                out.push(HEX[usize::from(b >> 4)] as char);
                out.push(HEX[usize::from(b & 0xF)] as char);
            }
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c if (c as u32) < 0x20 => {
                let b = c as u8;
                out.push_str("\\u00");
                out.push(HEX[usize::from(b >> 4)] as char);
                out.push(HEX[usize::from(b & 0xF)] as char);
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Inserts a member into an object node.
fn insert(src: &str, node: &Node, key: &str, encoded: &str) -> String {
    let name = encode_string(key);
    let unit = source_indent(src);
    let parent_indent = line_indent(src, node.start);
    let members = node.members().unwrap_or_default();
    let nl = newline(src);
    if src[node.start..node.end].contains('\n') {
        let child_indent = format!("{parent_indent}{unit}");
        let encoded = indent(encoded, &child_indent, unit, nl);
        let entry = format!("{child_indent}{name}: {encoded}");
        if members.is_empty() {
            return splice(
                src,
                node.start + 1,
                node.end - 1,
                &format!("{nl}{entry}{nl}{parent_indent}"),
            );
        }
        let last = members[members.len() - 1].value.end;
        return splice(src, last, last, &format!(",{nl}{entry}"));
    }
    let (separator, comma) = if src[node.start..node.end].contains(": ") {
        (": ", ", ")
    } else {
        (":", ",")
    };
    let entry = format!("{name}{separator}{encoded}");
    if members.is_empty() {
        return splice(src, node.start + 1, node.start + 1, &entry);
    }
    let last = members[members.len() - 1].value.end;
    splice(src, last, last, &format!("{comma}{entry}"))
}

/// Returns the indentation of the line that holds `offset`, up to `offset`.
fn line_indent(src: &str, offset: usize) -> &str {
    let start = src[..offset].rfind('\n').map_or(0, |i| i + 1);
    let rest = &src[start..offset];
    let len = rest.len() - rest.trim_start_matches([' ', '\t']).len();
    &src[start..start + len]
}

/// Returns the indentation unit of the source: the indentation of its first indented key.
fn source_indent(src: &str) -> &str {
    for line in src.split('\n') {
        let trimmed = line.trim_start_matches([' ', '\t']);
        let len = line.len() - trimmed.len();
        if len > 0 && trimmed.starts_with('"') {
            return &line[..len];
        }
    }
    "  "
}

/// Indents compact JSON like Go `json.Indent`. Empty arrays and objects stay compact.
fn indent(encoded: &str, prefix: &str, unit: &str, nl: &str) -> String {
    let mut out = String::with_capacity(encoded.len() * 2);
    let mut depth = 0usize;
    let mut in_string = false;
    let mut escaped = false;
    let mut chars = encoded.chars().peekable();
    let line = |out: &mut String, depth: usize| {
        out.push_str(nl);
        out.push_str(prefix);
        for _ in 0..depth {
            out.push_str(unit);
        }
    };
    while let Some(c) = chars.next() {
        if in_string {
            out.push(c);
            if escaped {
                escaped = false;
            } else if c == '\\' {
                escaped = true;
            } else if c == '"' {
                in_string = false;
            }
            continue;
        }
        match c {
            '"' => {
                in_string = true;
                out.push(c);
            }
            '{' | '[' => {
                out.push(c);
                let close = if c == '{' { '}' } else { ']' };
                if chars.peek() == Some(&close) {
                    out.push(close);
                    chars.next();
                } else {
                    depth += 1;
                    line(&mut out, depth);
                }
            }
            '}' | ']' => {
                depth = depth.saturating_sub(1);
                line(&mut out, depth);
                out.push(c);
            }
            ',' => {
                out.push(c);
                line(&mut out, depth);
            }
            ':' => out.push_str(": "),
            c => out.push(c),
        }
    }
    out
}

fn newline(src: &str) -> &'static str {
    if src.contains("\r\n") { "\r\n" } else { "\n" }
}

fn splice(src: &str, start: usize, end: usize, value: &str) -> String {
    let mut out = String::with_capacity(src.len() - (end - start) + value.len());
    out.push_str(&src[..start]);
    out.push_str(value);
    out.push_str(&src[end..]);
    out
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    const EXAMPLE: &str = include_str!("testdata/example.json");

    fn valid(src: &str) -> bool {
        parse(src).is_ok()
    }

    #[test]
    fn set_and_delete_keep_order_newline_and_idempotence() {
        for src in [
            EXAMPLE,
            "{\"z\":1,\"app\":{\"name\":\"old\",\"keep\":true},\"a\":2}",
            "{\n\t\"z\": 1,\n\t\"app\": {\"name\": \"old\"}\n}\n",
        ] {
            assert_eq!(set(src, &["app", "name"], &json!("old")).unwrap(), src);
            for path in [
                &["app", "name"][..],
                &["app", "extra"],
                &["missing", "child"],
            ] {
                let out = set(src, path, &json!("new")).unwrap();
                assert!(valid(&out), "{out}");
                assert_eq!(
                    set(&out, path, &json!("new")).unwrap(),
                    out,
                    "not idempotent"
                );
                assert_eq!(out.ends_with('\n'), src.ends_with('\n'), "newline changed");
                assert!(out.find("\"z\"") < out.find("\"app\""), "reordered: {out}");
            }
            for path in [&["z"][..], &["app", "name"], &["a"], &["missing"]] {
                let (out, _) = delete(src, path).unwrap();
                assert!(valid(&out), "{out}");
                let (again, changed) = delete(&out, path).unwrap();
                assert!(!changed && again == out, "delete not idempotent");
            }
        }
    }

    #[test]
    fn set_writes_go_layout() {
        let out = set(EXAMPLE, &["app", "name"], &json!({"b": [1, 2], "a": {}})).unwrap();
        assert_eq!(
            out,
            "{\n    \"z\": 1,\n    \"app\": {\n        \"name\": {\n            \"a\": {},\n            \"b\": [\n                1,\n                2\n            ]\n        },\n        \"keep\": true\n    },\n    \"a\": 2\n}\n"
        );
        let out = set(EXAMPLE, &["new", "child"], &json!(true)).unwrap();
        assert!(
            out.ends_with("    \"a\": 2,\n    \"new\": {\n        \"child\": true\n    }\n}\n"),
            "{out}"
        );
        let out = set("{\"a\": 1}", &["b"], &json!("<&>")).unwrap();
        assert_eq!(out, "{\"a\": 1, \"b\": \"\\u003c\\u0026\\u003e\"}");
        let out = set("{\"a\":1}", &["b"], &json!(1.5e-7)).unwrap();
        assert_eq!(out, "{\"a\":1,\"b\":1.5e-7}");
        assert_eq!(set("{\"a\":1.0}", &["a"], &json!(1)).unwrap(), "{\"a\":1}");
        assert_eq!(set("[1]", &[], &json!([1])).unwrap(), "[1]");
        assert_eq!(set("[1]", &[], &json!([2])).unwrap(), "[\n  2\n]");
    }

    #[test]
    fn edges_of_empty_objects_and_errors() {
        for src in ["{}", "{ }", "{\n}", "{\r\n}\r\n"] {
            let out = set(src, &["a", "b"], &json!({"x": true})).unwrap();
            assert!(valid(&out), "{out:?}");
            let (out, changed) = delete(&out, &["a"]).unwrap();
            assert!(changed && valid(&out), "{out:?}");
        }
        assert_eq!(
            set("{\r\n}\r\n", &["a"], &json!(1)).unwrap(),
            "{\r\n  \"a\": 1\r\n}\r\n"
        );
        for src in [
            "[",
            "{\"a\":1} {}",
            "{\"a\":1,\"a\":2}",
            "{\"a\":01}",
            "\"\u{1}\"",
        ] {
            let err = set(src, &["a"], &json!(1)).unwrap_err().to_string();
            assert!(
                err.starts_with("edit JSON: invalid JSON document"),
                "{src}: {err}"
            );
            assert!(err.ends_with("\nTry: check the document and the migration input"));
        }
        let err = set("{\"a\":1}", &["a", "b"], &json!(1)).unwrap_err();
        assert!(err.to_string().contains("crosses a non-object"), "{err}");
        assert!(delete("{}", &[]).is_err());
        assert_eq!(
            delete("{\"a\":1}", &["a", "b"]).unwrap(),
            ("{\"a\":1}".to_string(), false)
        );
    }

    #[test]
    fn strings_and_numbers_encode_like_go() {
        assert_eq!(
            encode(&json!("a\"\\\n\u{1}\u{2028}é")),
            "\"a\\\"\\\\\\n\\u0001\\u2028é\""
        );
        assert_eq!(encode(&json!(1e21)), "1e+21");
        assert_eq!(encode(&json!(123456789.0)), "123456789");
        assert_eq!(encode(&json!(-0.5)), "-0.5");
        assert_eq!(encode(&json!(u64::MAX)), "18446744073709551615");
        let compact = encode(&json!({"b": 1, "a": [true, null, {}]}));
        assert_eq!(compact, "{\"a\":[true,null,{}],\"b\":1}");
        assert_eq!(
            indent(&compact, "", "  ", "\n"),
            "{\n  \"a\": [\n    true,\n    null,\n    {}\n  ],\n  \"b\": 1\n}"
        );
    }

    #[test]
    fn decoding_reads_escapes_and_surrogates() {
        let node = parse("{\"k\\u00e9\": \"\\ud83d\\ude00 \\/ \\t\"}").unwrap();
        assert_eq!(node.members().unwrap()[0].key, "ké");
        assert_eq!(
            node.value,
            Plain::Object([("ké".to_string(), Plain::String("😀 / \t".to_string()))].into())
        );
    }
}
