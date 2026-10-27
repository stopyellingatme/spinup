//! `spinup db`: the database commands (Go `internal/cli/db*.go`, ADR 0021 §6).
//!
//! This file only wires clap to `spinup_db`. `db sqlc` of Go is `db prepare` in Rust.

use std::io::{self, Read, Write};
use std::path::PathBuf;

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_db::{lint, migrate, ops, prepare};

use super::{new::boolean, upgrade::Output};
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static DB: CommandDef = CommandDef {
    name: "db",
    group: Group::Data,
    build,
    run,
};

fn i_know(help: &'static str) -> Arg {
    boolean("i-know", help, false)
}

fn build() -> Command {
    Command::new("db")
        .about("Operate the application database")
        .subcommand(
            Command::new("migrate")
                .about("Run the kit migrations, then the app migrations, with sqlx-cli")
                .arg(Arg::new("sub").value_name("up|down|status"))
                .arg(i_know("acknowledge that down can lose data")),
        )
        .subcommand(
            Command::new("new")
                .about("Create an empty reversible SQL migration")
                .arg(Arg::new("name").value_name("name").required(true)),
        )
        .subcommand(
            Command::new("lint")
                .about("Check migrations for unsafe deploy changes")
                .arg(Arg::new("files").value_name("file").num_args(0..)),
        )
        .subcommand(Command::new("status").about("List applied and pending migrations"))
        .subcommand(
            Command::new("redo")
                .about("Roll back the last migration and apply it again")
                .arg(i_know("acknowledge that redo can lose data")),
        )
        .subcommand(
            Command::new("to")
                .about("Migrate up or down to a version")
                .arg(Arg::new("version").value_name("version").required(true))
                .arg(i_know("acknowledge that a move down can lose data")),
        )
        .subcommand(
            Command::new("prepare")
                .about("Migrate up, then refresh the SQLx query cache in api/.sqlx"),
        )
        .subcommand(Command::new("psql").about("Open psql on the development database"))
        .subcommand(
            Command::new("dump")
                .about(format!(
                    "Dump the development database (default {})",
                    ops::DEFAULT_DUMP_FILE
                ))
                .arg(Arg::new("file").value_name("file")),
        )
        .subcommand(
            Command::new("restore")
                .about("Restore a dump into the development database")
                .arg(Arg::new("file").value_name("file").required(true)),
        )
        .subcommand(Command::new("seed").about("Run the seed files in the development database"))
        .subcommand(Command::new("reset").about("Recreate the development database and migrate up"))
        .subcommand(
            Command::new("doctor").about("Check the PostgreSQL version and pending migrations"),
        )
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let Some((name, sub)) = matches.subcommand() else {
        app.print_help(&["db"]);
        return Ok(());
    };
    if name == "lint" {
        return lint_cmd(app, sub);
    }
    let know = sub
        .try_get_one::<bool>("i-know")
        .ok()
        .flatten()
        .copied()
        .unwrap_or(false);
    let text = |id: &str| {
        sub.try_get_one::<String>(id)
            .ok()
            .flatten()
            .cloned()
            .unwrap_or_default()
    };
    let mut out = Output::new(&app.ui);
    let mut err = io::stderr();
    let mut input = io::stdin();
    let mut o = options(app, know, &mut out, &mut err, &mut input);
    let runner = app.runner.as_ref();
    let cancel = &app.cancel;
    let result = match name {
        "migrate" => text("sub")
            .parse::<migrate::Sub>()
            .and_then(|op| migrate::run(&mut o, op, runner, cancel)),
        "new" => ops::new(&mut o, &text("name")),
        "status" => ops::status(&mut o, runner, cancel),
        "redo" => ops::redo(&mut o, runner, cancel),
        "to" => ops::to(&mut o, runner, cancel, &text("version")),
        "prepare" => prepare::run(&mut o, runner, cancel),
        "psql" => ops::psql(&mut o, runner, cancel),
        "dump" => ops::dump(&mut o, runner, cancel, &text("file")),
        "restore" => ops::restore(&mut o, runner, cancel, &text("file")),
        "seed" => ops::seed(&mut o, runner, cancel),
        "reset" => ops::reset(&mut o, runner, cancel),
        "doctor" => ops::doctor(&mut o, runner, cancel),
        _ => {
            app.print_help(&["db"]);
            Ok(())
        }
    };
    drop(o);
    let _ = out.flush();
    result
}

