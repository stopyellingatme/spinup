//! Migration pairs and parent rows for generated resource tests.

use std::{collections::BTreeSet, path::Path};

use anyhow::{Result, bail};
use regex::Regex;
use spinup_db::names::{MIGRATIONS_DIR, next_migration_name};
use spinup_gen::{fields::Name, part::Spec};
use spinup_vfs::plan::{File, Plan};

/// A table definition from an up migration.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TableDef {
    /// The migration filename.
    pub file: String,
    /// The text between the table parentheses, without comments.
    pub body: String,
}

/// A required column without a default or an identity.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RequiredColumn {
    /// The column name.
    pub name: String,
    /// A suitable SQL literal, or an empty string for unsupported types.
    pub value: String,
    /// The referenced table, or an empty string.
    pub reference: String,
}

/// One parent row that a generated test creates before the resource row.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParentInsert {
    /// The parent table.
    pub table: String,
    /// The INSERT statement, without a trailing semicolon.
    pub sql: String,
}

/// Plans a reversible migration pair with seed classification.
/// With force, reuses a generated pair or leaves a custom migration unchanged.
///
/// # Errors
/// Rejects an existing table without force and reports migration naming or read failures.
pub fn plan_migration(spec: &Spec<'_>, force: bool) -> Result<Plan> {
    let table = spec.name.table();
    let existing = find_table(spec.root, table)?;
    let pair = match existing {
        Some(def) if !force => {
            bail!(
                "the table {table} already exists in {MIGRATIONS_DIR}/{}\nTry: pass --force to generate the files again, or choose another name",
                def.file
            );
        }
        Some(def) if def.file.ends_with(&format!("_create_{table}.up.sql")) => {
            let stem = def.file.strip_suffix(".up.sql").unwrap_or(&def.file);
            let number = stem.split('_').next().unwrap_or_default();
            if number.len() != 5 || !number.bytes().all(|c| c.is_ascii_digit()) {
                return Ok(Plan::new());
            }
            (def.file.clone(), format!("{stem}.down.sql"))
        }
        Some(_) => return Ok(Plan::new()),
        None => next_migration_name(&spec.root.join(MIGRATIONS_DIR), &format!("create_{table}"))
            .map_err(|error| {
                if let Some(ui) = error.downcast_ref::<spinup_ui::error::Error>() {
                    anyhow::anyhow!("{error}\nTry: {}", ui.try_hint)
                } else {
                    anyhow::anyhow!(
                        "{error:#}\nTry: check the migration names and directory permissions"
                    )
                }
            })?,
    };
    let mut up = format!(
        "CREATE TABLE {table} (\n    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n"
    );
    if spec.owned {
        up.push_str("    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,\n");
    }
    for field in &spec.fields {
        let mut ty = if field.r#type.reference.is_empty() {
            field.r#type.sql.clone()
        } else {
            "bigint".into()
        };
        if !field.nullable {
            ty.push_str(" NOT NULL");
        }
        if !field.r#type.reference.is_empty() {
            let parent = Name::parse(&field.r#type.reference)?;
            ty.push_str(&format!(" REFERENCES {} (id)", parent.table()));
        }
        if !field.r#type.r#enum.is_empty() {
            let values = field
                .r#type
                .r#enum
                .iter()
                .map(|value| format!("'{}'", value.replace('\'', "''")))
                .collect::<Vec<_>>()
                .join(", ");
            ty.push_str(&format!(" CHECK ({} IN ({values}))", field.column()));
        }
        up.push_str(&format!("    {} {ty},\n", field.column()));
    }
    up.push_str("    created_at timestamptz NOT NULL DEFAULT now(),\n    updated_at timestamptz NOT NULL DEFAULT now()\n);\n");
    if spec.owned {
        up.push_str(&format!(
            "CREATE INDEX {table}_user_id_id_idx ON {table} (user_id, id);\n"
        ));
    }
    for field in &spec.fields {
        if !field.r#type.reference.is_empty() {
            let column = field.column();
            up.push_str(&format!(
                "CREATE INDEX {table}_{column}_idx ON {table} ({column});\n"
            ));
        }
    }
    let mut plan = Plan::new();
    for (name, data) in [(pair.0, up), (pair.1, format!("DROP TABLE {table};\n"))] {
        plan.add(File {
            path: format!("{MIGRATIONS_DIR}/{name}"),
            data: data.into_bytes(),
            class: "seed".into(),
            ..File::default()
        })?;
    }
    Ok(plan.sorted())
}

