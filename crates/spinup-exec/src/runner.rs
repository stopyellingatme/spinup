//! The subprocess contract: [`Cmd`], [`Output`], [`Cancellation`], [`ExecError`], and the
//! synchronous [`Runner`] trait (R0.4, ADR 0019 §3, ADR 0020 §1).
//!
//! Contract file from the R1 pre-seed. Main loop only: units do not edit it.
//!
//! Unit R1.2 implements the trait (`OsRunner`, `Fake`) and `Display` for [`Cmd`]: the
//! shell-quoted command line without secret values, as Go `Cmd.String()`.

use std::io::Write;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

/// The bound on pipe draining after a cancel or after the direct child exits (Go
/// `processWaitDelay`).
pub const PROCESS_WAIT_DELAY: Duration = Duration::from_secs(2);

/// The standard input of a command.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub enum Stdin {
    /// No input (the null device). This is the default, as a nil Go `Stdin`.
    #[default]
    Null,
    /// These bytes, then end of file.
    Bytes(Vec<u8>),
    /// The standard input of the spinup process, for interactive commands.
    Inherit,
}

/// One subprocess invocation (Go `exec.Cmd`).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Cmd {
    /// The program name or path (Go `Name`). A name without a path separator is looked up
    /// on `PATH`, with the `PATH` of [`Cmd::env`] when it sets one.
    pub program: String,
    /// The arguments after the program.
    pub args: Vec<String>,
    /// The working directory. `None` is the directory of the spinup process.
    pub dir: Option<PathBuf>,
    /// Environment variables. They extend the parent environment, or replace it when
    /// [`Cmd::replace_env`] is true. A later pair overrides an earlier pair with the same name.
    pub env: Vec<(String, String)>,
    /// Use only [`Cmd::env`] and not the parent environment.
    pub replace_env: bool,
    /// The standard input.
    pub stdin: Stdin,
    /// The time limit. `None` means no limit. A timeout stops the command like a cancel.
    pub timeout: Option<Duration>,
    /// Secret values. The runner replaces each complete match with `***` in the captured
    /// output, in the streamed output, and in error text.
    pub redact: Vec<String>,
    /// Keep the process group of the parent on Unix, so the command can read the terminal.
    /// A cancel then kills only the direct child. Windows always kills only the direct child.
    pub interactive: bool,
}

impl Cmd {
    /// Returns a command with no arguments and the defaults of the other fields.
    pub fn new(program: impl Into<String>) -> Self {
        Self {
            program: program.into(),
            ..Self::default()
        }
    }

    /// Appends arguments.
    pub fn args<I, S>(mut self, args: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        self.args.extend(args.into_iter().map(Into::into));
        self
    }

    /// Sets the working directory.
    pub fn dir(mut self, dir: impl Into<PathBuf>) -> Self {
        self.dir = Some(dir.into());
        self
    }

    /// Appends one environment variable.
    pub fn env(mut self, name: impl Into<String>, value: impl Into<String>) -> Self {
        self.env.push((name.into(), value.into()));
        self
    }
}

/// The redacted output of a command that ran (Go `exec.Result`).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Output {
    /// The redacted standard output.
    pub stdout: Vec<u8>,
    /// The redacted standard error.
    pub stderr: Vec<u8>,
    /// The exit code. It is -1 when a signal stopped the process.
    pub exit_code: i32,
    /// The wall time from start to the end of pipe draining.
    pub duration: Duration,
}

/// A cancel signal that the CLI boundary shares with every runner call.
///
/// The CLI installs one signal handler that calls [`Cancellation::cancel`]. A runner checks
/// the flag while the command runs and kills the command (the whole process group on Unix,
/// unless [`Cmd::interactive`]).
#[derive(Debug, Clone, Default)]
pub struct Cancellation(Arc<AtomicBool>);

impl Cancellation {
    /// Returns a new signal that is not cancelled.
    pub fn new() -> Self {
        Self::default()
    }

    /// Cancels every holder of this signal.
    pub fn cancel(&self) {
        self.0.store(true, Ordering::Release);
    }

    /// Returns true after [`Cancellation::cancel`].
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::Acquire)
    }
}

/// Optional live sinks for the output of a command (Go `Cmd.Stdout` and `Cmd.Stderr`).
/// The runner writes redacted bytes to them while the command runs. It captures the output
/// in [`Output`] in every case.
#[derive(Default)]
pub struct Sinks<'a> {
    /// The live sink for standard output.
    pub stdout: Option<&'a mut (dyn Write + Send)>,
    /// The live sink for standard error.
    pub stderr: Option<&'a mut (dyn Write + Send)>,
}

impl Sinks<'_> {
    /// Returns sinks that discard the live output.
    pub fn none() -> Self {
        Self::default()
    }
}

/// Why a command stopped before it finished.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StopReason {
    /// The [`Cancellation`] signal was set.
    Cancelled,
    /// The [`Cmd::timeout`] passed.
    TimedOut,
}

