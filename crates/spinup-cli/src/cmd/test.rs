//! `spinup test`: run the application tests (Go `internal/cli/test.go`).
//!
//! This file only wires clap to `spinup_appcmd::apptest`. The Go flag `--race` is removed,
//! because Rust has no race detector (ADR 0021 §7).

use std::io::{self, Write};
use std::path::PathBuf;

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_appcmd::apptest;
use spinup_ui::error::Error as UiError;

use super::{new::boolean, upgrade::Output};
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static TEST: CommandDef = CommandDef {
    name: "test",
    group: Group::Develop,
    build,
    run,
};

fn build() -> Command {
    Command::new("test")
        .about("Run application tests")
        .arg(boolean("api", "run API tests (default)", true))
        .arg(
            Arg::new("run")
                .long("run")
                .help("run tests whose names match this filter"),
        )
        .arg(boolean(
            "cover",
            "report test coverage with cargo llvm-cov",
            false,
        ))
        .arg(boolean("web", "run web tests (M4)", false))
        .arg(boolean("e2e", "run end-to-end tests (M4)", false))
        .arg(boolean("contract", "run contract tests (M4)", false))
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let web = matches.get_flag("web");
    let e2e = matches.get_flag("e2e");
    let contract = matches.get_flag("contract");
    // The unavailable targets fail before the project search, as in Go.
    let root = if web || e2e || contract {
        PathBuf::new()
    } else {
        spinup_project::root::find_root(&app.flags.dir).map_err(|error| {
            UiError::new("Cannot find the project")
                .why(error.to_string())
                .try_hint("run inside a spinup project, or pass --dir")
        })?
    };
    let mut out = Output::new(&app.ui);
    let mut err = io::stderr();
    let mut options = apptest::Options {
        root,
        run: matches
            .get_one::<String>("run")
            .cloned()
            .unwrap_or_default(),
        cover: matches.get_flag("cover"),
        web,
        e2e,
        contract,
        dry_run: app.flags.dry_run,
        out: Some(&mut out),
        err_out: Some(&mut err),
    };
    let result = apptest::run(&app.cancel, &mut options, app.runner.as_ref());
    drop(options);
    let _ = out.flush();
    result
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn dry_run_prints_the_nextest_command() {
        let dir = std::env::temp_dir().join(format!("spinup-cli-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("spinup.toml"), "").unwrap();
        let path = dir.to_string_lossy().into_owned();
        for (args, want) in [
            (vec![], "cargo nextest run --locked\n"),
            (
                vec!["--api", "--run", "todo"],
                "cargo nextest run --locked todo\n",
            ),
            (
                vec!["--run", "todo", "--cover"],
                "cargo llvm-cov nextest --locked todo\n",
            ),
        ] {
            let mut argv = vec!["test", "--dir", path.as_str(), "--dry-run"];
            argv.extend(args);
            let (code, out, err) = run_args(&argv);
            assert_eq!(code, exit::OK, "{argv:?}: {err}");
            assert_eq!(out, want, "{argv:?}");
        }
    }

    #[test]
    fn race_is_not_a_flag() {
        let (code, _, err) = run_args(&["test", "--race"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn unavailable_targets_fail_with_a_hint() {
        for target in ["--web", "--e2e", "--contract"] {
            let (code, _, err) = run_args(&["test", target]);
            assert_ne!(code, exit::OK, "{target}");
            assert!(err.contains("M4"), "{target}: {err}");
            assert!(err.contains("Try:"), "{target}: {err}");
        }
    }

    #[test]
    fn outside_a_project_fails_with_a_hint() {
        let dir = std::env::temp_dir().join("spinup-cli-test-no-project");
        let _ = std::fs::create_dir_all(&dir);
        let dir = dir.to_string_lossy().into_owned();
        let (code, _, err) = run_args(&["-C", &dir, "test", "--dry-run"]);
        assert_ne!(code, exit::OK);
        assert!(err.contains("Cannot find the project"), "{err}");
        assert!(err.contains("Try:"), "{err}");
    }
}
