//! The JSON form of a [`RunResult`]. It equals the Go `json.Encoder` output: compact, the Go
//! field names, HTML characters escaped, and one newline at the end.
//!
//! The crate has no JSON dependency, and the result has only strings and lists, so this
//! module writes the JSON itself.

use std::fmt::Write as _;
use std::io::Write;

use anyhow::anyhow;

use super::{RunResult, class_name, step_name};

/// Writes `result` as one JSON line.
pub(super) fn print(out: &mut (dyn Write + Send), result: &RunResult) -> anyhow::Result<()> {
    let mut line = encode(result);
    line.push('\n');
    out.write_all(line.as_bytes())
        .map_err(|e| anyhow!("write generator JSON: {e}\nTry: check the output destination"))
}

/// Returns the compact JSON object of `result`. The `commands` field is absent when it is
/// empty (Go `omitempty`).
pub(super) fn encode(result: &RunResult) -> String {
    let mut s = String::from("{\"files\":[");
    for (i, file) in result.files.iter().enumerate() {
        comma(&mut s, i);
        s.push_str("{\"path\":");
        string(&mut s, &file.path);
        s.push_str(",\"action\":");
        string(&mut s, file.action.as_str());
        s.push_str(",\"class\":");
        string(&mut s, class_name(file.class));
        s.push('}');
    }
    s.push_str("],\"post_steps\":");
    list(
        &mut s,
        result.post_steps.iter().map(|step| step_name(*step)),
    );
    s.push_str(",\"next\":");
    list(&mut s, result.next.iter().map(String::as_str));
    if !result.commands.is_empty() {
        s.push_str(",\"commands\":[");
        for (i, c) in result.commands.iter().enumerate() {
            comma(&mut s, i);
            s.push_str("{\"Dir\":");
            string(&mut s, &c.dir);
            s.push_str(",\"Argv\":");
            list(&mut s, c.argv.iter().map(String::as_str));
            s.push('}');
        }
        s.push(']');
    }
    s.push('}');
    s
}

fn comma(s: &mut String, index: usize) {
    if index > 0 {
        s.push(',');
    }
}

fn list<'a>(s: &mut String, items: impl Iterator<Item = &'a str>) {
    s.push('[');
    for (i, item) in items.enumerate() {
        comma(s, i);
        string(s, item);
    }
    s.push(']');
}

/// Writes a JSON string with the escapes of Go `encoding/json` (`SetEscapeHTML(true)`).
fn string(s: &mut String, value: &str) {
    s.push('"');
    for c in value.chars() {
        match c {
            '"' => s.push_str("\\\""),
            '\\' => s.push_str("\\\\"),
            '\n' => s.push_str("\\n"),
            '\r' => s.push_str("\\r"),
            '\t' => s.push_str("\\t"),
            '\u{8}' => s.push_str("\\b"),
            '\u{c}' => s.push_str("\\f"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                let _ = write!(s, "\\u{:04x}", u32::from(c));
            }
            c if u32::from(c) < 0x20 => {
                let _ = write!(s, "\\u{:04x}", u32::from(c));
            }
            c => s.push(c),
        }
    }
    s.push('"');
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strings_use_go_escapes() {
        let mut s = String::new();
        string(&mut s, "a\"b\\c\n\r\t\u{8}\u{c}\u{1}<>&\u{2028}\u{2029}é");
        assert_eq!(
            s,
            r#""a\"b\\c\n\r\t\b\f\u0001\u003c\u003e\u0026\u2028\u2029é""#
        );
    }
}
