//! Web client generator and its registered post-step.
use anyhow::{Context, Result, bail};
use spinup_exec::runner::{Cancellation, Cmd, Runner};
use spinup_gen::{
    contract::{Generator, Input, Output, PostStep},
    run::{StepInput, StepRunner},
};
use spinup_manifest::lock::Lock;
use spinup_project::webtool::Tool;
use std::{io::Write, path::Path};

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static CLIENT: Generator = Generator {
    name: "client",
    aliases: &[],
    short: "Generate TypeScript types from OpenAPI",
    usage: "",
    args: |cmd| cmd,
    run: plan,
};
#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::run::POST_STEPS)]
static POST_CLIENT: StepRunner = StepRunner {
    step: PostStep::Client,
    run: post_step,
    retry: |_| "spinup g client".into(),
};

fn plan(input: &Input<'_>) -> Result<Output> {
    if !input.args.is_empty() {
        bail!("cannot generate client: expected no positional arguments\nTry: spinup g client");
    }
    Ok(Output {
        post_steps: vec![PostStep::OpenApiEmit, PostStep::Client],
        next: vec![format!(
            "cd web && {}",
            Tool::from_lock(Some(input.lock)).run("check", &[])
        )],
        ..Output::default()
    })
}
fn post_step(input: &StepInput<'_>) -> Result<()> {
    generate(
        input.root,
        input.runner,
        input.cancel,
        &mut std::io::stderr(),
    )
}

/// Generates the client, or writes a skip notice for an app without a web bundle.
///
/// # Errors
/// Returns an error if the lock, dependencies, or client tool fails.
pub fn generate(
    root: &Path,
    runner: &dyn Runner,
    cancel: &Cancellation,
    notices: &mut dyn Write,
) -> Result<()> {
    let lock = Lock::load(root.join("spinup.lock"))?;
    let reason = if !lock.packs.iter().any(|p| p == "web") {
        "no web pack"
    } else if !crate::pages::exists(root, "api/openapi/bundle.gen.yaml")? {
        "api/openapi/bundle.gen.yaml is missing"
    } else {
        ""
    };
    if !reason.is_empty() {
        writeln!(notices, "client generation skipped: {reason}")
            .context("write client notice failed\nTry: check the output destination")?;
        return Ok(());
    }
    let tool = Tool::from_lock(Some(&lock));
    match std::fs::metadata(root.join("web/node_modules")) {
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => run(
            runner,
            cancel,
            &tool.in_web(root, tool.install(true, false)),
        )?,
        Err(e) => bail!("inspect web/node_modules: {e}\nTry: check the directory permissions"),
        Ok(info) if !info.is_dir() => bail!(
            "cannot generate client: web/node_modules is not a directory\nTry: remove web/node_modules, then run spinup g client"
        ),
        Ok(_) => {}
    }
    generate_types(root, tool.client_output(), runner, cancel)
}

/// Runs the selected client tool with output relative to `web/`, or an absolute output.
///
/// # Errors
/// Returns an error if the lock or a client command fails.
pub fn generate_types(
    root: &Path,
    output: &str,
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> Result<()> {
    let lock = Lock::load(root.join("spinup.lock"))?;
    let tool = Tool::from_lock(Some(&lock));
    run(runner, cancel, &tool.in_web(root, tool.client(output)))?;
    if tool.hey_api() {
        let schema = Path::new(output)
            .parent()
            .unwrap_or_else(|| Path::new(""))
            .join("schema.d.ts")
            .to_string_lossy()
            .replace('\\', "/");
        run(runner, cancel, &tool.in_web(root, tool.schema(&schema)))?;
    }
    Ok(())
}
fn run(runner: &dyn Runner, cancel: &Cancellation, cmd: &Cmd) -> Result<()> {
    let output = runner.output(cmd, cancel).map_err(|e| {
        let output = e.output().map(|o| format!("{}{}", String::from_utf8_lossy(&o.stdout), String::from_utf8_lossy(&o.stderr))).unwrap_or_default();
        anyhow::anyhow!("run {} for client generation: {e}\n{output}\nTry: ensure {} is on PATH, correct the tool error, then run spinup g client", cmd.program, cmd.program)
    })?;
    if output.exit_code != 0 {
        bail!(
            "{} for client generation exited with code {}\n{}{}\nTry: correct the tool error, then run spinup g client",
            cmd.program,
            output.exit_code,
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use spinup_exec::fake::Fake;
    #[test]
    #[allow(
        clippy::disallowed_methods,
        reason = "Temporary test fixture for the client post-step."
    )]
    fn clients_managers_install_and_skips() -> Result<()> {
        for (manager, client) in [
            ("pnpm", "openapi-fetch"),
            ("pnpm", "heyapi"),
            ("bun", "openapi-fetch"),
            ("bun", "heyapi"),
        ] {
            let root = tempfile::tempdir()?;
            let mut lock = Lock::new();
            lock.packs.push("web".into());
            lock.answers.insert(
                "package_manager".into(),
                serde_json::value::RawValue::from_string(serde_json::to_string(manager)?)?,
            );
            lock.answers.insert(
                "api_client".into(),
                serde_json::value::RawValue::from_string(serde_json::to_string(client)?)?,
            );
            lock.save(root.path().join("spinup.lock"))?;
            let runner = Fake::new();
            let cancel = Cancellation::new();
            let mut notices = Vec::new();
            generate(root.path(), &runner, &cancel, &mut notices)?;
            assert_eq!(
                String::from_utf8(notices)?,
                "client generation skipped: api/openapi/bundle.gen.yaml is missing\n"
            );
            assert!(runner.calls().is_empty());
            std::fs::create_dir_all(root.path().join("api/openapi"))?;
            std::fs::write(
                root.path().join("api/openapi/bundle.gen.yaml"),
                "openapi: 3.1.0",
            )?;
            let tool = Tool::new(manager, client);
            let install = tool.in_web(root.path(), tool.install(true, false));
            runner.expect(&install.program, &install.args);
            let cmd = tool.in_web(root.path(), tool.client(tool.client_output()));
            runner.expect(&cmd.program, &cmd.args);
            if tool.hey_api() {
                let cmd = tool.in_web(root.path(), tool.schema("src/lib/api/schema.d.ts"));
                runner.expect(&cmd.program, &cmd.args);
            }
            generate(root.path(), &runner, &cancel, &mut Vec::new())?;
            assert_eq!(runner.calls()[0], install);
            assert_eq!(runner.calls()[1], cmd);
            runner.assert_done()?;
            std::fs::write(root.path().join("web/node_modules"), "not a directory").or_else(
                |e| {
                    if e.kind() == std::io::ErrorKind::NotFound {
                        std::fs::create_dir_all(root.path().join("web"))?;
                        std::fs::write(root.path().join("web/node_modules"), "not a directory")
                    } else {
                        Err(e)
                    }
                },
            )?;
            assert!(
                generate(root.path(), &runner, &cancel, &mut Vec::new())
                    .unwrap_err()
                    .to_string()
                    .contains("not a directory")
            );
        }
        Ok(())
    }
}
