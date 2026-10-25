//! `spinup new`: flags and prompts for application creation.

use anyhow::Result;
use clap::{Arg, ArgAction, ArgMatches, Command, parser::ValueSource};
use serde_json::{Map, Value};
use spinup_newapp::{lockfile, questions, select};
use spinup_packs::loader::Question;

use crate::root::{App, COMMANDS, CommandDef, Group};
use crate::version::VERSION;

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static NEW: CommandDef = CommandDef {
    name: "new",
    group: Group::Develop,
    build,
    run,
};

fn build() -> Command {
    question_flags(
        Command::new("new")
            .about("Create an application from template packs")
            .arg(Arg::new("name").required(true))
            .arg(
                Arg::new("plugin")
                    .long("plugin")
                    .action(ArgAction::Append)
                    .help("add a trusted pack source with optional @version (repeatable)"),
            )
            .arg(boolean("trust", "trust plugin sources and hashes", false))
            .arg(boolean("api-only", "create only the Go API", false))
            .arg(boolean(
                "no-git",
                "do not initialize a git repository",
                false,
            )),
        false,
    )
}

/// Builds a boolean flag with the pflag value forms.
pub(super) fn boolean(name: &str, help: &str, default: bool) -> Arg {
    Arg::new(name.to_string())
        .long(name.to_string())
        .help(help.to_string())
        .num_args(0..=1)
        .require_equals(true)
        .default_missing_value("true")
        .default_value(if default { "true" } else { "false" })
        .value_parser(|value: &str| match value {
            "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
            "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
            _ => Err(format!("invalid boolean value {value:?}")),
        })
        .action(ArgAction::Set)
        .hide_default_value(true)
        .hide_possible_values(true)
}

fn catalog_questions() -> Result<Vec<Question>> {
    questions::questions(&select::catalog()?)
}

/// Adds flags for public pack questions. Execution reports a catalog error.
pub(super) fn question_flags(mut command: Command, module_add: bool) -> Command {
    if let Ok(questions) = catalog_questions() {
        for q in questions {
            if q.key == "app_name" || (module_add && q.key == "modules") {
                continue;
            }
            let name = q.key.replace('_', "-");
            command = command.arg(if q.r#type == "bool" {
                boolean(&name, &q.prompt, q.default == "true")
            } else {
                Arg::new(name.clone())
                    .long(name)
                    .help(q.prompt)
                    .default_value(q.default)
            });
        }
    }
    command
}

/// Reads explicit flags only. The library completes and validates the answers.
pub(super) fn answers(matches: &ArgMatches, module_add: bool) -> Result<Map<String, Value>> {
    let mut answers = Map::new();
    for q in catalog_questions()? {
        if q.key == "app_name" || (module_add && q.key == "modules") {
            continue;
        }
        let name = q.key.replace('_', "-");
        if matches.value_source(&name) != Some(ValueSource::CommandLine) {
            continue;
        }
        let value = if q.r#type == "bool" {
            Value::Bool(matches.get_flag(&name))
        } else {
            Value::String(
                matches
                    .get_one::<String>(&name)
                    .cloned()
                    .unwrap_or_default(),
            )
        };
        answers.insert(q.key, value);
    }
    Ok(answers)
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let options = select::Options {
        name: matches
            .get_one::<String>("name")
            .cloned()
            .unwrap_or_default(),
        dir: app.flags.dir.clone().into(),
        version: VERSION.into(),
        kit_replace: std::env::var_os("SPINUP_KIT_REPLACE")
            .unwrap_or_default()
            .into(),
        api_only: matches.get_flag("api-only"),
        no_git: matches.get_flag("no-git"),
        dry_run: app.flags.dry_run,
        json: app.flags.json,
        interactive: app.interactive(),
        answers: answers(matches, false)?,
        plugins: matches
            .get_many::<String>("plugin")
            .map(|v| v.cloned().collect())
            .unwrap_or_default(),
        trust: matches.get_flag("trust"),
        offline: app.flags.offline,
    };
    let mut prompter = app.prompter();
    let result = select::generate(&options, Some(prompter.as_mut()))?;
    lockfile::apply(&result, &options, app.runner.as_ref(), &app.cancel, &app.ui)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::root::tests::run_args;

    #[test]
    fn only_explicit_answers_override_library_defaults() {
        let matches = build()
            .try_get_matches_from([
                "new",
                "todo",
                "--module",
                "example.com/todo",
                "--ci-deploy-on-push=false",
            ])
            .unwrap();
        let answers = answers(&matches, false).unwrap();
        assert_eq!(answers["module"], "example.com/todo");
        assert_eq!(answers["ci_deploy_on_push"], false);
        assert!(!answers.contains_key("modules"));
    }

    #[test]
    fn every_question_has_a_flag() {
        let command = build();
        for q in catalog_questions().unwrap() {
            let name = q.key.replace('_', "-");
            assert_eq!(
                command.get_arguments().any(|a| a.get_long() == Some(&name)),
                q.key != "app_name",
                "{name}"
            );
        }
    }

    #[test]
    fn dry_run_reports_json_without_writes() {
        let parent = std::env::temp_dir().join(format!("spinup-r122-dry-{}", std::process::id()));
        let (code, out, err) = run_args(&[
            "new",
            "todo",
            "--dir",
            parent.to_str().unwrap(),
            "--api-only",
            "--module",
            "example.com/todo",
            "--no-git",
            "--non-interactive",
            "--json",
            "--dry-run",
        ]);
        assert_eq!(code, 0, "{err}");
        let value: Value = serde_json::from_str(&out).unwrap();
        assert_eq!(value["dir"], parent.join("todo").to_str().unwrap());
        assert_eq!(value["packs"].as_array().unwrap().len(), 4);
        assert!(value["files"].as_u64().unwrap() > 0);
        assert!(!parent.exists());
    }
}
