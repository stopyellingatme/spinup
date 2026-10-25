//! Process groups, cancellable pipes, and standard input for each platform.

use std::io;
use std::process::{Child, Command};

#[cfg(unix)]
mod unix;
#[cfg(unix)]
pub(crate) use unix::{Input, Pipe, prepare_pipe, read_pipe};
#[cfg(windows)]
mod windows;
#[cfg(windows)]
pub(crate) use windows::{Input, Pipe, prepare_pipe, read_pipe};

/// Configures the process group before a command starts.
pub fn configure(command: &mut Command, interactive: bool) {
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        if !interactive {
            command.process_group(0);
        }
    }
    #[cfg(windows)]
    let _ = (command, interactive);
}

/// Kills the Unix group, or only the child for interactive commands and Windows.
/// A group can still exist after the direct child exits.
pub fn kill(child: &mut Child, interactive: bool) -> io::Result<()> {
    #[cfg(windows)]
    let _ = interactive;
    #[cfg(unix)]
    if !interactive {
        return unix::kill_group(child.id());
    }
    match child.kill() {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == io::ErrorKind::InvalidInput => Ok(()),
        Err(error) => Err(error),
    }
}