impl std::fmt::Display for StopReason {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        // The Go text of context.Canceled and context.DeadlineExceeded.
        f.write_str(match self {
            Self::Cancelled => "context canceled",
            Self::TimedOut => "context deadline exceeded",
        })
    }
}

/// A command that ran and returned a nonzero exit code (Go `exec.ExitError`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ExitError {
    /// The redacted, shell-quoted command line.
    pub cmd: String,
    /// The exit code.
    pub code: i32,
    /// The last lines of the redacted standard error, or an empty string.
    pub stderr_tail: String,
    /// The full output. Callers read it for commands that use the exit code as a result,
    /// such as `git merge-file`.
    pub output: Output,
}

impl std::fmt::Display for ExitError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "command {} failed with exit code {}",
            self.cmd, self.code
        )?;
        if !self.stderr_tail.is_empty() {
            write!(f, ": {}", self.stderr_tail)?;
        }
        f.write_str("\nTry: inspect the command output and retry.")
    }
}

impl std::error::Error for ExitError {}

/// The errors of [`Runner::run`]. The texts equal the Go texts (ADR 0020 §6).
#[derive(Debug, thiserror::Error)]
pub enum ExecError {
    /// The program is not on `PATH` or not at the given path (Go `exec.NotFoundError`).
    #[error("command {name:?} was not found. Try: install {name}, or run spinup doctor")]
    NotFound {
        /// The program name.
        name: String,
    },
    /// The command ran and returned a nonzero exit code. The error is boxed, because it
    /// holds the full output.
    #[error(transparent)]
    Exit(Box<ExitError>),
    /// A cancel or a timeout stopped the command. The output holds what it wrote first.
    #[error("command {cmd} stopped: {reason}\nTry: inspect the command timeout or context")]
    Stopped {
        /// The redacted, shell-quoted command line.
        cmd: String,
        /// Cancel or timeout.
        reason: StopReason,
        /// The output up to the stop.
        output: Box<Output>,
    },
    /// The command did not start, or the runner failed to read its pipes.
    #[error("run command {cmd}: {source}\nTry: check the command name and working directory")]
    Io {
        /// The redacted, shell-quoted command line.
        cmd: String,
        /// The operating system error.
        source: std::io::Error,
    },
}

impl From<ExitError> for ExecError {
    fn from(error: ExitError) -> Self {
        Self::Exit(Box::new(error))
    }
}

impl ExecError {
    /// Returns the output of a command that ran, also when it failed or stopped.
    pub fn output(&self) -> Option<&Output> {
        match self {
            Self::Exit(error) => Some(&error.output),
            Self::Stopped { output, .. } => Some(output),
            Self::NotFound { .. } | Self::Io { .. } => None,
        }
    }
}

/// Runs subprocesses. Every subprocess of the CLI goes through a `Runner` (CLAUDE.md,
/// Layering). The trait is synchronous: the CLI uses threads, not an async runtime (R0.4).
///
/// An implementation must be safe to share between threads, so tests can pass a `Fake`
/// where the code expects `&dyn Runner`.
pub trait Runner: Send + Sync {
    /// Runs `cmd`, writes its redacted output to `sinks` while it runs, and returns the
    /// captured output. A nonzero exit code returns [`ExecError::Exit`].
    fn run(&self, cmd: &Cmd, cancel: &Cancellation, sinks: Sinks<'_>) -> Result<Output, ExecError>;

    /// Runs `cmd` without live output.
    fn output(&self, cmd: &Cmd, cancel: &Cancellation) -> Result<Output, ExecError> {
        self.run(cmd, cancel, Sinks::none())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn error_texts_equal_go() {
        let output = Output::default();
        let exit = ExitError {
            cmd: "git status".into(),
            code: 128,
            stderr_tail: "fatal: not a git repository".into(),
            output: output.clone(),
        };
        assert_eq!(
            ExecError::from(exit).to_string(),
            "command git status failed with exit code 128: fatal: not a git repository\nTry: inspect the command output and retry."
        );
        let not_found = ExecError::NotFound { name: "git".into() };
        assert_eq!(
            not_found.to_string(),
            "command \"git\" was not found. Try: install git, or run spinup doctor"
        );
        let stopped = ExecError::Stopped {
            cmd: "sleep 9".into(),
            reason: StopReason::TimedOut,
            output: Box::new(output),
        };
        assert_eq!(
            stopped.to_string(),
            "command sleep 9 stopped: context deadline exceeded\nTry: inspect the command timeout or context"
        );
    }

    #[test]
    fn cancellation_is_shared() {
        let cancel = Cancellation::new();
        let copy = cancel.clone();
        assert!(!copy.is_cancelled());
        cancel.cancel();
        assert!(copy.is_cancelled());
    }

    #[test]
    fn runner_is_object_safe() {
        fn takes(_: &dyn Runner) {}
        let _ = takes;
    }
}
