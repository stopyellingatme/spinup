//! `spinup generate` and the generator picker.

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_gen::{contract::Generator, picker, registry::Registry, run as genrun};
use spinup_ui::{error::Error as UiError, exit};

use super::new::boolean;
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static GENERATE: CommandDef = CommandDef {
    name: "generate",
    group: Group::Develop,
    build,
    run,
};

fn generator_command(generator: &Generator) -> Command {
    let mut command = (generator.args)(
        Command::new(generator.name)
            .aliases(generator.aliases.iter().copied())
            .about(generator.short),
    );
    if command.get_positionals().next().is_none() {
        command = command.arg(Arg::new("args").num_args(0..));
    }
    command
}

fn positional_args(generator: &Generator, matches: &ArgMatches) -> Vec<String> {
    let mut command = generator_command(generator);
    command.build();
    let mut positionals: Vec<_> = command.get_positionals().collect();
    positionals.sort_by_key(|arg| arg.get_index());
    positionals
        .into_iter()
        .flat_map(|arg| {
            matches
                .get_raw(arg.get_id().as_str())
                .into_iter()
                .flatten()
                .map(|value| value.to_string_lossy().into_owned())
        })
        .collect()
}

fn build() -> Command {
    let mut command = Command::new("generate")
        .visible_alias("g")
        .about("Generate application files")
        .arg(Arg::new("unknown-kind"))
        .arg(boolean("diff", "show unified diffs without writing", false).global(true))
        .arg(boolean("force", "overwrite files with different content", false).global(true));
    if let Ok(registry) = Registry::builtin() {
        for generator in registry.all() {
            command = command.subcommand(generator_command(generator));
        }
    }
    command
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let registry = Registry::builtin()?;
    if let Some((name, sub)) = matches.subcommand() {
        let generator = registry
            .lookup(name)
            .ok_or_else(|| picker::unknown_kind(name))?;
        return generate(app, generator, sub, matches);
    }
    if let Some(kind) = matches.get_one::<String>("unknown-kind") {
        return Err(picker::unknown_kind(kind).into());
    }
    if !app.interactive() {
        app.ui.print(&picker::listing(registry.all()));
        return Err(picker::no_terminal().into());
    }
    let mut prompter = app.prompter();
    let (generator, args) = picker::pick(registry.all(), prompter.as_mut())?;
    let mut command = generator_command(generator)
        .arg(boolean(
            "diff",
            "show unified diffs without writing",
            matches.get_flag("diff"),
        ))
        .arg(boolean(
            "force",
            "overwrite files with different content",
            matches.get_flag("force"),
        ));
    let parsed = match command
        .try_get_matches_from_mut(std::iter::once(generator.name.to_string()).chain(args))
    {
        Ok(parsed) => parsed,
        Err(error) if error.kind() == clap::error::ErrorKind::DisplayHelp => {
            app.ui.print(&error.to_string());
            return Ok(());
        }
        Err(error) => {
            return Err(UiError::new("Invalid generator flags")
                .why(error.to_string())
                .try_hint(format!("spinup g {} --help", generator.name))
                .code(exit::USAGE)
                .into());
        }
    };
    generate(app, generator, &parsed, &parsed)
}

fn generate(
    app: &App,
    generator: &Generator,
    matches: &ArgMatches,
    policy: &ArgMatches,
) -> Result<()> {
    let root = spinup_project::root::find_root(&app.flags.dir).map_err(|error| {
        UiError::new("Cannot find the project")
            .why(error.to_string())
            .try_hint("run inside a spinup project, or pass --dir")
    })?;
    let args = positional_args(generator, matches);
    let mut out = super::upgrade::Output::new(&app.ui);
    let mut options = genrun::Options::new(&root, &mut out, app.runner.as_ref(), &app.cancel);
    options.dry_run = app.flags.dry_run;
    options.diff = policy.get_flag("diff");
    options.force = policy.get_flag("force");
    options.json = app.flags.json;
    let result = genrun::run(generator, &args, matches, options);
    result.map(|_| ())
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn generator_hook_controls_its_arguments() {
        use super::*;
        use spinup_gen::contract::Output;
        let generator = Generator {
            name: "example",
            aliases: &["e"],
            short: "Example generator",
            usage: "<name> [fields...]",
            args: |command| {
                command
                    .arg(Arg::new("name").required(true))
                    .arg(Arg::new("fields").num_args(0..))
                    .arg(Arg::new("tag").long("tag"))
            },
            run: |_| Ok(Output::default()),
        };
        let matches = generator_command(&generator)
            .try_get_matches_from(["example", "todo", "title:string", "--tag", "selected"])
            .unwrap();
        assert_eq!(
            positional_args(&generator, &matches),
            ["todo", "title:string"]
        );
        assert_eq!(matches.get_one::<String>("tag").unwrap(), "selected");
    }

    #[test]
    fn picker_requires_a_terminal() {
        for args in [vec!["g"], vec!["generate", "--non-interactive"]] {
            let (code, out, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{err}");
            assert!(out.contains("Available generators:"), "{out}");
            assert!(err.contains("Try: spinup g <kind> --help"), "{err}");
        }
    }

    #[test]
    fn picker_help_and_unknown_kind() {
        let (code, out, err) = run_args(&["g", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.contains("Generate application files"), "{out}");
        assert!(out.contains("generate"), "{out}");
        let (code, _, err) = run_args(&["g", "nope"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("nope"), "{err}");
        assert!(err.contains("Try:"), "{err}");
    }
}
