//! The todo API binary: `serve` (the default), `worker`, `migrate`,
//! `openapi`, and `healthcheck`. `serve` has the full kit: auth, the
//! password reset (with `SMTP_URL`), OIDC, and telemetry.

use std::process::ExitCode;
#[cfg(feature = "auth")]
use std::sync::Arc;

use clap::{Parser, Subcommand};
#[cfg(feature = "auth")]
use kit::auth::Auth;
#[cfg(feature = "auth")]
use kit::auth::reset::ResetOptions;
#[cfg(feature = "auth")]
use kit::mail::SmtpMailer;
use kit::{App, Config};

/// The feature `mimalloc` replaces the system allocator.
#[cfg(feature = "mimalloc")]
#[global_allocator]
static GLOBAL: mimalloc::MiMalloc = mimalloc::MiMalloc;

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
            #[cfg(feature = "auth")]
            let result = {
                let auth = Auth::new(pool.clone(), todo_api::auth_options(&config))?;
                let api = todo_api::router_with_auth(pool.clone(), &auth, reset_options()?)?;
                let result = App::new(config, api, Some(pool)).serve().await;
                auth.close().await;
                result
            };
            #[cfg(not(feature = "auth"))]
            let result = App::new(config, todo_api::router(pool.clone()), Some(pool))
                .serve()
                .await;
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

#[cfg(feature = "auth")]
/// The password reset settings: on when SMTP_URL is set (Mailpit in
/// development: smtp://localhost:1025).
fn reset_options() -> anyhow::Result<Option<ResetOptions>> {
    let env = |k: &str| std::env::var(k).ok().filter(|v| !v.is_empty());
    let Some(url) = env("SMTP_URL") else {
        return Ok(None);
    };
    let origin = env("PUBLIC_ORIGIN").unwrap_or_else(|| "http://localhost:5173".into());
    Ok(Some(ResetOptions {
        mailer: Arc::new(SmtpMailer::from_url(&url)?),
        from: env("MAIL_FROM").unwrap_or_else(|| "no-reply@localhost".into()),
        base_url: format!("{origin}/reset"),
    }))
}

/// The service name is the app name plus `-api`: the Prometheus `job` label
/// of the monitoring pack.
const SERVICE_NAME: &str = "todo-api";

#[cfg(feature = "telemetry")]
use kit::telemetry::Telemetry;

/// Without the feature `telemetry`, the binary writes only the JSON log.
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
