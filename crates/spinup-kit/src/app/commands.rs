//! The command dispatch (Go `App.Run` and `app.Main`).

use std::future::Future;
use std::io::Write;
use std::process::ExitCode;

use utoipa::openapi::OpenApi;

use super::{App, CommandInput, healthcheck, openapi, serve};

impl App {
    /// Runs the command that `args` names (the process arguments without the program
    /// name), or serves HTTP when `args` is empty (Go `App.Run`). `shutdown` stops the
    /// server or a long-running command. The error of a registered command comes back
    /// unchanged.
    ///
    /// `openapi` is not dispatched here: [`main`] answers it before the configuration load.
    pub async fn run(
        mut self,
        args: Vec<String>,
        shutdown: impl Future<Output = ()> + Send + 'static,
    ) -> anyhow::Result<()> {
        let exit = std::mem::take(&mut self.exit);
        let result = self.dispatch(args, shutdown).await;
        let mut failures = Vec::new();
        for hook in exit.into_iter().rev() {
            if let Err(e) = hook().await {
                failures.push(e);
            }
        }
        match (result, failures.into_iter().next()) {
            (Err(e), _) => Err(e),
            (Ok(()), Some(e)) => Err(e),
            (Ok(()), None) => Ok(()),
        }
    }

    async fn dispatch(
        mut self,
        args: Vec<String>,
        shutdown: impl Future<Output = ()> + Send + 'static,
    ) -> anyhow::Result<()> {
        let Some(name) = args.first().cloned() else {
            return self.serve(shutdown).await;
        };
        if let Some(command) = self.commands.remove(&name) {
            return command
                .run(CommandInput::new(args[1..].to_vec(), shutdown))
                .await;
        }
        match name.as_str() {
            "serve" => self.serve(shutdown).await,
            "healthcheck" => {
                let port = healthcheck_port(&self.listen_addr())?;
                tokio::task::spawn_blocking(move || healthcheck(port))
                    .await
                    .map_err(|e| {
                        anyhow::anyhow!("healthcheck: {e}\nTry: run the healthcheck again")
                    })?
            }
            "version" => {
                let mut out = std::io::stdout().lock();
                writeln!(out, "{}", self.version_json())
                    .map_err(|e| anyhow::anyhow!("version: {e}\nTry: check the output pipe"))
            }
            _ => {
                // The list holds the built-in commands too. `openapi` is answered by
                // [`main`], so the app binary accepts it.
                let mut names: Vec<&str> = self.commands.keys().map(String::as_str).collect();
                names.extend(BUILT_IN);
                names.sort_unstable();
                names.dedup();
                let commands = names.join(", ");
                anyhow::bail!(
                    "cannot run command {name:?}: unknown command. Available commands: {commands}.\nTry: use a listed command, or omit the command to serve HTTP"
                )
            }
        }
    }
}

/// The commands that every app binary has, in addition to the registered commands.
const BUILT_IN: [&str; 4] = ["healthcheck", "openapi", "serve", "version"];

/// The port of a listen address such as `:8080`, `0.0.0.0:8080`, or `[::]:8080`.
fn healthcheck_port(addr: &str) -> anyhow::Result<u16> {
    addr.rsplit_once(':')
        .and_then(|(_, port)| port.parse::<u16>().ok())
        .filter(|p| *p != 0)
        .ok_or_else(|| {
            anyhow::anyhow!(
                "healthcheck: cannot read the port from {addr:?}\nTry: set PORT or App::addr"
            )
        })
}

/// Runs an app binary. It reads the process arguments. For `openapi` it writes the YAML of
/// `openapi()` to stdout and returns, before it calls `start`: the spec export needs no
/// setting and no secret. For every other command it starts a multi-thread tokio runtime,
/// calls `start` (which loads the configuration, starts the log, and builds the [`App`]),
/// and runs [`App::run`] with [`super::shutdown_signal`].
///
/// On an error it writes the error text to stderr and returns exit code 1.
pub fn main<O, S, F>(openapi: O, start: S) -> ExitCode
where
    O: FnOnce() -> OpenApi,
    S: FnOnce() -> F,
    F: Future<Output = anyhow::Result<App>>,
{
    let args: Vec<String> = std::env::args_os()
        .skip(1)
        .map(|a| a.to_string_lossy().into_owned())
        .collect();
    if args.first().map(String::as_str) == Some("openapi") {
        return exit_code(openapi::write_yaml(
            openapi(),
            &mut std::io::stdout().lock(),
        ));
    }
    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(rt) => rt,
        Err(e) => {
            return exit_code(Err(anyhow::anyhow!(
                "cannot start the async runtime: {e}\nTry: check the thread limits of the process"
            )));
        }
    };
    let result = runtime.block_on(async move {
        let app = start().await?;
        app.run(args, serve::shutdown_signal()).await
    });
    exit_code(result)
}

fn exit_code(result: anyhow::Result<()>) -> ExitCode {
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("{e:#}");
            ExitCode::FAILURE
        }
    }
}
