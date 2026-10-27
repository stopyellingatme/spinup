//! Plain axum handlers and the shared module lists.

#[cfg(test)]
mod tests;

use anyhow::{Result, bail};
use spinup_gen::contract::{Generator, Input, Output};
use spinup_vfs::plan::{File, Plan};

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static HANDLER: Generator = Generator {
    name: "handler",
    aliases: &[],
    short: "Generate a plain HTTP handler and its test",
    usage: "<name>",
    args: |cmd| cmd,
    run,
};

pub(crate) fn name(value: &str) -> Result<crate::job::Name> {
    if !value
        .as_bytes()
        .first()
        .is_some_and(u8::is_ascii_alphabetic)
        || value.split(['_', '-']).any(str::is_empty)
    {
        bail!(
            "handler name {value:?} is not valid: use letters, digits, and underscores, and start with a letter\nTry: choose a name such as ping or health_report"
        );
    }
    crate::job::item_name(value, "handler")
}

fn run(input: &Input<'_>) -> Result<Output> {
    let [value] = input.args else {
        bail!("expected one handler name\nTry: spinup g handler ping");
    };
    let name = name(value)?;
    match std::fs::metadata(input.root.join("api/Cargo.toml")) {
        Ok(meta) if meta.is_file() => {}
        Ok(_) => bail!(
            "cannot generate a handler: api/Cargo.toml is not a file\nTry: run the generator in a spinup project with an API"
        ),
        Err(error) => bail!(
            "read api/Cargo.toml: {error}\nTry: run the generator in a spinup project with an API"
        ),
    }
    let mut plan = Plan::new();
    for (suffix, template) in [
        (".rs", include_str!("../../templates/handler/handler.rs.j2")),
        (
            "_tests.rs",
            include_str!("../../templates/handler/tests.rs.j2"),
        ),
    ] {
        plan.add(File {
            path: format!("api/src/handlers/{}{suffix}", name.snake),
            data: crate::job::render("handler", template, &name)?,
            class: "scaffold".into(),
            ..File::default()
        })?;
    }
    Ok(Output {
        plan: crate::resource_core::modules::plan_module_lists(input.root, plan, &[])?,
        next: vec![format!("cd api && cargo test --locked {}", name.snake)],
        ..Output::default()
    })
}
