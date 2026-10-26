//! Run SQLx migrations, with kit migrations before app migrations.

pub use crate::ops::Options;
use crate::ops::{capture, database_cmd, guard_migration, load, op_error, stream};
use spinup_exec::runner::{Cancellation, Cmd, Runner};
use std::path::{Path, PathBuf};

/// A supported migration operation.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Sub {
    /// Apply pending kit migrations, then app migrations.
    #[default]
    Up,
    /// Revert the latest app migration.
    Down,
    /// Show kit migration status, then app migration status.
    Status,
}

/// Apply pending kit migrations, then app migrations.
pub fn up(o: &mut Options<'_>, runner: &dyn Runner, cancel: &Cancellation) -> anyhow::Result<()> {
    run(o, Sub::Up, runner, cancel)
}

/// Run a migration operation with environment and rollback guards.
/// Dry runs print deterministic placeholders and never invoke the runner.
pub fn run(
    o: &mut Options<'_>,
    sub: Sub,
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> anyhow::Result<()> {
    let what = "Cannot run migration";
    if sub == Sub::Down && !o.i_know {
        return Err(spinup_ui::error::Error::new(
            "This rolls back the last migration and can lose data",
        )
        .why("down requires explicit acknowledgement")
        .try_hint("spinup db migrate down --i-know")
        .into());
    }
    let (root, config) = load(o, what)?;
    guard_migration(
        o,
        &config,
        what,
        if sub == Sub::Down {
            "db migrate down"
        } else {
            "db migrate"
        },
        sub == Sub::Down,
    )?;
    run_at(o, &root, sub, runner, cancel)
}

pub(crate) fn run_at(
    o: &mut Options<'_>,
    root: &Path,
    sub: Sub,
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> anyhow::Result<()> {
    let what = "Cannot run migration";
    if sub == Sub::Down {
        let cmd = database_cmd(o, root, app_command(root, "revert"), what)?;
        return stream(o, runner, cancel, what, &cmd);
    }
    let verb = if sub == Sub::Status { "info" } else { "run" };
    let sources = Sources::resolve(o, root, runner, cancel)?;
    let kit = database_cmd(o, root, sources.kit_command(root, verb), what)?;
    let app = database_cmd(o, root, app_command(root, verb), what)?;
    stream(o, runner, cancel, what, &kit)?;
    stream(o, runner, cancel, what, &app)
}

pub(crate) fn app_command(root: &Path, verb: &str) -> Cmd {
    info_color(
        Cmd::new("sqlx")
            .args(["migrate", verb, "--source", "migrations"])
            .dir(root.join("api")),
        verb,
    )
}

struct Sources {
    kit: PathBuf,
    config: PathBuf,
    // Keep the configuration alive until both commands finish.
    _temp: Option<tempfile::TempDir>,
}

impl Sources {
    fn resolve(
        o: &mut Options<'_>,
        root: &Path,
        runner: &dyn Runner,
        cancel: &Cancellation,
    ) -> anyhow::Result<Self> {
        if o.dry_run {
            return Ok(Self {
                kit: "<kit>/migrations".into(),
                config: "<tmp>/kit-migrations.toml".into(),
                _temp: None,
            });
        }
        let what = "Cannot run migration";
        let metadata = Cmd::new("cargo")
            .args(["metadata", "--format-version", "1", "--locked"])
            .dir(root.join("api"));
        let output = capture(o, runner, cancel, what, &metadata, false)?;
        let value: serde_json::Value =
            serde_json::from_slice(&output.stdout).map_err(|e| op_error(what, e))?;
        let manifests: Vec<_> = value
            .get("packages")
            .and_then(|p| p.as_array())
            .into_iter()
            .flatten()
            .filter(|package| package.get("name").and_then(|n| n.as_str()) == Some("spinup-kit"))
            .filter_map(|package| package.get("manifest_path").and_then(|p| p.as_str()))
            .collect();
        let [manifest] = manifests.as_slice() else {
            return Err(spinup_ui::error::Error::new(what)
                .why("cargo metadata did not identify exactly one spinup-kit package")
                .try_hint("check the spinup-kit dependency in api/Cargo.toml, then retry")
                .into());
        };
        let manifest = Path::new(manifest);
        let parent = manifest
            .parent()
            .filter(|p| !p.as_os_str().is_empty())
            .ok_or_else(|| {
                spinup_ui::error::Error::new(what)
                    .why("spinup-kit has no manifest directory")
                    .try_hint("check cargo metadata in api/")
            })?;
        let temp = tempfile::tempdir().map_err(|e| op_error(what, e))?;
        let config = temp.path().join("kit-migrations.toml");
        std::fs::write(
            &config,
            "[migrate]\ntable-name = \"_spinup_kit_migrations\"\n",
        )
        .map_err(|e| op_error(what, e))?;
        Ok(Self {
            kit: parent.join("migrations"),
            config,
            _temp: Some(temp),
        })
    }

    fn kit_command(&self, root: &Path, verb: &str) -> Cmd {
        let cmd = Cmd::new("sqlx")
            .args(["migrate", verb, "--source"])
            .args([
                self.kit.to_string_lossy().into_owned(),
                "--config".into(),
                self.config.to_string_lossy().into_owned(),
            ])
            .dir(root.join("api"));
        info_color(cmd, verb)
    }
}

fn info_color(cmd: Cmd, verb: &str) -> Cmd {
    if verb == "info" {
        cmd.env("NO_COLOR", "1").env("CLICOLOR_FORCE", "0")
    } else {
        cmd
    }
}

/// Parse a migration operation from a CLI value.
impl std::str::FromStr for Sub {
    type Err = anyhow::Error;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "" | "up" => Ok(Self::Up),
            "down" => Ok(Self::Down),
            "status" => Ok(Self::Status),
            _ => Err(spinup_ui::error::Error::new("Cannot run migration")
                .why(format!("unknown migration command {value}"))
                .try_hint("spinup db migrate --help")
                .code(spinup_ui::exit::USAGE)
                .into()),
        }
    }
}

#[cfg(test)]
mod tests;
