//! The smallest generated app: the kit routes and nothing else.

use axum::Router;
use kit::{App, Config};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let config = Config::from_env()?;
    kit::app::init_logging(&config);
    App::new(config, Router::new(), None).serve().await
}
