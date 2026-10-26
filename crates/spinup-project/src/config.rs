//! The `spinup.toml` model and validation.

use std::collections::BTreeMap;
use std::path::Path;

use serde::{Deserialize, Serialize};

/// The project configuration filename.
pub const CONFIG_FILE: &str = "spinup.toml";
/// The project lock filename.
pub const LOCK_FILE: &str = "spinup.lock";

/// A project operation that failed, with a recovery hint.
#[derive(Debug, thiserror::Error)]
#[error("{message}\nTry: {hint}")]
pub struct Error {
    message: String,
    hint: String,
}

impl Error {
    pub(crate) fn new(message: impl Into<String>, hint: impl Into<String>) -> Self {
        Self {
            message: message.into(),
            hint: hint.into(),
        }
    }
}

/// The application identity.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct App {
    /// The application slug.
    pub name: String,
    /// The application module path.
    pub module: String,
    /// The owner of the container images in the registry, lowercase (ADR 0021 §5). Empty:
    /// `spinup build --push` and `spinup deploy` fail with a `Try:` line.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub registry_owner: String,
}

/// A deployment environment.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Env {
    /// The resolved environment name.
    pub name: String,
    /// The SSH hostname.
    pub host: String,
    /// The SSH username.
    pub user: String,
    /// The public application URL.
    pub public_url: String,
    /// Require confirmation before protected operations.
    pub protected: bool,
    /// The SSH port, or zero for the default.
    pub port: i64,
    /// The image transport: empty, ssh, or registry.
    pub transport: String,
    /// The pinned SSH host keys.
    pub host_keys: Vec<String>,
}

/// A declared plugin.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Plugin {
    /// The plugin name.
    pub name: String,
    /// The plugin source.
    pub source: String,
    /// The requested version.
    pub version: String,
}

/// The monitoring settings.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Monitoring {
    /// The monitoring profile: lite, full, or none.
    pub profile: String,
    /// The external heartbeat URL.
    pub heartbeat_url: String,
    /// The recipient of Grafana notifications.
    pub alert_email: String,
    /// The SMTP relay address and port.
    pub smtp_host: String,
    /// The SMTP relay username.
    pub smtp_user: String,
    /// The notification sender.
    pub smtp_from: String,
    /// The ntfy notification URL.
    pub ntfy_url: String,
    /// The maximum age of metrics.
    pub prometheus_retention: String,
    /// The disk limit for metrics.
    pub prometheus_retention_size: String,
    /// The maximum age of traces.
    pub tempo_retention: String,
}

/// The backup settings.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Backups {
    /// The backup retention policy.
    pub retention: String,
    /// The backup backend, or empty for pgbackrest.
    pub backend: String,
    /// The systemd calendar for full backups.
    pub schedule_full: String,
    /// The systemd calendar for differential backups.
    pub schedule_diff: String,
    /// The systemd calendar for backup verification.
    pub schedule_verify: String,
}

/// The maintenance settings.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Maintenance {
    /// The UTC maintenance window. Empty allows any time.
    pub window: String,
}

/// The configuration of a spinup project.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Config {
    /// The application identity.
    pub app: App,
    /// The requested runtime behavior version.
    pub behavior_version: i64,
    /// The enabled modules.
    pub modules: Vec<String>,
    /// The monitoring settings.
    pub monitoring: Monitoring,
    /// The backup settings.
    pub backups: Backups,
    /// The maintenance settings.
    pub maintenance: Maintenance,
    /// The environments, ordered by name.
    #[serde(rename = "env")]
    pub envs: BTreeMap<String, Env>,
    /// The declared plugins.
    pub plugins: Vec<Plugin>,
    /// The default environment name.
    pub default_env: String,
    /// Unknown settings at the document root.
    #[serde(flatten)]
    pub extra: BTreeMap<String, toml::Value>,
}

/// Read and parse the configuration at the project root.
pub fn load(root: impl AsRef<Path>) -> Result<Config, Error> {
    let path = root.as_ref().join(CONFIG_FILE);
    let bytes = std::fs::read(&path).map_err(|e| {
        Error::new(
            format!("read {}: {e}", path.display()),
            "check spinup.toml and its permissions, or pass --dir",
        )
    })?;
    parse(&bytes)
}

