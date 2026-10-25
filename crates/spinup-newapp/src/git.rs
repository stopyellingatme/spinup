//! Git initialization through `spinup-exec`.

use spinup_exec::runner::{Cancellation, Cmd, Runner};
use spinup_ui::printer::Printer;
use std::path::Path;

/// Creates the main branch and initial commit. Failure leaves the app available.
pub fn initialize_git(
    dir: &Path,
    version: &str,
    runner: &dyn Runner,
    cancel: &Cancellation,
    printer: &Printer,
) {
    let version = if version.is_empty() { "dev" } else { version };
    let message = format!("Initial commit from spinup {version}");
    for args in [
        vec!["init", "-b", "main"],
        vec!["add", "-A"],
        vec!["commit", "-m", &message],
    ] {
        let cmd = Cmd::new("git").args(args).dir(dir);
        if let Err(error) = runner.output(&cmd, cancel) {
            printer.warn(&format!("Git initialization did not finish: {error}\nTry: configure git and commit the app manually."));
            return;
        }
    }
}
