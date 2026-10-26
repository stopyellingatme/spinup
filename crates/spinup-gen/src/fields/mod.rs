//! Resource names and field types shared by the generators.

#[cfg(test)]
mod tests;

use std::collections::BTreeSet;

use anyhow::{anyhow, bail};
use serde::Serialize;

/// A normalized resource name in singular and plural forms.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Name {
    /// The singular snake case name.
    pub singular: String,
    /// The plural snake case name.
    pub plural: String,
}

impl Name {
    /// Parses a resource name. See [`parse_name`].
    ///
    /// # Errors
    /// Returns an error when the name is invalid or reserved.
    pub fn parse(value: &str) -> anyhow::Result<Self> {
        parse_name(value)
    }

    /// Returns the singular snake case name.
    pub fn snake(&self) -> &str {
        &self.singular
    }
    /// Returns the plural snake case name.
    pub fn plural_snake(&self) -> &str {
        &self.plural
    }
    /// Returns the singular Pascal case name.
    pub fn pascal(&self) -> String {
        pascal(&self.singular)
    }
    /// Returns the plural Pascal case name.
    pub fn plural_pascal(&self) -> String {
        pascal(&self.plural)
    }
    /// Returns the singular camel case name.
    pub fn camel(&self) -> String {
        camel(&self.singular)
    }
    /// Returns the plural camel case name.
    pub fn plural_camel(&self) -> String {
        camel(&self.plural)
    }
    /// Returns the singular kebab case name.
    pub fn kebab(&self) -> String {
        self.singular.replace('_', "-")
    }
    /// Returns the plural kebab case name.
    pub fn plural_kebab(&self) -> String {
        self.plural.replace('_', "-")
    }
    /// Returns the SQL table name.
    pub fn table(&self) -> &str {
        &self.plural
    }
}

/// A field type across Rust, SQL, OpenAPI, and TypeScript.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Type {
    /// The CLI type name.
    pub name: String,
    /// The SQL type, with a reference constraint when applicable.
    pub sql: String,
    /// The Rust storage type.
    pub rust_store: String,
    /// The Rust API type before nullable wrapping.
    pub rust_api: String,
    /// The OpenAPI type.
    pub openapi_type: String,
    /// The OpenAPI format, or an empty string.
    pub openapi_format: String,
    /// The TypeScript type.
    pub ts: String,
    /// The allowed enum values in input order.
    pub r#enum: Vec<String>,
    /// The singular target name for a reference.
    pub reference: String,
}

/// One writable field of a resource.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Field {
    /// The field name. Both forms retain the input plurality.
    pub name: Name,
    /// The field type.
    pub r#type: Type,
    /// Whether the database column accepts NULL.
    pub nullable: bool,
}

impl Field {
    /// Parses one field. See [`parse`].
    ///
    /// # Errors
    /// Returns an error when the field name or type is invalid.
    pub fn parse(arg: &str) -> anyhow::Result<Self> {
        Ok(parse(&[arg])?.remove(0))
    }

    /// Returns the SQL column name, with `_id` for references.
    pub fn column(&self) -> String {
        if self.r#type.reference.is_empty() {
            self.name.singular.clone()
        } else {
            format!("{}_id", self.name.singular)
        }
    }

    /// Returns the Rust API type, including `Option` for nullable fields.
    pub fn rust_type(&self) -> String {
        if self.nullable {
            format!("Option<{}>", self.r#type.rust_api)
        } else {
            self.r#type.rust_api.clone()
        }
    }

    /// Returns a SQL bind expression. Decimal values pass through text into numeric.
    pub fn bind(&self, placeholder: &str) -> String {
        if self.r#type.name == "decimal" {
            format!("{placeholder}::text::numeric")
        } else {
            placeholder.to_string()
        }
    }

    /// Returns a SQL select expression. Decimal values return as text.
    pub fn select(&self) -> String {
        let column = self.column();
        if self.r#type.name == "decimal" {
            format!("{column}::text")
        } else {
            column
        }
    }
}

/// Parses a singular or plural resource name with Go-compatible case rules.
///
/// # Errors
/// Returns an error with a `Try:` line for invalid or reserved names.
pub fn parse_name(value: &str) -> anyhow::Result<Name> {
    let normalized = normalize(value)?;
    let singular = singularize(&normalized);
    if singular.is_empty() || rust_reserved(&singular) {
        return Err(name_error(value, "its singular name is empty or reserved"));
    }
    let plural = pluralize(&singular);
    if sql_reserved(&plural) {
        return Err(name_error(
            value,
            &format!("table name {plural:?} is reserved"),
        ));
    }
    Ok(Name { singular, plural })
}

