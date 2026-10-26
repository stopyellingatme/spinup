//! The environment loader (Go `kit/config`).
//!
//! A configuration type implements [`FromEnv`]. It reads each setting through an [`Env`],
//! which applies the Go rules:
//!
//! - `NAME_FILE` names a file that holds the value. The loader removes one final newline
//!   (`\n` or `\r\n`). When both `NAME` and `NAME_FILE` are set, the load fails.
//! - A value that does not parse adds a problem, and the load continues. [`ConfigError`]
//!   names every bad variable together, without the values, and ends with a `Try:` line.
//! - [`load`] reads `./.env` (or `../.env` when `./.env` is absent) when `SPINUP_ENV` is
//!   empty or `dev` in the process environment. A variable of the process environment wins
//!   over both forms of the same setting in `.env`. The loader never changes the process
//!   environment.
//!
//! Tests never call `std::env::set_var`. They call [`load_from`] or [`load_with`] with a
//! lookup function and a file reader.
//!
//! ```
//! use spinup_kit::config::{Base, Env, FromEnv};
//!
//! struct Config {
//!     base: Base,
//!     public_origin: String,
//! }
//!
//! impl FromEnv for Config {
//!     fn from_env(env: &mut Env<'_>) -> Self {
//!         Self {
//!             base: Base::from_env(env),
//!             public_origin: env.value("PUBLIC_ORIGIN", "http://localhost:5173".to_string()),
//!         }
//!     }
//! }
//!
//! let lookup = |key: &str| (key == "PORT").then(|| "9090".to_string());
//! let config: Config = spinup_kit::config::load_from(lookup, |path| std::fs::read(path)).unwrap();
//! assert_eq!(config.base.port, 9090);
//! assert_eq!(config.public_origin, "http://localhost:5173");
//! ```

pub mod dotenv;
pub mod goduration;
mod load;
mod parse_bool;

use std::time::Duration;

use serde::{Serialize, Serializer};

pub use load::{ConfigError, Env, EnvValue, FromEnv, load, load_from, load_with};
pub use parse_bool::parse_bool;

/// The common settings of a spinup API (Go `config.Base`).
///
/// `Serialize` writes the environment variable names as keys, because the dev tools list
/// the settings by those names. The duration is a Go duration text such as `20s`.
#[derive(Clone, PartialEq, Eq, Serialize)]
pub struct Base {
    /// `SPINUP_ENV`: the application environment. The default is `production`.
    #[serde(rename = "SPINUP_ENV")]
    pub env: String,
    /// `SPINUP_API_DOCS`: turns on the API reference outside development. The default is
    /// false.
    #[serde(rename = "SPINUP_API_DOCS")]
    pub api_docs: bool,
    /// `PORT`: the HTTP listen port. The default is 8080. The app uses 8080 for zero.
    #[serde(rename = "PORT")]
    pub port: u16,
    /// `DATABASE_URL`: the PostgreSQL connection string. Empty when it is not set.
    #[serde(rename = "DATABASE_URL")]
    pub database_url: String,
    /// `DATABASE_MAX_CONNS`: the largest size of the database pool. Zero keeps the kit
    /// default.
    #[serde(rename = "DATABASE_MAX_CONNS")]
    pub database_max_conns: u32,
    /// `LOG_LEVEL`: the log threshold (`debug`, `info`, `warn`, or `error`). The default
    /// is `info`. An unknown level gives `info`.
    #[serde(rename = "LOG_LEVEL")]
    pub log_level: String,
    /// `SHUTDOWN_TIMEOUT`: the limit of the graceful shutdown, as a Go duration. The
    /// default is 20 seconds. The app uses 20 seconds for zero.
    #[serde(rename = "SHUTDOWN_TIMEOUT", serialize_with = "go_duration")]
    pub shutdown_timeout: Duration,
    /// `SPINUP_BEHAVIOR_VERSION`: the kit behavior defaults. The default is 1.
    #[serde(rename = "SPINUP_BEHAVIOR_VERSION")]
    pub behavior_version: i64,
}

fn go_duration<S: Serializer>(value: &Duration, serializer: S) -> Result<S::Ok, S::Error> {
    serializer.serialize_str(&goduration::format_std(*value))
}

impl Default for Base {
    /// The Go defaults: `production`, port 8080, log level `info`, a 20 second shutdown
    /// timeout, and behavior version 1.
    fn default() -> Self {
        Self {
            env: "production".into(),
            api_docs: false,
            port: 8080,
            database_url: String::new(),
            database_max_conns: 0,
            log_level: "info".into(),
            shutdown_timeout: Duration::from_secs(20),
            behavior_version: 1,
        }
    }
}

impl std::fmt::Debug for Base {
    /// Masks the password of `DATABASE_URL`, so a debug log line shows no secret.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Base")
            .field("env", &self.env)
            .field("api_docs", &self.api_docs)
            .field("port", &self.port)
            .field("database_url", &crate::logx::mask_urls(&self.database_url))
            .field("database_max_conns", &self.database_max_conns)
            .field("log_level", &self.log_level)
            .field("shutdown_timeout", &self.shutdown_timeout)
            .field("behavior_version", &self.behavior_version)
            .finish()
    }
}

impl Base {
    /// Reports whether the app opted into behavior version `v` (Go `Base.Behavior`).
    pub fn behavior(&self, v: i64) -> bool {
        self.behavior_version >= v
    }

    /// Reports whether `SPINUP_ENV` is `dev`.
    pub fn is_dev(&self) -> bool {
        self.env == "dev"
    }
}

impl FromEnv for Base {
    fn from_env(env: &mut Env<'_>) -> Self {
        let d = Self::default();
        Self {
            env: env.value("SPINUP_ENV", d.env),
            api_docs: env.value("SPINUP_API_DOCS", d.api_docs),
            port: env.value("PORT", d.port),
            database_url: env.value("DATABASE_URL", d.database_url),
            database_max_conns: env.value("DATABASE_MAX_CONNS", d.database_max_conns),
            log_level: env.value("LOG_LEVEL", d.log_level),
            shutdown_timeout: env.value("SHUTDOWN_TIMEOUT", d.shutdown_timeout),
            behavior_version: env.value("SPINUP_BEHAVIOR_VERSION", d.behavior_version),
        }
    }
}
