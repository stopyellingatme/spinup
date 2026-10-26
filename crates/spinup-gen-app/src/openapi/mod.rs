//! Emit the API OpenAPI document into a tracked seed file.

#[cfg(test)]
mod tests;

use anyhow::{Context, Result, bail};
use spinup_exec::runner::Cmd;
use spinup_gen::{
    contract::{Generator, Input, Output, PostStep},
    run::{StepInput, StepRunner},
};
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_project::env::{SQLX_NO_CACHE, SQLX_PREPARE_TRY};
use spinup_vfs::plan::{File, Plan};

/// The emitted OpenAPI seed file, relative to the project root.
pub const BUNDLE: &str = "api/openapi/bundle.gen.yaml";

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::contract::GENERATORS)]
static OPENAPI: Generator = Generator {
    name: "openapi",
    aliases: &[],
    short: "Emit the API OpenAPI document",
    usage: "",
    args: |cmd| cmd,
    run: plan,
};

#[allow(unsafe_code)]
#[linkme::distributed_slice(spinup_gen::run::POST_STEPS)]
static EMIT: StepRunner = StepRunner {
    step: PostStep::OpenApiEmit,
    run: emit,
    retry: |_| "spinup db prepare, then spinup g openapi".into(),
};

fn plan(input: &Input<'_>) -> Result<Output> {
    if !input.args.is_empty() {
        bail!("cannot generate OpenAPI: expected no positional arguments\nTry: spinup g openapi");
    }
    Ok(Output {
        post_steps: vec![PostStep::OpenApiEmit],
        next: vec!["cd api && cargo build --locked".into()],
        ..Output::default()
    })
}

fn emit(input: &StepInput<'_>) -> Result<()> {
    let lock_path = input.root.join("spinup.lock");
    let mut lock = Lock::load(&lock_path)?;
    let cmd = Cmd::new("cargo")
        .args(["run", "--locked", "--quiet", "--", "openapi"])
        .dir(input.root.join("api"));
    let output = match input.runner.output(&cmd, input.cancel) {
        Ok(output) if output.exit_code == 0 => output,
        Ok(output) => {
            return Err(emit_error(format!(
                "cargo run exited with code {}\n{}{}",
                output.exit_code,
                String::from_utf8_lossy(&output.stdout),
                String::from_utf8_lossy(&output.stderr)
            )));
        }
        Err(error) => {
            let mut failure = error.to_string();
            if let Some(output) = error.output() {
                failure.push_str(&format!(
                    "\n{}{}",
                    String::from_utf8_lossy(&output.stdout),
                    String::from_utf8_lossy(&output.stderr)
                ));
            }
            return Err(emit_error(failure));
        }
    };
    let doc: yaml_serde::Value = yaml_serde::from_slice(&output.stdout)
        .context("cannot read the emitted OpenAPI document: invalid YAML\nTry: correct the API openapi command, then run spinup g openapi")?;
    if doc
        .get("openapi")
        .and_then(yaml_serde::Value::as_str)
        .is_none()
    {
        bail!(
            "cannot emit OpenAPI: the API output has no OpenAPI version\nTry: correct the API openapi command, then run spinup g openapi"
        );
    }
    let mut plan = Plan::new();
    plan.add(File {
        path: BUNDLE.into(),
        data: output.stdout,
        class: "seed".into(),
        ..File::default()
    })?;
    spinup_vfs::apply::apply(input.root, &plan, Default::default())?;
    lock.set_file(
        BUNDLE,
        FileEntry {
            class: Class::Seed,
            template: "gen/openapi".into(),
            hash: spinup_manifest::hash::hash(&plan.files[0].data),
            mode: "0644".into(),
        },
    )?;
    lock.save(lock_path)?;
    Ok(())
}

fn emit_error(failure: String) -> anyhow::Error {
    let hint = if failure.contains(SQLX_NO_CACHE) {
        SQLX_PREPARE_TRY
    } else {
        "Try: correct the API build error, then run spinup g openapi"
    };
    anyhow::anyhow!("cannot emit OpenAPI: {failure}\n{hint}")
}
