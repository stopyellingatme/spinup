//! Resource names and fields. A small subset of the Go `internal/gen/fields`
//! package: enough types for the todo and note resources and the stress set.

use anyhow::{Result, bail};
use serde::Serialize;

use crate::filters::{pascal, snake};

/// A resource name in its singular and plural snake case forms.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Name {
    pub singular: String,
    pub plural: String,
}

impl Name {
    /// Parses a singular or plural name. `Todo`, `todos`, and `todo` give
    /// the same name.
    pub fn parse(s: &str) -> Result<Self> {
        let value = snake(s);
        if value.is_empty() || value.starts_with(|c: char| c.is_ascii_digit()) {
            bail!("resource name {s:?} is invalid\nTry: use a name such as todo");
        }
        let singular = singularize(&value);
        let plural = pluralize(&singular);
        Ok(Self { singular, plural })
    }
}

fn singularize(s: &str) -> String {
    if let Some(stem) = s.strip_suffix("ies") {
        return format!("{stem}y");
    }
    for end in ["sses", "xes", "ches", "shes"] {
        if let Some(stem) = s.strip_suffix(end) {
            return format!("{stem}{}", &end[..end.len() - 2]);
        }
    }
    if s.ends_with('s') && !s.ends_with("ss") {
        return s[..s.len() - 1].to_owned();
    }
    s.to_owned()
}

fn pluralize(s: &str) -> String {
    if let Some(stem) = s.strip_suffix('y')
        && !stem.ends_with(['a', 'e', 'i', 'o', 'u'])
    {
        return format!("{stem}ies");
    }
    if s.ends_with("ss") || s.ends_with('x') || s.ends_with("ch") || s.ends_with("sh") {
        return format!("{s}es");
    }
    format!("{s}s")
}

/// The type of a field.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Kind {
    String,
    Text,
    Int,
    BigInt,
    Bool,
    Float,
    Time,
    Date,
    Enum(Vec<String>),
}

/// A writable field of a resource.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Field {
    pub name: String,
    pub kind: Kind,
    pub nullable: bool,
}

impl Field {
    /// Parses `name:type`, `name:type?`, or `name:enum:a|b|c`.
    pub fn parse(arg: &str) -> Result<Self> {
        let Some((name, ty)) = arg.split_once(':') else {
            bail!("field {arg:?} must use name:type\nTry: use a field such as title:string");
        };
        let name = snake(name);
        if name.is_empty() || ["id", "created_at", "updated_at"].contains(&name.as_str()) {
            bail!("field {arg:?} has an invalid or automatic name\nTry: choose another name");
        }
        let (ty, nullable) = match ty.strip_suffix('?') {
            Some(t) => (t, true),
            None => (ty, false),
        };
        let kind = match ty {
            "string" => Kind::String,
            "text" => Kind::Text,
            "int" => Kind::Int,
            "bigint" => Kind::BigInt,
            "bool" => Kind::Bool,
            "float" => Kind::Float,
            "time" => Kind::Time,
            "date" => Kind::Date,
            _ => match ty.strip_prefix("enum:") {
                Some(values) if !values.is_empty() => {
                    Kind::Enum(values.split('|').map(str::to_owned).collect())
                }
                _ => bail!(
                    "field {arg:?} has an unknown type {ty:?}\nTry: use string, text, int, bigint, bool, float, time, date, or enum:a|b"
                ),
            },
        };
        Ok(Self {
            name,
            kind,
            nullable,
        })
    }

