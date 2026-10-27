//! The model generator. It plans a migration, model queries, and module lists.

#[cfg(test)]
mod tests;

use anyhow::Result;
use spinup_gen::contract::{Generator, Input, Output, PostStep};

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static MODEL: Generator = Generator {
    name: "model",
    aliases: &[],
    short: "Generate a table: migration, model types, and queries",
    usage: "<name> <field:type>...",
    args: |cmd| cmd,
    run,
};

fn run(input: &Input<'_>) -> Result<Output> {
    let spec = crate::resource_core::parse_spec(
        input.root,
        input.lock,
        input.args,
        "model <name> <field:type>... (for example: spinup g model tag name:string)",
    )?;
    let force = input
        .matches
        .try_get_one::<bool>("force")
        .ok()
        .flatten()
        .copied()
        .unwrap_or(false);
    let mut plan = crate::resource_core::sql::plan_migration(&spec, force)?;
    plan.add(crate::resource_rs::model_file(&spec)?)?;
    let plan = crate::resource_core::modules::plan_module_lists(input.root, plan, &[])?;
    Ok(Output {
        plan,
        post_steps: vec![
            PostStep::Migrate,
            PostStep::SqlxPrepare,
            PostStep::Build,
            PostStep::OpenApiEmit,
        ],
        next: vec![
            "spinup db migrate up".into(),
            "cd api && cargo build".into(),
        ],
        ..Output::default()
    })
}
