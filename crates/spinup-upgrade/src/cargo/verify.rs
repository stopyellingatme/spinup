//! Cargo, web, and local doctor verification.

use std::sync::{Arc, mpsc};

use anyhow::{Result, anyhow, bail};
use spinup_doctor::registry::{self, Context, Report, Scope, Status};
use spinup_exec::runner::{Cancellation, Cmd, ExecError, Output, Runner, Sinks};

use super::{clean_env, command, deps, exists, migrations, print};
use crate::contract::{Action, Step};

pub(super) fn run(s: &mut Step<'_>) -> Result<()> {
    if s.options.no_verify {
        return Ok(());
    }
    if !s.conflicts.is_empty()
        || s.plan.as_ref().is_some_and(|plan| {
            plan.files
                .iter()
                .any(|file| file.action == Action::Conflict)
        })
    {
        return print(
            s,
            "Skipped verify: the step has committed conflict markers\n",
        );
    }
    let owners = migrations::provenance(s);
    let mut failures = Vec::new();
    if exists(&s.worktree, "api/Cargo.toml")? {
        for args in [
            vec![
                "clippy",
                "--locked",
                "--all-targets",
                "--",
                "-D",
                "warnings",
            ],
            vec!["test", "--locked"],
        ] {
            if let Err(e) = command(s, Cmd::new("cargo").args(args).dir(s.worktree.join("api"))) {
                failures.push(migrations::attribute(format!("{e:#}"), &owners));
            }
        }
    }
    if exists(&s.worktree, "web/package.json")? {
        let tool = deps::tool(s);
        for script in ["check", "build"] {
            if let Err(e) = command(s, tool.run(script, &[]).dir(s.worktree.join("web"))) {
                failures.push(migrations::attribute(format!("{e:#}"), &owners));
            }
        }
    }
    for report in doctor(s)? {
        // Quick checks inspect the machine. The upgrade worktree has no .env.
        if !report.check.quick && report.result.status == Status::Fail {
            failures.push(migrations::attribute(
                format!("doctor {}: {}", report.check.id, report.result.message),
                &owners,
            ));
        }
    }
    if !failures.is_empty() {
        bail!(
            "upgrade verification failed:\n{}\nTry: fix the failures in {}, then run spinup upgrade --continue",
            failures.join("\n"),
            s.worktree.display()
        );
    }
    Ok(())
}

// The doctor owns an Arc<dyn Runner>. Step borrows its runner. A scoped worker keeps
// that borrow valid while the owned relay forwards each doctor command to the same runner.
struct Relay(mpsc::Sender<Request>);
struct Request {
    cmd: Cmd,
    cancel: Cancellation,
    reply: mpsc::SyncSender<std::result::Result<Output, ExecError>>,
}

impl Runner for Relay {
    fn run(
        &self,
        cmd: &Cmd,
        cancel: &Cancellation,
        mut sinks: Sinks<'_>,
    ) -> std::result::Result<Output, ExecError> {
        let (reply, response) = mpsc::sync_channel(1);
        self.0
            .send(Request {
                cmd: clean_env(cmd.clone()),
                cancel: cancel.clone(),
                reply,
            })
            .map_err(|e| relay_error(cmd, e.to_string()))?;
        let result = response
            .recv()
            .map_err(|e| relay_error(cmd, e.to_string()))?;
        let output = result
            .as_ref()
            .ok()
            .or_else(|| result.as_ref().err().and_then(ExecError::output));
        if let Some(output) = output {
            for (sink, bytes) in [
                (&mut sinks.stdout, &output.stdout),
                (&mut sinks.stderr, &output.stderr),
            ] {
                if let Some(sink) = sink {
                    sink.write_all(bytes)
                        .map_err(|e| relay_error(cmd, e.to_string()))?;
                }
            }
        }
        result
    }
}

fn relay_error(cmd: &Cmd, message: String) -> ExecError {
    ExecError::Io {
        cmd: cmd.to_string(),
        source: std::io::Error::other(message),
    }
}

fn doctor(s: &Step<'_>) -> Result<Vec<Report<'static>>> {
    std::thread::scope(|scope| {
        let (sender, requests) = mpsc::channel::<Request>();
        let runner = s.runner;
        let worker = scope.spawn(move || {
            while let Ok(request) = requests.recv() {
                let result = runner.output(&request.cmd, &request.cancel);
                let _ = request.reply.send(result);
            }
        });
        let mut ctx = Context::new(&s.worktree);
        ctx.cancel = s.cancel.clone();
        ctx.runner = Some(Arc::new(Relay(sender)));
        ctx.deps
            .insert("cli_version".into(), Arc::new(s.lock.cli_version.clone()));
        let reports = registry::run_all(Scope::Local, &ctx);
        drop(ctx);
        worker.join().map_err(|_| anyhow!("run the local doctor: the command worker panicked\nTry: run spinup doctor in the upgrade worktree"))?;
        Ok(reports)
    })
}
