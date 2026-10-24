//! Configuration from the environment, as `kit/config.Base` in Go.

use std::fmt;
use std::time::Duration;

/// The base configuration of every app.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Config {
    /// `SPINUP_ENV`, default `production`.
    pub env: String,
    /// `PORT`, default 8080.
    pub port: u16,
    /// `DATABASE_URL`. Empty when it is not set.
    pub database_url: String,
    /// `DATABASE_MAX_CONNS`. Zero selects the default pool size.
    pub database_max_conns: u32,
    /// `LOG_LEVEL`, default `info`.
    pub log_level: String,
    /// `SHUTDOWN_TIMEOUT`, default 20 s.
    pub shutdown_timeout: Duration,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            env: "production".into(),
            port: 8080,
            database_url: String::new(),
            database_max_conns: 0,
            log_level: "info".into(),
            shutdown_timeout: Duration::from_secs(20),
        }
    }
}

/// A configuration error. Its text says what failed and why, and ends with a
/// `Try:` line.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConfigError {
    problems: Vec<String>,
}

impl fmt::Display for ConfigError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "cannot load the configuration: {}\nTry: correct the environment variables, then start the app again",
            self.problems.join("; ")
        )
    }
}

impl std::error::Error for ConfigError {}

impl Config {
    /// Loads the configuration from the process environment.
    pub fn from_env() -> Result<Self, ConfigError> {
        Self::load(|key| std::env::var(key).ok())
    }

    /// Loads the configuration with `lookup`. Tests pass a map.
    pub fn load(lookup: impl Fn(&str) -> Option<String>) -> Result<Self, ConfigError> {
        let mut cfg = Self::default();
        let mut problems = Vec::new();
        let get = |key: &str| lookup(key).filter(|v| !v.is_empty());
        if let Some(v) = get("SPINUP_ENV") {
            cfg.env = v;
        }
        if let Some(v) = get("PORT") {
            match v.parse::<u16>() {
                Ok(p) if p > 0 => cfg.port = p,
                _ => problems.push(format!("PORT is {v:?}, want a number from 1 to 65535")),
            }
        }
        if let Some(v) = get("DATABASE_URL") {
            cfg.database_url = v;
        }
        if let Some(v) = get("DATABASE_MAX_CONNS") {
            match v.parse::<u32>() {
                Ok(n) => cfg.database_max_conns = n,
                Err(_) => problems.push(format!(
                    "DATABASE_MAX_CONNS is {v:?}, want a positive number"
                )),
            }
        }
        if let Some(v) = get("LOG_LEVEL") {
            match v.as_str() {
                "debug" | "info" | "warn" | "error" => cfg.log_level = v,
                _ => problems.push(format!(
                    "LOG_LEVEL is {v:?}, want debug, info, warn, or error"
                )),
            }
        }
        if let Some(v) = get("SHUTDOWN_TIMEOUT") {
            match parse_duration(&v) {
                Some(d) => cfg.shutdown_timeout = d,
                None => problems.push(format!(
                    "SHUTDOWN_TIMEOUT is {v:?}, want a duration such as 20s or 500ms"
                )),
            }
        }
        if problems.is_empty() {
            Ok(cfg)
        } else {
            Err(ConfigError { problems })
        }
    }
}

/// Parses the Go duration forms that the generated apps use: `500ms`, `20s`,
/// `2m`, `1h`.
fn parse_duration(s: &str) -> Option<Duration> {
    let (num, unit) = s.split_at(s.find(|c: char| !c.is_ascii_digit())?);
    let n: u64 = num.parse().ok()?;
    match unit {
        "ms" => Some(Duration::from_millis(n)),
        "s" => Some(Duration::from_secs(n)),
        "m" => Some(Duration::from_secs(n * 60)),
        "h" => Some(Duration::from_secs(n * 3600)),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn load(pairs: &[(&str, &str)]) -> Result<Config, ConfigError> {
        let env: HashMap<String, String> = pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect();
        Config::load(|k| env.get(k).cloned())
    }

    #[test]
    fn defaults() {
        assert_eq!(load(&[]).unwrap(), Config::default());
    }

    #[test]
    fn values() {
        let cfg = load(&[
            ("PORT", "9090"),
            ("SHUTDOWN_TIMEOUT", "500ms"),
            ("DATABASE_URL", "postgres://x"),
        ])
        .unwrap();
        assert_eq!(cfg.port, 9090);
        assert_eq!(cfg.shutdown_timeout, Duration::from_millis(500));
        assert_eq!(cfg.database_url, "postgres://x");
    }

    #[test]
    fn errors_name_every_variable_and_end_with_try() {
        let err = load(&[
            ("PORT", "abc"),
            ("SHUTDOWN_TIMEOUT", "soon"),
            ("LOG_LEVEL", "loud"),
        ])
        .unwrap_err()
        .to_string();
        for want in ["PORT", "SHUTDOWN_TIMEOUT", "LOG_LEVEL", "\nTry: "] {
            assert!(err.contains(want), "{err:?} does not contain {want:?}");
        }
    }
}