/// Finds the first up migration that creates a table, in filename order.
/// Only `.up.sql` files count. Comments and quoted values do not define tables.
///
/// # Errors
/// Returns read failures with a retry hint.
pub fn find_table(root: &Path, table: &str) -> Result<Option<TableDef>> {
    let dir = root.join(MIGRATIONS_DIR);
    let entries = match std::fs::read_dir(&dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(migration_read_error(MIGRATIONS_DIR, error, true)),
    };
    let mut names = Vec::new();
    for entry in entries {
        let entry = entry.map_err(|error| migration_read_error(MIGRATIONS_DIR, error, true))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if !entry
            .file_type()
            .map_err(|error| {
                migration_read_error(&format!("{MIGRATIONS_DIR}/{name}"), error, false)
            })?
            .is_dir()
            && name.ends_with(".up.sql")
        {
            names.push(name);
        }
    }
    names.sort();
    let create = Regex::new(&format!(
        r#"(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"?public"?\s*\.\s*)?"?{}"?\s*\("#,
        regex::escape(table)
    ))?;
    for file in names {
        let source = std::fs::read_to_string(dir.join(&file)).map_err(|error| {
            migration_read_error(&format!("{MIGRATIONS_DIR}/{file}"), error, false)
        })?;
        let source = without_comments(&source);
        if let Some(found) = create
            .find_iter(&source)
            .find(|m| !in_string(&source[..m.start()]))
        {
            let body = paren_body(&source[found.end()..]).to_string();
            return Ok(Some(TableDef { file, body }));
        }
    }
    Ok(None)
}

fn migration_read_error(path: &str, error: std::io::Error, directory: bool) -> anyhow::Error {
    let hint = if directory { "directory" } else { "file" };
    anyhow::anyhow!("read {path}: {error}\nTry: check the {hint} permissions")
}

fn in_string(text: &str) -> bool {
    text.bytes().filter(|c| *c == b'\'').count() % 2 == 1
}

fn without_comments(source: &str) -> String {
    let mut output = String::with_capacity(source.len());
    let mut chars = source.chars().peekable();
    let mut quoted = false;
    let mut block_depth = 0;
    while let Some(c) = chars.next() {
        if block_depth > 0 {
            if c == '*' && chars.peek() == Some(&'/') {
                chars.next();
                block_depth -= 1;
            } else if c == '/' && chars.peek() == Some(&'*') {
                chars.next();
                block_depth += 1;
            } else if c == '\n' {
                output.push(c);
            }
        } else if c == '\'' {
            quoted = !quoted;
            output.push(c);
        } else if !quoted && c == '-' && chars.peek() == Some(&'-') {
            for next in chars.by_ref() {
                if next == '\n' {
                    output.push(next);
                    break;
                }
            }
            output.push(' ');
        } else if !quoted && c == '/' && chars.peek() == Some(&'*') {
            chars.next();
            block_depth = 1;
            output.push(' ');
        } else {
            output.push(c);
        }
    }
    output
}

fn paren_body(text: &str) -> &str {
    let mut depth = 1;
    let mut quoted = false;
    for (index, c) in text.char_indices() {
        match c {
            '\'' => quoted = !quoted,
            '(' if !quoted => depth += 1,
            ')' if !quoted => {
                depth -= 1;
                if depth == 0 {
                    return &text[..index];
                }
            }
            _ => {}
        }
    }
    text
}

fn split_columns(text: &str) -> Vec<&str> {
    let mut columns = Vec::new();
    let (mut start, mut depth, mut quoted) = (0, 0, false);
    for (index, c) in text.char_indices() {
        match c {
            '\'' => quoted = !quoted,
            '(' if !quoted => depth += 1,
            ')' if !quoted => depth -= 1,
            ',' if !quoted && depth == 0 => {
                columns.push(&text[start..index]);
                start = index + 1;
            }
            _ => {}
        }
    }
    columns.push(&text[start..]);
    columns
}

