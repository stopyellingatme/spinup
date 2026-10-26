//! The API commands: serve, migrate, openapi, healthcheck, and version.

use std::process::ExitCode;

use kit::app::App;
use kit::config::Base;

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
    #[cfg(feature = "auth")]
    let (auth, routes) = {
        use kit::config::FromEnv;

        struct Web {
            public_origin: String,
        }

        impl FromEnv for Web {
            fn from_env(env: &mut kit::config::Env<'_>) -> Self {
                Self {
                    public_origin: env.value("PUBLIC_ORIGIN", "http://localhost:5173".into()),
                }
            }
        }

        let Web { public_origin } = kit::config::load()?;
        let auth = kit::auth::Auth::new(
            pool.clone(),
            kit::auth::Options {
                secure: !config.is_dev(),
                origins: vec![public_origin.clone()],
                ..kit::auth::Options::default()
            },
        )?;
        let routes = api::router(pool.clone()).merge(auth.routes());
        #[cfg(feature = "oidc")]
        let routes = routes.merge(kit::auth::oidc::routes(
            &auth,
            kit::auth::oidc::OidcOptions::load(),
        ));
        let routes = auth.layer(routes);
        (auth, routes)
    };
    #[cfg(not(feature = "auth"))]
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
    #[cfg(feature = "auth")]
    let app = app.shutdown(move || async move {
        auth.close().await;
        Ok(())
    });
    #[cfg(feature = "telemetry")]
    let app = app.with_telemetry(&telemetry);
    #[cfg(feature = "apidocs")]
    let app = app.with_api_docs(api::openapi::openapi().to_yaml()?);
    #[cfg(feature = "devtools")]
    let app = app.with_devtools(Some(kit::db::migration_status(pool)));
    Ok(app)
}
