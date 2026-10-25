//! `spinup upgrade`: modes, extension flags, and engine wiring.

use std::io::{self, Write};

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_ui::{error::Error as UiError, exit, printer::Printer};
use spinup_upgrade::{
    contract::{EXTENSIONS, Options},
    run::Runner,
};

use super::new::boolean;
use crate::root::{App, COMMANDS, CommandDef, Group};
use crate::version::VERSION;

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static UPGRADE: CommandDef = CommandDef {
    name: "upgrade",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    let mut command = recovery_flags(
        Command::new("upgrade").about("Upgrade app templates in a separate Git worktree"),
    )
    .arg(
        Arg::new("to")
            .long("to")
            .help("target template version (default newest loadable)"),
    )
    .arg(boolean(
        "plan",
        "print the plan without changing project files",
        false,
    ))
    .arg(boolean(
        "allow-dirty",
        "allow changes in the main checkout",
        false,
    ))
    .arg(boolean(
        "force-managed",
        "overwrite changed managed files",
        false,
    ));
    let mut extensions: Vec<_> = EXTENSIONS.iter().collect();
    extensions.sort_by_key(|extension| extension.name);
    for extension in extensions {
        command = (extension.args)(command);
    }
    command
}

/// Adds the common upgrade and module recovery flags.
pub(super) fn recovery_flags(command: Command) -> Command {
    command
        .arg(boolean(
            "continue",
            "continue after resolving conflicts",
            false,
        ))
        .arg(boolean(
            "abort",
            "remove the recorded worktree and branch",
            false,
        ))
        .arg(boolean(
            "no-verify",
            "skip verification in the worktree",
            false,
        ))
        .arg(boolean(
            "commit-conflicts",
            "commit conflict markers and continue",
            false,
        ))
}

/// Reads upgrade policy. Module commands omit the upgrade-only flags.
pub(super) fn options(app: &App, matches: &ArgMatches) -> Options {
    let flag = |name| {
        matches
            .try_get_one::<bool>(name)
            .ok()
            .flatten()
            .copied()
            .unwrap_or(false)
    };
    Options {
        to: matches
            .try_get_one::<String>("to")
            .ok()
            .flatten()
            .filter(|value| !value.is_empty())
            .cloned(),
        allow_dirty: flag("allow-dirty"),
        force_managed: flag("force-managed"),
        no_verify: flag("no-verify"),
        commit_conflicts: flag("commit-conflicts"),
        yes: app.flags.yes,
        offline: app.flags.offline,
        dry_run: app.flags.dry_run,
    }
}

/// Runs a command through the real template source and upgrade runner.
pub(super) fn with_runner(
    app: &App,
    matches: &ArgMatches,
    run: impl FnOnce(&mut Runner<'_>) -> Result<()>,
) -> Result<()> {
    let root = spinup_project::root::find_root(&app.flags.dir)?;
    let source = spinup_tplstore::store::default(VERSION, app.flags.offline)?;
    let merger = spinup_merge::git::new_git(app.runner.as_ref());
    let mut out = Output::new(&app.ui);
    let mut prompter = app.prompter();
    let mut runner = Runner::new(
        root,
        &source,
        app.runner.as_ref(),
        &merger,
        &app.cancel,
        &mut out,
    );
    runner.interactive = app.interactive();
    runner.prompter = Some(prompter.as_mut());
    runner.cli_version = VERSION.into();
    runner.matches = Some(matches);
    run(&mut runner)
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let plan = matches.get_flag("plan");
    let resume = matches.get_flag("continue");
    let abort = matches.get_flag("abort");
    if [plan, resume, abort]
        .into_iter()
        .filter(|selected| *selected)
        .count()
        > 1
    {
        return Err(UiError::new("Invalid upgrade flags")
            .why("--plan, --continue, and --abort are mutually exclusive")
            .try_hint("select only one upgrade mode")
            .code(exit::USAGE)
            .into());
    }
    let options = options(app, matches);
    with_runner(app, matches, |runner| {
        if plan || (options.dry_run && !resume && !abort) {
            runner.plan(options).map(|_| ())
        } else if resume {
            runner.continue_upgrade(options)
        } else if abort {
            runner.abort(options)
        } else {
            runner.run(options)
        }
    })
}

/// Forwards progress before the runner asks for confirmation.
pub(super) struct Output<'a> {
    printer: &'a Printer,
    pending: Vec<u8>,
}

impl<'a> Output<'a> {
    pub(super) fn new(printer: &'a Printer) -> Self {
        Self {
            printer,
            pending: Vec::new(),
        }
    }
}

impl Write for Output<'_> {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.pending.extend_from_slice(bytes);
        match std::str::from_utf8(&self.pending) {
            Ok(text) => {
                self.printer.print(text);
                self.pending.clear();
            }
            Err(error) if error.error_len().is_none() => {
                let valid = error.valid_up_to();
                self.printer
                    .print(&String::from_utf8_lossy(&self.pending[..valid]));
                self.pending.drain(..valid);
            }
            Err(_) => {
                self.printer.print(&String::from_utf8_lossy(&self.pending));
                self.pending.clear();
            }
        }
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        self.printer.print(&String::from_utf8_lossy(&self.pending));
        self.pending.clear();
        Ok(())
    }
}

impl Drop for Output<'_> {
    fn drop(&mut self) {
        let _ = self.flush();
    }
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn upgrade_guards_and_help() {
        for args in [
            vec!["upgrade", "--plan", "--continue"],
            vec!["upgrade", "--plan", "--abort"],
            vec!["upgrade", "--continue", "--abort"],
            vec!["upgrade", "extra"],
        ] {
            let (code, _, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{args:?}: {err}");
            assert!(err.contains("Try:"), "{err}");
        }
        let (code, out, err) = run_args(&["upgrade", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        for flag in [
            "--to",
            "--plan",
            "--continue",
            "--abort",
            "--allow-dirty",
            "--force-managed",
            "--no-verify",
            "--commit-conflicts",
            "--yes",
            "--offline",
            "--ui",
        ] {
            assert!(out.contains(flag), "{flag}: {out}");
        }
    }
}
