//! `spinup build`: the local release build of the API (Go `internal/cli/build.go`).
//!
//! This file only wires clap to `spinup_build::local`. The `--image` flags arrive in R3a.

use std::io::{self, Write};
use std::path::PathBuf;

use anyhow::Result;
use clap::{Arg, ArgMatches, Command};
use spinup_build::local;

use super::upgrade::Output;
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static BUILD: CommandDef = CommandDef {
    name: "build",
    group: Group::Ship,
    build,
    run,
};

fn build() -> Command {
    Command::new("build")
        .about("Build the application API")
        .arg(
            Arg::new("target")
                .long("target")
                .default_value("api")
                .help("target to build: api, web, or all"),
        )
        .arg(
            Arg::new("tag")
                .long("tag")
                .help("version tag (default dev)"),
        )
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let text = |id: &str| matches.get_one::<String>(id).cloned().unwrap_or_default();
    let mut out = Output::new(&app.ui);
    let mut err = io::stderr();
    let mut options = local::Options {
        dir: PathBuf::from(&app.flags.dir),
        target: text("target"),
        tag: text("tag"),
        git_sha: String::new(),
        dry_run: app.flags.dry_run,
        out: Some(&mut out),
        err_out: Some(&mut err),
    };
    let result = local::local(&app.cancel, &mut options, app.runner.as_ref());
    drop(options);
    let _ = out.flush();
    result
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn rejects_positional_arguments() {
        let (code, _, err) = run_args(&["build", "extra"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn help_lists_the_flags_without_image() {
        let (code, out, err) = run_args(&["build", "--help"]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.contains("Build the application API"), "{out}");
        for flag in ["--target", "--tag"] {
            assert!(out.contains(flag), "{flag}: {out}");
        }
        assert!(!out.contains("--image"), "{out}");
    }

    #[test]
    fn unknown_and_web_targets_fail_with_a_hint() {
        for (target, text) in [
            ("nope", "unknown target nope"),
            ("web", "Cannot build the web frontend"),
        ] {
            let (code, _, err) = run_args(&["build", "--target", target, "--dry-run"]);
            assert_ne!(code, exit::OK, "{target}");
            assert!(err.contains(text), "{target}: {err}");
            assert!(err.contains("Try: pass --target api"), "{target}: {err}");
        }
    }

    #[test]
    fn dry_run_prints_the_cargo_command() {
        let dir = tempfile_dir("spinup-cli-build-dry-run");
        std::fs::create_dir_all(dir.join("api")).unwrap();
        std::fs::write(dir.join("spinup.toml"), "").unwrap();
        std::fs::write(
            dir.join("api/rust-toolchain.toml"),
            "[toolchain]\nchannel = \"1.99.1\"\n",
        )
        .unwrap();
        let path = dir.to_string_lossy().into_owned();
        let (code, out, err) = run_args(&[
            "build",
            "-C",
            &path,
            "--target",
            "all",
            "--tag",
            "v0.1.0",
            "--dry-run",
        ]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(
            out.starts_with("Building available targets: api\n"),
            "{out}"
        );
        assert!(out.contains("SPINUP_APP_VERSION=v0.1.0"), "{out}");
        assert!(out.contains("SPINUP_APP_RUST=1.99.1"), "{out}");
        assert!(out.contains("cargo build --release --locked"), "{out}");
        assert!(!dir.join("api/bin").exists());
    }

    #[test]
    fn outside_a_project_fails_with_a_hint() {
        let dir = tempfile_dir("spinup-cli-build-no-project");
        let path = dir.to_string_lossy().into_owned();
        let (code, _, err) = run_args(&["-C", &path, "build", "--dry-run"]);
        assert_ne!(code, exit::OK);
        assert!(err.contains("Cannot build the API"), "{err}");
        assert!(err.contains("Try:"), "{err}");
    }

    /// Returns a new empty directory under the system temporary directory.
    fn tempfile_dir(name: &str) -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!("{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }
}
