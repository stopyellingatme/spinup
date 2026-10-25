//! The external commands of a generator (Go `internal/gen/run/commands.go`).

use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::{Mutex, PoisonError};

use anyhow::anyhow;
use spinup_exec::runner::{Cancellation, Cmd, Runner, Sinks};

use crate::contract::Command;

/// Checks the commands of a generator: each has a program name and runs in a clean
/// directory inside the project. An empty directory becomes `.`.
pub(super) fn validate(name: &str, commands: &[Command]) -> anyhow::Result<Vec<Command>> {
    commands
        .iter()
        .map(|c| {
            let dir = if c.dir.is_empty() { "." } else { c.dir.as_str() };
            if dir != "." && spinup_vfs::path::clean_path(dir).ok().as_deref() != Some(dir) {
                return Err(anyhow!(
                    "generator {name:?} asks to run a command in {:?}: the directory must be a clean path inside the project\nTry: repair the generator commands",
                    c.dir
                ));
            }
            if c.argv.first().is_none_or(String::is_empty) {
                return Err(anyhow!(
                    "generator {name:?} asks to run a command without a program name\nTry: repair the generator commands"
                ));
            }
            Ok(Command {
                dir: dir.to_string(),
                argv: c.argv.clone(),
            })
        })
        .collect()
}

/// The command of `c` in `dir` (no directory for the preview).
fn cmd(c: &Command, dir: Option<PathBuf>) -> Cmd {
    Cmd {
        dir,
        ..Cmd::new(&c.argv[0]).args(&c.argv[1..])
    }
}

/// The shell-quoted command line of `c`.
pub(super) fn line(c: &Command) -> String {
    cmd(c, None).to_string()
}

/// Writes one `run` line for each command, for `--dry-run` and `--diff`.
pub(super) fn preview(out: &mut (dyn Write + Send), commands: &[Command]) -> anyhow::Result<()> {
    for c in commands {
        writeln!(out, "run {} (in {})", line(c), c.dir).map_err(|e| {
            anyhow!("write generator preview: {e}\nTry: check the output destination")
        })?;
    }
    Ok(())
}

/// The working directory of `c` below `root`.
fn dir(root: &Path, c: &Command) -> PathBuf {
    if c.dir == "." {
        return root.to_path_buf();
    }
    c.dir
        .split('/')
        .fold(root.to_path_buf(), |p, part| p.join(part))
}

/// A writer that shares one output between the standard output and the standard error of a
/// command.
struct Shared<'a, 'b>(&'a Mutex<&'b mut (dyn Write + Send)>);

impl Write for Shared<'_, '_> {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .write(buf)
    }

    fn flush(&mut self) -> std::io::Result<()> {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .flush()
    }
}

/// Runs the commands in order in `<root>/<dir>`. The command output goes to `out`. The first
/// failure stops the run. The files that the generator wrote stay on disk.
pub(super) fn run_all(
    root: &Path,
    commands: &[Command],
    runner: &dyn Runner,
    cancel: &Cancellation,
    out: &mut (dyn Write + Send),
) -> anyhow::Result<()> {
    let out = Mutex::new(out);
    for c in commands {
        let (mut stdout, mut stderr) = (Shared(&out), Shared(&out));
        let sinks = Sinks {
            stdout: Some(&mut stdout),
            stderr: Some(&mut stderr),
        };
        let failure = match runner.run(&cmd(c, Some(dir(root, c))), cancel, sinks) {
            Ok(output) if output.exit_code == 0 => None,
            Ok(output) => Some(format!("exit code {}", output.exit_code)),
            Err(e) => Some(reason(&e.to_string())),
        };
        if let Some(why) = failure {
            let line = line(c);
            return Err(anyhow!(
                "generator command {line} failed in {}: {why}\nTry: correct the problem, then run {line} in {} by hand (the generated files are already written)",
                c.dir,
                c.dir
            ));
        }
    }
    Ok(())
}

/// Returns the error text without its `Try:` lines and without an inline ` Try: ` hint, so
/// that the caller adds the one `Try:` line.
pub(super) fn reason(text: &str) -> String {
    let lines: Vec<&str> = text
        .split('\n')
        .filter(|line| !line.trim().starts_with("Try:"))
        .collect();
    let mut text = lines.join("\n");
    if let Some(i) = text.find(" Try: ") {
        text.truncate(i);
    }
    text.trim().to_string()
}
