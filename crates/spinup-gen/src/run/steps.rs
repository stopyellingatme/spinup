//! Post-step registration and order (Go `internal/gen/run/poststeps.go`).
//!
//! R1 registers no post-step runner. The runners register into [`POST_STEPS`] from their own
//! module (R2.11), so this module has no central list.

use std::path::Path;

use anyhow::anyhow;
use spinup_exec::runner::{Cancellation, Runner};

use crate::contract::PostStep;

/// What a post-step runner gets.
#[derive(Clone, Copy)]
pub struct StepInput<'a> {
    /// The project root.
    pub root: &'a Path,
    /// The sorted plan paths, relative to the project root.
    pub files: &'a [String],
    /// Runs the tools of the step.
    pub runner: &'a dyn Runner,
    /// The cancel signal of the CLI.
    pub cancel: &'a Cancellation,
}

/// The runner of one [`PostStep`].
pub struct StepRunner {
    /// The step that this runner runs.
    pub step: PostStep,
    /// Runs the step after the write. The error text should include the tool output.
    pub run: fn(&StepInput<'_>) -> anyhow::Result<()>,
    /// The `Try:` hint after a failure, such as `run cargo sqlx prepare in api/ by hand`.
    /// It gets the sorted plan paths.
    pub retry: fn(&[String]) -> String,
}

impl std::fmt::Debug for StepRunner {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("StepRunner")
            .field("step", &self.step)
            .finish_non_exhaustive()
    }
}

/// Every registered post-step runner, in link order. Each step has at most one runner.
#[linkme::distributed_slice]
pub static POST_STEPS: [StepRunner];

/// The name of a step in the JSON result and in error texts.
pub fn step_name(step: PostStep) -> &'static str {
    match step {
        PostStep::Migrate => "migrate",
        PostStep::SqlxPrepare => "sqlx-prepare",
        PostStep::Build => "build",
        PostStep::OpenApiEmit => "openapi-emit",
        PostStep::Client => "client",
    }
}

/// Returns each requested step once, in [`PostStep`] order (ADR 0019 §4).
pub(crate) fn ordered(requested: &[PostStep]) -> Vec<PostStep> {
    let mut steps = requested.to_vec();
    steps.sort_unstable();
    steps.dedup();
    steps
}

/// Finds the runner of each step before any step runs.
pub(crate) fn runners<'a>(
    steps: &[PostStep],
    registered: &'a [StepRunner],
) -> anyhow::Result<Vec<&'a StepRunner>> {
    steps
        .iter()
        .map(|&step| {
            let mut found = registered.iter().filter(|r| r.step == step);
            match (found.next(), found.next()) {
                (Some(runner), None) => Ok(runner),
                (None, _) => Err(anyhow!(
                    "cannot run generator post-step {:?}: no runner is registered\nTry: register the {} post-step runner, then repeat the generator",
                    step_name(step),
                    step_name(step)
                )),
                (Some(_), Some(_)) => Err(anyhow!(
                    "cannot run generator post-step {:?}: two runners are registered\nTry: register one {} post-step runner, then repeat the generator",
                    step_name(step),
                    step_name(step)
                )),
            }
        })
        .collect()
}