    /// The view of the field that the templates read.
    pub fn view(&self) -> FieldView {
        let (base, sql, serde_with) = match &self.kind {
            Kind::String | Kind::Text => ("String".to_owned(), "text".to_owned(), None),
            Kind::Int => ("i32".to_owned(), "integer".to_owned(), None),
            Kind::BigInt => ("i64".to_owned(), "bigint".to_owned(), None),
            Kind::Bool => ("bool".to_owned(), "boolean".to_owned(), None),
            Kind::Float => ("f64".to_owned(), "double precision".to_owned(), None),
            Kind::Time => (
                "OffsetDateTime".to_owned(),
                "timestamptz".to_owned(),
                Some(if self.nullable {
                    "time::serde::rfc3339::option"
                } else {
                    "time::serde::rfc3339"
                }),
            ),
            Kind::Date => ("Date".to_owned(), "date".to_owned(), None),
            Kind::Enum(values) => {
                let list = values
                    .iter()
                    .map(|v| format!("'{v}'"))
                    .collect::<Vec<_>>()
                    .join(", ");
                (
                    pascal(&self.name),
                    format!("text CHECK ({} IN ({list}))", self.name),
                    None,
                )
            }
        };
        let is_enum = matches!(self.kind, Kind::Enum(_));
        let wrap = |t: &str| {
            if self.nullable {
                format!("Option<{t}>")
            } else {
                t.to_owned()
            }
        };
        let row_type = wrap(if is_enum { "String" } else { &base });
        // The bind argument of an INSERT or UPDATE for `input.<name>`.
        let bind = match (is_enum, self.nullable) {
            (true, false) => format!("input.{}.as_str()", self.name),
            (true, true) => format!("input.{}.map({base}::as_str)", self.name),
            _ => format!("input.{}", self.name),
        };
        // The conversion from `row.<name>` to the API type.
        let from_row = match (is_enum, self.nullable) {
            (true, false) => format!("{base}::parse(&row.{})?", self.name),
            (true, true) => format!(
                "row.{}.as_deref().map({base}::parse).transpose()?",
                self.name
            ),
            _ => format!("row.{}", self.name),
        };
        FieldView {
            name: self.name.clone(),
            rust_type: wrap(&base),
            row_type,
            sql_type: match (self.nullable, sql.split_once(" CHECK ")) {
                (true, _) => sql,
                (false, Some((ty, check))) => format!("{ty} NOT NULL CHECK {check}"),
                (false, None) => format!("{sql} NOT NULL"),
            },
            nullable: self.nullable,
            serde_with: serde_with.map(str::to_owned),
            bind,
            from_row,
            enum_type: is_enum.then(|| base.clone()),
            variants: match &self.kind {
                Kind::Enum(values) => values
                    .iter()
                    .map(|v| Variant {
                        name: pascal(v),
                        value: v.clone(),
                    })
                    .collect(),
                _ => Vec::new(),
            },
        }
    }
}

/// One enum value: the Rust variant and the stored text.
#[derive(Debug, Clone, Serialize)]
pub struct Variant {
    pub name: String,
    pub value: String,
}

/// The template view of a field. The templates do no type logic.
#[derive(Debug, Clone, Serialize)]
pub struct FieldView {
    pub name: String,
    pub rust_type: String,
    pub row_type: String,
    pub sql_type: String,
    pub nullable: bool,
    pub serde_with: Option<String>,
    pub bind: String,
    pub from_row: String,
    pub enum_type: Option<String>,
    pub variants: Vec<Variant>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names() {
        for (input, singular, plural) in [
            ("todo", "todo", "todos"),
            ("Todos", "todo", "todos"),
            ("category", "category", "categories"),
            ("box", "box", "boxes"),
            ("TodoItem", "todo_item", "todo_items"),
        ] {
            let n = Name::parse(input).unwrap();
            assert_eq!((n.singular.as_str(), n.plural.as_str()), (singular, plural));
        }
        assert!(Name::parse("9lives").is_err());
    }

    #[test]
    fn fields() {
        let f = Field::parse("due:date?").unwrap();
        assert_eq!(f.view().rust_type, "Option<Date>");
        assert_eq!(f.view().sql_type, "date");
        let f = Field::parse("priority:enum:low|normal|high").unwrap();
        let v = f.view();
        assert_eq!(v.rust_type, "Priority");
        assert_eq!(v.row_type, "String");
        assert_eq!(
            v.sql_type,
            "text NOT NULL CHECK (priority IN ('low', 'normal', 'high'))"
        );
        let err = Field::parse("title").unwrap_err().to_string();
        assert!(err.contains("\nTry: "), "{err}");
        assert!(Field::parse("x:blob").is_err());
        assert!(Field::parse("id:int").is_err());
    }
}
