//! The loader: [`Env`], [`FromEnv`], [`EnvValue`], and the three load functions (Go
//! `config.Load` and `config.LoadFrom`).

use std::collections::BTreeMap;
use std::fmt;
use std::io;
use std::path::Path;
use std::time::Duration;

use super::{dotenv, goduration, parse_bool};

/// The text of the `Try:` line of every configuration error.
const TRY: &str = "Try: correct the named environment variables or secret files and restart";

/// A failed load. It names every bad variable, one line for each problem, and never holds a
/// value. Its text ends with a `Try:` line.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ConfigError {
    problems: Vec<String>,
}

impl ConfigError {
    /// Returns an error with the given problem lines.
    pub fn new(problems: Vec<String>) -> Self {
        Self { problems }
    }

    /// The problem lines, such as `PORT: expected an integer`.
    pub fn problems(&self) -> &[String] {
        &self.problems
    }
}

impl fmt::Display for ConfigError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}\n{TRY}", self.problems.join("\n"))
    }
}

impl std::error::Error for ConfigError {}

/// A type that reads its settings from an [`Env`]. The loader calls `from_env` once. A bad
/// value adds a problem to the `Env` and gives the default, so `from_env` always returns.
pub trait FromEnv: Sized {
    /// Reads every setting from `env`.
    fn from_env(env: &mut Env<'_>) -> Self;
}

/// A value type of one setting. `parse_env` returns the Go problem text (such as
/// `expected an integer`) for a bad value.
pub trait EnvValue: Sized {
    /// Parses the raw text of a setting.
    fn parse_env(raw: &str) -> Result<Self, &'static str>;
}

impl EnvValue for String {
    fn parse_env(raw: &str) -> Result<Self, &'static str> {
        Ok(raw.to_string())
    }
}

impl EnvValue for bool {
    /// The Go `strconv.ParseBool` forms: `1`, `t`, `T`, `TRUE`, `true`, `True`, and the
    /// false forms.
    fn parse_env(raw: &str) -> Result<Self, &'static str> {
        parse_bool(raw).ok_or("expected a boolean")
    }
}

macro_rules! env_int {
    ($($t:ty),*) => {$(
        impl EnvValue for $t {
            fn parse_env(raw: &str) -> Result<Self, &'static str> {
                raw.parse().map_err(|_| "expected an integer")
            }
        }
    )*};
}

env_int!(i8, i16, i32, i64, isize, u8, u16, u32, u64, usize);

impl EnvValue for f64 {
    fn parse_env(raw: &str) -> Result<Self, &'static str> {
        raw.parse().map_err(|_| "expected a number")
    }
}

impl EnvValue for f32 {
    fn parse_env(raw: &str) -> Result<Self, &'static str> {
        raw.parse().map_err(|_| "expected a number")
    }
}

impl EnvValue for Duration {
    /// A Go duration such as `20s`, `1m30s`, or `500ms`. A negative duration gives zero:
    /// the Go kit treats a value that is not positive as "use the default".
    fn parse_env(raw: &str) -> Result<Self, &'static str> {
        let ns = goduration::parse(raw).ok_or("expected a duration")?;
        Ok(Duration::from_nanos(u64::try_from(ns).unwrap_or(0)))
    }
}

impl EnvValue for Vec<String> {
    /// A comma-separated list. Each item is trimmed. An empty value gives an empty list.
    fn parse_env(raw: &str) -> Result<Self, &'static str> {
        if raw.is_empty() {
            return Ok(Vec::new());
        }
        Ok(raw.split(',').map(|s| s.trim().to_string()).collect())
    }
}

/// The reader that a [`FromEnv`] type uses. It collects the problems of all settings.
pub struct Env<'a> {
    lookup: &'a dyn Fn(&str) -> Option<String>,
    read_file: &'a dyn Fn(&Path) -> io::Result<Vec<u8>>,
    problems: Vec<String>,
}

impl fmt::Debug for Env<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Env")
            .field("problems", &self.problems)
            .finish_non_exhaustive()
    }
}

impl<'a> Env<'a> {
    /// Returns a reader over `lookup` (a variable, or `None` when it is not set) and
    /// `read_file` (for `NAME_FILE`).
    pub fn new(
        lookup: &'a dyn Fn(&str) -> Option<String>,
        read_file: &'a dyn Fn(&Path) -> io::Result<Vec<u8>>,
    ) -> Self {
        Self {
            lookup,
            read_file,
            problems: Vec::new(),
        }
    }

