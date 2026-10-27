//! Scan literal axum registrations without loading application dependencies.

use super::Route;
use anyhow::{Context, bail};

struct Token {
    text: String,
    literal: bool,
    line: usize,
}

pub(super) fn routes(source: &str) -> anyhow::Result<Vec<Route>> {
    let tokens = tokens(source)?;
    let mut result = Vec::new();
    for (index, token) in tokens.iter().enumerate() {
        if token.text != "." || token.literal {
            continue;
        }
        let Some(call) = tokens.get(index + 1..index + 6) else {
            continue;
        };
        if call[0].text != "route" || call[1].text != "(" || !call[2].literal || call[3].text != ","
        {
            continue;
        }
        // Inspect only top-level calls in the method router. Handler bodies and
        // arguments can contain unrelated calls with the same method names.
        let mut depth = 0usize;
        for (offset, item) in tokens[index + 5..].iter().enumerate() {
            if item.literal {
                continue;
            }
            if item.text == ")" && depth == 0 {
                break;
            }
            if depth == 0
                && tokens
                    .get(index + 6 + offset)
                    .is_some_and(|next| next.text == "(")
                && let Some(method) = method(&item.text)
            {
                result.push(Route {
                    method: method.into(),
                    path: call[2].text.clone(),
                    source: format!("routes.rs:{}", token.line),
                });
            }
            match item.text.as_str() {
                "(" | "[" | "{" => depth += 1,
                ")" | "]" | "}" => depth = depth.saturating_sub(1),
                _ => {}
            }
        }
    }
    Ok(result)
}

fn method(name: &str) -> Option<&'static str> {
    Some(match name {
        "get" => "GET",
        "post" => "POST",
        "put" => "PUT",
        "delete" => "DELETE",
        "head" => "HEAD",
        "options" => "OPTIONS",
        "patch" => "PATCH",
        "trace" => "TRACE",
        "connect" => "CONNECT",
        "any" => "*",
        _ => return None,
    })
}

fn tokens(source: &str) -> anyhow::Result<Vec<Token>> {
    let bytes = source.as_bytes();
    let mut index = 0;
    let mut line = 1;
    let mut result = Vec::new();
    while index < bytes.len() {
        let start = index;
        let start_line = line;
        if bytes[index].is_ascii_whitespace() {
            index += 1;
        } else if source[index..].starts_with("//") {
            index += source[index..].find('\n').unwrap_or(bytes.len() - index);
        } else if source[index..].starts_with("/*") {
            index += 2;
            let mut depth = 1;
            while index < bytes.len() && depth > 0 {
                if bytes[index..].starts_with(b"/*") {
                    depth += 1;
                    index += 2;
                } else if bytes[index..].starts_with(b"*/") {
                    depth -= 1;
                    index += 2;
                } else {
                    index += 1;
                }
            }
            if depth != 0 {
                bail!("unclosed comment at line {start_line}");
            }
        } else if bytes[index] == b'"' {
            index += 1;
            let content = index;
            while index < bytes.len() && bytes[index] != b'"' {
                if bytes[index] == b'\\' {
                    index += 1;
                }
                index += 1;
            }
            if index >= bytes.len() {
                bail!("unclosed string at line {start_line}");
            }
            let text = unescape(&source[content..index])
                .with_context(|| format!("string at line {start_line}"))?;
            index += 1;
            result.push(Token {
                text,
                literal: true,
                line: start_line,
            });
        } else if let Some((content, end)) = raw_string(source, index)? {
            result.push(Token {
                text: source[content..end.0].into(),
                literal: true,
                line: start_line,
            });
            index = end.1;
        } else if bytes[index] == b'\'' && char_end(source, index).is_some() {
            // A character can contain a quote or a parenthesis. Lifetimes have no
            // closing quote and remain ordinary tokens.
            index = char_end(source, index).expect("checked character");
            result.push(Token {
                text: String::new(),
                literal: false,
                line: start_line,
            });
        } else {
            let ch = source[index..].chars().next().expect("character exists");
            index += ch.len_utf8();
            if ch.is_alphanumeric() || ch == '_' {
                while index < bytes.len() {
                    let ch = source[index..].chars().next().expect("character exists");
                    if !ch.is_alphanumeric() && ch != '_' {
                        break;
                    }
                    index += ch.len_utf8();
                }
            }
            result.push(Token {
                text: source[start..index].into(),
                literal: false,
                line: start_line,
            });
        }
        line += bytes[start..index]
            .iter()
            .filter(|&&byte| byte == b'\n')
            .count();
    }
    Ok(result)
}

// Return the content start, content end, and token end for a raw string.
fn raw_string(source: &str, start: usize) -> anyhow::Result<Option<(usize, (usize, usize))>> {
    let bytes = source.as_bytes();
    if bytes[start] != b'r' {
        return Ok(None);
    }
    let mut quote = start + 1;
    while bytes.get(quote) == Some(&b'#') {
        quote += 1;
    }
    if bytes.get(quote) != Some(&b'"') {
        return Ok(None);
    }
    let delimiter = format!("\"{}", "#".repeat(quote - start - 1));
    let content = quote + 1;
    let end = source[content..]
        .find(&delimiter)
        .context("unclosed raw string")?
        + content;
    Ok(Some((content, (end, end + delimiter.len()))))
}

fn char_end(source: &str, start: usize) -> Option<usize> {
    let mut chars = source[start + 1..].char_indices();
    let (_, ch) = chars.next()?;
    if ch == '\\' {
        for (offset, ch) in chars {
            if ch == '\'' {
                return Some(start + offset + 2);
            }
            if ch == '\n' {
                return None;
            }
        }
        None
    } else {
        let (offset, ch) = chars.next()?;
        (ch == '\'').then_some(start + offset + 2)
    }
}

fn unescape(source: &str) -> anyhow::Result<String> {
    let mut chars = source.chars().peekable();
    let mut result = String::new();
    while let Some(ch) = chars.next() {
        if ch != '\\' {
            result.push(ch);
            continue;
        }
        let escaped = chars.next().context("incomplete string escape")?;
        result.push(match escaped {
            'n' => '\n',
            'r' => '\r',
            't' => '\t',
            '0' => '\0',
            '\\' => '\\',
            '"' => '"',
            '\'' => '\'',
            'x' => {
                let digits: String = chars.by_ref().take(2).collect();
                let value = u8::from_str_radix(&digits, 16).context("invalid ASCII escape")?;
                if value > 127 || digits.len() != 2 {
                    bail!("invalid ASCII escape");
                }
                char::from(value)
            }
            'u' => {
                if chars.next() != Some('{') {
                    bail!("invalid Unicode escape");
                }
                let mut digits = String::new();
                loop {
                    match chars.next().context("unclosed Unicode escape")? {
                        '}' => break,
                        '_' => {}
                        digit => digits.push(digit),
                    }
                }
                let value = u32::from_str_radix(&digits, 16).context("invalid Unicode escape")?;
                char::from_u32(value).context("invalid Unicode character")?
            }
            '\n' => {
                while chars.peek().is_some_and(|ch| ch.is_ascii_whitespace()) {
                    chars.next();
                }
                continue;
            }
            _ => bail!("invalid string escape \\{escaped}"),
        });
    }
    Ok(result)
}
