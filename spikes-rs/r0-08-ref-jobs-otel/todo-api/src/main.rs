//! The todo API binary: `serve` (the default), `worker`, `migrate`,
//! `openapi`, and `healthcheck`.

use std::process::ExitCode;

use clap::{Parser, Subcommand};
use kit::{App, Config};

#[derive(Parser)]
#[command(name = "todo-api", about = "The todo API")]
struct Cli {
    #[command(subcommand)]
    command: Option<Command>,
}

#[derive(Subcommand)]
enum Command {
    /// Serve HTTP on PORT (the default command).
    Serve,
    /// Work the background jobs until SIGINT or SIGTERM.
    #[cfg(feature = "jobs")]
    Worker,
    /// Run the embedded migrations: up, down, status, or version.
    Migrate { sub: Option<String> },
    /// Write the OpenAPI 3.1 document as YAML to stdout.
    Openapi,
    /// Exit 0 when the server on PORT answers /healthz with 200.
    Healthcheck,
}

#[tokio::main]
async fn main() -> ExitCode {
    let cli = Cli::parse();
    match run(cli.command.unwrap_or(Command::Serve)).await {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("todo-api: {err}");
            ExitCode::FAILURE
        }
    }
}

async fn run(command: Command) -> anyhow::Result<()> {
    if let Command::Openapi = command {
        print!("{}", todo_api::openapi().to_yaml()?);
        return Ok(());
    }
    let config = Config::from_env()?;
    match command {
        Command::Openapi => unreachable!("handled above"),
        Command::Healthcheck => kit::app::healthcheck(config.port).await,
        Command::Migrate { sub } => {
            let pool = kit::db::pool(&config)?;
            let mut out = std::io::stdout();
            let result = kit::db::migrate_command(
                &pool,
                &todo_api::MIGRATOR,
                sub.as_deref().unwrap_or(""),
                &mut out,
            )
            .await;
            pool.close().await;
            result
        }
        Command::Serve => {
            let telemetry = start_telemetry(&config)?;
            let pool = kit::db::pool(&config)?;
            let api = todo_api::router(pool.clone());
            let result = App::new(config, api, Some(pool)).serve().await;
            stop_telemetry(telemetry, result).await
        }
        #[cfg(feature = "jobs")]
        Command::Worker => {
            let telemetry = start_telemetry(&config)?;
            let pool = kit::db::pool(&config)?;
            let worker = todo_api::jobs::worker(pool.clone(), kit::jobs::Config::default());
            let result = worker.run(kit::app::shutdown_signal()).await;
            pool.close().await;
            stop_telemetry(telemetry, result).await
        }
    }
}

/// The service name is the app name plus `-api`: the Prometheus `job` label
/// of the monitoring pack.
const SERVICE_NAME: &str = "todo-api";

#[cfg(feature = "telemetry")]
use kit::telemetry::Telemetry;

/// The R0.6 build has no telemetry: only the JSON log.
#[cfg(not(feature = "telemetry"))]
struct Telemetry;

#[cfg(not(feature = "telemetry"))]
impl Telemetry {
    async fn shutdown(self) -> anyhow::Result<()> {
        Ok(())
    }
}

#[cfg(feature = "telemetry")]
fn start_telemetry(config: &Config) -> anyhow::Result<Telemetry> {
    let opts = kit::telemetry::Options {
        service_version: env!("CARGO_PKG_VERSION").into(),
        environment: config.env.clone(),
        ..kit::telemetry::Options::new(SERVICE_NAME)
    };
    kit::telemetry::init(config, &opts)
}

#[cfg(not(feature = "telemetry"))]
fn start_telemetry(config: &Config) -> anyhow::Result<Telemetry> {
    let _ = SERVICE_NAME;
    kit::app::init_logging(config);
    Ok(Telemetry)
}

/// Flushes the telemetry. The error of the run comes first.
async fn stop_telemetry(telemetry: Telemetry, result: anyhow::Result<()>) -> anyhow::Result<()> {
    let flushed = telemetry.shutdown().await;
    result.and(flushed)
}
