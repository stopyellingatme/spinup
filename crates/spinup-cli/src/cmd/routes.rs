//! `spinup routes`: list the HTTP routes of the application (Go `internal/cli/routes.go`).
//!
//! This file only wires clap to `spinup_appcmd::routes`. The command reads files and never
//! builds the app (ADR 0021 §7).

use std::io::Write;

use anyhow::Result;
use clap::{ArgMatches, Command};
use spinup_appcmd::routes;
use spinup_ui::error::Error as UiError;

use super::upgrade::Output;
use crate::root::{App, COMMANDS, CommandDef, Group};

// SAFETY: linkme places this command in its own link section, as required by ADR 0020.
#[allow(unsafe_code)]
#[linkme::distributed_slice(COMMANDS)]
static ROUTES: CommandDef = CommandDef {
    name: "routes",
    group: Group::Develop,
    build,
    run,
};

fn build() -> Command {
    Command::new("routes").about("List application HTTP routes")
}

fn run(app: &App, _matches: &ArgMatches) -> Result<()> {
    let root = spinup_project::root::find_root(&app.flags.dir).map_err(|error| {
        UiError::new("Cannot find the project")
            .why(error.to_string())
            .try_hint("run inside a spinup project, or pass --dir")
    })?;
    let list = routes::list(&root)?;
    let mut out = Output::new(&app.ui);
    let result = routes::print(&mut out, &list, app.flags.json);
    let _ = out.flush();
    result
}

#[cfg(test)]
mod tests {
    use crate::root::tests::run_args;
    use spinup_ui::exit;

    #[test]
    fn json_lists_the_kit_routes() {
        let dir = std::env::temp_dir().join(format!("spinup-cli-routes-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("spinup.toml"), "").unwrap();
        let path = dir.to_string_lossy().into_owned();
        let (code, out, err) = run_args(&["routes", "--dir", &path, "--json"]);
        assert_eq!(code, exit::OK, "{err}");
        assert_eq!(
            out,
            "[{\"method\":\"GET\",\"path\":\"/healthz\",\"source\":\"kit\"},\
             {\"method\":\"GET\",\"path\":\"/readyz\",\"source\":\"kit\"},\
             {\"method\":\"GET\",\"path\":\"/version\",\"source\":\"kit\"}]\n"
        );
        let (code, out, err) = run_args(&["routes", "--dir", &path]);
        assert_eq!(code, exit::OK, "{err}");
        assert!(out.starts_with("METHOD  PATH      SOURCE\n"), "{out}");
    }

    #[test]
    fn rejects_positional_arguments() {
        let (code, _, err) = run_args(&["routes", "extra"]);
        assert_eq!(code, exit::USAGE, "{err}");
        assert!(err.contains("Try:"), "{err}");
    }

    #[test]
    fn outside_a_project_fails_with_a_hint() {
        let dir = std::env::temp_dir().join("spinup-cli-routes-no-project");
        let _ = std::fs::create_dir_all(&dir);
        let dir = dir.to_string_lossy().into_owned();
        let (code, _, err) = run_args(&["-C", &dir, "routes"]);
        assert_ne!(code, exit::OK);
        assert!(err.contains("Cannot find the project"), "{err}");
        assert!(err.contains("Try:"), "{err}");
    }
}
