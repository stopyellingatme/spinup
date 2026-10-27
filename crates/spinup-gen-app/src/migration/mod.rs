//! Empty SQL migration pairs and the database post-steps.

#[cfg(test)]
mod tests;

use anyhow::{Result, bail};
use spinup_gen::{
    contract::{Generator, Input, Output, PostStep},
    run::{StepInput, StepRunner},
};
use spinup_vfs::plan::{File, Plan};

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static MIGRATION: Generator = Generator {
    name: "migration",
    aliases: &[],
    short: "Generate an empty SQL migration pair",
    usage: "<snake_name>",
    args: |cmd| cmd,
    run,
};

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::run::POST_STEPS)]
static MIGRATE: StepRunner = StepRunner {
    step: PostStep::Migrate,
    run: migrate,
    retry: |_| "spinup db migrate up".into(),
};

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::run::POST_STEPS)]
static PREPARE: StepRunner = StepRunner {
    step: PostStep::SqlxPrepare,
    run: prepare,
    retry: |_| "spinup db prepare".into(),
};

fn run(input: &Input<'_>) -> Result<Output> {
    let [name] = input.args else {
        bail!("expected one migration name\nTry: spinup g migration add_index_to_todos");
    };
    let dir = spinup_db::names::MIGRATIONS_DIR;
    let (up, down) = spinup_db::names::next_migration_name(&input.root.join(dir), name)
        .map_err(database_error)?;
    let mut plan = Plan::new();
    for (name, data) in [
        (&up, "-- Write the SQL for this migration here.\n"),
        (
            &down,
            "-- Write the SQL that reverses the up migration here.\n",
        ),
    ] {
        plan.add(File {
            path: format!("{dir}/{name}"),
            data: data.as_bytes().to_vec(),
            class: "seed".into(),
            ..File::default()
        })?;
    }
    Ok(Output {
        plan: plan.sorted(),
        next: vec![format!("edit {dir}/{up}"), "spinup db migrate up".into()],
        ..Output::default()
    })
}

fn migrate(input: &StepInput<'_>) -> Result<()> {
    spinup_db::migrate::up(
        &mut spinup_db::ops::Options {
            dir: input.root.into(),
            ..Default::default()
        },
        input.runner,
        input.cancel,
    )
    .map_err(database_error)
}

fn prepare(input: &StepInput<'_>) -> Result<()> {
    spinup_db::prepare::run(
        &mut spinup_db::ops::Options {
            dir: input.root.into(),
            ..Default::default()
        },
        input.runner,
        input.cancel,
    )
    .map_err(database_error)
}

fn database_error(error: anyhow::Error) -> anyhow::Error {
    let parts = spinup_ui::error::parts(&error);
    let hint = if parts.try_hint.is_empty() {
        "check the database configuration and repeat the command"
    } else {
        &parts.try_hint
    };
    anyhow::anyhow!("{error}\nTry: {hint}")
}
