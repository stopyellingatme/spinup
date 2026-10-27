//! Database operations and their confirmation guards.
//!
//! All child processes use the synchronous runner. Callers supply streams and cancellation.

mod dev;
mod doctor;
mod migrations;
pub use dev::{dump, psql, reset, restore, seed};
pub use doctor::{VersionCheck, check_server_version, doctor};
pub use migrations::{new, redo, status, to};

use spinup_exec::runner::{Cancellation, Cmd, ExecError, Output, Runner, Sinks};
use spinup_project::{
    config::{Config, Env},
    confirm::{Confirm, confirm_protected},
};
use spinup_ui::error::Error;
use std::io::{BufRead, BufReader, Read, Write};
use std::path::{Path, PathBuf};

/// The development Compose file, relative to the project root.
pub const COMPOSE_FILE: &str = "deploy/compose.dev.yml";
/// The default archive path, relative to the project root.
pub const DEFAULT_DUMP_FILE: &str = ".spinup/dumps/app.dump";
/// The installation hint for the required SQLx CLI.
pub const SQLX_INSTALL_TRY: &str = "cargo install sqlx-cli --version 0.9.0 --no-default-features --features postgres,rustls,sqlx-toml";
const START_DB: &str = "docker compose -f deploy/compose.dev.yml up -d";

/// The environment lookup supplied by a caller.
pub type EnvLookup<'a> = dyn Fn(&str) -> Option<String> + 'a;

/// Flags, streams, and environment lookup for one database operation.
#[derive(Default)]
pub struct Options<'a> {
    /// The directory where the project search starts. Empty means the current directory.
    pub dir: PathBuf,
    /// The selected environment. Empty selects local development for Compose commands.
    pub env: String,
    /// Acknowledge data loss for a rollback.
    pub i_know: bool,
    /// Permit prompts when the caller has a terminal.
    pub interactive: bool,
    /// Answer yes to a confirmation prompt.
    pub yes: bool,
    /// Permit `--yes` to confirm a protected environment in CI.
    pub ci: bool,
    /// The explicit protected environment name.
    pub confirm_name: String,
    /// Print commands without running them. Guards still apply.
    pub dry_run: bool,
    /// The prompt input. Interactive psql inherits the process input.
    pub input: Option<&'a mut dyn Read>,
    /// The output stream. None discards output.
    pub out: Option<&'a mut (dyn Write + Send)>,
    /// The error stream. None discards output.
    pub err_out: Option<&'a mut (dyn Write + Send)>,
    /// Read environment variables. None uses the process environment.
    pub lookup: Option<&'a EnvLookup<'a>>,
}

pub(crate) fn load(o: &Options<'_>, what: &str) -> anyhow::Result<(PathBuf, Config)> {
    let start = if o.dir.as_os_str().is_empty() {
        Path::new(".")
    } else {
        &o.dir
    };
    let root = spinup_project::root::find_root(start).map_err(|e| op_error(what, e))?;
    let config = spinup_project::config::load(&root).map_err(|e| op_error(what, e))?;
    Ok((root, config))
}

fn confirm(o: &mut Options<'_>, env: &Env, action: &str, what: &str) -> anyhow::Result<()> {
    let input = o.input.as_mut().map(|input| &mut **input as &mut dyn Read);
    let output = o.out.as_mut().map(|output| &mut **output as &mut dyn Write);
    confirm_protected(
        env,
        action,
        Confirm {
            input,
            output,
            interactive: o.interactive,
            yes: o.yes,
            ci: o.ci,
            name: &o.confirm_name,
        },
    )
    .map_err(|e| op_error(what, e))
}

pub(crate) fn guard_migration(
    o: &mut Options<'_>,
    config: &Config,
    what: &str,
    action: &str,
    destructive: bool,
) -> anyhow::Result<()> {
    if config.envs.is_empty() && o.env.is_empty() {
        return Ok(());
    }
    let env = config.resolve_env(&o.env).map_err(|e| op_error(what, e))?;
    if destructive {
        confirm(o, &env, action, what)?;
    }
    Ok(())
}

fn guard_local(
    o: &mut Options<'_>,
    config: &Config,
    what: &str,
    action: &str,
    destructive: bool,
) -> anyhow::Result<()> {
    if o.env.is_empty() {
        return Ok(());
    }
    if !matches!(o.env.as_str(), "local" | "dev" | "development") {
        return Err(Error::new(what).why(format!("spinup {action} works only on the local development database, and --env names {:?}", o.env))
            .try_hint(format!("omit --env to use the postgres service in {COMPOSE_FILE}")).into());
    }
    if destructive && let Some(env) = config.envs.get(&o.env) {
        let mut env = env.clone();
        env.name.clone_from(&o.env);
        confirm(o, &env, action, what)?;
    }
    Ok(())
}

fn confirm_yes(o: &mut Options<'_>, what: &str, question: &str, hint: &str) -> anyhow::Result<()> {
    if o.yes {
        return Ok(());
    }
    if !o.interactive || o.input.is_none() {
        return Err(Error::new(what)
            .why("this needs confirmation, and spinup cannot prompt without a terminal")
            .try_hint(hint)
            .into());
    }
    write_out(o, what, &format!("{question} [y/N] "))?;
    if let Some(out) = &mut o.out {
        out.flush().map_err(|e| op_error(what, e))?;
    }
    let mut answer = String::new();
    if let Some(input) = &mut o.input {
        BufReader::new(input)
            .read_line(&mut answer)
            .map_err(|e| op_error(what, e))?;
    }
    if matches!(answer.trim().to_lowercase().as_str(), "y" | "yes") {
        return Ok(());
    }
    Err(Error::new(what)
        .why("you did not confirm")
        .try_hint(hint)
        .into())
}

