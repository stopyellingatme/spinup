//! `spinup eject`: take ownership of a managed file.

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_drift::eject;

use super::diff::options;
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static EJECT: CommandDef = CommandDef {
    name: "eject",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    Command::new("eject")
        .about("Take ownership of a managed file")
        .arg(Arg::new("path").value_name("path").required(true))
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let path = matches
        .get_one::<String>("path")
        .map(String::as_str)
        .unwrap_or_default();
    eject::eject(&options(app), path)
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn needs_exactly_one_path() {
        for args in [vec!["eject"], vec!["eject", "a", "b"]] {
            let (code, _, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{args:?}: {err}");
            assert!(err.contains("Try:"), "{err}");
        }
    }
}
