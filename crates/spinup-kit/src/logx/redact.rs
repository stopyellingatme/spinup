//! The redactor (Go `kit/logx/redact.go`).
//!
//! Every log field goes through the current [`Redactor`] before the log layer writes it.
//! The default redactor writes `****` for the value of a secret key (a key that contains
//! `password`, `secret`, `token`, `authorization`, `cookie`, `api_key`, or `dsn`, in any
//! case), and masks the password of every URL in a text (`postgres://user:****@db/app`).
//! The log message gets the URL mask too.

use std::fmt;
use std::sync::{Arc, LazyLock, RwLock};

use regex::Regex;

/// The value of one log field.
#[derive(Debug, Clone, PartialEq)]
pub enum Value {
    /// A text. Debug and display fields (`?x`, `%x`) and errors are texts too.
    Str(String),
    /// A boolean.
    Bool(bool),
    /// A signed integer.
    I64(i64),
    /// An unsigned integer.
    U64(u64),
    /// A floating point number.
    F64(f64),
}

impl fmt::Display for Value {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Value::Str(s) => f.write_str(s),
            Value::Bool(b) => write!(f, "{b}"),
            Value::I64(n) => write!(f, "{n}"),
            Value::U64(n) => write!(f, "{n}"),
            Value::F64(n) => write!(f, "{n}"),
        }
    }
}

/// Changes log field values before they are written. Implementations must support
/// concurrent calls (Go `logx.Redactor`).
pub trait Redactor: Send + Sync {
    /// Returns the value to write for the field `key`.
    fn redact(&self, key: &str, value: Value) -> Value;
}

/// The default redactor: secret keys and URL passwords (see the module documentation).
#[derive(Debug, Clone, Copy, Default)]
pub struct DefaultRedactor;

impl Redactor for DefaultRedactor {
    fn redact(&self, key: &str, value: Value) -> Value {
        if is_secret_key(key) {
            return Value::Str("****".into());
        }
        match value {
            Value::Str(s) => Value::Str(mask_urls(&s)),
            other => other,
        }
    }
}

static CURRENT: RwLock<Option<Arc<dyn Redactor>>> = RwLock::new(None);

/// Replaces the redactor of every log layer of the kit, including layers that exist now
/// (Go `logx.SetRedactor`). `None` restores the default redactor. It is safe for
/// concurrent use.
pub fn set_redactor(redactor: Option<Arc<dyn Redactor>>) {
    let mut current = CURRENT
        .write()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    *current = redactor;
}

/// Applies the current redactor to one field. The telemetry log bridge uses it too.
pub fn redact(key: &str, value: Value) -> Value {
    let current = CURRENT
        .read()
        .unwrap_or_else(std::sync::PoisonError::into_inner)
        .clone();
    match current {
        Some(r) => r.redact(key, value),
        None => DefaultRedactor.redact(key, value),
    }
}

/// Reports whether `key` names a secret (see the module documentation).
pub fn is_secret_key(key: &str) -> bool {
    let lower = key.to_ascii_lowercase();
    [
        "password",
        "secret",
        "token",
        "authorization",
        "cookie",
        "api_key",
        "dsn",
    ]
    .iter()
    .any(|s| lower.contains(s))
}

static URL_CREDENTIALS: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"([A-Za-z][A-Za-z0-9+.-]*://[^/@\s:]*:)[^\s]*@").expect("the URL pattern is valid")
});

/// Replaces the password of each URL in `text` with `****`.
pub fn mask_urls(text: &str) -> String {
    URL_CREDENTIALS.replace_all(text, "${1}****@").into_owned()
}