pub(crate) fn database_cmd(
    o: &Options<'_>,
    root: &Path,
    cmd: Cmd,
    what: &str,
) -> anyhow::Result<Cmd> {
    if o.dry_run {
        return Ok(cmd);
    }
    let process_lookup = |key: &str| std::env::var(key).ok();
    let lookup = o.lookup.unwrap_or(&process_lookup);
    let url = spinup_project::env::database_url(root, lookup)
        .map_err(|e| op_error(what, e))?
        .ok_or_else(|| {
            Error::new(what)
                .why("DATABASE_URL is not set")
                .try_hint("set DATABASE_URL, DATABASE_URL_FILE, or DATABASE_URL in api/.env")
        })?;
    let mut cmd = cmd.env("DATABASE_URL", &url);
    cmd.redact.push(url);
    Ok(cmd)
}

pub(crate) fn stream(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
    what: &str,
    cmd: &Cmd,
) -> anyhow::Result<()> {
    if o.dry_run {
        return print_cmd(o, what, cmd, "");
    }
    capture(o, runner, cancel, what, cmd, true).map(|_| ())
}

pub(crate) fn capture(
    o: &mut Options<'_>,
    runner: &dyn Runner,
    cancel: &Cancellation,
    what: &str,
    cmd: &Cmd,
    stdout: bool,
) -> anyhow::Result<Output> {
    let out = if stdout {
        o.out
            .as_mut()
            .map(|out| &mut **out as &mut (dyn Write + Send))
    } else {
        None
    };
    let err = o
        .err_out
        .as_mut()
        .map(|err| &mut **err as &mut (dyn Write + Send));
    runner
        .run(
            cmd,
            cancel,
            Sinks {
                stdout: out,
                stderr: err,
            },
        )
        .map_err(|e| op_error(what, e))
}

pub(crate) fn write_out(o: &mut Options<'_>, what: &str, text: &str) -> anyhow::Result<()> {
    if let Some(out) = &mut o.out {
        out.write_all(text.as_bytes())
            .map_err(|e| op_error(what, e))?;
    }
    Ok(())
}

fn write_err(o: &mut Options<'_>, what: &str, text: &str) -> anyhow::Result<()> {
    if let Some(out) = &mut o.err_out {
        out.write_all(text.as_bytes())
            .map_err(|e| op_error(what, e))?;
    }
    Ok(())
}

fn print_cmd(o: &mut Options<'_>, what: &str, cmd: &Cmd, redirect: &str) -> anyhow::Result<()> {
    let suffix = if redirect.is_empty() {
        String::new()
    } else {
        format!(" {redirect}")
    };
    write_out(o, what, &format!("{cmd}{suffix}\n"))
}

fn compose(root: &Path, tty: bool, args: &[&str]) -> Cmd {
    let mut cmd = Cmd::new("docker")
        .args(["compose", "-f", COMPOSE_FILE, "exec"])
        .dir(root);
    if !tty {
        cmd.args.push("-T".into());
    }
    cmd.args.push("postgres".into());
    cmd.args.extend(args.iter().map(|s| (*s).into()));
    cmd
}

pub(crate) fn op_error(what: &str, error: impl Into<anyhow::Error>) -> anyhow::Error {
    let error = error.into();
    if error.downcast_ref::<Error>().is_some() {
        return error;
    }
    let mut why = format!("{error:#}");
    let mut hint = "check the command output, then retry".to_string();
    let mut code = spinup_ui::exit::ERROR;
    if let Some(exec) = error.downcast_ref::<ExecError>() {
        match exec {
            ExecError::NotFound { name } if name == "docker" => {
                why = "the docker command was not found".into();
                hint = format!("install Docker, then start the database: {START_DB}");
            }
            ExecError::NotFound { name } if name == "sqlx" => {
                why = "the sqlx command was not found".into();
                hint = SQLX_INSTALL_TRY.into();
            }
            ExecError::NotFound { name } => {
                why = format!("the {name} command was not found");
                hint = format!("install {name}, or run spinup doctor");
            }
            ExecError::Exit(exit) => {
                code = exit.code;
                why = format!("{} exited with status {}", exit.cmd, exit.code);
                let output = format!(
                    "{}{}",
                    String::from_utf8_lossy(&exit.output.stderr),
                    String::from_utf8_lossy(&exit.output.stdout)
                );
                if (exit.cmd.starts_with("sqlx ") || exit.cmd.starts_with("cargo sqlx "))
                    && (output.contains("--config")
                        || output.contains("sqlx-toml")
                        || output.contains("no such command: `sqlx`"))
                {
                    hint = SQLX_INSTALL_TRY.into();
                } else if exit.cmd.starts_with("docker ") {
                    hint = format!(
                        "check the output above. If the database is not running, start it: {START_DB}"
                    );
                }
            }
            _ => {}
        }
    }
    if let Some((before, after)) = why.split_once("Try:") {
        hint = after.trim().into();
        why = before.trim().trim_end_matches('.').into();
    }
    Error::new(what).why(why).try_hint(hint).code(code).into()
}

#[cfg(test)]
mod tests;
