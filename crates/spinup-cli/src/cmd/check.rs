//! `spinup check`: report application drift for CI.

use anyhow::Result;
use clap::{ArgMatches, Command};
use spinup_drift::check;

use super::{diff::options, new::boolean};
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static CHECK: CommandDef = CommandDef {
    name: "check",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    Command::new("check")
        .about("Check application drift for CI")
        .arg(boolean(
            "allow-behind",
            "allow a newer loadable template version",
            false,
        ))
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    check::check(&options(app), matches.get_flag("allow-behind"), &app.ui)
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn rejects_positional_arguments() {
        let (code, _, err) = run_args(&["check", "extra"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn help_lists_the_flag() {
        let (code, out, err) = run_args(&["check", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.contains("--allow-behind"), "{out}");
    }
}
