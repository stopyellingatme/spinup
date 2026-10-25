//! Spike r0-10: a resource generator for the Rust API, with minijinja
//! templates and three formatting plans.
//!
//! The generator follows ADR 0007: [`plan_resource`] renders the files of a
//! resource and never writes. [`apply`] writes the plan, keeps scaffold files
//! that the user changed (unless `force`), rewrites generator-owned files, and
//! records each file in the app lock (`spinup.lock`).

pub mod fields;
pub mod filters;
pub mod format;

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;

use anyhow::{Context, Result, bail};
use minijinja::value::Serde;
use minijinja::{AutoEscape, Environment, UndefinedBehavior};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

pub use fields::{Field, Name};
pub use format::Plan;

/// The class of a planned file (ADR 0007 §1).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Class {
    /// Generator-owned: each run rewrites it from the list of resources.
    Managed,
    /// Written once. The user owns it after that.
    Scaffold,
}

/// One planned file, relative to the app directory.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct File {
    pub path: String,
    pub contents: String,
    pub class: Class,
}

/// The templates, embedded in the binary.
const TEMPLATES: &[(&str, &str)] = &[
    ("model.rs", include_str!("../templates/model.rs.j2")),
    ("handlers.rs", include_str!("../templates/handlers.rs.j2")),
    (
        "migration.up.sql",
        include_str!("../templates/migration.up.sql.j2"),
    ),
    (
        "migration.down.sql",
        include_str!("../templates/migration.down.sql.j2"),
    ),
    (
        "handlers_mod.rs",
        include_str!("../templates/handlers_mod.rs.j2"),
    ),
    (
        "models_mod.rs",
        include_str!("../templates/models_mod.rs.j2"),
    ),
    (
        "routes_gen.rs",
        include_str!("../templates/routes_gen.rs.j2"),
    ),
];

/// The minijinja environment: the R0.2 delimiters, strict undefined values,
/// no escaping, and block whitespace control for hand-written templates.
pub fn environment() -> Environment<'static> {
    let mut env = Environment::new();
    env.set_syntax(
        minijinja::syntax::SyntaxConfig::builder()
            .variable_delimiters("[[", "]]")
            .block_delimiters("[%", "%]")
            .comment_delimiters("[#", "#]")
            .keep_trailing_newline(true)
            .trim_blocks(true)
            .lstrip_blocks(true)
            .build()
            .expect("valid delimiters"),
    );
    env.set_undefined_behavior(UndefinedBehavior::Strict);
    env.set_auto_escape_callback(|_| AutoEscape::None);
    env.add_filter("snake", |s: &str| filters::snake(s));
    env.add_filter("pascal", |s: &str| filters::pascal(s));
    env.add_filter("camel", |s: &str| filters::camel(s));
    env.add_filter("kebab", |s: &str| filters::kebab(s));
    env.add_filter("quote", |s: &str| filters::quote(s));
    for (name, source) in TEMPLATES {
        env.add_template(name, source).expect("valid template");
    }
    env
}

/// A resource in the list that generator-owned files read.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Serialize)]
struct ResourceRef {
    plural: String,
    singular: String,
}

/// An enum field in the model template.
#[derive(Serialize)]
struct EnumView {
    enum_type: String,
    human: String,
    variants: Vec<fields::Variant>,
}

/// The context of the resource templates.
#[derive(Serialize)]
struct ResourceView {
    singular: String,
    plural: String,
    table: String,
    #[serde(rename = "type")]
    type_name: String,
    plural_type: String,
    human: String,
    plural_human: String,
    article: &'static str,
    fields: Vec<fields::FieldView>,
    enums: Vec<EnumView>,
    uses_date: bool,
    derives: String,
    columns: String,
    insert_columns: String,
    insert_values: String,
    set_clause: String,
    model_imports: String,
}

