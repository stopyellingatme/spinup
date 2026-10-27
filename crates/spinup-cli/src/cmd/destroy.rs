//! `spinup destroy`: remove unchanged generated files (Go `internal/cli/destroy.go`).
//!
//! This file only wires clap to `spinup_gen_app::destroy`.

use std::io::{self, Write};
use std::sync::{Mutex, PoisonError, mpsc};

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_gen_app::destroy;
use spinup_ui::error::Error as UiError;

use super::upgrade::Output;
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static DESTROY: CommandDef = CommandDef {
    name: "destroy",
    group: Group::Develop,
    build,
    run,
};

fn build() -> Command {
    Command::new("destroy")
        .about("Remove unchanged generated files")
        .arg(Arg::new("kind").value_name("kind").required(true))
        .arg(Arg::new("name").value_name("name").required(true))
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let text = |id: &str| matches.get_one::<String>(id).cloned().unwrap_or_default();
    let root = spinup_project::root::find_root(&app.flags.dir).map_err(|error| {
        UiError::new("Cannot find the project")
            .why(error.to_string())
            .try_hint("run inside a spinup project, or pass --dir")
    })?;
    let (sender, receiver) = mpsc::channel();
    // The library output needs a 'static writer. A channel moves the text to the printer,
    // so each line shows while the post-steps run.
    let channel: Mutex<ChannelWriter> = Mutex::new(ChannelWriter(sender));
    let options = destroy::Options {
        root,
        kind: text("kind"),
        name: text("name"),
        dry_run: app.flags.dry_run,
        out: Some(&channel),
        database_url: None,
    };
    let mut out = Output::new(&app.ui);
    let result = std::thread::scope(|scope| {
        let worker = scope.spawn(|| {
            let result = destroy::run(&options, app.runner.as_ref(), &app.cancel);
            // Close the channel, so the loop below ends.
            drop(std::mem::replace(
                &mut *channel.lock().unwrap_or_else(PoisonError::into_inner),
                ChannelWriter(mpsc::channel().0),
            ));
            result
        });
        for chunk in &receiver {
            let _ = out.write_all(&chunk);
        }
        worker
            .join()
            .unwrap_or_else(|panic| std::panic::resume_unwind(panic))
    });
    let _ = out.flush();
    result
}

/// A writer that sends each write to a channel.
struct ChannelWriter(mpsc::Sender<Vec<u8>>);

impl Write for ChannelWriter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.0
            .send(bytes.to_vec())
            .map_err(|_| io::Error::new(io::ErrorKind::BrokenPipe, "the printer stopped"))?;
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn needs_a_kind_and_a_name() {
        for args in [
            vec!["destroy"],
            vec!["destroy", "resource"],
            vec!["destroy", "resource", "todo", "extra"],
        ] {
            let (code, _, err) = run_args(&args);
            assert_eq!(code, exit::USAGE, "{args:?}: {err}");
            assert!(err.contains("Try:"), "{err}");
        }
    }

    #[test]
    fn help_names_the_command() {
        let (code, out, err) = run_args(&["destroy", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.contains("Remove unchanged generated files"), "{out}");
    }

    #[test]
    fn outside_a_project_fails_with_a_hint() {
        let dir = std::env::temp_dir().join("spinup-cli-destroy-no-project");
        let _ = std::fs::create_dir_all(&dir);
        let dir = dir.to_string_lossy().into_owned();
        let (code, _, err) = run_args(&["-C", &dir, "destroy", "resource", "todo"]);
        assert_ne!(code, exit::OK);
        assert!(err.contains("Cannot find the project"), "{err}");
        assert!(err.contains("Try:"), "{err}");
    }
}