/// Maps the global flags to the options of a database operation (Go `dbOptions`).
fn options<'a>(
    app: &App,
    i_know: bool,
    out: &'a mut (dyn Write + Send),
    err: &'a mut (dyn Write + Send),
    input: &'a mut dyn Read,
) -> ops::Options<'a> {
    let interactive = app.interactive();
    ops::Options {
        dir: PathBuf::from(&app.flags.dir),
        env: app.flags.env.clone(),
        i_know,
        interactive,
        yes: app.flags.yes,
        ci: std::env::var_os("CI").is_some_and(|value| !value.is_empty()),
        confirm_name: app.flags.confirm_env.clone(),
        dry_run: app.flags.dry_run,
        input: interactive.then_some(input),
        out: Some(out),
        err_out: Some(err),
        lookup: None,
    }
}

fn lint_cmd(app: &App, matches: &ArgMatches) -> Result<()> {
    let files: Vec<PathBuf> = matches
        .get_many::<String>("files")
        .map(|values| values.map(PathBuf::from).collect())
        .unwrap_or_default();
    let dir = if app.flags.dir.is_empty() {
        PathBuf::from(".")
    } else {
        PathBuf::from(&app.flags.dir)
    };
    let mut out = Output::new(&app.ui);
    let result = lint::run(&dir, &files, app.flags.json, &mut out);
    let _ = out.flush();
    result
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn help_lists_the_subcommands() {
        let (code, out, err) = run_args(&["db", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        for name in [
            "migrate", "new", "lint", "status", "redo", "to", "prepare", "psql", "dump", "restore",
            "seed", "reset", "doctor",
        ] {
            assert!(out.contains(name), "{name}: {out}");
        }
        assert!(!out.contains("sqlc"), "{out}");
        let (code, out, err) = run_args(&["db"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.contains("Operate the application database"), "{out}");
    }

    #[test]
    fn arguments_are_checked() {
        for args in [
            vec!["db", "new"],
            vec!["db", "new", "a", "b"],
            vec!["db", "to"],
            vec!["db", "restore"],
            vec!["db", "status", "extra"],
            vec!["db", "dump", "a", "b"],
        ] {
            let (code, _, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{args:?}: {err}");
            assert!(err.contains("Try:"), "{err}");
        }
    }

    #[test]
    fn data_loss_needs_i_know() {
        // `db to` reads the migration files first to find the direction. The script
        // `db_cmds` checks its guard in a project.
        for (args, hint) in [
            (
                vec!["db", "redo", "--dry-run"],
                "Try: spinup db redo --i-know",
            ),
            (
                vec!["db", "migrate", "down", "--dry-run"],
                "Try: spinup db migrate down --i-know",
            ),
        ] {
            let (code, _, err) = run_args(&args);
            assert_ne!(code, exit::OK, "{args:?}");
            assert!(err.contains(hint), "{args:?}: {err}");
        }
    }

    #[test]
    fn unknown_migrate_command_is_a_usage_error() {
        let (code, _, err) = run_args(&["db", "migrate", "sideways"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("unknown migration command sideways"), "{err}");
        assert!(err.contains("Try: spinup db migrate --help"), "{err}");
    }

    #[test]
    fn commands_outside_a_project_fail_with_a_hint() {
        let dir = std::env::temp_dir().join("spinup-cli-db-no-project");
        let _ = std::fs::create_dir_all(&dir);
        let dir = dir.to_string_lossy().into_owned();
        for args in [
            vec!["-C", dir.as_str(), "db", "new", "add_widgets"],
            vec!["-C", dir.as_str(), "db", "lint"],
            vec!["-C", dir.as_str(), "db", "status", "--dry-run"],
        ] {
            let (code, _, err) = run_args(&args);
            assert_ne!(code, exit::OK, "{args:?}");
            assert!(err.contains("Try:"), "{args:?}: {err}");
        }
    }
}