    /// Returns the raw text of `key` from `key` or `key_FILE`, or `None` when neither is
    /// set. A set but empty variable counts as set. It adds a problem when both forms are
    /// set or the file cannot be read, and then returns `None`.
    pub fn raw(&mut self, key: &str) -> Option<String> {
        let direct = (self.lookup)(key);
        let file = (self.lookup)(&format!("{key}_FILE"));
        match (direct, file) {
            (Some(_), Some(_)) => {
                self.problem(key, format!("both {key} and {key}_FILE are set"));
                None
            }
            (None, Some(path)) => match (self.read_file)(Path::new(&path)) {
                Ok(data) => Some(trim_file_value(&String::from_utf8_lossy(&data))),
                Err(_) => {
                    self.problem(key, format!("cannot read {key}_FILE"));
                    None
                }
            },
            (direct, None) => direct,
        }
    }

    /// Returns the value of `key`, or `default` when it is not set. A bad value adds a
    /// problem and gives `default`.
    pub fn value<T: EnvValue>(&mut self, key: &str, default: T) -> T {
        self.optional(key).unwrap_or(default)
    }

    /// Returns the value of `key`, or `None` when it is not set or bad. A bad value adds a
    /// problem.
    pub fn optional<T: EnvValue>(&mut self, key: &str) -> Option<T> {
        let raw = self.raw(key)?;
        match T::parse_env(&raw) {
            Ok(v) => Some(v),
            Err(message) => {
                self.problem(key, message);
                None
            }
        }
    }

    /// Returns the value of `key`. A missing or empty value adds the problem
    /// `required value is missing` and gives `T::default()`.
    pub fn required<T: EnvValue + Default>(&mut self, key: &str) -> T {
        let before = self.problems.len();
        match self.raw(key) {
            Some(raw) if !raw.is_empty() => match T::parse_env(&raw) {
                Ok(v) => v,
                Err(message) => {
                    self.problem(key, message);
                    T::default()
                }
            },
            _ => {
                // A problem of `raw` (both forms, or a bad file) already names the key.
                if self.problems.len() == before {
                    self.problem(key, "required value is missing");
                }
                T::default()
            }
        }
    }

    /// Adds a problem line `key: message`. Use it for checks that a type adds, such as a
    /// range. The message must not hold the value.
    pub fn problem(&mut self, key: &str, message: impl fmt::Display) {
        self.problems.push(format!("{key}: {message}"));
    }

    /// The problems so far.
    pub fn problems(&self) -> &[String] {
        &self.problems
    }
}

/// Removes one final `\n` (and then one `\r`), as Go does for `NAME_FILE`.
fn trim_file_value(text: &str) -> String {
    match text.strip_suffix('\n') {
        Some(rest) => rest.strip_suffix('\r').unwrap_or(rest).to_string(),
        None => text.to_string(),
    }
}

/// Loads `T` with `lookup` and `read_file` (Go `config.LoadFrom`). It reads no `.env` file.
pub fn load_from<T: FromEnv>(
    lookup: impl Fn(&str) -> Option<String>,
    read_file: impl Fn(&Path) -> io::Result<Vec<u8>>,
) -> Result<T, ConfigError> {
    let mut env = Env::new(&lookup, &read_file);
    let value = T::from_env(&mut env);
    if env.problems.is_empty() {
        Ok(value)
    } else {
        Err(ConfigError::new(env.problems))
    }
}

/// Loads `T` with the `.env` rules of [`load`], but with `lookup` as the process
/// environment and `read_file` for `.env`, `../.env`, and `NAME_FILE`. Relative paths are
/// relative to the working directory of the reader. Tests use it.
pub fn load_with<T: FromEnv>(
    lookup: impl Fn(&str) -> Option<String>,
    read_file: impl Fn(&Path) -> io::Result<Vec<u8>>,
) -> Result<T, ConfigError> {
    let values = match lookup("SPINUP_ENV").as_deref() {
        None | Some("" | "dev") => {
            dotenv::load(&read_file).map_err(|e| ConfigError::new(vec![e]))?
        }
        Some(_) => BTreeMap::new(),
    };
    let merged = |key: &str| -> Option<String> {
        if let Some(v) = lookup(key) {
            return Some(v);
        }
        // The environment wins over both forms of a setting in `.env`.
        let other = match key.strip_suffix("_FILE") {
            Some(base) => base.to_string(),
            None => format!("{key}_FILE"),
        };
        if lookup(&other).is_some() {
            return None;
        }
        values.get(key).cloned()
    };
    load_from(merged, read_file)
}

/// Loads `T` from the process environment (Go `config.Load`). When `SPINUP_ENV` is empty
/// or `dev`, it also reads `./.env`, or `../.env` when `./.env` is absent. It does not
/// change the process environment.
pub fn load<T: FromEnv>() -> Result<T, ConfigError> {
    load_with(
        |key| std::env::var_os(key).map(|v| v.to_string_lossy().into_owned()),
        |path| std::fs::read(path),
    )
}
