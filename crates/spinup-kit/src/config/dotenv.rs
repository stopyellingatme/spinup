//! The `.env` parser of the Go kit (`kit/config/dotenv.go`). The kit does not use
//! `dotenvy`: the rules must equal Go.
//!
//! - Empty lines and lines that start with `#` are skipped. A line can start with
//!   `export `.
//! - A key has letters, digits, and `_`, and does not start with a digit.
//! - An unquoted value ends at a `#` after a space or a tab. The value is trimmed.
//! - A single-quoted value is literal. A double-quoted value takes the Go escapes (`\n`,
//!   `\t`, `\"`, `\\`, `\xNN`, `\uNNNN`, ...). After the closing quote, only a comment can
//!   follow.

use std::collections::BTreeMap;
use std::io;
use std::path::Path;

/// Reads `.env`, or `../.env` when `.env` is absent, through `read_file`. No file gives an
/// empty map. The error names the file, such as `.env: line 2: expected KEY=value`.
pub fn load(
    read_file: &dyn Fn(&Path) -> io::Result<Vec<u8>>,
) -> Result<BTreeMap<String, String>, String> {
    for path in [".env", "../.env"] {
        match read_file(Path::new(path)) {
            Ok(data) => {
                return parse(&String::from_utf8_lossy(&data)).map_err(|e| format!("{path}: {e}"));
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => continue,
            Err(_) => return Err(format!("{path}: cannot read dotenv file")),
        }
    }
    Ok(BTreeMap::new())
}

/// Parses the text of a `.env` file. The error has one line for each bad line, such as
/// `line 3 (A): unterminated quoted value`. It never holds a value.
pub fn parse(text: &str) -> Result<BTreeMap<String, String>, String> {
    let mut values = BTreeMap::new();
    let mut problems = Vec::new();
    for (index, line) in text.split('\n').enumerate() {
        let number = index + 1;
        let mut text = line.trim();
        if text.is_empty() || text.starts_with('#') {
            continue;
        }
        if text.starts_with("export ") || text.starts_with("export\t") {
            text = text["export".len()..].trim();
        }
        let Some((key, raw)) = text.split_once('=') else {
            problems.push(format!("line {number}: expected KEY=value"));
            continue;
        };
        let key = key.trim();
        if !valid_key(key) {
            problems.push(format!("line {number}: expected KEY=value"));
            continue;
        }
        match value(raw.trim()) {
            Ok(v) => {
                values.insert(key.to_string(), v);
            }
            Err(e) => problems.push(format!("line {number} ({key}): {e}")),
        }
    }
    if problems.is_empty() {
        Ok(values)
    } else {
        Err(problems.join("\n"))
    }
}

fn valid_key(key: &str) -> bool {
    !key.is_empty()
        && key
            .bytes()
            .enumerate()
            .all(|(i, c)| c == b'_' || c.is_ascii_alphabetic() || (i > 0 && c.is_ascii_digit()))
}

fn value(raw: &str) -> Result<String, &'static str> {
    let bytes = raw.as_bytes();
    let Some(&quote) = bytes.first() else {
        return Ok(String::new());
    };
    if quote != b'\'' && quote != b'"' {
        for (i, c) in bytes.iter().enumerate() {
            if *c == b'#' && (i == 0 || bytes[i - 1] == b' ' || bytes[i - 1] == b'\t') {
                return Ok(raw[..i].trim().to_string());
            }
        }
        return Ok(raw.to_string());
    }
    let mut end = None;
    let mut i = 1;
    while i < bytes.len() {
        if quote == b'"' && bytes[i] == b'\\' {
            i += 2;
            continue;
        }
        if bytes[i] == quote {
            end = Some(i);
            break;
        }
        i += 1;
    }
    let Some(end) = end else {
        return Err("unterminated quoted value");
    };
    let tail = raw[end + 1..].trim();
    if !tail.is_empty() && !tail.starts_with('#') {
        return Err("unexpected text after quoted value");
    }
    if quote == b'\'' {
        return Ok(raw[1..end].to_string());
    }
    unquote(&raw[1..end]).ok_or("invalid quoted value")
}

/// The escapes of Go `strconv.Unquote` for a double-quoted text without its quotes.
fn unquote(body: &str) -> Option<String> {
    let mut out = String::with_capacity(body.len());
    let mut chars = body.chars();
    while let Some(c) = chars.next() {
        if c == '"' {
            return None;
        }
        if c != '\\' {
            out.push(c);
            continue;
        }
        let escape = chars.next()?;
        match escape {
            'a' => out.push('\u{7}'),
            'b' => out.push('\u{8}'),
            'f' => out.push('\u{c}'),
            'n' => out.push('\n'),
            'r' => out.push('\r'),
            't' => out.push('\t'),
            'v' => out.push('\u{b}'),
            '\\' => out.push('\\'),
            '"' => out.push('"'),
            'x' | 'u' | 'U' => {
                let width = match escape {
                    'x' => 2,
                    'u' => 4,
                    _ => 8,
                };
                let digits: String = chars.by_ref().take(width).collect();
                if digits.len() != width {
                    return None;
                }
                let code = u32::from_str_radix(&digits, 16).ok()?;
                if escape == 'x' {
                    // Go writes the byte. A byte above 0x7f is not valid UTF-8 alone.
                    out.push(char::from_u32(code).filter(|_| code < 0x80)?);
                } else {
                    out.push(char::from_u32(code)?);
                }
            }
            '0'..='7' => {
                let rest: String = chars.by_ref().take(2).collect();
                if rest.len() != 2 {
                    return None;
                }
                let code = u32::from_str_radix(&format!("{escape}{rest}"), 8).ok()?;
                out.push(char::from_u32(code).filter(|_| code < 0x80)?);
            }
            _ => return None,
        }
    }
    Some(out)
}
