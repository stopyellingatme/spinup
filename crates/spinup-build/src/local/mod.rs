//! Build the local release binary and optional embedded web assets.

use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{Context, bail};
use spinup_exec::runner::{Cancellation, Cmd, ExecError, Runner, Sinks};
use spinup_manifest::lock::Lock;
use spinup_project::env::{SQLX_NO_CACHE, SQLX_PREPARE_TRY};
use spinup_project::webtool::Tool;

/// Select the project, version, and output streams for a local build.
#[derive(Default)]
pub struct Options<'a> {
    /// Start the project search here. An empty path selects the current directory.
    pub dir: PathBuf,
    /// Select `api` or `all`. An empty value selects `api`.
    pub target: String,
    /// Set the application version. An empty value selects `dev`.
    pub tag: String,
    /// Set the application commit SHA. An empty value records no SHA.
    pub git_sha: String,
    /// Print the build commands without running commands or changing files.
    pub dry_run: bool,
    /// Receive command output. None discards output.
    pub out: Option<&'a mut (dyn Write + Send)>,
    /// Receive command diagnostics. None discards diagnostics.
    pub err_out: Option<&'a mut (dyn Write + Send)>,
}

/// Build the API and copy the Cargo artifact to `api/bin/api`.
/// Embedded apps build and copy the web assets first.
///
/// # Errors
/// Return an error with a repair hint when discovery, a build, or a copy fails.
pub fn local(
    cancel: &Cancellation,
    options: &mut Options<'_>,
    runner: &dyn Runner,
) -> anyhow::Result<()> {
    build(cancel, options, runner).map_err(build_error)
}

fn build(cancel: &Cancellation, o: &mut Options<'_>, runner: &dyn Runner) -> anyhow::Result<()> {
    match o.target.as_str() {
        "" | "api" | "all" => {}
        "web" => bail!(
            "Cannot build the web frontend: the web pack is not available as a local target\nTry: pass --target api"
        ),
        target => bail!(
            "Cannot build target: unknown target {target}\nTry: pass --target api or --target all"
        ),
    }
    let root = spinup_project::root::find_root(&o.dir)?;
    let lock_path = root.join("spinup.lock");
    let lock = match std::fs::read(&lock_path) {
        Ok(bytes) => Some(spinup_manifest::json::parse(&bytes).context("read spinup.lock")?),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => None,
        Err(error) => return Err(error).context("read spinup.lock"),
    };
    let toolchain = std::fs::read_to_string(root.join("api/rust-toolchain.toml"))
        .context("read api/rust-toolchain.toml")?;
    let toolchain: toml::Value =
        toml::from_str(&toolchain).context("parse api/rust-toolchain.toml")?;
    let channel = toolchain
        .get("toolchain")
        .and_then(|t| t.get("channel"))
        .and_then(toml::Value::as_str)
        .context("api/rust-toolchain.toml has no toolchain.channel string")?;
    let mut cmd = Cmd::new("cargo")
        .args([
            "build",
            "--release",
            "--locked",
            "--message-format=json-render-diagnostics",
        ])
        .dir(root.join("api"));
    cmd.env = vec![
        (
            "SPINUP_APP_VERSION".into(),
            if o.tag.is_empty() {
                "dev".into()
            } else {
                o.tag.clone()
            },
        ),
        ("SPINUP_APP_GIT_SHA".into(), o.git_sha.clone()),
        ("SPINUP_APP_RUST".into(), channel.into()),
    ];
    if embeds_web(lock.as_ref()) {
        build_web(&root, lock.as_ref(), cancel, o, runner)?;
    }
    if o.target == "all" {
        print(o, "Building available targets: api")?;
    }
    if o.dry_run {
        let preview = Cmd::new("env")
            .args(
                cmd.env
                    .iter()
                    .map(|(name, value)| format!("{name}={value}")),
            )
            .args([cmd.program.clone()])
            .args(cmd.args.clone());
        return print(o, &preview.to_string());
    }
    // Cargo stdout contains the artifact protocol. Do not print its JSON to the user.
    let result = runner.run(
        &cmd,
        cancel,
        Sinks {
            stdout: None,
            stderr: o
                .err_out
                .as_mut()
                .map(|s| &mut **s as &mut (dyn Write + Send)),
        },
    );
    let output = match result {
        Ok(output) => output,
        Err(error) => {
            if let Some(output) = error.output() {
                render_messages(&output.stdout, o)?;
                let text = format!(
                    "{error}\n{}\n{}",
                    String::from_utf8_lossy(&output.stdout),
                    String::from_utf8_lossy(&output.stderr)
                );
                bail!("{text}");
            }
            return Err(error.into());
        }
    };
    render_messages(&output.stdout, o)?;
    let artifact = executable(&output.stdout)?;
    let destination = root.join("api/bin/api");
    std::fs::create_dir_all(destination.parent().expect("binary has parent"))
        .context("create api/bin")?;
    std::fs::copy(&artifact, &destination)
        .with_context(|| format!("copy {} to api/bin/api", artifact.display()))?;
    Ok(())
}

