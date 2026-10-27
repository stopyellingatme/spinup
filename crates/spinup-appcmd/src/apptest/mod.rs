//! Run the tests of a generated Rust application.

use std::io::Write;
use std::path::PathBuf;

use spinup_exec::runner::{Cancellation, Cmd, ExecError, Runner, Sinks};

/// Select the tests and their output streams.
#[derive(Default)]
pub struct Options<'a> {
    /// The project directory.
    pub root: PathBuf,
    /// A nextest test name filter. An empty string selects all tests.
    pub run: String,
    /// Run the tests with `cargo llvm-cov nextest`.
    pub cover: bool,
    /// Select web tests, available in M4.
    pub web: bool,
    /// Select end-to-end tests, available in M4.
    pub e2e: bool,
    /// Select contract tests, available in M4.
    pub contract: bool,
    /// Print the command without execution.
    pub dry_run: bool,
    /// Receive standard output. None discards the output.
    pub out: Option<&'a mut (dyn Write + Send)>,
    /// Receive standard error. None discards the diagnostics.
    pub err_out: Option<&'a mut (dyn Write + Send)>,
}

/// Run the API tests in `api/` and preserve their exit code.
///
/// # Errors
/// Return a repair hint when the target, test command, or output stream fails.
pub fn run(
    cancel: &Cancellation,
    options: &mut Options<'_>,
    runner: &dyn Runner,
) -> anyhow::Result<()> {
    for (name, selected) in [
        ("web", options.web),
        ("e2e", options.e2e),
        ("contract", options.contract),
    ] {
        if selected {
            anyhow::bail!(
                "cannot run {name} tests: this target is unavailable\nTry: use --api until M4 adds {name} tests"
            );
        }
    }
    let args = if options.cover {
        ["llvm-cov", "nextest", "--locked"]
    } else {
        ["nextest", "run", "--locked"]
    };
    let mut cmd = Cmd::new("cargo").args(args).dir(options.root.join("api"));
    if !options.run.is_empty() {
        cmd.args.push(options.run.clone());
    }
    if options.dry_run {
        if let Some(out) = options.out.as_mut() {
            writeln!(out, "{cmd}").map_err(|error| {
                anyhow::anyhow!(
                    "write test command: {error}\nTry: check the output stream and run spinup test"
                )
            })?;
        }
        return Ok(());
    }
    let result = runner.run(
        &cmd,
        cancel,
        Sinks {
            stdout: options
                .out
                .as_mut()
                .map(|out| &mut **out as &mut (dyn Write + Send)),
            stderr: options
                .err_out
                .as_mut()
                .map(|out| &mut **out as &mut (dyn Write + Send)),
        },
    );
    match result {
        Ok(output) if output.exit_code == 0 => Ok(()),
        Ok(output) => Err(test_error(
            format!("{cmd} exited with code {}", output.exit_code),
            output.exit_code,
            &String::from_utf8_lossy(&output.stderr),
        )),
        Err(error) => {
            let output = error.output();
            let code = output.map_or(1, |out| out.exit_code);
            let diagnostics = output.map(|out| String::from_utf8_lossy(&out.stderr));
            let missing = match &error {
                ExecError::NotFound { name } => format!("missing executable {name}"),
                _ => String::new(),
            };
            let diagnostics = format!("{missing}\n{}", diagnostics.as_deref().unwrap_or_default());
            Err(test_error(error.to_string(), code, &diagnostics))
        }
    }
}

fn test_error(reason: String, code: i32, diagnostics: &str) -> anyhow::Error {
    let hint = if diagnostics.contains("missing executable cargo-nextest")
        || ((diagnostics.contains("no such command") || diagnostics.contains("no such subcommand"))
            && diagnostics.contains("nextest"))
    {
        "cargo install cargo-nextest --locked, then run spinup test"
    } else if diagnostics.contains("missing executable cargo-llvm-cov")
        || ((diagnostics.contains("no such command") || diagnostics.contains("no such subcommand"))
            && diagnostics.contains("llvm-cov"))
    {
        "cargo install cargo-llvm-cov --locked, then run spinup test --cover"
    } else if diagnostics.contains("missing executable cargo") {
        "install Rust and Cargo, then run spinup test"
    } else {
        "correct the test errors, then run spinup test"
    };
    spinup_ui::error::Error::new("Application tests failed")
        .why(format!("{reason}\nTry: {hint}"))
        .try_hint(hint)
        .code(if code > 0 { code } else { 1 })
        .into()
}

#[cfg(test)]
mod tests;
