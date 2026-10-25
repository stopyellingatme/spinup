//! `spinup restore`: restore a tracked file from its template.

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_drift::restore;

use super::{diff::options, new::boolean};
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static RESTORE: CommandDef = CommandDef {
    name: "restore",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    Command::new("restore")
        .about("Restore a tracked file from its template")
        .arg(Arg::new("path").value_name("path").required(true))
        .arg(boolean("force", "replace an existing edited file", false))
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let path = matches
        .get_one::<String>("path")
        .map(String::as_str)
        .unwrap_or_default();
    restore::restore(&options(app), path, matches.get_flag("force"))
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn needs_exactly_one_path() {
        for args in [vec!["restore"], vec!["restore", "a", "b"]] {
            let (code, _, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{args:?}: {err}");
            assert!(err.contains("Try:"), "{err}");
        }
    }

    #[test]
    fn help_lists_the_flag() {
        let (code, out, err) = run_args(&["restore", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.contains("--force"), "{out}");
    }
}
