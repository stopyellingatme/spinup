//! Go-compatible helpers for converted minijinja templates.
//! `unicode.json` preserves the Go Unicode tables from the R0.2 oracle.
use minijinja::value::{Rest, Value, ValueKind};
use minijinja::{Environment, Error, ErrorKind};
use serde::Deserialize;
use spinup_stack::stack::Stack;
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
    DATA.get_or_init(|| serde_json::from_str(include_str!("unicode.json")).unwrap())
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
    if value.is_undefined() || key.is_undefined() {
        return Err(invalid("index received an undefined key"));
    }
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

pub(crate) fn go_display(value: &Value) -> Result<String, Error> {
    if value.is_undefined() {
        return Err(invalid("display received an undefined key"));
    }
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

fn join(separator: &str, values: Value) -> Result<String, Error> {
    if values.kind() != ValueKind::Seq {
        return Err(invalid("join requires a string list"));
    }
    let strings: Result<Vec<_>, _> = values
        .try_iter()?
        .map(|v| {
            v.as_str()
                .map(str::to_owned)
                .ok_or_else(|| invalid("join requires a string list"))
        })
        .collect();
    Ok(strings?.join(separator))
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

/// Quotes arbitrary Go string bytes, including invalid UTF-8.
pub fn quote_bytes(bytes: &[u8]) -> String {
    use std::fmt::Write;
    let mut result = String::from("\"");
    for chunk in bytes.utf8_chunks() {
        let valid = quote(chunk.valid());
        result.push_str(&valid[1..valid.len() - 1]);
        for byte in chunk.invalid() {
            write!(result, "\\x{byte:02x}").unwrap();
        }
    }
    result.push('"');
    result
}

pub(crate) fn register(env: &mut Environment<'static>, stack: Option<&Stack>) {
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
    env.add_function("join", join);
    env.add_function("indent", indent);
    env.add_function("page_indent", |s: &str, prefix: &str| {
        s.replace('\n', &("\n".to_owned() + prefix))
    });
    env.add_function("has", has);
    env.add_function("go_len", |v: Value| {
        if v.kind() == ValueKind::String {
            return Ok(v.as_str().unwrap().len());
        }
        v.len().ok_or_else(|| invalid("len requires a collection"))
    });
    let stack = stack.cloned().map(Arc::new);
    for name in ["image", "npm", "gomod", "uses"] {
        let stack = stack.clone();
        env.add_function(name, move |key: &str| -> Result<String, Error> {
            let stack = stack
                .as_ref()
                .ok_or_else(|| invalid(&format!("{name} {key:?}: stack versions are missing")))?;
            match name {
                "image" => stack.image(key).map(|image| image.reference()),
                "npm" => stack.npm_version(key).map(str::to_owned),
                "gomod" => stack.go_module(key).map(str::to_owned),
                "uses" => stack.action(key).map(|action| action.uses(key)),
                _ => unreachable!(),
            }
            .map_err(|e| invalid(&e.to_string()))
        });
    }
}

#[cfg(test)]
mod tests {
    use base64::{Engine, engine::general_purpose::STANDARD};
    use serde::Deserialize;
    use spinup_stack::stack::Stack;

    #[derive(Deserialize)]
    struct QuoteCase {
        input: String,
        expected: String,
    }

    #[test]
    fn oracle_quotes() {
        let cases: Vec<QuoteCase> =
            serde_json::from_str(include_str!("../../../testdata/oracle/quotes.json")).unwrap();
        for case in cases {
            assert_eq!(super::quote(&case.input), case.expected);
        }
    }

    #[test]
    fn oracle_quotes_bytes() {
        let cases: Vec<QuoteCase> =
            serde_json::from_str(include_str!("../../../testdata/oracle/quotes-r1.json")).unwrap();
        for case in cases {
            assert_eq!(
                super::quote_bytes(&STANDARD.decode(case.input).unwrap()),
                case.expected
            );
        }
    }

    #[test]
    fn template_helpers() {
        use minijinja::value::Serde;
        use serde_json::json;
        let mut stack = Stack::default();
        stack.images.insert(
            "postgres".into(),
            spinup_stack::stack::Image {
                image: "postgres".into(),
                tag: "18".into(),
                digest: "sha256:abc".into(),
            },
        );
        stack.npm.insert("svelte".into(), "5.0".into());
        stack
            .go_modules
            .insert("example.com/module".into(), "v1.0.0".into());
        stack.actions.insert(
            "owner/repo".into(),
            spinup_stack::stack::Action {
                sha: "abc".into(),
                tag: "v4".into(),
            },
        );
        let env = crate::env::environment(Some(&stack));
        for (source, expected) in [
            ("[[ join(',', ['jobs', 'mail']) ]]", "jobs,mail"),
            ("[[ go_join(',', ['jobs', 'mail']) ]]", "jobs,mail"),
            ("[[ indent(2, 'a\\nb\\n') ]]", "  a\n  b\n  "),
            ("[[ indent(2, '') ]]", ""),
            ("[[ default('fallback', '') ]]", "fallback"),
            ("[[ default('fallback', false) ]]", "fallback"),
            ("[[ default('fallback', 0) ]]", "fallback"),
            ("[[ default('fallback', []) ]]", "fallback"),
            ("[[ default('fallback', {}) ]]", "fallback"),
            ("[[ default('fallback', none) ]]", "fallback"),
            ("[[ default('fallback', 'value') ]]", "value"),
            ("[[ image('postgres') ]]", "postgres:18@sha256:abc"),
            ("[[ npm('svelte') ]]", "5.0"),
            ("[[ gomod('example.com/module') ]]", "v1.0.0"),
            ("[[ uses('owner/repo') ]]", "owner/repo@abc # v4"),
            ("[[ has('jobs', ['jobs', 1]) ]]", "true"),
            ("[[ has('auth', ['jobs']) ]]", "false"),
            ("[[ has('auth', none) ]]", "false"),
            ("[[ get({}, 'absent') is none ]]", "true"),
            ("[[ get(['first', 'second'], 1) ]]", "second"),
            ("[[ go_len('é') ]]", "2"),
            (
                "[[ go_printf('%.2s %% %q %v', 'é界x', 'x', ['a', 'b']) ]]",
                "é界 % \"x\" [a b]",
            ),
            (
                "[% for k, v in go_items({'z': 2, 'a': 1}) %][[ k ]]=[[ v ]];[% endfor %]",
                "a=1;z=2;",
            ),
        ] {
            assert_eq!(
                env.render_str(source, Serde(json!({}))).unwrap(),
                expected,
                "{source}"
            );
        }
        for source in [
            "[[ image('missing') ]]",
            "[[ npm('missing') ]]",
            "[[ gomod('missing') ]]",
            "[[ uses('missing') ]]",
            "[[ get([1], 2) ]]",
            "[[ get([1], -1) ]]",
            "[[ has('x', 'text') ]]",
            "[[ join(',', [1]) ]]",
            "[[ indent(-1, 'x') ]]",
            "[[ go_printf('%d', 1) ]]",
            "[[ go_printf('%s') ]]",
            "[[ go_printf('%s', 1) ]]",
            "[[ go_printf('%s', 'x', 'y') ]]",
        ] {
            assert!(
                env.render_str(source, Serde(json!({}))).is_err(),
                "{source}"
            );
        }
        let env = crate::env::environment(None);
        for helper in ["image", "npm", "gomod", "uses"] {
            let source = format!("[[ {helper}('missing') ]]");
            assert!(
                env.render_str(&source, Serde(json!({})))
                    .unwrap_err()
                    .to_string()
                    .contains("stack versions are missing")
            );
        }
    }

    #[test]
    fn oracle_words() {
        let cases: Vec<serde_json::Value> =
            serde_json::from_str(include_str!("../../../testdata/oracle/case-words.json")).unwrap();
        let env = crate::env::environment(None);
        for case in cases {
            for name in ["lower", "upper", "snake", "kebab", "camel", "pascal"] {
                let template = format!("[[ {name}(input) ]]");
                assert_eq!(
                    env.render_str(
                        &template,
                        minijinja::value::Serde(serde_json::json!({"input": case["input"]}))
                    )
                    .unwrap(),
                    case[name].as_str().unwrap(),
                    "{name}: {}",
                    case["input"]
                );
            }
        }
    }
}
