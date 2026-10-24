//! The todo API binary: `serve` (the default), `migrate`, `openapi`, and
//! `healthcheck`.

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
            kit::app::init_logging(&config);
            let pool = kit::db::pool(&config)?;
            let api = todo_api::router(pool.clone());
            App::new(config, api, Some(pool)).serve().await
        }
    }
}
