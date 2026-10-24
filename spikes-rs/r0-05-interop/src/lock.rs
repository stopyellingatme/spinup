use anyhow::{Result, bail, ensure};
use serde_json::value::RawValue;
use std::collections::BTreeMap;

// RawValue is necessary: serde_json::Number normalizes exponent spelling even
// with arbitrary_precision. Go json.Number preserves the original token.
#[derive(Clone, Debug)]
enum Json {
    Object(BTreeMap<String, Json>),
    Array(Vec<Json>),
    String(String),
    Token(String),
}

impl Json {
    fn parse(raw: &str) -> Result<Self> {
        let valid: Box<RawValue> = serde_json::from_str(raw)?;
        let raw = valid.get();
        Ok(match raw.as_bytes()[0] {
            b'{' => Self::Object(
                serde_json::from_str::<BTreeMap<String, Box<RawValue>>>(raw)?
                    .into_iter()
                    .map(|(k, v)| Ok((k, Self::parse(v.get())?)))
                    .collect::<Result<_>>()?,
            ),
            b'[' => Self::Array(
                serde_json::from_str::<Vec<Box<RawValue>>>(raw)?
                    .into_iter()
                    .map(|v| Self::parse(v.get()))
                    .collect::<Result<_>>()?,
            ),
            b'"' => Self::String(serde_json::from_str(raw)?),
            _ => Self::Token(raw.into()),
        })
    }
    fn object(&mut self) -> Result<&mut BTreeMap<String, Self>> {
        match self {
            Self::Object(v) => Ok(v),
            _ => bail!("expected object"),
        }
    }
    fn string(&self) -> &str {
        match self {
            Self::String(v) => v,
            _ => "",
        }
    }
    fn null(&self) -> bool {
        matches!(self, Self::Token(v) if v == "null")
    }
    fn write(&self, out: &mut String, depth: usize) {
        match self {
            Self::String(v) => out.push_str(&escaped(v)),
            Self::Token(v) => out.push_str(v),
            Self::Object(items) => {
                out.push('{');
                for (i, (key, value)) in items.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    indent(out, depth + 1);
                    out.push_str(&escaped(key));
                    out.push_str(": ");
                    value.write(out, depth + 1);
                }
                if !items.is_empty() {
                    indent(out, depth);
                }
                out.push('}');
            }
            Self::Array(items) => {
                out.push('[');
                for (i, value) in items.iter().enumerate() {
                    if i > 0 {
                        out.push(',');
                    }
                    indent(out, depth + 1);
                    value.write(out, depth + 1);
                }
                if !items.is_empty() {
                    indent(out, depth);
                }
                out.push(']');
            }
        }
    }
}

fn escaped(value: &str) -> String {
    serde_json::to_string(value)
        .expect("strings serialize")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('&', "\\u0026")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}
fn indent(out: &mut String, depth: usize) {
    out.push('\n');
    out.push_str(&"  ".repeat(depth));
}

/// Reproduce schema 2 canonical bytes for valid Go lock inputs.
/// Schema migration and full lock validation remain outside this spike.
pub fn canonical_lock(input: &str) -> Result<String> {
    let mut source = Json::parse(input)?;
    let mut lock = Json::parse(
        r#"{"schema":2,"cli_version":"","template_version":"","packs":[],"min_cli":"","kit":{"module":"","version":""},"behavior_version":0,"formatter_go":"","answers":{},"stack_versions":{},"plugins":[],"files":{},"tombstones":[],"orphans":[],"applied_migrations":[]}"#,
    )?;
    let fields = lock.object()?;
    for (key, value) in source.object()? {
        if fields.contains_key(key) {
            fields.insert(key.clone(), value.clone());
        }
    }
    ensure!(
        matches!(&fields["schema"], Json::Token(s) if s=="2"),
        "expected schema 2"
    );
    for key in ["answers", "stack_versions", "files"] {
        if fields[key].null() {
            fields.insert(key.into(), Json::Object(BTreeMap::new()));
        }
    }
    for key in [
        "packs",
        "plugins",
        "tombstones",
        "orphans",
        "applied_migrations",
    ] {
        if fields[key].null() {
            fields.insert(key.into(), Json::Array(Vec::new()));
        }
    }
    for key in ["tombstones", "orphans", "applied_migrations"] {
        if let Json::Array(values) = fields.get_mut(key).expect("default field") {
            values.sort_by(|a, b| a.string().cmp(b.string()));
            values.dedup_by(|a, b| a.string() == b.string());
        }
    }
    if let Json::Array(plugins) = fields.get_mut("plugins").expect("default field") {
        plugins.sort_by_key(|plugin| {
            if let Json::Object(p) = plugin {
                let get = |k: &str| p.get(k).map(Json::string).unwrap_or("").to_owned();
                let api = match p.get("api") {
                    Some(Json::Token(v)) => v.parse::<i64>().unwrap_or(0),
                    _ => 0,
                };
                (get("name"), get("version"), get("sha256"), api)
            } else {
                (String::new(), String::new(), String::new(), 0)
            }
        });
    }
    if let Json::Object(files) = fields.get_mut("files").expect("default field") {
        for entry in files.values_mut() {
            entry.object()?.retain(|key, value| {
                key == "class"
                    || (["template", "hash", "mode"].contains(&key.as_str())
                        && !value.string().is_empty())
            });
        }
    }
    let mut out = String::new();
    lock.write(&mut out, 0);
    out.push('\n');
    Ok(out)
}
