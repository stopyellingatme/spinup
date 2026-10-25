//! `spinup module`: module addition and recovery wiring.

use anyhow::{Result, anyhow};
use clap::{Arg, ArgMatches, Command};
use spinup_ui::{error::Error as UiError, exit};
use spinup_upgrade::module::ModuleOptions;

use super::{new, upgrade};
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static MODULE: CommandDef = CommandDef {
    name: "module",
    group: Group::Develop,
    build,
    run,
};

fn build() -> Command {
    Command::new("module")
        .about("List and enable built-in modules")
        .subcommand(Command::new("list").about("List modules and their enabled state"))
        .subcommand(new::question_flags(
            upgrade::recovery_flags(
                Command::new("add")
                    .about("Enable modules on a branch at the locked template version")
                    .arg(Arg::new("names").value_name("name").num_args(0..)),
            ),
            true,
        ))
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    match matches.subcommand() {
        Some(("add", sub)) => add(app, sub),
        Some(("list", _)) => list(app),
        _ => {
            app.print_help(&["module"]);
            Ok(())
        }
    }
}

fn list(app: &App) -> Result<()> {
    let root = spinup_project::root::find_root(&app.flags.dir).map_err(|error| {
        anyhow!("list modules: {error}\nTry: run inside a spinup project or pass --dir")
    })?;
    let config = spinup_project::config::load(root)
        .map_err(|error| anyhow!("list modules: {error}\nTry: correct spinup.toml"))?;
    app.ui.print(&spinup_render::modules::list(
        Some(&config),
        app.flags.json,
    )?);
    Ok(())
}

fn add(app: &App, matches: &ArgMatches) -> Result<()> {
    let names: Vec<String> = matches
        .get_many::<String>("names")
        .map(|v| v.cloned().collect())
        .unwrap_or_default();
    let resume = matches.get_flag("continue");
    let abort = matches.get_flag("abort");
    if (resume && abort) || ((resume || abort) != names.is_empty()) {
        return Err(UiError::new("Invalid module arguments")
            .why("supply module names, --continue, or --abort")
            .try_hint("spinup module add --help")
            .code(exit::USAGE)
            .into());
    }
    let options = upgrade::options(app, matches);
    let answers = new::answers(matches, true)?;
    let mut prompter = app.prompter();
    upgrade::with_runner(app, matches, |runner| {
        if resume {
            runner.continue_upgrade(options)
        } else if abort {
            runner.abort(options)
        } else {
            runner.add_modules(ModuleOptions {
                names,
                answers,
                prompter: Some(prompter.as_mut()),
                options,
            })
        }
    })
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn module_list_loads_the_project_from_a_child_path() {
        let dir = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../testdata/golden/base/README.md"
        );
        let (code, out, err) = run_args(&["module", "list", "--dir", dir]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(err.is_empty(), "{err}");
        assert!(out.starts_with("NAME"), "{out}");
        let rows: Vec<Vec<&str>> = out
            .lines()
            .map(|line| line.split_whitespace().collect())
            .collect();
        assert!(rows.iter().any(|row| row[0] == "ci" && row[1] == "on"));
        assert!(rows.iter().any(|row| row[0] == "deploy" && row[1] == "on"));
        assert!(rows.iter().any(|row| row[0] == "jobs" && row[1] == "off"));

        let (code, out, err) = run_args(&["module", "list", "--dir", dir, "--json"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(err.is_empty(), "{err}");
        assert_eq!(out.lines().count(), 1);
        assert!(out.ends_with('\n'));
        let rows: Vec<serde_json::Value> = serde_json::from_str(&out).unwrap();
        assert!(
            rows.iter()
                .any(|row| row["name"] == "ci" && row["enabled"] == true)
        );
        assert!(
            rows.iter()
                .any(|row| row["name"] == "jobs" && row["enabled"] == false)
        );
    }

    #[test]
    fn module_list_reports_project_lookup_failure() {
        let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/../../testdata/golden");
        let (code, out, err) = run_args(&["module", "list", "--dir", dir]);
        assert_eq!(code, exit::ERROR);
        assert!(out.is_empty(), "{out}");
        assert!(
            err.contains("list modules: could not find spinup.toml"),
            "{err}"
        );
        assert!(
            err.ends_with("Try: run inside a spinup project or pass --dir\n"),
            "{err}"
        );
        assert_eq!(err.matches("Try:").count(), 1, "{err}");
    }

    #[test]
    fn module_guards_and_help() {
        for args in [
            vec!["module", "add"],
            vec!["module", "add", "jobs", "--continue"],
            vec!["module", "add", "--continue", "--abort"],
            vec!["module", "list", "extra"],
        ] {
            let (code, _, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{args:?}: {err}");
            assert!(err.contains("Try:"), "{err}");
        }
        let (code, out, err) = run_args(&["module", "add", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        for flag in [
            "--continue",
            "--abort",
            "--monitoring-profile",
            "--ci-deploy-on-push",
            "--yes",
            "--non-interactive",
        ] {
            assert!(out.contains(flag), "{flag}: {out}");
        }
    }
}
