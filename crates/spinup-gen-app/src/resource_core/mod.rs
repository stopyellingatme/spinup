//! Resource specifications, migration SQL, module lists, and resource parts.

pub mod modules;
pub mod paths;
pub mod sql;

#[cfg(test)]
mod tests;

use std::path::Path;

use anyhow::{Result, bail};
use spinup_gen::{
    contract::{Generator, Input, Output, PostStep},
    fields::{self, Name},
    part::{Part, RESOURCE_PARTS, Spec},
};
use spinup_manifest::lock::Lock;

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static RESOURCE: Generator = Generator {
    name: "resource",
    aliases: &[],
    short: "Generate a CRUD API resource: migration, model, handlers, and tests",
    usage: "<name> <field:type>...",
    args: |mut cmd| {
        for (name, help) in [
            ("api-only", "skip resource parts, such as web pages"),
            ("no-tests", "skip the handler test file"),
            (
                "shared",
                "share rows between signed-in users (requires auth)",
            ),
        ] {
            cmd = cmd.arg(
                clap::Arg::new(name)
                    .long(name)
                    .action(clap::ArgAction::SetTrue)
                    .help(help),
            );
        }
        cmd
    },
    run,
};

/// Parses a resource name and fields against the app migrations.
/// Ownership defaults to false. The resource generator applies the auth flags.
///
/// # Errors
/// Rejects missing fields, reserved names, missing parents, and required self references.
pub fn parse_spec<'a>(
    root: &'a Path,
    lock: &'a Lock,
    args: &[String],
    usage: &str,
) -> Result<Spec<'a>> {
    if args.len() < 2 {
        bail!("expected a name and at least one field\nTry: spinup g {usage}");
    }
    let name = Name::parse(&args[0])?;
    if matches!(name.snake(), "doc" | "docs" | "openapi" | "openapis")
        || matches!(name.plural_snake(), "docs" | "openapi")
    {
        bail!(
            "resource name {:?} is reserved for the API reference\nTry: choose another name, for example document",
            args[0]
        );
    }
    let fields = fields::parse(&args[1..])?;
    for field in &fields {
        let reference = &field.r#type.reference;
        if reference.is_empty() {
            continue;
        }
        if reference == name.snake() {
            if !field.nullable {
                bail!(
                    "field {:?} refers to {} itself, so it must be nullable: the first row has nothing to refer to\nTry: use {}:ref:{}?",
                    field.name.snake(),
                    name.table(),
                    field.name.snake(),
                    reference
                );
            }
            continue;
        }
        let parent = Name::parse(reference)?;
        if sql::find_table(root, parent.table())?.is_none() {
            bail!(
                "field {:?} refers to the table {}, but no migration in {} creates it\nTry: spinup g model {} <field:type>... first",
                field.name.snake(),
                parent.table(),
                spinup_db::names::MIGRATIONS_DIR,
                parent.snake()
            );
        }
    }
    Ok(Spec {
        root,
        lock,
        name,
        fields,
        owned: false,
        shared: false,
    })
}

/// Returns the resource parts in name order.
///
/// # Errors
/// Rejects an empty name or a duplicate name with a retry hint.
pub fn registered_parts() -> Result<Vec<&'static Part>> {
    sorted_parts(RESOURCE_PARTS.iter())
}

fn sorted_parts<'a>(parts: impl IntoIterator<Item = &'a Part>) -> Result<Vec<&'a Part>> {
    let mut parts: Vec<_> = parts.into_iter().collect();
    parts.sort_by_key(|part| part.name);
    for (index, part) in parts.iter().enumerate() {
        if part.name.is_empty() {
            bail!("resource part has an empty name\nTry: give each resource part a non-empty name");
        }
        if index > 0 && parts[index - 1].name == part.name {
            bail!(
                "duplicate resource part {:?}\nTry: give each resource part a unique name",
                part.name
            );
        }
    }
    Ok(parts)
}

/// Returns sorted resource paths, including parts, without migrations or shared files.
///
/// # Errors
/// Returns an error if the resource part registry is invalid.
pub fn paths(name: &Name) -> Result<Vec<String>> {
    let mut paths = vec![
        paths::handlers_path(name),
        paths::handler_tests_path(name),
        paths::model_path(name),
    ];
    for part in registered_parts()? {
        if let Some(part_paths) = part.paths {
            paths.extend(part_paths(name));
        }
    }
    paths.sort();
    paths.dedup();
    Ok(paths)
}

fn flag(input: &Input<'_>, name: &str) -> bool {
    input
        .matches
        .try_get_one::<bool>(name)
        .ok()
        .flatten()
        .copied()
        .unwrap_or(false)
}

fn run(input: &Input<'_>) -> Result<Output> {
    build(input, &registered_parts()?)
}

fn build(input: &Input<'_>, parts: &[&Part]) -> Result<Output> {
    let mut spec = parse_spec(
        input.root,
        input.lock,
        input.args,
        "resource <name> <field:type>... (for example: spinup g resource todo title:string done:bool)",
    )?;
    let auth = input.lock.packs.iter().any(|pack| pack == "auth");
    spec.shared = flag(input, "shared");
    if spec.shared && !auth {
        bail!("cannot generate a shared resource without the auth module\nTry: drop --shared");
    }
    spec.owned = auth && !spec.shared;
    if spec.owned && spec.fields.iter().any(|field| field.column() == "user_id") {
        bail!(
            "field \"user_id\" conflicts with the resource owner column\nTry: choose another field name"
        );
    }
    let mut plan = sql::plan_migration(&spec, flag(input, "force"))?;
    let api = crate::resource_rs::plan_api(&spec, !flag(input, "no-tests"))?;
    plan = plan.merge(&api)?;
    let mut post_steps = vec![
        PostStep::Migrate,
        PostStep::SqlxPrepare,
        PostStep::Build,
        PostStep::OpenApiEmit,
    ];
    let mut pages_ran = false;
    if !flag(input, "api-only") {
        for part in sorted_parts(parts.iter().copied())? {
            let extra = (part.plan)(&spec).map_err(|error| part_error(part.name, error))?;
            plan = plan
                .merge(&extra.plan)
                .map_err(|error| part_error(part.name, error))?;
            post_steps.extend(extra.post_steps);
            pages_ran |= part.name == "pages";
        }
    }
    if pages_ran {
        plan = spinup_gen_web::nav::plan_nav(input.root, plan, &[])?;
    }
    plan = modules::plan_module_lists(input.root, plan, &[])?;
    Ok(Output {
        plan,
        post_steps,
        next: vec![
            "spinup db migrate up".into(),
            "cd api && cargo nextest run".into(),
        ],
        ..Output::default()
    })
}

fn part_error(name: &str, error: anyhow::Error) -> anyhow::Error {
    let error = spinup_ui::error::parts(&error);
    let hint = if error.try_hint.is_empty() {
        "check the resource part inputs"
    } else {
        &error.try_hint
    };
    let reason = if error.why.is_empty() {
        error.what
    } else {
        format!("{}: {}", error.what, error.why)
    };
    anyhow::anyhow!("resource part {name:?}: {reason}\nTry: {hint}")
}
