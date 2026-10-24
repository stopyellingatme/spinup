//! R0.2 renderer. Go owns the oracle inputs and Unicode tables.
use minijinja::value::{Rest, Value, ValueKind};
use minijinja::{AutoEscape, Environment, Error, ErrorKind, UndefinedBehavior};
use serde::Deserialize;
use std::collections::BTreeMap;
use std::sync::{Arc, OnceLock};

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

fn lower(s: &str) -> String {
    s.chars().map(|c| mapped(c, &unicode().to_lower)).collect()
}

fn upper(s: &str) -> String {
    s.chars().map(|c| mapped(c, &unicode().to_upper)).collect()
}

/// Match internal/render/functions.go, including acronym and Unicode boundaries.
fn words(s: &str) -> Vec<String> {
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

fn title(s: &str, pascal: bool) -> String {
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

fn invalid(message: &str) -> Error {
    Error::new(ErrorKind::InvalidOperation, message.to_owned())
}

fn get(value: Value, key: Value) -> Result<Value, Error> {
    if value.kind() == ValueKind::Map {
        let item = value.get_item(&key)?;
        return Ok(if item.is_undefined() {
            Value::from(())
        } else {
            item
        });
    }
    if value.kind() == ValueKind::Seq {
        let i = key
            .as_usize()
            .ok_or_else(|| invalid("index must be nonnegative"))?;
        if i >= value.len().unwrap_or(0) {
            return Err(invalid("index is out of range"));
        }
        return value.get_item_by_index(i);
    }
    Err(invalid("index requires a map or sequence"))
}

fn default_value(fallback: Value, value: Value) -> Result<Value, Error> {
    if value.is_undefined() || fallback.is_undefined() {
        return Err(invalid("default received an undefined key"));
    }
    Ok(if !value.is_true() { fallback } else { value })
}

fn go_items(value: Value) -> Result<Vec<Value>, Error> {
    if value.is_none() {
        return Ok(Vec::new());
    }
    match value.kind() {
        ValueKind::Seq => value
            .try_iter()?
            .enumerate()
            .map(|(i, v)| Ok(Value::from(vec![Value::from(i), v])))
            .collect(),
        ValueKind::Map => {
            let mut keys: Vec<_> = value.try_iter()?.collect();
            keys.sort();
            keys.into_iter()
                .map(|k| {
                    let v = value.get_item(&k)?;
                    Ok(Value::from(vec![k, v]))
                })
                .collect()
        }
        _ => Err(invalid("range requires a map or sequence")),
    }
}

fn go_display(value: &Value) -> Result<String, Error> {
    if value.is_none() {
        return Ok("<nil>".into());
    }
    match value.kind() {
        ValueKind::Bool => Ok(if value.is_true() { "true" } else { "false" }.to_owned()),
        ValueKind::Seq => {
            let parts: Result<Vec<_>, _> = value.try_iter()?.map(|v| go_display(&v)).collect();
            Ok(format!("[{}]", parts?.join(" ")))
        }
        ValueKind::Map => {
            let mut keys: Vec<_> = value.try_iter()?.collect();
            keys.sort();
            let parts: Result<Vec<_>, Error> = keys
                .into_iter()
                .map(|k| {
                    Ok(format!(
                        "{}:{}",
                        go_display(&k)?,
                        go_display(&value.get_item(&k)?)?
                    ))
                })
                .collect();
            Ok(format!("map[{}]", parts?.join(" ")))
        }
        _ => Ok(value.to_string()),
    }
}

fn printf(format: &str, args: Rest<Value>) -> Result<String, Error> {
    let mut result = String::new();
    let mut chars = format.chars();
    let mut args = args.iter();
    while let Some(c) = chars.next() {
        if c != '%' {
            result.push(c);
            continue;
        }
        let mut verb = chars
            .next()
            .ok_or_else(|| invalid("incomplete printf format"))?;
        let mut precision = None;
        if verb == '.' {
            let mut digits = String::new();
            loop {
                verb = chars
                    .next()
                    .ok_or_else(|| invalid("incomplete printf precision"))?;
                if !verb.is_ascii_digit() {
                    break;
                }
                digits.push(verb);
            }
            precision = Some(
                digits
                    .parse::<usize>()
                    .map_err(|_| invalid("invalid printf precision"))?,
            );
        }
        if verb == '%' {
            result.push('%');
            continue;
        }
        let value = args
            .next()
            .ok_or_else(|| invalid("missing printf argument"))?;
        match verb {
            's' => {
                let text = value
                    .as_str()
                    .ok_or_else(|| invalid("%s requires a string"))?;
                if let Some(n) = precision {
                    result.extend(text.chars().take(n));
                } else {
                    result.push_str(text);
                }
            }
            'q' => result.push_str(&quote(
                value
                    .as_str()
                    .ok_or_else(|| invalid("%q requires a string"))?,
            )),
            'v' => result.push_str(&go_display(value)?),
            _ => return Err(invalid("unsupported printf verb")),
        }
    }
    if args.next().is_some() {
        return Err(invalid("extra printf arguments"));
    }
    Ok(result)
}

fn join(separator: &str, values: Vec<String>) -> String {
    values.join(separator)
}

fn indent(width: i64, s: &str) -> Result<String, Error> {
    let width = usize::try_from(width).map_err(|_| invalid("indent width must not be negative"))?;
    if s.is_empty() {
        return Ok(String::new());
    }
    let prefix = " ".repeat(width);
    Ok(prefix.clone() + &s.replace('\n', &("\n".to_owned() + &prefix)))
}

fn has(needle: &str, values: Value) -> Result<bool, Error> {
    if values.is_none() {
        return Ok(false);
    }
    if values.kind() != ValueKind::Seq {
        return Err(invalid("has requires a list"));
    }
    Ok(values.try_iter()?.any(|v| v.as_str() == Some(needle)))
}

fn lookup(stack: &serde_json::Value, section: &str, key: &str) -> Result<String, Error> {
    stack[section][key]
        .as_str()
        .map(str::to_owned)
        .ok_or_else(|| invalid(&format!("missing {section} key {key:?}")))
}

/// Configure explicit delimiters, strict keys, no HTML escaping, and Go helpers.
pub fn environment() -> Environment<'static> {
    let mut env = Environment::new();
    env.set_syntax(
        minijinja::syntax::SyntaxConfig::builder()
            .variable_delimiters("[[", "]]")
            .block_delimiters("[%", "%]")
            .comment_delimiters("[#", "#]")
            .keep_trailing_newline(true)
            .build()
            .unwrap(),
    );
    env.set_undefined_behavior(UndefinedBehavior::Strict);
    env.set_auto_escape_callback(|_| AutoEscape::None);
    env.set_formatter(|out, state, value| {
        if value.is_undefined() {
            return minijinja::escape_formatter(out, state, value);
        }
        let text = if value.is_none() {
            "<no value>".to_owned()
        } else {
            go_display(value)?
        };
        out.write_str(&text).map_err(Error::from)
    });
    env.add_function("quote", quote);
    env.add_filter("quote", quote);
    env.add_function("lower", lower);
    env.add_filter("lower", lower);
    env.add_function("upper", upper);
    env.add_filter("upper", upper);
    env.add_function("snake", |s: &str| words(s).join("_"));
    env.add_filter("snake", |s: &str| words(s).join("_"));
    env.add_function("kebab", |s: &str| words(s).join("-"));
    env.add_filter("kebab", |s: &str| words(s).join("-"));
    env.add_function("camel", |s: &str| title(s, false));
    env.add_filter("camel", |s: &str| title(s, false));
    env.add_function("pascal", |s: &str| title(s, true));
    env.add_filter("pascal", |s: &str| title(s, true));
    env.add_function("get", get);
    env.add_filter("get", get);
    env.add_function("go_default", default_value);
    env.add_function("default", default_value);
    env.add_filter("default", |value: Value, fallback: Value| {
        default_value(fallback, value)
    });
    env.add_function("go_items", go_items);
    env.add_function("go_printf", printf);
    env.add_function("go_join", join);
    env.add_function("indent", indent);
    env.add_function("page_indent", |s: &str, prefix: &str| {
        s.replace('\n', &("\n".to_owned() + prefix))
    });
    env.add_function("has", has);
    env.add_function("go_len", |v: Value| {
        v.len().ok_or_else(|| invalid("len requires a collection"))
    });
    let stack: Arc<serde_json::Value> =
        Arc::new(serde_json::from_str(include_str!("../fixtures/stack.json")).unwrap());
    for (name, section) in [("npm", "NPM"), ("gomod", "GoModules")] {
        let stack = stack.clone();
        env.add_function(name, move |key: &str| lookup(&stack, section, key));
    }
    let images = stack.clone();
    env.add_function("image", move |key: &str| {
        let image = &images["Images"][key];
        let field = |name| {
            image[name]
                .as_str()
                .ok_or_else(|| invalid("missing image field"))
        };
        Ok::<_, Error>(format!(
            "{}:{}@{}",
            field("Image")?,
            field("Tag")?,
            field("Digest")?
        ))
    });
    env.add_function("uses", move |key: &str| {
        let action = &stack["Actions"][key];
        let sha = action["SHA"]
            .as_str()
            .ok_or_else(|| invalid("missing action SHA"))?;
        let tag = action["Tag"]
            .as_str()
            .ok_or_else(|| invalid("missing action tag"))?;
        Ok::<_, Error>(format!("{key}@{sha} # {tag}"))
    });
    env
}

/// Match manifest.Normalize. Binary data and empty files remain unchanged.
pub fn normalize(data: &[u8]) -> Vec<u8> {
    if data.is_empty() || data[..data.len().min(8000)].contains(&0) {
        return data.to_vec();
    }
    let mut out = Vec::with_capacity(data.len() + 1);
    let mut bytes = data.iter().copied().peekable();
    while let Some(byte) = bytes.next() {
        if byte == b'\r' {
            if bytes.peek() == Some(&b'\n') {
                bytes.next();
            }
            out.push(b'\n');
        } else {
            out.push(byte);
        }
    }
    while out.last() == Some(&b'\n') {
        out.pop();
    }
    out.push(b'\n');
    out
}
