//! Find external module declarations without treating comments or strings as Rust items.

use std::path::{Path, PathBuf};

#[derive(Debug, PartialEq, Eq)]
enum Token {
    Word(String),
    Literal(String),
    Punct(u8),
}

pub(super) fn children(file: &Path, source: &str) -> Vec<PathBuf> {
    let parent = file.parent().unwrap_or(Path::new(""));
    let stem = file.file_stem().unwrap_or_default();
    let base = if ["lib", "main", "mod"].iter().any(|s| stem == *s) {
        parent.to_path_buf()
    } else {
        parent.join(stem)
    };
    let mut dirs = vec![(base, parent.to_path_buf())];
    let tokens = tokens(source);
    let mut children = Vec::new();
    let mut path = None;
    let mut i = 0;
    while i < tokens.len() {
        let (dir, attr_dir) = dirs.last().unwrap().clone();
        if let [
            Token::Punct(b'#'),
            Token::Punct(b'['),
            Token::Word(key),
            Token::Punct(b'='),
            Token::Literal(value),
            Token::Punct(b']'),
            ..,
        ] = &tokens[i..]
            && key == "path"
        {
            path = Some(attr_dir.join(value));
            i += 6;
            continue;
        }
        if let [
            Token::Word(keyword),
            Token::Word(name),
            Token::Punct(end),
            ..,
        ] = &tokens[i..]
            && keyword == "mod"
            && b";{".contains(end)
        {
            let child = path.take();
            if *end == b';' {
                children.push(child.unwrap_or_else(|| dir.join(format!("{name}.rs"))));
            } else {
                let child = child.unwrap_or_else(|| dir.join(name));
                dirs.push((child.clone(), child));
            }
            i += 3;
            continue;
        }
        match tokens[i] {
            Token::Punct(b'{') => dirs.push((dir, attr_dir)),
            Token::Punct(b'}') if dirs.len() > 1 => {
                dirs.pop();
            }
            _ => {}
        }
        i += 1;
    }
    children
}

fn tokens(source: &str) -> Vec<Token> {
    let bytes = source.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i].is_ascii_whitespace() {
            i += 1;
            continue;
        }
        if bytes[i..].starts_with(b"//") {
            while i < bytes.len() && bytes[i] != b'\n' {
                i += 1;
            }
            continue;
        }
        if bytes[i..].starts_with(b"/*") {
            i += 2;
            let mut depth = 1;
            while i < bytes.len() && depth > 0 {
                if bytes[i..].starts_with(b"/*") {
                    depth += 1;
                    i += 2;
                } else if bytes[i..].starts_with(b"*/") {
                    depth -= 1;
                    i += 2;
                } else {
                    i += 1;
                }
            }
            continue;
        }
        if bytes[i] == b'r' {
            let start = i;
            let mut quote = i + 1;
            while bytes.get(quote) == Some(&b'#') {
                quote += 1;
            }
            if bytes.get(quote) == Some(&b'"') {
                let end = format!("\"{}", "#".repeat(quote - start - 1));
                let body = &source[quote + 1..];
                let len = body.find(&end).unwrap_or(body.len());
                out.push(Token::Literal(body[..len].into()));
                i = (quote + 1 + len + end.len()).min(bytes.len());
                continue;
            }
            if bytes.get(i + 1) == Some(&b'#') {
                i += 2;
            }
        }
        if bytes.get(i) == Some(&b'"') {
            i += 1;
            let mut value = Vec::new();
            while i < bytes.len() && bytes[i] != b'"' {
                if bytes[i] == b'\\' && i + 1 < bytes.len() {
                    i += 1;
                }
                value.push(bytes[i]);
                i += 1;
            }
            out.push(Token::Literal(String::from_utf8_lossy(&value).into_owned()));
            i = (i + 1).min(bytes.len());
            continue;
        }
        // Character literals can contain braces. Lifetimes have no closing quote.
        if bytes.get(i) == Some(&b'\'') {
            let rest = &source[i + 1..];
            let char_len = rest.chars().next().map_or(0, char::len_utf8);
            let end = i + 1 + char_len;
            if bytes.get(end) == Some(&b'\'') {
                i = end + 1;
                continue;
            }
            if bytes.get(i + 1) == Some(&b'\\')
                && let Some(end) = rest[1..].find('\'')
            {
                i += end + 3;
                continue;
            }
        }
        if bytes
            .get(i)
            .is_some_and(|b| b.is_ascii_alphanumeric() || *b == b'_')
        {
            let start = i;
            while i < bytes.len() && (bytes[i].is_ascii_alphanumeric() || bytes[i] == b'_') {
                i += 1;
            }
            out.push(Token::Word(source[start..i].into()));
        } else if i < bytes.len() {
            out.push(Token::Punct(bytes[i]));
            i += 1;
        }
    }
    out
}