/// Decode a configuration without validating its application settings.
pub fn parse(data: &[u8]) -> Result<Config, Error> {
    let text = std::str::from_utf8(data).map_err(|e| {
        Error::new(
            format!("parse {CONFIG_FILE}: {e}"),
            "use UTF-8 in spinup.toml",
        )
    })?;
    let mut config: Config = toml::from_str(text).map_err(|e| {
        Error::new(
            format!("parse {CONFIG_FILE}: {e}"),
            "correct the TOML syntax and value types in spinup.toml",
        )
    })?;
    if config.monitoring.profile.is_empty() {
        config.monitoring.profile = "lite".into();
    }
    Ok(config)
}

impl Config {
    /// Return version 1 when the behavior version is missing or invalid.
    pub fn effective_behavior_version(&self) -> i64 {
        self.behavior_version.max(1)
    }

    /// Validate the settings and report all problems in deterministic order.
    pub fn validate(&self) -> Result<(), Error> {
        let mut problems = Vec::new();
        if !valid_name(&self.app.name) || self.app.name.len() > 63 {
            problems.push(format!(
                "app name {:?} must match ^[a-z][a-z0-9-]{{0,62}}$",
                self.app.name
            ));
        }
        if self.app.module.is_empty() {
            problems.push("app module must not be empty".into());
        }
        if !valid_registry_owner(&self.app.registry_owner) {
            problems.push(format!(
                "invalid registry_owner {:?} in spinup.toml: use the lowercase GitHub owner, such as acme",
                self.app.registry_owner
            ));
        }
        if !matches!(self.monitoring.profile.as_str(), "lite" | "full" | "none") {
            problems.push(format!(
                "monitoring profile {:?} must be lite, full, or none",
                self.monitoring.profile
            ));
        }
        if !matches!(
            self.backups.backend.as_str(),
            "" | "pgbackrest" | "pgdump" | "walg"
        ) {
            problems.push(format!(
                "backups backend {:?} must be pgbackrest, pgdump, or walg",
                self.backups.backend
            ));
        }
        for (name, env) in &self.envs {
            if !valid_name(name) {
                problems.push(format!(
                    "environment name {name:?} must match ^[a-z][a-z0-9-]*$"
                ));
            }
            if !matches!(env.transport.as_str(), "" | "ssh" | "registry") {
                problems.push(format!(
                    "environment {name:?}: transport {:?} must be ssh or registry",
                    env.transport
                ));
            }
        }
        if !self.default_env.is_empty() && !self.envs.contains_key(&self.default_env) {
            problems.push(format!(
                "default environment {:?} does not exist",
                self.default_env
            ));
        }
        if problems.is_empty() {
            Ok(())
        } else {
            Err(Error::new(
                problems.join("\n"),
                "correct the listed settings in spinup.toml",
            ))
        }
    }

    /// Resolve an explicit name, the default, or the sole environment.
    pub fn resolve_env(&self, name: &str) -> Result<Env, Error> {
        let name = if name.is_empty() {
            self.default_env.as_str()
        } else {
            name
        };
        let name = if name.is_empty() && self.envs.len() == 1 {
            self.envs.keys().next().map(String::as_str).unwrap_or("")
        } else {
            name
        };
        if !name.is_empty()
            && let Some(env) = self.envs.get(name)
        {
            return Ok(Env {
                name: name.into(),
                ..env.clone()
            });
        }
        Err(Error::new(
            format!(
                "could not resolve environment {name:?}. Valid environments: {}.",
                self.envs.keys().cloned().collect::<Vec<_>>().join(", ")
            ),
            "pass --env <name>",
        ))
    }
}

/// Reports whether `owner` is empty or a lowercase GitHub owner:
/// `^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$` (ADR 0021 §5).
fn valid_registry_owner(owner: &str) -> bool {
    let bytes = owner.as_bytes();
    if bytes.is_empty() {
        return true;
    }
    let alnum = |c: &u8| c.is_ascii_lowercase() || c.is_ascii_digit();
    bytes.len() <= 39
        && bytes.first().is_some_and(alnum)
        && bytes.last().is_some_and(alnum)
        && bytes.iter().all(|c| alnum(c) || *c == b'-')
}

