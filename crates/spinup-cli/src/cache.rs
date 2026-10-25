//! `spinup cache` with `warm`, `list`, and `clean` (Go `internal/cli/cache.go`).

use anyhow::Result;
use clap::{Arg, ArgAction, ArgMatches, Command};
use spinup_tplstore::store::{self, Store};
use spinup_ui::error::Error as UiError;
use spinup_ui::exit;

use crate::root::{App, COMMANDS, CommandDef, Group};
use crate::version::VERSION;

// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static CACHE: CommandDef = CommandDef {
    name: "cache",
    group: Group::Maintain,
    build,
    run,
};

fn build() -> Command {
    Command::new("cache")
        .about("Manage cached template versions")
        .subcommand(
            Command::new("warm")
                .about("Download and verify missing template versions")
                .arg(
                    Arg::new("version")
                        .value_name("version")
                        .num_args(0..)
                        .action(ArgAction::Append)
                        .help("template versions; the default is the version of the project lock"),
                ),
        )
        .subcommand(Command::new("list").about("List loadable template versions and their sources"))
        .subcommand(
            Command::new("clean").about("Remove downloaded template archives and signatures"),
        )
}

fn run(app: &App, matches: &ArgMatches) -> Result<()> {
    match matches.subcommand() {
        Some(("warm", sub)) => warm(app, sub),
        Some(("list", _)) => list(app),
        Some(("clean", _)) => clean(app),
        _ => {
            app.print_help(&["cache"]);
            Ok(())
        }
    }
}

fn open(app: &App) -> Result<Store> {
    store::default(VERSION, app.flags.offline)
}

fn warm(app: &App, matches: &ArgMatches) -> Result<()> {
    if app.flags.offline || std::env::var("SPINUP_OFFLINE").is_ok_and(|value| value == "1") {
        return Err(UiError::new("Cannot warm templates")
            .why("offline mode disables downloads")
            .try_hint("remove --offline and unset SPINUP_OFFLINE")
            .code(exit::USAGE)
            .into());
    }
    let mut versions: Vec<String> = matches
        .get_many::<String>("version")
        .map(|values| values.cloned().collect())
        .unwrap_or_default();
    let source = open(app)?;
    if app.flags.dry_run {
        app.ui
            .println(&format!("Would warm templates: {}", versions.join(", ")));
        return Ok(());
    }
    if versions.is_empty() {
        versions.push(source.warm_project(&app.flags.dir)?);
    } else {
        source.warm(&versions)?;
    }
    app.ui
        .println(&format!("Warmed templates: {}", versions.join(", ")));
    Ok(())
}

fn list(app: &App) -> Result<()> {
    let entries = open(app)?.list()?;
    if app.flags.json {
        return app.ui.json(&entries);
    }
    for entry in entries {
        app.ui
            .println(&format!("{}\t{}", entry.version, entry.source));
    }
    Ok(())
}

fn clean(app: &App) -> Result<()> {
    let source = open(app)?;
    let directory = source.cache_dir().display().to_string();
    if app.flags.dry_run {
        app.ui.println(&format!("Would remove {directory}"));
        return Ok(());
    }
    source.clean()?;
    app.ui.println(&format!("Removed {directory}"));
    Ok(())
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn offline_warm_is_a_usage_error() {
        let (code, _, err) = run_args(&["cache", "warm", "1.0.0", "--offline"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("Error: Cannot warm templates"), "{err}");
        assert!(err.contains("Try: remove --offline"), "{err}");
    }

    #[test]
    fn list_shows_the_current_version() {
        let (code, out, err) = run_args(&["cache", "list", "--offline"]);
        assert_eq!(code, 0, "{err}");
        assert!(out.contains("0.0.0-dev\tcurrent"), "{out}");
        let (code, out, err) = run_args(&["cache", "list", "--offline", "--json"]);
        assert_eq!(code, 0, "{err}");
        let entries: Vec<serde_json::Value> = serde_json::from_str(&out).expect("JSON");
        assert!(!entries.is_empty());
        assert!(entries[0]["version"].is_string() && entries[0]["source"].is_string());
    }

    #[test]
    fn clean_dry_run_names_the_directory() {
        let (code, out, err) = run_args(&["cache", "clean", "--dry-run"]);
        assert_eq!(code, 0, "{err}");
        assert!(out.starts_with("Would remove "), "{out}");
        assert!(out.contains("spinup"), "{out}");
    }

    #[test]
    fn warm_dry_run_and_bad_version() {
        let (code, out, err) = run_args(&["cache", "warm", "1.0.0", "2.0.0", "--dry-run"]);
        if std::env::var("SPINUP_OFFLINE").is_ok_and(|v| v == "1") {
            assert_eq!(code, exit::USAGE, "{err}");
            return;
        }
        assert_eq!(code, 0, "{err}");
        assert_eq!(out, "Would warm templates: 1.0.0, 2.0.0\n");
        let (code, _, err) = run_args(&["cache", "warm", "v1"]);
        assert_eq!(code, exit::ERROR, "{err}");
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn warm_outside_a_project_fails() {
        if std::env::var("SPINUP_OFFLINE").is_ok_and(|v| v == "1") {
            return;
        }
        let dir = std::env::temp_dir();
        let dir = dir.to_string_lossy();
        let (code, _, err) = run_args(&["cache", "warm", "--dir", &dir]);
        assert_eq!(code, exit::ERROR, "{err}");
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn cache_without_subcommand_prints_help() {
        let (code, out, _) = run_args(&["cache"]);
        assert_eq!(code, 0);
        assert!(out.contains("warm"), "{out}");
    }
}
