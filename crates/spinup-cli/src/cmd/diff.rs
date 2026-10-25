//! `spinup diff`: compare tracked files with their templates.

use anyhow::Result;
use clap::{Arg, ArgAction, ArgMatches, Command};
use spinup_drift::diff::{self, Options};

use crate::root::{App, COMMANDS, CommandDef, Group};
use crate::version::VERSION;

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static DIFF: CommandDef = CommandDef {
    name: "diff",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    Command::new("diff")
        .about("Compare tracked files with their templates")
        .arg(
            Arg::new("path")
                .value_name("path")
                .num_args(0..)
                .action(ArgAction::Append),
        )
}

/// The drift options of the global flags (Go `driftOptions`).
pub(super) fn options(app: &App) -> Options {
    Options {
        dir: app.flags.dir.clone().into(),
        version: VERSION.into(),
        offline: app.flags.offline,
        json: app.flags.json,
        dry_run: app.flags.dry_run,
        source: None,
    }
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let paths: Vec<String> = matches
        .get_many::<String>("path")
        .map(|values| values.cloned().collect())
        .unwrap_or_default();
    diff::diff(&options(app), &paths, &app.ui)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_many_paths() {
        let matches = build().try_get_matches_from(["diff", "a", "b/c"]).unwrap();
        assert_eq!(matches.get_many::<String>("path").unwrap().count(), 2);
        assert!(build().try_get_matches_from(["diff"]).is_ok());
    }
}