fn valid_name(name: &str) -> bool {
    name.as_bytes().first().is_some_and(u8::is_ascii_lowercase)
        && name
            .bytes()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-')
}

#[cfg(test)]
mod tests {
    use super::*;

    const APP: &str = "[app]\nname = 'demo-app'\nmodule = 'example.com/demo'\n";

    #[test]
    fn parse_validate_and_extra() {
        let c = parse(format!("behavior_version = 2\nmodules = ['api']\ndefault_env = 'prod'\nfuture_setting = 'kept'\n{APP}\n[monitoring]\nheartbeat_url = 'https://example.test/health'\n[env.prod]\nhost = 'example.test'\nuser = 'deploy'\nport = 22\nprotected = true\npublic_url = 'https://example.test'\nhost_keys = ['ssh-ed25519 AAAA']\n").as_bytes()).unwrap();
        c.validate().unwrap();
        assert_eq!(c.monitoring.profile, "lite");
        assert_eq!(c.extra["future_setting"].as_str(), Some("kept"));
        assert_eq!(c.effective_behavior_version(), 2);
        assert_eq!(c.modules, ["api"]);
        let env = c.resolve_env("").unwrap();
        assert_eq!(env.name, "prod");
        assert_eq!(env.port, 22);
        assert_eq!(env.host_keys, ["ssh-ed25519 AAAA"]);
        assert!(env.protected);
    }

    #[test]
    fn validate_reports_all_problems_in_order() {
        let mut c = Config::default();
        c.app.name = "Invalid".into();
        c.monitoring.profile = "invalid".into();
        c.envs.insert("Bad_Name".into(), Env::default());
        c.default_env = "missing".into();
        let err = c.validate().unwrap_err().to_string();
        let mut previous = 0;
        for text in [
            "app name",
            "app module",
            "monitoring profile",
            "environment name",
            "default environment",
            "\nTry:",
        ] {
            let index = err.find(text).unwrap();
            assert!(index >= previous);
            previous = index;
        }
    }

    #[test]
    fn registry_owner_rules() {
        for (owner, ok) in [
            ("", true),
            ("acme", true),
            ("a", true),
            ("acme-co2", true),
            (&"a".repeat(39) as &str, true),
            (&"a".repeat(40) as &str, false),
            ("Acme", false),
            ("-acme", false),
            ("acme-", false),
            ("ac_me", false),
        ] {
            let c = parse(format!("[app]\nname = 'demo'\nmodule = 'example.com/demo'\nregistry_owner = '{owner}'\n").as_bytes()).unwrap();
            assert_eq!(c.app.registry_owner, owner);
            let result = c.validate();
            assert_eq!(result.is_ok(), ok, "{owner}");
            if !ok {
                let err = result.unwrap_err().to_string();
                assert!(
                    err.contains(&format!("invalid registry_owner {owner:?} in spinup.toml")),
                    "{err}"
                );
                assert!(err.contains("\nTry:"), "{err}");
            }
        }
    }

    #[test]
    fn environment_resolution() {
        let c = parse(format!("{APP}\n[env.prod]\n[env.dev]\n").as_bytes()).unwrap();
        assert_eq!(c.resolve_env("prod").unwrap().name, "prod");
        assert!(
            c.resolve_env("")
                .unwrap_err()
                .to_string()
                .contains("Valid environments: dev, prod")
        );
        let mut single = c.clone();
        single.envs.remove("prod");
        assert_eq!(single.resolve_env("").unwrap().name, "dev");
        single.default_env = "dev".into();
        assert_eq!(single.resolve_env("").unwrap().name, "dev");
        assert!(single.resolve_env("missing").is_err());
        assert!(Config::default().resolve_env("").is_err());
    }