/// Returns an independent type table in a stable order.
pub fn types() -> Vec<Type> {
    [
        ("string", "text", "String", "string", "", "string"),
        ("text", "text", "String", "string", "", "string"),
        ("int", "integer", "i32", "integer", "int32", "number"),
        ("bigint", "bigint", "i64", "integer", "int64", "number"),
        ("bool", "boolean", "bool", "boolean", "", "boolean"),
        (
            "float",
            "double precision",
            "f64",
            "number",
            "double",
            "number",
        ),
        (
            "decimal", "numeric", "String", "string", "decimal", "string",
        ),
        (
            "time",
            "timestamptz",
            "time::OffsetDateTime",
            "string",
            "date-time",
            "string",
        ),
        ("date", "date", "time::Date", "string", "date", "string"),
        ("uuid", "uuid", "uuid::Uuid", "string", "uuid", "string"),
        (
            "json",
            "jsonb",
            "serde_json::Value",
            "object",
            "",
            "Record<string, unknown>",
        ),
        ("ref", "bigint", "i64", "integer", "int64", "number"),
        ("enum", "text", "String", "string", "", "string"),
    ]
    .into_iter()
    .map(|(name, sql, rust, openapi_type, openapi_format, ts)| Type {
        name: name.into(),
        sql: sql.into(),
        rust_store: rust.into(),
        rust_api: rust.into(),
        openapi_type: openapi_type.into(),
        openapi_format: openapi_format.into(),
        ts: ts.into(),
        r#enum: Vec::new(),
        reference: String::new(),
    })
    .collect()
}

/// Parses `name:type` arguments and rejects duplicate or automatic columns.
///
/// # Errors
/// Returns an error with a `Try:` line for an invalid name, type, or nullable marker.
pub fn parse(args: &[impl AsRef<str>]) -> anyhow::Result<Vec<Field>> {
    let mut seen = BTreeSet::new();
    let mut fields = Vec::with_capacity(args.len());
    for arg in args {
        let arg = arg.as_ref();
        let Some((name, text)) = arg
            .split_once(':')
            .filter(|(n, t)| !n.is_empty() && !t.is_empty())
        else {
            bail!("field {arg:?} must use name:type\nTry: use a field such as title:string");
        };
        let value = normalize(name)?;
        if rust_reserved(&value) {
            return Err(name_error(name, &format!("{value:?} is reserved")));
        }
        let nullable = text.ends_with('?');
        let text = text.strip_suffix('?').unwrap_or(text);
        if text.is_empty() || text.contains('?') {
            bail!(
                "field {name:?} has an invalid nullable marker\nTry: put one ? after a valid type"
            );
        }
        let field = Field {
            name: Name {
                singular: value.clone(),
                plural: value,
            },
            r#type: parse_type(text)?,
            nullable,
        };
        let column = field.column();
        if ["id", "created_at", "updated_at"].contains(&column.as_str()) {
            bail!(
                "field {:?} is automatic\nTry: choose a field other than id, created_at, or updated_at",
                field.name.snake()
            );
        }
        if sql_reserved(&column) {
            return Err(name_error(
                field.name.snake(),
                &format!("column name {column:?} is reserved"),
            ));
        }
        if !seen.insert(column) {
            bail!(
                "field {:?} is duplicated\nTry: use each field name once",
                field.name.snake()
            );
        }
        fields.push(field);
    }
    Ok(fields)
}

fn parse_type(text: &str) -> anyhow::Result<Type> {
    let table = types();
    if let Some(target) = text.strip_prefix("ref:") {
        let name =
            parse_name(target).map_err(|e| anyhow!("reference type {text:?} is invalid: {e}"))?;
        let mut ty = table[11].clone();
        ty.sql = format!("bigint REFERENCES {}(id)", name.table());
        ty.reference = name.singular;
        return Ok(ty);
    }
    if let Some(values) = text.strip_prefix("enum:") {
        if values.is_empty() {
            bail!("enum has no values\nTry: use enum:draft|published");
        }
        let mut seen = BTreeSet::new();
        let mut ty = table[12].clone();
        for value in values.split('|') {
            if value.is_empty()
                || value.starts_with('_')
                || value.ends_with('_')
                || !value
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_')
            {
                bail!("enum value {value:?} is invalid\nTry: use lowercase snake case values");
            }
            if !seen.insert(value) {
                bail!("enum value {value:?} is duplicated\nTry: use each enum value once");
            }
            ty.r#enum.push(value.into());
        }
        ty.ts = ty
            .r#enum
            .iter()
            .map(|v| format!("'{v}'"))
            .collect::<Vec<_>>()
            .join(" | ");
        return Ok(ty);
    }
    table.into_iter().take(11).find(|ty| ty.name == text).ok_or_else(|| anyhow!(
        "field type {text:?} is unknown\nTry: use string, text, int, bigint, bool, float, decimal, time, date, uuid, json, ref:<model>, or enum:a|b"
    ))
}