fn context(name: &Name, fields: &[Field]) -> ResourceView {
    let views: Vec<_> = fields.iter().map(Field::view).collect();
    let names: Vec<_> = views.iter().map(|f| f.name.as_str()).collect();
    let type_name = filters::pascal(&name.singular);
    let human = name.singular.replace('_', " ");
    let mut imports = vec![
        type_name.clone(),
        format!("{type_name}Input"),
        format!("{type_name}List"),
        format!("{type_name}Row"),
    ];
    // A nullable enum binds with `Enum::as_str`, so the handlers name the type.
    for f in &views {
        if let (Some(t), true) = (&f.enum_type, f.nullable) {
            imports.push(t.clone());
        }
    }
    imports.sort();
    imports.dedup();
    let has_float = fields.iter().any(|f| f.kind == fields::Kind::Float);
    ResourceView {
        singular: name.singular.clone(),
        plural: name.plural.clone(),
        table: name.plural.clone(),
        plural_type: filters::pascal(&name.plural),
        article: if human.starts_with(['a', 'e', 'i', 'o', 'u']) {
            "an"
        } else {
            "a"
        },
        plural_human: name.plural.replace('_', " "),
        human,
        enums: views
            .iter()
            .filter_map(|f| {
                Some(EnumView {
                    enum_type: f.enum_type.clone()?,
                    human: f.name.replace('_', " "),
                    variants: f.variants.clone(),
                })
            })
            .collect(),
        uses_date: fields.iter().any(|f| f.kind == fields::Kind::Date),
        derives: if has_float {
            "Debug, Clone, PartialEq, Serialize, Deserialize, ToSchema"
        } else {
            "Debug, Clone, PartialEq, Eq, Serialize, Deserialize, ToSchema"
        }
        .to_owned(),
        columns: ["id"]
            .into_iter()
            .chain(names.iter().copied())
            .chain(["created_at", "updated_at"])
            .collect::<Vec<_>>()
            .join(", "),
        insert_columns: names.join(", "),
        insert_values: (1..=names.len())
            .map(|i| format!("${i}"))
            .collect::<Vec<_>>()
            .join(", "),
        set_clause: names
            .iter()
            .enumerate()
            .map(|(i, n)| format!("{n} = ${}", i + 2))
            .collect::<Vec<_>>()
            .join(", "),
        model_imports: imports.join(", "),
        type_name,
        fields: views,
    }
}

/// The resources that the app has now, from `src/handlers/*.rs` and
/// `src/models/*.rs`.
fn existing_resources(app: &Path) -> Result<BTreeSet<ResourceRef>> {
    let mut out = BTreeSet::new();
    let dir = app.join("src/handlers");
    if !dir.exists() {
        return Ok(out);
    }
    for entry in fs::read_dir(&dir).with_context(|| format!("read {}", dir.display()))? {
        let path = entry?.path();
        let Some(stem) = path.file_stem().and_then(|s| s.to_str()) else {
            continue;
        };
        if path.extension().is_some_and(|e| e == "rs") && stem != "mod" {
            let name = Name::parse(stem)?;
            out.insert(ResourceRef {
                plural: name.plural,
                singular: name.singular,
            });
        }
    }
    Ok(out)
}

/// Returns the migration base name of the resource: the existing
/// `NNNNN_create_<table>` when there is one, or the next free number. The
/// first app migration is `00002`, as in the Go golden apps.
fn migration_name(app: &Path, table: &str) -> Result<String> {
    let dir = app.join("migrations");
    let want = format!("create_{table}");
    let mut highest = 1;
    if dir.exists() {
        for entry in fs::read_dir(&dir)? {
            let file = entry?.file_name();
            let Some(base) = file.to_str().and_then(|f| f.strip_suffix(".up.sql")) else {
                continue;
            };
            let Some((num, rest)) = base.split_once('_') else {
                continue;
            };
            let Ok(n) = num.parse::<u32>() else { continue };
            if rest == want {
                return Ok(base.to_owned());
            }
            highest = highest.max(n);
        }
    }
    Ok(format!("{:05}_{want}", highest + 1))
}

/// Plans the files of one resource and the generator-owned files. It reads
/// the app directory but never writes to it.
pub fn plan_resource(app: &Path, name: &Name, fields: &[Field], plan: Plan) -> Result<Vec<File>> {
    if fields.is_empty() {
        bail!("expected at least one field\nTry: gen resource todo title:string");
    }
    let env = environment();
    let ctx = context(name, fields);
    let mut resources = existing_resources(app)?;
    resources.insert(ResourceRef {
        plural: name.plural.clone(),
        singular: name.singular.clone(),
    });
    let list =
        minijinja::context! { resources => Serde(resources.into_iter().collect::<Vec<_>>()) };
    let migration = migration_name(app, &name.plural)?;
    let render = |tmpl: &str, ctx: &minijinja::Value| -> Result<String> {
        Ok(env.get_template(tmpl)?.render(ctx)?)
    };
    let ctx = minijinja::Value::from(Serde(ctx));
    let mut files = vec![
        (
            format!("src/models/{}.rs", name.singular),
            "model.rs",
            Class::Scaffold,
        ),
        (
            format!("src/handlers/{}.rs", name.plural),
            "handlers.rs",
            Class::Scaffold,
        ),
        (
            format!("migrations/{migration}.up.sql"),
            "migration.up.sql",
            Class::Scaffold,
        ),
        (
            format!("migrations/{migration}.down.sql"),
            "migration.down.sql",
            Class::Scaffold,
        ),
    ]
    .into_iter()
    .map(|(path, tmpl, class)| {
        Ok(File {
            path,
            contents: render(tmpl, &ctx)?,
            class,
        })
    })
    .collect::<Result<Vec<_>>>()?;
    for (path, tmpl) in [
        ("src/handlers/mod.rs", "handlers_mod.rs"),
        ("src/models/mod.rs", "models_mod.rs"),
        ("src/routes_gen.rs", "routes_gen.rs"),
    ] {
        files.push(File {
            path: path.to_owned(),
            contents: render(tmpl, &list)?,
            class: Class::Managed,
        });
    }
    format::format_files(plan, app, &mut files)?;
    files.sort_by(|a, b| a.path.cmp(&b.path));
    Ok(files)
}

