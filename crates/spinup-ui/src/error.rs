//! User-facing errors and the error text mapper (ADR 0020 §6).

use std::fmt;

/// A user-facing error. It says what failed, why, and what to try.
///
/// `Display` gives `what`, followed by `: why` when `why` is set. This is the Go `Error()` text.
/// The cause is not part of the `source()` chain, so `{:#}` gives the same text as Go.
/// Read the cause with [`Error::cause`].
#[derive(Debug, Default)]
pub struct Error {
    /// States what failed.
    pub what: String,
    /// States the cause. It is optional (empty when unset).
    pub why: String,
    /// States the next step for the user. It is optional (empty when unset).
    pub try_hint: String,
    /// The process exit code. Zero means [`crate::exit::ERROR`].
    pub code: i32,
    cause: Option<Box<dyn std::error::Error + Send + Sync>>,
}

impl Error {
    /// Create an error from the `what` text.
    #[must_use]
    pub fn new(what: impl Into<String>) -> Self {
        Self {
            what: what.into(),
            ..Self::default()
        }
    }

    /// Set the `why` text.
    #[must_use]
    pub fn why(mut self, why: impl Into<String>) -> Self {
        self.why = why.into();
        self
    }

    /// Set the `Try:` hint.
    #[must_use]
    pub fn try_hint(mut self, hint: impl Into<String>) -> Self {
        self.try_hint = hint.into();
        self
    }

    /// Set the exit code.
    #[must_use]
    pub fn code(mut self, code: i32) -> Self {
        self.code = code;
        self
    }

    /// Set the wrapped cause.
    #[must_use]
    pub fn cause_err(mut self, cause: impl std::error::Error + Send + Sync + 'static) -> Self {
        self.cause = Some(Box::new(cause));
        self
    }

    /// The wrapped cause, when set.
    #[must_use]
    pub fn cause(&self) -> Option<&(dyn std::error::Error + Send + Sync + 'static)> {
        self.cause.as_deref()
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.what)?;
        if !self.why.is_empty() {
            write!(f, ": {}", self.why)?;
        }
        Ok(())
    }
}

impl std::error::Error for Error {}

/// Find the first [`Error`] in the chain of `err`, as Go `errors.As` does.
#[must_use]
pub fn find_ui_error(err: &anyhow::Error) -> Option<&Error> {
    err.chain().find_map(|e| e.downcast_ref::<Error>())
}

/// The parts of an error, ready to print.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Parts {
    /// What failed.
    pub what: String,
    /// Why it failed.
    pub why: String,
    /// The one `Try:` hint.
    #[serde(rename = "try")]
    pub try_hint: String,
    /// The exit code.
    pub code: i32,
}

/// Split an error into `what`, `why`, `try`, and `code` (Go `RenderError` rules).
///
/// A [`Error`] in the chain gives the fields. Any other error gives its `{:#}` text as `what`.
/// The mapper removes the `Try:` lines from `what` and `why`.
/// It keeps one hint: the own `try_hint`, else the last embedded `Try:` line.
#[must_use]
pub fn parts(err: &anyhow::Error) -> Parts {
    let (mut what, mut why, mut hint) = match find_ui_error(err) {
        Some(ue) => (ue.what.clone(), ue.why.clone(), ue.try_hint.clone()),
        None => (format!("{err:#}"), String::new(), String::new()),
    };
    let mut embedded = Vec::new();
    what = strip_try(&what, &mut embedded);
    why = strip_try(&why, &mut embedded);
    if hint.is_empty()
        && let Some(last) = embedded.pop()
    {
        hint = last;
    }
    Parts {
        what,
        why,
        try_hint: hint,
        code: crate::exit::exit_code(err),
    }
}

/// Remove the lines that start with `Try:` from `text`. Append their hints to `tries`.
#[must_use]
pub fn strip_try(text: &str, tries: &mut Vec<String>) -> String {
    if !text.contains("Try:") {
        return text.to_string();
    }
    let mut kept = Vec::new();
    for line in text.split('\n') {
        if let Some(hint) = line.trim().strip_prefix("Try:") {
            tries.push(hint.trim().to_string());
        } else {
            kept.push(line);
        }
    }
    kept.join("\n").trim_end_matches(['\n', ' ']).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use anyhow::Context;

    #[test]
    fn display_and_cause() {
        let cause = std::io::Error::other("cause");
        let e = Error::new("a").why("b").cause_err(cause);
        assert_eq!(e.to_string(), "a: b");
        assert_eq!(Error::new("a").to_string(), "a");
        assert_eq!(e.cause().map(ToString::to_string).as_deref(), Some("cause"));
        // The cause stays out of the alternate text, as in Go.
        assert_eq!(format!("{:#}", anyhow::Error::new(e)), "a: b");
    }

    #[test]
    fn strip_try_keeps_non_try_lines() {
        let mut t = Vec::new();
        assert_eq!(strip_try("a\n  Try: x\nb\nTry: y\n", &mut t), "a\nb");
        assert_eq!(t, ["x", "y"]);
    }

    #[test]
    fn finds_error_in_context_chain() {
        let err: anyhow::Error = Err::<(), _>(Error::new("w").why("y").try_hint("t"))
            .context("outer")
            .unwrap_err();
        let p = parts(&err);
        assert_eq!(
            (p.what.as_str(), p.why.as_str(), p.try_hint.as_str()),
            ("w", "y", "t")
        );
    }
}