fn normalize(value: &str) -> anyhow::Result<String> {
    if value.is_empty() {
        return Err(name_error(value, "it is empty"));
    }
    if value.len() > 50 {
        return Err(name_error(value, "it is longer than 50 characters"));
    }
    let parts = if value.contains(['_', '-']) {
        if value.contains('_') && value.contains('-') {
            return Err(name_error(value, "it mixes snake case and kebab case"));
        }
        let parts: Vec<_> = value
            .split(if value.contains('_') { '_' } else { '-' })
            .collect();
        if parts
            .iter()
            .any(|p| p.is_empty() || !p.bytes().all(|b| b.is_ascii_alphanumeric()))
        {
            return Err(name_error(value, "it contains an invalid word"));
        }
        parts
    } else {
        if !value.bytes().all(|b| b.is_ascii_alphanumeric()) {
            return Err(name_error(value, "it contains an invalid character"));
        }
        let bytes = value.as_bytes();
        let mut start = 0;
        let mut parts = Vec::new();
        for i in 1..bytes.len() {
            if bytes[i].is_ascii_uppercase()
                && (bytes[i - 1].is_ascii_lowercase()
                    || bytes[i - 1].is_ascii_digit()
                    || bytes.get(i + 1).is_some_and(u8::is_ascii_lowercase))
            {
                parts.push(&value[start..i]);
                start = i;
            }
        }
        parts.push(&value[start..]);
        parts
    };
    let result = parts.join("_").to_ascii_lowercase();
    if result.starts_with(|c: char| c.is_ascii_digit()) {
        return Err(name_error(value, "it starts with a digit"));
    }
    Ok(result)
}

fn pascal(value: &str) -> String {
    value
        .split('_')
        .map(|word| {
            let mut chars = word.chars();
            chars.next().map_or_else(String::new, |c| {
                c.to_ascii_uppercase().to_string() + chars.as_str()
            })
        })
        .collect()
}

fn camel(value: &str) -> String {
    let p = pascal(value);
    let mut chars = p.chars();
    chars.next().map_or_else(String::new, |c| {
        c.to_ascii_lowercase().to_string() + chars.as_str()
    })
}

fn name_error(value: &str, reason: &str) -> anyhow::Error {
    anyhow!("name {value:?} is invalid because {reason}\nTry: use a short lowercase resource name")
}

const IRREGULAR: &[(&str, &str)] = &[
    ("person", "people"),
    ("child", "children"),
    ("man", "men"),
    ("woman", "women"),
    ("mouse", "mice"),
    ("goose", "geese"),
    ("tooth", "teeth"),
    ("foot", "feet"),
    ("series", "series"),
    ("news", "news"),
    ("species", "species"),
    ("movie", "movies"),
    ("cookie", "cookies"),
    ("tie", "ties"),
];

fn pluralize(value: &str) -> String {
    if let Some((_, plural)) = IRREGULAR.iter().find(|(s, _)| *s == value) {
        return (*plural).into();
    }
    if let Some(stem) = value
        .strip_suffix('y')
        .filter(|s| !s.is_empty() && !s.ends_with(['a', 'e', 'i', 'o', 'u']))
    {
        return format!("{stem}ies");
    }
    if value.ends_with(['s', 'x', 'z']) || value.ends_with("ch") || value.ends_with("sh") {
        format!("{value}es")
    } else {
        format!("{value}s")
    }
}

fn singularize(value: &str) -> String {
    if let Some((singular, _)) = IRREGULAR.iter().find(|(_, p)| *p == value) {
        return (*singular).into();
    }
    if let Some(stem) = value.strip_suffix("ies").filter(|s| !s.is_empty()) {
        return if stem.len() <= 1 || stem.ends_with(['a', 'e', 'i', 'o', 'u']) {
            value[..value.len() - 1].into()
        } else {
            format!("{stem}y")
        };
    }
    if value.ends_with("ses") {
        let stem = &value[..value.len() - 2];
        return if stem.ends_with("ss") || stem.ends_with("us") || stem.ends_with("is") {
            stem.into()
        } else {
            value[..value.len() - 1].into()
        };
    }
    if ["xes", "zes", "ches", "shes"]
        .iter()
        .any(|s| value.ends_with(s))
    {
        return value[..value.len() - 2].into();
    }
    if value.ends_with('s') && !["ss", "us", "is"].iter().any(|s| value.ends_with(s)) {
        return value[..value.len() - 1].into();
    }
    value.into()
}

fn rust_reserved(value: &str) -> bool {
    // Strict, reserved, and edition 2024 keywords. Raw identifiers are not generated.
    "as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while abstract become box do final gen macro override priv try typeof unsized virtual yield _".split_whitespace().any(|w| w.eq_ignore_ascii_case(value))
}

fn sql_reserved(value: &str) -> bool {
    "all analyse analyze and any array as asc asymmetric authorization between binary both case cast check collate column concurrently constraint create cross current_catalog current_date current_role current_time current_timestamp current_user default deferrable desc distinct do else end except false fetch for foreign from grant group having ilike in initially inner intersect into is join lateral leading left like limit localtime localtimestamp natural not notnull null offset on only or order outer overlaps placing primary references returning right select session_user similar some symmetric table then to trailing true union unique user using variadic verbose when where window with".split_whitespace().any(|w| w == value)
}
