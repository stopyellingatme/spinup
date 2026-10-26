//! The app: the builder, the built-in routes, serve with readiness and graceful shutdown,
//! and the command dispatch (Go `kit/app`).
//!
//! An app binary calls [`main`]. `main` answers `openapi` before it loads any setting, so
//! the spec export runs without app secrets. For every other command it runs the start
//! function, which loads the configuration and returns an [`App`], and then dispatches:
//!
//! | Command | Action |
//! |---|---|
//! | (none), `serve` | Serves HTTP until SIGINT or SIGTERM (Ctrl-C on Windows). |
//! | `healthcheck` | Asks `GET /healthz` of the server on the same port (the Docker health check of a distroless image). |
//! | `openapi` | Writes the OpenAPI document as YAML to stdout. |
//! | `version` | Writes `{"name","version","rust"}` to stdout. |
//! | a registered name | Runs the [`Command`] of [`App::command`], such as `migrate` and `worker`. |
//!
//! ```no_run
//! use std::process::ExitCode;
//!
//! use spinup_kit::app::App;
//! use spinup_kit::config::Base;
//!
//! fn openapi() -> utoipa::openapi::OpenApi {
//!     utoipa::openapi::OpenApiBuilder::new().build()
//! }
//!
//! fn main() -> ExitCode {
//!     spinup_kit::app::main(openapi, || async {
//!         let config: Base = spinup_kit::config::load()?;
//!         spinup_kit::logx::init(&config)?;
//!         Ok(App::new(&config)
//!             .name("todo")
//!             .version(option_env!("SPINUP_APP_VERSION").unwrap_or("dev"))
//!             .git_sha(option_env!("SPINUP_APP_GIT_SHA").unwrap_or_default())
//!             .rust_version(option_env!("SPINUP_APP_RUST").unwrap_or_default())
//!             .routes(axum::Router::new()))
//!     })
//! }
//! ```
//!
//! # Hooks for feature modules
//!
//! Feature modules of the kit (`telemetry`, `apidocs`, `devtools`, `spa`) add their builder
//! methods in their own directory, as an `impl App` block (an inherent impl may live in any
//! module of the crate). They use these `pub(crate)` hooks of [`App`]:
//!
//! - `mount_internal(router)`: merges a stateless router of kit routes (such as
//!   `/api/docs` or `/_dev`) under all app layers.
//! - `add_route_pattern(pattern)` and `route_patterns()`: the known route patterns, such as
//!   `GET /healthz`, for the dev tools. `record_route_patterns()` turns on a layer that adds
//!   the pattern of each matched request (Go `recordRoute`).
//! - `push_outer_layer(f)`: adds a router transform in the layer slot between the request ID
//!   layer and the access log layer. The first transform is the outermost. The telemetry
//!   HTTP layer goes here, so the access log line gets the trace ID.
//! - `set_fallback(f)`: replaces the problem 404 fallback (the SPA uses it).
//! - `on_exit(hook)`: runs after any command or serve, after the shutdown hooks (the
//!   telemetry flush uses it).
//! - `config()`, `app_name()`, `app_version()`: the values that the app was built with.

mod builder;
mod commands;
mod healthcheck;
pub mod openapi;
mod serve;

use std::fmt;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use serde::{Deserialize, Serialize};

pub use builder::{App, RoutePatterns};
pub use commands::main;
pub use healthcheck::healthcheck;
pub use openapi::hoist_path_params;
pub use serve::shutdown_signal;

/// A boxed future that can move between threads.
pub type BoxFuture<T> = Pin<Box<dyn Future<Output = T> + Send + 'static>>;

/// The input of a [`Command`]: the arguments after the command name, and a future that
/// completes on SIGINT or SIGTERM (a long-running command such as `worker` stops on it).
pub struct CommandInput {
    /// The arguments after the command name.
    pub args: Vec<String>,
    /// Completes when the process must stop.
    pub shutdown: BoxFuture<()>,
}

impl CommandInput {
    /// Returns an input with `args` and the `shutdown` future.
    pub fn new(args: Vec<String>, shutdown: impl Future<Output = ()> + Send + 'static) -> Self {
        Self {
            args,
            shutdown: Box::pin(shutdown),
        }
    }
}

impl fmt::Debug for CommandInput {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("CommandInput")
            .field("args", &self.args)
            .finish_non_exhaustive()
    }
}

/// A named app command, registered with [`App::command`] (Go `WithCommand`). The kit makes
/// `migrate` (`kit::db::migrate_command`) and `worker` (`kit::jobs::worker_command`). The
/// app returns the error of the command unchanged.
pub struct Command(Box<dyn FnOnce(CommandInput) -> BoxFuture<anyhow::Result<()>> + Send>);

impl Command {
    /// Returns a command that runs `f`.
    pub fn new<F, Fut>(f: F) -> Self
    where
        F: FnOnce(CommandInput) -> Fut + Send + 'static,
        Fut: Future<Output = anyhow::Result<()>> + Send + 'static,
    {
        Self(Box::new(move |input| Box::pin(f(input))))
    }

    /// Runs the command.
    pub async fn run(self, input: CommandInput) -> anyhow::Result<()> {
        (self.0)(input).await
    }
}

impl fmt::Debug for Command {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Command")
    }
}

/// One migration and its state (Go `devtools.Migration`). The dev tools list them.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Migration {
    /// The migration name, such as `00001_init`.
    pub name: String,
    /// True when the database has the migration.
    pub applied: bool,
}

/// Returns the migrations and their state (Go `WithMigrationStatus`).
/// `kit::db::migration_status(pool)` makes one. The dev tools take an
/// `Option<MigrationStatusFn>`, so they do not need the `db` feature.
pub type MigrationStatusFn = Arc<
    dyn Fn() -> Pin<Box<dyn Future<Output = anyhow::Result<Vec<Migration>>> + Send>> + Send + Sync,
>;
