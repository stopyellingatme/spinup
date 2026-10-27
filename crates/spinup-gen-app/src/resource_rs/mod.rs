//! Rust handlers, schema types, queries, and tests for one resource.

#[cfg(test)]
mod tests;

use anyhow::Result;
use serde_json::{Value, json};
use spinup_gen::{fields::Field, part::Spec};
use spinup_vfs::plan::{File, Plan};

use crate::resource_core::paths;

/// Plans the Rust API files. `tests` is false with `--no-tests`.
///
/// # Errors
/// Returns a template or parent migration error with a retry hint.
pub fn plan_api(spec: &Spec<'_>, tests: bool) -> anyhow::Result<Plan> {
    let context = context(spec, tests)?;
    let mut plan = Plan::new();
    plan.add(render_file(
        paths::handlers_path(&spec.name),
        "resource/handlers.rs.j2",
        include_str!("../../templates/resource/handlers.rs.j2"),
        &context,
    )?)?;
    plan.add(model_file(spec)?)?;
    if tests {
        plan.add(render_file(
            paths::handler_tests_path(&spec.name),
            "resource/tests.rs.j2",
            include_str!("../../templates/resource/tests.rs.j2"),
            &context,
        )?)?;
    }
    Ok(plan.sorted())
}

/// Renders the model and its queries without handlers.
pub(crate) fn model_file(spec: &Spec<'_>) -> Result<File> {
    render_file(
        paths::model_path(&spec.name),
        "model/model.rs.j2",
        include_str!("../../templates/model/model.rs.j2"),
        &context(spec, false)?,
    )
}

fn render_file(path: String, template: &str, source: &str, context: &Value) -> Result<File> {
    let mut env = spinup_render::env::environment(None);
    env.set_syntax(
        env.syntax()
            .to_builder()
            .trim_blocks(true)
            .lstrip_blocks(true)
            .build()
            .expect("the environment syntax is valid"),
    );
    let data = env
        .render_str(source, minijinja::value::Serde(context))
        .map_err(|error| {
            anyhow::anyhow!(
                "render {template} for {path}: {error}\nTry: check the resource fields and report the template error"
            )
        })?;
    Ok(File {
        path,
        data: data.into_bytes(),
        class: "scaffold".into(),
        template: format!("gen/{template}"),
        ..File::default()
    })
}