/// The SHA-256 of some bytes, as `sha256:<hex>`.
pub fn hash(bytes: &[u8]) -> String {
    format!("sha256:{}", hex::encode(Sha256::digest(bytes)))
}

/// One record of the app lock.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct LockEntry {
    pub class: Class,
    pub template: String,
    pub hash: String,
}

/// The part of `spinup.lock` that this spike writes: a record per generated
/// file, sorted by path.
#[derive(Debug, Default, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Lock {
    pub files: BTreeMap<String, LockEntry>,
}

/// The lock file name, in the app directory.
pub const LOCK: &str = "spinup.lock";

/// The result of [`apply`]: the files it wrote and the files it left as they
/// were. Paths are relative to the app.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct Report {
    pub written: Vec<String>,
    pub unchanged: Vec<String>,
}

/// Writes the plan. A scaffold file that exists with other content is an
/// error unless `force` is set. A managed file is rewritten when the lock
/// records it as generator-owned. The lock is written only when it changes.
pub fn apply(app: &Path, files: &[File], force: bool) -> Result<Report> {
    let lock_path = app.join(LOCK);
    let mut lock: Lock = match fs::read(&lock_path) {
        Ok(b) => serde_json::from_slice(&b).with_context(|| format!("read {LOCK}"))?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Lock::default(),
        Err(e) => return Err(e).context(format!("read {LOCK}")),
    };
    let mut report = Report::default();
    let mut writes = Vec::new();
    for f in files {
        let target = app.join(&f.path);
        match fs::read(&target) {
            Ok(old) if old == f.contents.as_bytes() => report.unchanged.push(f.path.clone()),
            Ok(_) => {
                let owned = f.class == Class::Managed
                    && lock
                        .files
                        .get(&f.path)
                        .is_some_and(|e| e.template.starts_with("gen/"));
                if !owned && !force {
                    bail!(
                        "{} exists and differs from the generated file\nTry: pass --force to replace it",
                        f.path
                    );
                }
                writes.push(f);
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => writes.push(f),
            Err(e) => return Err(e).context(format!("read {}", f.path)),
        }
    }
    for f in writes {
        let target = app.join(&f.path);
        if let Some(dir) = target.parent() {
            fs::create_dir_all(dir)?;
        }
        fs::write(&target, &f.contents).with_context(|| format!("write {}", f.path))?;
        report.written.push(f.path.clone());
    }
    let old_lock = lock.clone();
    for f in files {
        lock.files.insert(
            f.path.clone(),
            LockEntry {
                class: f.class,
                template: "gen/resource".to_owned(),
                hash: hash(f.contents.as_bytes()),
            },
        );
    }
    if lock != old_lock || !lock_path.exists() {
        let mut text = serde_json::to_string_pretty(&lock)?;
        text.push('\n');
        fs::write(&lock_path, text)?;
        report.written.push(LOCK.to_owned());
    } else {
        report.unchanged.push(LOCK.to_owned());
    }
    Ok(report)
}

/// The hash of a tree: SHA-256 over each relative path and its bytes, in
/// path order. Directories named `target` are skipped.
pub fn tree_hash(root: &Path) -> Result<String> {
    let mut files = Vec::new();
    collect(root, root, &mut files)?;
    files.sort();
    let mut h = Sha256::new();
    for rel in files {
        h.update(rel.as_bytes());
        h.update([0]);
        h.update(fs::read(root.join(&rel))?);
        h.update([0]);
    }
    Ok(format!("sha256:{}", hex::encode(h.finalize())))
}

fn collect(root: &Path, dir: &Path, out: &mut Vec<String>) -> Result<()> {
    for entry in fs::read_dir(dir)? {
        let path = entry?.path();
        if path.is_dir() {
            if path.file_name().is_some_and(|n| n != "target") {
                collect(root, &path, out)?;
            }
        } else {
            let rel = path
                .strip_prefix(root)?
                .to_string_lossy()
                .replace('\\', "/");
            out.push(rel);
        }
    }
    Ok(())
}

/// Parses `<name> <field:type>...` and plans the resource.
pub fn plan_args(app: &Path, args: &[String], plan: Plan) -> Result<Vec<File>> {
    let Some((name, rest)) = args.split_first() else {
        bail!("expected a name and at least one field\nTry: gen resource todo title:string");
    };
    let name = Name::parse(name)?;
    let fields = rest
        .iter()
        .map(|a| Field::parse(a))
        .collect::<Result<Vec<_>>>()?;
    plan_resource(app, &name, &fields, plan)
}
