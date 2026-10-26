//! Registered post-steps and their ordered execution.

#[cfg(test)]
mod tests;

use anyhow::anyhow;
use spinup_exec::runner::Cmd;
use spinup_project::env::{SQLX_NO_CACHE, SQLX_PREPARE_TRY};

use crate::contract::PostStep;
use crate::run::{POST_STEPS, StepInput, StepRunner, step_name};

// SAFETY: linkme uses its own link section for the registration contract.
#[allow(unsafe_code)]
#[linkme::distributed_slice(POST_STEPS)]
static BUILD: StepRunner = StepRunner {
    step: PostStep::Build,
    run: build,
    retry: |_| "run cargo build --locked in api/ by hand".into(),
};

/// Runs each requested post-step once, in contract order.
///
/// All runners must exist before the first step runs. A failure stops later steps.
///
/// # Errors
/// Returns an error with the tool output and a `Try:` line when a runner fails or is missing.
pub fn run_steps(steps: &[PostStep], input: &StepInput<'_>) -> anyhow::Result<()> {
    run_registered(steps, input, POST_STEPS.static_slice())
}

pub(crate) fn run_registered(
    steps: &[PostStep],
    input: &StepInput<'_>,
    registered: &[StepRunner],
) -> anyhow::Result<()> {
    let steps = crate::run::steps::ordered(steps);
    let runners = crate::run::steps::runners(&steps, registered)?;
    for (step, runner) in steps.into_iter().zip(runners) {
        if let Err(error) = (runner.run)(input) {
            let text = format!("{error:#}");
            let retry = if step == PostStep::Build && text.contains(SQLX_NO_CACHE) {
                SQLX_PREPARE_TRY.trim_start_matches("Try: ").to_string()
            } else {
                (runner.retry)(input.files)
            };
            return Err(anyhow!(
                "generator post-step {:?} failed: {}\nTry: {retry}",
                step_name(step),
                crate::run::commands::reason(&text),
            ));
        }
    }
    Ok(())
}

fn build(input: &StepInput<'_>) -> anyhow::Result<()> {
    let cmd = Cmd::new("cargo")
        .args(["build", "--locked"])
        .dir(input.root.join("api"));
    let failure = match input.runner.output(&cmd, input.cancel) {
        Ok(output) if output.exit_code == 0 => return Ok(()),
        Ok(output) => format!(
            "cargo build --locked exited with code {}\n{}{}",
            output.exit_code,
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        ),
        Err(error) => {
            let mut text = crate::run::commands::reason(&error.to_string());
            if let Some(output) = error.output() {
                text.push('\n');
                text.push_str(&String::from_utf8_lossy(&output.stdout));
                text.push_str(&String::from_utf8_lossy(&output.stderr));
            }
            text
        }
    };
    let retry = if failure.contains(SQLX_NO_CACHE) {
        SQLX_PREPARE_TRY
    } else {
        "Try: run cargo build --locked in api/ by hand"
    };
    Err(anyhow!("build API failed: {failure}\n{retry}"))
}