fn context(spec: &Spec<'_>, tests: bool) -> Result<Value> {
    let fields: Vec<_> = spec.fields.iter().map(|f| field(f, spec)).collect();
    let table = spec.name.table();
    let mut columns = vec!["id".to_owned()];
    columns.extend(spec.fields.iter().map(|field| {
        let col = field.column();
        match field.r#type.name.as_str() {
            "decimal" => format!(
                "{col}::text AS \"{col}{}\"",
                if field.nullable { "?" } else { "!" }
            ),
            "json" => format!("{col} AS \"{col}: serde_json::Value\""),
            _ => col,
        }
    }));
    columns.extend(["created_at".into(), "updated_at".into()]);
    let columns = columns.join(", ");
    let mut inserts: Vec<_> = spec.fields.iter().map(Field::column).collect();
    let mut values: Vec<_> = spec
        .fields
        .iter()
        .enumerate()
        .map(|(i, f)| f.bind(&format!("${}", i + 1)))
        .collect();
    let sets = spec
        .fields
        .iter()
        .enumerate()
        .map(|(i, f)| format!("{} = {}", f.column(), f.bind(&format!("${}", i + 2))))
        .collect::<Vec<_>>()
        .join(", ");
    if spec.owned {
        inserts.push("user_id".into());
        values.push(format!("${}", values.len() + 1));
    }
    let owner_where = if spec.owned {
        " WHERE user_id = $1"
    } else {
        ""
    };
    let id_where = if spec.owned {
        "id = $1 AND user_id = $2"
    } else {
        "id = $1"
    };
    let update_where = if spec.owned {
        format!("id = $1 AND user_id = ${}", spec.fields.len() + 2)
    } else {
        "id = $1".into()
    };
    let first = if spec.owned { 2 } else { 1 };
    let mut sample = serde_json::Map::new();
    for f in &spec.fields {
        sample.insert(f.column(), sample_value(f));
    }
    let parents = if tests {
        crate::resource_core::sql::parent_inserts(spec.root, spec)?
    } else {
        Vec::new()
    };
    Ok(json!({
        "singular": spec.name.snake(), "plural": spec.name.plural_snake(),
        "type": spec.name.pascal(), "plural_type": spec.name.plural_pascal(),
        "human": spec.name.snake().replace('_', " "),
        "plural_human": spec.name.plural_snake().replace('_', " "),
        "article": if spec.name.snake().starts_with(['a', 'e', 'i', 'o', 'u']) { "an" } else { "a" },
        "owned": spec.owned, "auth": spec.owned || spec.shared, "tests": tests,
        "fields": fields,
        "eq": !spec.fields.iter().any(|f| f.r#type.name == "float"),
        "date": spec.fields.iter().any(|f| f.r#type.name == "date"),
        "mutable_sample": spec.fields.iter().any(|f| !f.nullable && f.r#type.name == "ref"),
        "validate": spec.fields.iter().any(|f| ["decimal", "json"].contains(&f.r#type.name.as_str())),
        "queries": {
            "list": format!("SELECT {columns}\n         FROM {table}{owner_where} ORDER BY id LIMIT ${first} OFFSET ${}", first + 1),
            "count": format!("SELECT count(*) AS \"count!\" FROM {table}{owner_where}"),
            "create": format!("INSERT INTO {table} ({})\n         VALUES ({})\n         RETURNING {columns}", inserts.join(", "), values.join(", ")),
            "get": format!("SELECT {columns}\n         FROM {table} WHERE {id_where}"),
            "update": format!("UPDATE {table}\n         SET {sets}, updated_at = now()\n         WHERE {update_where}\n         RETURNING {columns}"),
            "delete": format!("DELETE FROM {table} WHERE {id_where}"),
        },
        "parents": parents.iter().map(|p| json!({"table": p.table, "sql": serde_json::to_string(&p.sql).expect("SQL serializes")})).collect::<Vec<_>>(),
        "sample": serde_json::to_string(&sample)?,
    }))
}

fn field(field: &Field, spec: &Spec<'_>) -> Value {
    let col = field.column();
    let ty = &field.r#type;
    let enum_type = if ty.r#enum.is_empty() {
        String::new()
    } else {
        format!("{}{}Value", spec.name.pascal(), field.name.pascal())
    };
    let base = if enum_type.is_empty() {
        ty.rust_api.clone()
    } else {
        enum_type.clone()
    };
    let rust_type = if field.nullable {
        format!("Option<{base}>")
    } else {
        base
    };
    let row_type = if field.nullable {
        format!("Option<{}>", ty.rust_store)
    } else {
        ty.rust_store.clone()
    };
    let bind = match ty.name.as_str() {
        "enum" if field.nullable => format!("input.{col}.map(|value| value.as_str())"),
        "enum" => format!("input.{col}.as_str()"),
        "json" => format!("input.{col}.clone() as _"),
        _ => format!("input.{col}"),
    };
    let from_row = if enum_type.is_empty() {
        format!("row.{col}")
    } else if field.nullable {
        format!("row.{col}.as_deref().map({enum_type}::parse).transpose()?")
    } else {
        format!("{enum_type}::parse(&row.{col})?")
    };
    let serde = match ty.name.as_str() {
        "time" if field.nullable => "time::serde::rfc3339::option",
        "time" => "time::serde::rfc3339",
        "date" if field.nullable => "date_format::option",
        "date" => "date_format",
        _ => "",
    };
    json!({"name": col, "kind": ty.name, "nullable": field.nullable,
        "ref_table": if ty.reference.is_empty() { String::new() } else { spinup_gen::fields::Name::parse(&ty.reference).expect("parsed reference").table().to_string() },
        "rust_type": rust_type, "row_type": row_type, "bind": bind,
        "enum_type": enum_type,
        "variants": ty.r#enum.iter().enumerate().map(|(i,v)| json!({"name": format!("Value{i}"), "value": v})).collect::<Vec<_>>(),
        "serde": serde, "from_row": from_row,
    })
}

fn sample_value(field: &Field) -> Value {
    if field.nullable && field.r#type.name == "ref" {
        return Value::Null;
    }
    match field.r#type.name.as_str() {
        "bool" => json!(true),
        "int" | "bigint" | "ref" => json!(1),
        "float" => json!(1.5),
        "decimal" => json!("12.50"),
        "date" => json!("2026-01-02"),
        "time" => json!("2026-01-02T03:04:05Z"),
        "uuid" => json!("00000000-0000-4000-8000-000000000001"),
        "json" => json!({}),
        "enum" => json!(field.r#type.r#enum[0]),
        _ => json!("sample"),
    }
}