/// Returns the columns that a parent INSERT must supply, in definition order.
pub fn required_columns(body: &str) -> Vec<RequiredColumn> {
    let reference = Regex::new(r#"(?i)\bREFERENCES\s+(?:"?public"?\s*\.\s*)?"?(\w+)"?"#)
        .expect("constant reference expression");
    let check = Regex::new(r#"(?i)\bCHECK\s*\(\s*"?\w+"?\s+IN\s*\(\s*'((?:''|[^'])*)'"#)
        .expect("constant enum expression");
    let body = without_comments(body);
    let mut columns = Vec::new();
    for def in split_columns(&body) {
        let words: Vec<_> = def.split_whitespace().collect();
        if words.len() < 2
            || [
                "constraint",
                "primary",
                "unique",
                "check",
                "foreign",
                "exclude",
                "like",
            ]
            .contains(&words[0].to_ascii_lowercase().as_str())
        {
            continue;
        }
        let upper = def.to_ascii_uppercase();
        if (!upper.contains("NOT NULL") && !upper.contains("PRIMARY KEY"))
            || ["DEFAULT", "GENERATED", "SERIAL"]
                .iter()
                .any(|s| upper.contains(s))
        {
            continue;
        }
        let mut column = RequiredColumn {
            name: words[0].trim_matches('"').into(),
            value: String::new(),
            reference: String::new(),
        };
        if let Some(captures) = reference.captures(def) {
            column.reference = captures[1].into();
        } else if let Some(captures) = check.captures(def) {
            column.value = format!("'{}'", &captures[1]);
        } else {
            column.value = sample_sql(&words[1].to_ascii_lowercase()).into();
        }
        columns.push(column);
    }
    columns
}

fn sample_sql(ty: &str) -> &'static str {
    let ty = ty
        .split('(')
        .next()
        .unwrap_or(ty)
        .trim_end_matches(['[', ']']);
    match ty {
        "text" | "varchar" | "character" | "char" | "citext" | "name" => "'sample'",
        "integer" | "int" | "int2" | "int4" | "int8" | "smallint" | "bigint" | "double"
        | "real" | "float4" | "float8" | "numeric" | "decimal" => "1",
        "boolean" | "bool" => "true",
        "timestamptz" | "timestamp" => "'2026-01-02T03:04:05Z'",
        "date" => "'2026-01-02'",
        "uuid" => "'00000000-0000-4000-8000-000000000001'",
        "jsonb" | "json" => "'{}'",
        _ => "",
    }
}

/// Plans parent INSERT statements, with dependencies before their children.
/// Skips self references and repeated tables. Limits recursive traversal to five levels.
///
/// # Errors
/// Returns an error if a parent migration cannot be read.
pub fn parent_inserts(root: &Path, spec: &Spec<'_>) -> Result<Vec<ParentInsert>> {
    let mut done = BTreeSet::from([spec.name.table().to_string()]);
    let mut output = Vec::new();
    for field in &spec.fields {
        if !field.r#type.reference.is_empty() {
            let parent = Name::parse(&field.r#type.reference)?;
            visit_parent(root, parent.table(), 0, &mut done, &mut output)?;
        }
    }
    Ok(output)
}

fn visit_parent(
    root: &Path,
    table: &str,
    depth: usize,
    done: &mut BTreeSet<String>,
    output: &mut Vec<ParentInsert>,
) -> Result<()> {
    if !done.insert(table.to_string()) {
        return Ok(());
    }
    let mut names = Vec::new();
    let mut values = Vec::new();
    if let Some(def) = find_table(root, table)? {
        for column in required_columns(&def.body) {
            let mut value = column.value;
            if !column.reference.is_empty() {
                if depth < 5 {
                    visit_parent(root, &column.reference, depth + 1, done, output)?;
                }
                value = format!("(SELECT max(id) FROM {})", column.reference);
            }
            if !value.is_empty() {
                names.push(column.name);
                values.push(value);
            }
        }
    }
    let sql = if names.is_empty() {
        format!("INSERT INTO {table} DEFAULT VALUES")
    } else {
        format!(
            "INSERT INTO {table} ({}) VALUES ({})",
            names.join(", "),
            values.join(", ")
        )
    };
    output.push(ParentInsert {
        table: table.into(),
        sql,
    });
    Ok(())
}