    #[test]
    fn transport_and_backup_validation() {
        for transport in ["", "ssh", "registry", "ftp"] {
            let c = parse(format!("{APP}\n[env.prod]\ntransport = '{transport}'\n").as_bytes())
                .unwrap();
            assert_eq!(c.envs["prod"].transport, transport);
            assert_eq!(c.validate().is_ok(), transport != "ftp");
        }
        for backend in ["", "pgbackrest", "pgdump", "walg", "unknown"] {
            let c = parse(format!("{APP}\n[backups]\nbackend = '{backend}'\n").as_bytes()).unwrap();
            assert_eq!(c.validate().is_ok(), backend != "unknown");
        }
    }

    #[test]
    fn monitoring_backup_maintenance_and_plugin_settings() {
        let c = parse(
            format!(
                r#"{APP}
[monitoring]
alert_email = "ops@example.com"
smtp_host = "smtp.example.com:587"
smtp_user = "alerts"
smtp_from = "Alerts <alerts@example.com>"
ntfy_url = "https://ntfy.example.com/alerts"
prometheus_retention = "14d"
prometheus_retention_size = "2GB"
tempo_retention = "72h"
[backups]
retention = "7d"
backend = "walg"
schedule_full = "Sun 02:00"
schedule_diff = "daily"
schedule_verify = "weekly"
[maintenance]
window = "Sun 02:00-05:00"
[[plugins]]
name = "custom"
source = "local/path"
version = "1.0.0"
[future]
enabled = true
"#
            )
            .as_bytes(),
        )
        .unwrap();
        c.validate().unwrap();
        assert_eq!(
            c.monitoring,
            Monitoring {
                profile: "lite".into(),
                alert_email: "ops@example.com".into(),
                smtp_host: "smtp.example.com:587".into(),
                smtp_user: "alerts".into(),
                smtp_from: "Alerts <alerts@example.com>".into(),
                ntfy_url: "https://ntfy.example.com/alerts".into(),
                prometheus_retention: "14d".into(),
                prometheus_retention_size: "2GB".into(),
                tempo_retention: "72h".into(),
                ..Monitoring::default()
            }
        );
        assert_eq!(
            c.backups,
            Backups {
                retention: "7d".into(),
                backend: "walg".into(),
                schedule_full: "Sun 02:00".into(),
                schedule_diff: "daily".into(),
                schedule_verify: "weekly".into(),
            }
        );
        assert_eq!(c.maintenance.window, "Sun 02:00-05:00");
        assert_eq!(
            c.plugins,
            [Plugin {
                name: "custom".into(),
                source: "local/path".into(),
                version: "1.0.0".into()
            }]
        );
        assert_eq!(c.extra.len(), 1);
        assert_eq!(c.extra["future"]["enabled"].as_bool(), Some(true));
    }

    #[test]
    fn name_boundaries_and_profiles() {
        for name in ["a".into(), "a-9".into(), "a".repeat(63)] {
            let mut c = parse(APP.as_bytes()).unwrap();
            c.app.name = name;
            c.validate().unwrap();
        }
        for name in [
            "".into(),
            "9a".into(),
            "a_b".into(),
            "é".into(),
            "a\n".into(),
            "a".repeat(64),
        ] {
            let mut c = parse(APP.as_bytes()).unwrap();
            c.app.name = name;
            assert!(c.validate().unwrap_err().to_string().contains("app name"));
        }
        for profile in ["", "lite", "full", "none", "invalid"] {
            let c =
                parse(format!("{APP}\n[monitoring]\nprofile = '{profile}'\n").as_bytes()).unwrap();
            assert_eq!(c.validate().is_ok(), profile != "invalid");
        }
    }

    #[test]
    fn behavior_defaults_and_parse_errors() {
        for (text, want) in [
            ("", 1),
            ("behavior_version = 0", 1),
            ("behavior_version = -2", 1),
            ("behavior_version = 3", 3),
        ] {
            assert_eq!(
                parse(text.as_bytes()).unwrap().effective_behavior_version(),
                want
            );
        }
        for text in [b"[".as_slice(), b"modules = 'api'", b"\xff"] {
            assert!(parse(text).unwrap_err().to_string().contains("\nTry:"));
        }
    }
}