fn embeds_web(lock: Option<&Lock>) -> bool {
    lock.and_then(|lock| lock.answers.get("frontend_mode"))
        .and_then(|raw| serde_json::from_str::<String>(raw.get()).ok())
        .is_some_and(|mode| mode == "embed")
}

fn build_web(
    root: &Path,
    lock: Option<&Lock>,
    cancel: &Cancellation,
    o: &mut Options<'_>,
    runner: &dyn Runner,
) -> anyhow::Result<()> {
    let tool = Tool::from_lock(lock);
    for cmd in [tool.install(true, false), tool.run("build", &[])] {
        let cmd = tool.in_web(root, cmd);
        if o.dry_run {
            print(o, &cmd.to_string())?;
        } else {
            runner.run(
                &cmd,
                cancel,
                Sinks {
                    stdout: o.out.as_mut().map(|s| &mut **s as &mut (dyn Write + Send)),
                    stderr: o
                        .err_out
                        .as_mut()
                        .map(|s| &mut **s as &mut (dyn Write + Send)),
                },
            )?;
        }
    }
    if o.dry_run {
        return print(o, "Copy web/build to api/webdist, replacing old assets");
    }
    let source = root.join("web/build");
    if !source.is_dir() {
        bail!("copy web/build: the build output is missing or is not a directory");
    }
    let dest = root.join("api/webdist");
    match std::fs::remove_dir_all(&dest) {
        Ok(()) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => return Err(error).context("replace api/webdist"),
    }
    for entry in walkdir::WalkDir::new(&source).sort_by_file_name() {
        let entry = entry.context("read web/build")?;
        let target = dest.join(entry.path().strip_prefix(&source)?);
        if entry.file_type().is_dir() {
            std::fs::create_dir_all(&target).context("create api/webdist directory")?;
        } else if entry.file_type().is_file() {
            std::fs::copy(entry.path(), &target).context("copy web/build to api/webdist")?;
        } else {
            bail!(
                "copy web/build: {} is not a regular file or directory",
                entry.path().display()
            );
        }
    }
    Ok(())
}

fn executable(bytes: &[u8]) -> anyhow::Result<PathBuf> {
    let mut executable = None;
    for line in bytes.split(|b| *b == b'\n').filter(|line| !line.is_empty()) {
        let value: serde_json::Value =
            serde_json::from_slice(line).context("read cargo JSON artifact messages")?;
        if value["reason"] == "compiler-artifact"
            && value["target"]["name"] == "api"
            && value["target"]["kind"]
                .as_array()
                .is_some_and(|k| k.iter().any(|v| v == "bin"))
            && let Some(path) = value["executable"].as_str()
        {
            executable = Some(PathBuf::from(path));
        }
    }
    executable.context("cargo build returned no executable artifact for the api binary")
}

fn render_messages(bytes: &[u8], o: &mut Options<'_>) -> anyhow::Result<()> {
    for line in bytes.split(|b| *b == b'\n') {
        if let Ok(value) = serde_json::from_slice::<serde_json::Value>(line)
            && let Some(text) = value["message"]["rendered"].as_str()
            && let Some(out) = o.err_out.as_mut()
        {
            out.write_all(text.as_bytes())?;
        }
    }
    Ok(())
}

fn print(o: &mut Options<'_>, text: &str) -> anyhow::Result<()> {
    if let Some(out) = o.out.as_mut() {
        writeln!(out, "{text}")?;
    }
    Ok(())
}

fn build_error(error: anyhow::Error) -> anyhow::Error {
    let text = format!("{error:#}");
    let hint = if text.contains(SQLX_NO_CACHE) {
        SQLX_PREPARE_TRY.to_string()
    } else if let Some(ExecError::NotFound { name }) = error.downcast_ref::<ExecError>() {
        format!("Try: install {name} and run spinup build again")
    } else if let Some(hint) = text.lines().rev().find(|line| line.starts_with("Try:")) {
        hint.to_string()
    } else {
        "Try: check the project and build output, then run spinup build again".into()
    };
    let reason = text
        .lines()
        .filter(|line| !line.starts_with("Try:"))
        .collect::<Vec<_>>()
        .join("\n");
    anyhow::anyhow!("Cannot build the API: {reason}\n{hint}")
}

#[cfg(test)]
mod tests;
