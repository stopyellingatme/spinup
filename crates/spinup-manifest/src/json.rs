//! Canonical lock JSON with Go-compatible number tokens and escaping.
use anyhow::{Result, bail};
use serde_json::value::RawValue;
use std::collections::BTreeMap;

// RawValue is necessary: serde_json::Number normalizes exponent spelling even
// with arbitrary_precision. Go json.Number preserves the original token.
// A JSON tree that preserves the original number tokens.
#[derive(Clone, Debug)]
enum Json {
    /// A sorted object.
    Object(BTreeMap<String, Json>),
    /// An array.
    Array(Vec<Json>),
    /// A decoded string.
    String(String),
    /// A number, boolean, or null token.
    Token(String),
}

impl Json {
    pub(crate) fn parse(raw: &str) -> Result<Self> {
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
    pub(crate) fn object(&mut self) -> Result<&mut BTreeMap<String, Self>> {
        match self {
            Self::Object(v) => Ok(v),
            _ => bail!("expected object"),
        }
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

use crate::lock::{Error, Lock, SCHEMA_VERSION, sorted_unique};

/// Parse and validate a schema 3 lock. Reject Go-era schemas without migration.
pub fn parse(data: &[u8]) -> std::result::Result<Lock, Error> {
    let invalid = |e| {
        Error::new(
            format!("parse spinup.lock: invalid JSON: {e}"),
            "repair the JSON in spinup.lock",
        )
    };
    let text = std::str::from_utf8(data).map_err(|e| invalid(e.to_string()))?;
    let mut raw = Json::parse(text).map_err(|e| invalid(e.to_string()))?;
    let fields = raw.object().map_err(|e| invalid(e.to_string()))?;
    let schema = match fields.get("schema") {
        Some(Json::Token(number)) => number.parse::<u32>().ok().filter(|n| *n >= 1),
        _ => None,
    }
    .ok_or_else(|| {
        Error::new(
            "parse spinup.lock: schema must be an integer of at least 1",
            "restore a valid spinup.lock",
        )
    })?;
    if schema < SCHEMA_VERSION {
        return Err(Error::new(
            format!(
                "parse spinup.lock: Go-era schema {schema} is unsupported (expected {SCHEMA_VERSION})"
            ),
            "create a new app with the Rust CLI using spinup new",
        ));
    }
    if schema > SCHEMA_VERSION {
        return Err(Error::new(
            format!(
                "parse spinup.lock: schema {schema} is newer than supported schema {SCHEMA_VERSION}"
            ),
            "spinup self-update",
        ));
    }
    for key in [
        "answers",
        "stack_versions",
        "files",
        "packs",
        "plugins",
        "tombstones",
        "orphans",
        "applied_migrations",
    ] {
        if matches!(fields.get(key), Some(Json::Token(token)) if token == "null") {
            fields.remove(key);
        }
    }
    let mut normalized = String::new();
    raw.write(&mut normalized, 0);
    let lock: Lock = serde_json::from_str(&normalized).map_err(|e| {
        Error::new(
            format!("parse spinup.lock: invalid field type: {e}"),
            "repair the fields in spinup.lock",
        )
    })?;
    lock.validate()?;
    Ok(lock)
}

impl Lock {
    /// Parse and validate a schema 3 lock.
    pub fn parse(data: &[u8]) -> std::result::Result<Self, Error> {
        parse(data)
    }

    /// Produce sorted, indented JSON with a final newline without changing this lock.
    pub fn marshal(&self) -> std::result::Result<Vec<u8>, Error> {
        self.validate()?;
        let mut canonical = self.clone();
        canonical.tombstones = sorted_unique(&self.tombstones);
        canonical.orphans = sorted_unique(&self.orphans);
        canonical.applied_migrations = sorted_unique(&self.applied_migrations);
        canonical.plugins.sort();
        let encode_error = |e| {
            Error::new(
                format!("marshal spinup.lock: cannot encode canonical JSON: {e}"),
                "use JSON-compatible answer values",
            )
        };
        let encoded = serde_json::to_string(&canonical).map_err(|e| encode_error(e.to_string()))?;
        let raw = Json::parse(&encoded).map_err(|e| encode_error(e.to_string()))?;
        let mut out = String::new();
        raw.write(&mut out, 0);
        out.push('\n');
        Ok(out.into_bytes())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;
    use serde_json::value::RawValue;

    #[derive(Deserialize)]
    struct Case {
        name: String,
        input: Box<RawValue>,
        expected: String,
    }

    // The Go oracle stays frozen. Apply only the three schema 3 field changes.
    fn schema3(input: &str, pretty: bool) -> String {
        let mut tree = Json::parse(input).unwrap();
        let object = tree.object().unwrap();
        object.insert("schema".into(), Json::Token("3".into()));
        object.insert("engine".into(), Json::String("minijinja".into()));
        let formatter = object.remove("formatter_go").unwrap();
        object.insert("formatter".into(), formatter);
        let kit = object.get_mut("kit").unwrap().object().unwrap();
        let module = kit.remove("module").unwrap();
        kit.insert("crate".into(), module);
        let mut result = String::new();
        tree.write(&mut result, 0);
        if pretty {
            result.push('\n');
        }
        result
    }

    #[test]
    fn oracle_lock_cases() {
        let cases: Vec<Case> =
            serde_json::from_str(include_str!("../../../testdata/oracle/locks.json")).unwrap();
        for case in cases {
            let lock = parse(schema3(case.input.get(), false).as_bytes()).unwrap();
            let bytes = lock.marshal().unwrap();
            assert_eq!(
                String::from_utf8(bytes.clone()).unwrap(),
                schema3(&case.expected, true),
                "{}",
                case.name
            );
            assert_eq!(parse(&bytes).unwrap().marshal().unwrap(), bytes);
        }
    }

    #[test]
    fn marshal_is_deterministic_and_does_not_change_collections() {
        let mut expected = None;
        for keys in [["z", "a", "m"], ["m", "z", "a"], ["a", "m", "z"]] {
            let mut lock = Lock::new();
            lock.formatter = "rustfmt 1.99.1".into();
            lock.kit = crate::lock::KitRef {
                crate_name: "spinup-kit".into(),
                version: "v1.0.0".into(),
            };
            for key in keys {
                lock.files.insert(
                    key.into(),
                    crate::lock::FileEntry {
                        class: crate::lock::Class::Managed,
                        template: "api/main.tmpl".into(),
                        hash: crate::hash::hash(b"original"),
                        mode: "0644".into(),
                    },
                );
                lock.stack_versions.insert(key.into(), key.into());
                // Vary nested object order while retaining large integer tokens.
                let inner = keys.map(|name| format!("\"{name}\":\"{name}\"")).join(",");
                lock.answers.insert(
                    key.into(),
                    RawValue::from_string(format!("[{{{inner}}},9007199254740993]")).unwrap(),
                );
                lock.plugins.push(crate::lock::Plugin {
                    name: key.into(),
                    version: "v1".into(),
                    sha256: key.into(),
                    api: 1,
                });
            }
            lock.tombstones = vec!["removed/z".into(), "removed/a".into(), "removed/z".into()];
            lock.orphans = vec!["orphan/z".into(), "orphan/a".into(), "orphan/z".into()];
            lock.applied_migrations = vec!["0002".into(), "0001".into(), "0002".into()];
            let before = serde_json::to_string(&lock).unwrap();
            let bytes = lock.marshal().unwrap();
            assert_eq!(serde_json::to_string(&lock).unwrap(), before);
            let parsed = parse(&bytes).unwrap();
            assert_eq!(parsed.marshal().unwrap(), bytes);
            assert_eq!(parsed.tombstones, ["removed/a", "removed/z"]);
            assert_eq!(parsed.orphans, ["orphan/a", "orphan/z"]);
            assert_eq!(parsed.applied_migrations, ["0001", "0002"]);
            assert_eq!(
                parsed
                    .plugins
                    .iter()
                    .map(|p| p.name.as_str())
                    .collect::<Vec<_>>(),
                ["a", "m", "z"]
            );
            if let Some(previous) = &expected {
                assert_eq!(&bytes, previous);
            }
            expected = Some(bytes);
        }
    }

    #[test]
    fn null_collections_become_empty_and_unknown_fields_are_ignored() {
        let lock = parse(br#"{"schema":3,"answers":null,"files":null,"stack_versions":null,"packs":null,"plugins":null,"tombstones":null,"orphans":null,"applied_migrations":null,"future":true}"#).unwrap();
        assert_eq!(lock.marshal().unwrap(), Lock::new().marshal().unwrap());
    }

    #[test]
    fn parse_errors_and_schema_rejection() {
        for input in [
            "",
            "{",
            "null",
            "[]",
            "{}",
            r#"{"schema":0}"#,
            r#"{"schema":-1}"#,
            r#"{"schema":1.5}"#,
            r#"{"schema":"3"}"#,
            r#"{"schema":1}"#,
            r#"{"schema":2}"#,
            r#"{"schema":4}"#,
            r#"{"schema":3} {}"#,
            r#"{"schema":3,"files":[]}"#,
            r#"{"schema":3,"files":{"a":{"class":"other"}}}"#,
        ] {
            assert!(
                parse(input.as_bytes())
                    .unwrap_err()
                    .to_string()
                    .contains("\nTry:"),
                "{input}"
            );
        }
    }

    #[test]
    fn answer_numbers_never_pass_through_floating_point() {
        let lock = parse(br#"{"schema":3,"answers":{"huge":1E+9999,"tiny":-1e-9999,"integer":1234567890123456789012345678901234567890}}"#).unwrap();
        let bytes = lock.marshal().unwrap();
        let text = String::from_utf8(bytes.clone()).unwrap();
        for token in [
            "1E+9999",
            "-1e-9999",
            "1234567890123456789012345678901234567890",
        ] {
            assert!(text.contains(token), "{text}");
        }
        assert_eq!(parse(&bytes).unwrap().marshal().unwrap(), bytes);
    }

    #[test]
    fn answer_edits_preserve_other_number_tokens() {
        let mut lock =
            parse(br#"{"schema":3,"answers":{"nested":{"keep":1.2300,"edit":1},"remove":1E-30}}"#)
                .unwrap();
        let mut nested: BTreeMap<String, Box<RawValue>> =
            serde_json::from_str(lock.answers["nested"].get()).unwrap();
        nested.insert("edit".into(), RawValue::from_string("2".into()).unwrap());
        lock.answers.insert(
            "nested".into(),
            serde_json::value::to_raw_value(&nested).unwrap(),
        );
        lock.answers.remove("remove");
        let output = String::from_utf8(lock.marshal().unwrap()).unwrap();
        assert!(output.contains("1.2300"));
        assert!(output.contains("\"edit\": 2"));
        assert!(!output.contains("remove"));
    }
}
