//! `spinup version` and the build information (Go `internal/cli/version.go`).
//!
//! The build sets the information with environment variables at compile time:
//! `SPINUP_VERSION` (`make rs-install` sets it), `SPINUP_COMMIT`, and `SPINUP_DATE`.

use anyhow::Result;
use clap::{Arg, ArgAction, ArgMatches, Command};
use spinup_ui::error::Error as UiError;
use spinup_ui::exit;

use crate::root::{App, COMMANDS, CommandDef, Group};

/// The CLI version. It is also the kit version. A build without `SPINUP_VERSION` is `dev`.
pub const VERSION: &str = match option_env!("SPINUP_VERSION") {
    Some(version) => version,
    None => "dev",
};

/// The git commit of the build, or `none`.
pub const COMMIT: &str = match option_env!("SPINUP_COMMIT") {
    Some(commit) => commit,
    None => "none",
};

/// The build date, or `unknown`.
pub const DATE: &str = match option_env!("SPINUP_DATE") {
    Some(date) => date,
    None => "unknown",
};

/// The build information that `spinup version` prints.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Info {
    /// The CLI version.
    pub version: String,
    /// The git commit.
    pub commit: String,
    /// The build date.
    pub date: String,
    /// The Rust version that the crate declares, such as `rust1.99`.
    pub rust: String,
    /// The kit version. It equals the CLI version.
    pub kit: String,
}

/// Returns the build information.
pub fn current() -> Info {
    Info {
        version: VERSION.into(),
        commit: COMMIT.into(),
        date: DATE.into(),
        rust: format!("rust{}", env!("CARGO_PKG_RUST_VERSION")),
        kit: VERSION.into(),
    }
}

// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static VERSION_COMMAND: CommandDef = CommandDef {
    name: "version",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    Command::new("version")
        .about("Print the spinup version")
        .arg(
            Arg::new("check")
                .long("check")
                .help("check for a newer stable release")
                .action(ArgAction::SetTrue),
        )
}

/// The error for a release check in offline mode (Go `updateOnline`).
pub fn offline_release_error() -> UiError {
    UiError::new("Cannot check spinup releases")
        .why("offline mode disables downloads")
        .try_hint("remove --offline and unset SPINUP_OFFLINE")
        .code(exit::USAGE)
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    let info = current();
    if matches.get_flag("check") {
        if app.flags.offline {
            return Err(offline_release_error().into());
        }
        // The release check arrives with spinup-selfupdate (R4).
        return Err(UiError::new("Cannot check spinup releases")
            .why("this build has no release check")
            .try_hint("see https://github.com/stopyellingatme/spinup/releases")
            .into());
    }
    if app.flags.json {
        return app.ui.json(&info);
    }
    app.ui.println(&format!(
        "spinup {} (commit {}, built {}, {})",
        info.version, info.commit, info.date, info.rust
    ));
    Ok(())
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn version_text() {
        let (code, out, _) = run_args(&["version"]);
        assert_eq!(code, 0);
        assert!(out.starts_with("spinup "), "{out}");
        assert!(out.contains("(commit "), "{out}");
    }

    #[test]
    fn version_json() {
        let (code, out, _) = run_args(&["version", "--json"]);
        assert_eq!(code, 0);
        let value: serde_json::Map<String, serde_json::Value> =
            serde_json::from_str(&out).expect("JSON");
        assert_eq!(value.len(), 5, "{value:?}");
        for key in ["version", "commit", "date", "rust", "kit"] {
            assert!(
                value[key].as_str().is_some_and(|text| !text.is_empty()),
                "key {key} in {value:?}"
            );
        }
        assert_eq!(value["kit"], value["version"]);
    }

    #[test]
    fn version_check() {
        let (code, _, err) = run_args(&["version", "--check", "--offline"]);
        assert_eq!(code, exit::USAGE);
        assert!(err.contains("Try: remove --offline"), "{err}");
        let (code, _, err) = run_args(&["version", "--check"]);
        assert_eq!(code, exit::ERROR);
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn global_offline_flag_propagates() {
        let (code, _, err) = run_args(&["version", "--offline", "--check"]);
        assert_eq!(code, exit::USAGE, "{err}");
    }
}
