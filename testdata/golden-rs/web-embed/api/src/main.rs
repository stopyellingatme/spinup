//! The API commands: serve, migrate, openapi, healthcheck, and version.

use std::process::ExitCode;

use kit::app::App;
use kit::config::Base;

#[cfg(feature = "embedweb")]
include!(concat!(env!("OUT_DIR"), "/webdist.rs"));

fn main() -> ExitCode {
    kit::app::main(api::openapi::openapi, start)
}

async fn start() -> anyhow::Result<App> {
    let config: Base = kit::config::load()?;
    #[cfg(feature = "telemetry")]
    let telemetry = kit::telemetry::init(
        &config,
        &kit::telemetry::Options {
            service_version: option_env!("SPINUP_APP_VERSION").unwrap_or("dev").into(),
            environment: config.env.clone(),
            ..kit::telemetry::Options::new("todo-api")
        },
    )?;
    #[cfg(not(feature = "telemetry"))]
    kit::logx::init(&config)?;

    let pool = kit::db::pool(&config)?;
    let routes = api::router(pool.clone());
    let app = App::new(&config)
        .name("todo")
        .version(option_env!("SPINUP_APP_VERSION").unwrap_or("dev"))
        .git_sha(option_env!("SPINUP_APP_GIT_SHA").unwrap_or_default())
        .rust_version(option_env!("SPINUP_APP_RUST").unwrap_or_default())
        .routes(routes)
        .readiness("db", kit::db::readiness(pool.clone()))
        .command(
            "migrate",
            kit::db::migrate_command(
                kit::db::Migrations::new(sqlx::migrate!())
                    .database_url(config.database_url.clone()),
            ),
        )
        .shutdown({
            let pool = pool.clone();
            move || async move {
                pool.close().await;
                Ok(())
            }
        });
    #[cfg(feature = "telemetry")]
    let app = app.with_telemetry(&telemetry);
    #[cfg(feature = "apidocs")]
    let app = app.with_api_docs(api::openapi::openapi().to_yaml()?);
    #[cfg(feature = "devtools")]
    let app = app.with_devtools(Some(kit::db::migration_status(pool)));
    #[cfg(feature = "embedweb")]
    let app = app.with_spa(&WEB_FILES);
    Ok(app)
}
