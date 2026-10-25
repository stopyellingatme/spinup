//! The case and quote helpers of the R0.2 renderer (`spikes-rs/r0-02-minijinja`),
//! copied as they are. Go owns the Unicode tables (`fixtures/unicode.json`), so
//! `snake`, `pascal`, and `camel` match `internal/render/functions.go`.
use serde::Deserialize;
use std::collections::BTreeMap;
use std::sync::OnceLock;

#[derive(Deserialize)]
#[serde(rename_all = "PascalCase")]
struct Unicode {
    letter: Vec<[u32; 2]>,
    digit: Vec<[u32; 2]>,
    upper: Vec<[u32; 2]>,
    lower: Vec<[u32; 2]>,
    print: Vec<[u32; 2]>,
    to_lower: BTreeMap<u32, u32>,
    to_upper: BTreeMap<u32, u32>,
}

fn unicode() -> &'static Unicode {
    static DATA: OnceLock<Unicode> = OnceLock::new();
    DATA.get_or_init(|| serde_json::from_str(include_str!("../fixtures/unicode.json")).unwrap())
}

fn in_ranges(c: char, ranges: &[[u32; 2]]) -> bool {
    let c = c as u32;
    let i = ranges.partition_point(|r| r[1] < c);
    ranges.get(i).is_some_and(|r| r[0] <= c)
}

fn mapped(c: char, map: &BTreeMap<u32, u32>) -> char {
    map.get(&(c as u32))
        .and_then(|&v| char::from_u32(v))
        .unwrap_or(c)
}

pub fn lower(s: &str) -> String {
    s.chars().map(|c| mapped(c, &unicode().to_lower)).collect()
}

pub fn upper(s: &str) -> String {
    s.chars().map(|c| mapped(c, &unicode().to_upper)).collect()
}

/// Match internal/render/functions.go, including acronym and Unicode boundaries.
pub fn words(s: &str) -> Vec<String> {
    let u = unicode();
    let chars: Vec<_> = s.chars().collect();
    let mut parts = Vec::new();
    let mut start = 0;
    for (i, &c) in chars.iter().enumerate() {
        if !in_ranges(c, &u.letter) && !in_ranges(c, &u.digit) {
            if start < i {
                parts.push(lower(&chars[start..i].iter().collect::<String>()));
            }
            start = i + 1;
            continue;
        }
        if i <= start {
            continue;
        }
        let prev = chars[i - 1];
        let boundary = in_ranges(c, &u.upper)
            && (in_ranges(prev, &u.lower)
                || in_ranges(prev, &u.digit)
                || in_ranges(prev, &u.upper)
                    && chars.get(i + 1).is_some_and(|&v| in_ranges(v, &u.lower)));
        if boundary {
            parts.push(lower(&chars[start..i].iter().collect::<String>()));
            start = i;
        }
    }
    if start < chars.len() {
        parts.push(lower(&chars[start..].iter().collect::<String>()));
    }
    parts
}

pub fn title(s: &str, pascal: bool) -> String {
    words(s)
        .into_iter()
        .enumerate()
        .map(|(i, part)| {
            if i == 0 && !pascal {
                part
            } else {
                let mut chars = part.chars();
                let first = mapped(chars.next().unwrap(), &unicode().to_upper);
                format!("{first}{}", chars.as_str())
            }
        })
        .collect()
}

/// Match strconv.Quote for valid UTF-8 strings. JSON quoting has different rules.
pub fn quote(s: &str) -> String {
    use std::fmt::Write;
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if in_ranges(c, &unicode().print) => out.push(c),
            c if c < ' ' || c == '\u{7f}' => write!(out, "\\x{:02x}", c as u32).unwrap(),
            c if (c as u32) <= 0xffff => write!(out, "\\u{:04x}", c as u32).unwrap(),
            c => write!(out, "\\U{:08x}", c as u32).unwrap(),
        }
    }
    out.push('"');
    out
}

/// Returns the snake case form: `TodoItem` becomes `todo_item`.
pub fn snake(s: &str) -> String {
    words(s).join("_")
}

/// Returns the kebab case form: `TodoItem` becomes `todo-item`.
pub fn kebab(s: &str) -> String {
    words(s).join("-")
}

/// Returns the Pascal case form: `todo_item` becomes `TodoItem`.
pub fn pascal(s: &str) -> String {
    title(s, true)
}

/// Returns the camel case form: `todo_item` becomes `todoItem`.
pub fn camel(s: &str) -> String {
    title(s, false)
}
