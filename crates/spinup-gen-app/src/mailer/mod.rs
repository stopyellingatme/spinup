//! Typed mailers, template seeds, and sorted mail modules.
//!
//! The mail pack supplies `mailers::Mailers` with a public `from: String` field.
//! Its `send(&kit::mail::Message)` method returns `anyhow::Result<()>` asynchronously.
//! With jobs enabled, the pack also supplies `worker.rs`.

#[cfg(test)]
mod tests;

use std::collections::BTreeSet;

use anyhow::{Result, bail};
use spinup_gen::{
    contract::{Generator, Input, Output},
    fields,
};
use spinup_vfs::plan::{File, Plan};

/// The directory of application mailers.
pub const DIR: &str = "api/src/mail";
/// The generator-owned mail module list.
pub const MODULE_FILE: &str = "api/src/mail/mod.rs";

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static MAILER: Generator = Generator {
    name: "mailer",
    aliases: &[],
    short: "Generate a typed mailer and email templates",
    usage: "<name> [field:type...]",
    args: |cmd| cmd,
    run,
};

fn run(input: &Input<'_>) -> Result<Output> {
    crate::job::require_module(input, "mail", "mailer")?;
    let Some((value, args)) = input.args.split_first() else {
        bail!(
            "cannot generate a mailer: expected a mailer name\nTry: spinup g mailer welcome name:string"
        );
    };
    let name = crate::job::item_name(value, "mailer")?;
    if matches!(
        name.snake.as_str(),
        "mailers" | "templates" | "send" | "new" | "worker" | "password_reset"
    ) {
        bail!(
            "invalid mailer name {value:?}: the name is reserved\nTry: spinup g mailer welcome name:string"
        );
    }
    let fields = fields::parse(args)?;
    let mut seen = BTreeSet::new();
    for field in &fields {
        if !seen.insert(field.name.pascal()) {
            bail!(
                "cannot generate a mailer: the field {} is duplicated\nTry: use fields with distinct Rust names",
                field.name.pascal()
            );
        }
    }
    let mut names = crate::job::scan(input.root, DIR)?;
    crate::job::check_collision(&names, &name, "mailer")?;
    names.insert(name.snake.clone());
    let context = serde_json::json!({
        "snake": name.snake,
        "pascal": name.pascal,
        "fields": fields.iter().map(|f| serde_json::json!({"name": f.name.snake(), "rust": f.rust_type()})).collect::<Vec<_>>(),
    });
    let mut plan = Plan::new();
    plan.add(File {
        path: format!("{DIR}/{}.rs", name.snake),
        data: crate::job::render(
            "mailer",
            include_str!("../../templates/mailer/mailer.rs.j2"),
            &context,
        )?,
        class: "managed".into(),
        ..File::default()
    })?;
    plan.add(File {
        path: MODULE_FILE.into(),
        data: module_source(&names)?.into_bytes(),
        class: "managed".into(),
        ..File::default()
    })?;
    let mut body = "Hello".to_string();
    for field in &fields {
        body.push_str(&format!(" {{{{ {} }}}}", field.name.snake()));
    }
    for (suffix, data) in [
        ("subject", format!("{}\n", name.pascal)),
        ("txt", format!("{body}\n")),
        ("html", format!("<p>{body}</p>\n")),
    ] {
        plan.add(File {
            path: format!("api/templates/mail/{}.{suffix}.tmpl", name.snake),
            data: data.into_bytes(),
            class: "seed".into(),
            ..File::default()
        })?;
    }
    Ok(Output {
        plan: plan.sorted(),
        next: vec!["cd api && cargo test --locked mail".into()],
        ..Output::default()
    })
}

/// Returns the stable module list that the mail pack also ships when empty.
/// The pack supplies `mailers::Mailers` and the optional jobs worker.
pub fn module_source(names: &BTreeSet<String>) -> Result<String> {
    crate::job::render(
        "mail modules",
        include_str!("../../templates/mailer/mod.rs.j2"),
        &serde_json::json!({"names": names}),
    )
    .and_then(|data| String::from_utf8(data).map_err(Into::into))
}
