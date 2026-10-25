//! The `git merge-file --diff3 --diff-algorithm=histogram` merger and the git version check.
//!
use crate::contract::{Input, MAX_SIZE, Merged, Merger};
use anyhow::{Result, anyhow};
use regex::Regex;
use spinup_compat::version::parse_version;
use spinup_exec::runner::{Cancellation, Cmd, ExecError, Runner};
use std::io::Write;
use std::path::Path;
use tempfile::{Builder, TempPath};

/// The oldest Git version that supports the required merge flags.
pub const MIN_GIT_VERSION: &str = "2.44.0";

/// A three-way merger that runs Git through the supplied runner.
pub struct Git<'a> {
    runner: &'a dyn Runner,
}

/// Returns a merger that uses `git merge-file` for each merge.
pub fn new_git(runner: &dyn Runner) -> Git<'_> {
    Git { runner }
}

impl Merger for Git<'_> {
    fn merge(&self, input: &Input<'_>, cancel: &Cancellation) -> Result<Merged> {
        validate_input(input)?;
        // These private scratch files never change the project. The TempPath guards
        // close the handles before Git reads them and remove them on every return.
        let dir = Builder::new().prefix("spinup-merge-").tempdir().map_err(|error| {
            anyhow!("create merge directory: {error}\nTry: check the temporary directory permissions")
        })?;
        let ours = write_input(dir.path(), "ours", input.ours)?;
        let base = write_input(dir.path(), "base", input.base)?;
        let theirs = write_input(dir.path(), "theirs", input.theirs)?;
        let mut cmd = Cmd::new("git").args([
            "merge-file",
            "-p",
            "--diff3",
            "--diff-algorithm=histogram",
            "-L",
            &format!("{} (yours)", input.path),
            "-L",
            &format!("{} (base)", input.path),
            "-L",
            &format!("{} (new template)", input.path),
        ]);
        for path in [&ours, &base, &theirs] {
            let path = path.to_str().ok_or_else(|| anyhow!(
                "write merge input: temporary path is not valid UTF-8\nTry: use a temporary directory with a UTF-8 path"
            ))?;
            cmd.args.push(path.to_owned());
        }
        let output = match self.runner.output(&cmd, cancel) {
            Ok(output) => output,
            Err(ExecError::Exit(error)) if (1..=127).contains(&error.output.exit_code) => {
                return Ok(Merged {
                    data: error.output.stdout,
                    conflicted: true,
                });
            }
            Err(error) => {
                return Err(anyhow!(
                    "run git merge-file for {:?}: {error}\nTry: install git {MIN_GIT_VERSION} or later and retry",
                    input.path
                ));
            }
        };
        match output.exit_code {
            0..=127 => Ok(Merged {
                data: output.stdout,
                conflicted: output.exit_code != 0,
            }),
            code => Err(anyhow!(
                "git merge-file for {:?} returned exit code {code}\nTry: inspect the git installation and retry",
                input.path
            )),
        }
    }
}

fn write_input(dir: &Path, name: &str, data: &[u8]) -> Result<TempPath> {
    let write = || -> std::io::Result<TempPath> {
        // The private directory provides uniqueness. Keep the Go scratch names.
        let mut file = Builder::new().prefix(name).rand_bytes(0).tempfile_in(dir)?;
        file.write_all(data)?;
        Ok(file.into_temp_path())
    };
    write().map_err(|error| {
        anyhow!("write merge input: {error}\nTry: check the temporary directory permissions")
    })
}

/// Verifies that Git supports the merge flags and returns its numeric version.
///
/// # Errors
/// Returns an error when Git fails, its version cannot be parsed, or it is too old.
pub fn check_git_version(runner: &dyn Runner, cancel: &Cancellation) -> Result<String> {
    let output = runner
        .output(&Cmd::new("git").args(["--version"]), cancel)
        .map_err(|error| anyhow!("check git version: {error}\nTry: install git 2.44 or later"))?;
    let text = String::from_utf8_lossy(&output.stdout);
    let pattern = Regex::new(
        r"(?m)^git version ([0-9]+\.[0-9]+\.[0-9]+)(?:\.[^\t\n\x0B\x0C\r ]+)?(?:[\t\n\x0B\x0C\r ]|$)",
    ).expect("the Git version pattern is valid");
    let captures = pattern.captures(&text).ok_or_else(|| {
        anyhow!(
            "cannot parse git version from {:?}\nTry: install git 2.44 or later",
            text.trim()
        )
    })?;
    let version = &captures[1];
    let found = parse_version(version).map_err(|error| {
        anyhow!("cannot parse git version {version:?}: {error}\nTry: install git 2.44 or later")
    })?;
    let minimum = parse_version(MIN_GIT_VERSION).map_err(|error| {
        anyhow!(
            "parse minimum git version {MIN_GIT_VERSION:?}: {error}\nTry: install git 2.44 or later"
        )
    })?;
    if found < minimum {
        return Err(anyhow!(
            "git version check found {version}, but requires {MIN_GIT_VERSION}\nTry: install git 2.44 or later"
        ));
    }
    Ok(version.to_owned())
}

fn validate_input(input: &Input<'_>) -> Result<()> {
    for (name, data) in [
        ("base", input.base),
        ("ours", input.ours),
        ("theirs", input.theirs),
    ] {
        if data.len() > MAX_SIZE {
            return Err(anyhow!(
                "cannot merge {:?}: {name} input is larger than {MAX_SIZE} bytes\nTry: overwrite or keep the file instead",
                input.path
            ));
        }
        if data.contains(&0) {
            return Err(anyhow!(
                "cannot merge {:?}: {name} input contains a NUL byte\nTry: overwrite or keep the binary file instead",
                input.path
            ));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests;
