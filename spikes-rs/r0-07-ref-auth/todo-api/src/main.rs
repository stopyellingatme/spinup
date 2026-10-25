//! The todo API binary: `serve` (the default), `migrate`, `openapi`, and
//! `healthcheck`.

use std::process::ExitCode;

use clap::{Parser, Subcommand};
use std::sync::Arc;

use kit::auth::Auth;
use kit::auth::reset::ResetOptions;
use kit::mail::SmtpMailer;
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
            let auth = Auth::new(pool.clone(), todo_api::auth_options(&config))?;
            let api = todo_api::router_with_auth(pool.clone(), &auth, reset_options()?)?;
            let result = App::new(config, api, Some(pool)).serve().await;
            auth.close().await;
            result
        }
    }
}

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
