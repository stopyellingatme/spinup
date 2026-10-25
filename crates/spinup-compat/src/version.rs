//! The Go-compatible semantic version type (oracle semver cases).

use std::cmp::Ordering;
use std::fmt;

/// A semantic version without build metadata.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct Version {
    /// The major number.
    pub major: i64,
    /// The minor number.
    pub minor: i64,
    /// The patch number.
    pub patch: i64,
    /// The prerelease text. It is empty for a release.
    pub pre: String,
}

/// A version text that does not parse. `Display` is the Go error text.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct ParseError(String);

/// Parse a semantic version with an optional `v` prefix and prerelease.
///
/// # Errors
/// Returns [`ParseError`] when the text is not `X.Y.Z` with an optional `-prerelease`.
pub fn parse_version(s: &str) -> Result<Version, ParseError> {
    let original = s;
    let s = s.strip_prefix('v').unwrap_or(s);
    let (core, pre) = match s.split_once('-') {
        Some((c, p)) => (c, Some(p)),
        None => (s, None),
    };
    let parts: Vec<&str> = core.split('.').collect();
    if parts.len() != 3 {
        return Err(ParseError(format!(
            "invalid version {}: expected X.Y.Z",
            go_quote(original)
        )));
    }
    let mut values = [0i64; 3];
    for (slot, part) in values.iter_mut().zip(&parts) {
        *slot = parse_number(part)
            .map_err(|e| ParseError(format!("invalid version {}: {e}", go_quote(original))))?;
    }
    if let Some(p) = pre
        && !valid_prerelease(p)
    {
        return Err(ParseError(format!(
            "invalid version {}: invalid prerelease",
            go_quote(original)
        )));
    }
    Ok(Version {
        major: values[0],
        minor: values[1],
        patch: values[2],
        pre: pre.unwrap_or_default().to_string(),
    })
}

impl Version {
    /// Parse a version. See [`parse_version`].
    ///
    /// # Errors
    /// Returns [`ParseError`] when the text is not a version.
    pub fn parse(s: &str) -> Result<Self, ParseError> {
        parse_version(s)
    }

    /// Compare as Go `Compare` does: -1, 0, or 1.
    #[must_use]
    pub fn compare(&self, other: &Self) -> i32 {
        match self.cmp(other) {
            Ordering::Less => -1,
            Ordering::Equal => 0,
            Ordering::Greater => 1,
        }
    }
}

impl Ord for Version {
    fn cmp(&self, o: &Self) -> Ordering {
        self.major
            .cmp(&o.major)
            .then(self.minor.cmp(&o.minor))
            .then(self.patch.cmp(&o.patch))
            .then_with(|| match (self.pre.is_empty(), o.pre.is_empty()) {
                (true, true) => Ordering::Equal,
                (true, false) => Ordering::Greater,
                (false, true) => Ordering::Less,
                (false, false) => compare_prerelease(&self.pre, &o.pre),
            })
    }
}

impl PartialOrd for Version {
    fn partial_cmp(&self, other: &Self) -> Option<Ordering> {
        Some(self.cmp(other))
    }
}

impl fmt::Display for Version {
    /// The canonical text with a `v` prefix.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "v{}.{}.{}", self.major, self.minor, self.patch)?;
        if !self.pre.is_empty() {
            write!(f, "-{}", self.pre)?;
        }
        Ok(())
    }
}

/// Quote text as Go `%q` does for the inputs that matter here (printable text with `"` and `\`).
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\u{b}' => out.push_str("\\v"),
            c if c.is_control() => {
                let n = c as u32;
                if n < 0x100 {
                    out.push_str(&format!("\\x{n:02x}"));
                } else {
                    out.push_str(&format!("\\u{n:04x}"));
                }
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

fn parse_number(s: &str) -> Result<i64, String> {
    let bad = || format!("invalid numeric identifier {}", go_quote(s));
    if s.is_empty() || (s.len() > 1 && s.starts_with('0')) || !s.bytes().all(|b| b.is_ascii_digit())
    {
        return Err(bad());
    }
    s.parse::<i64>().map_err(|_| bad())
}

fn valid_prerelease(pre: &str) -> bool {
    if pre.is_empty() {
        return false;
    }
    pre.split('.').all(|id| {
        !id.is_empty()
            && id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
            && !(is_numeric(id) && id.len() > 1 && id.starts_with('0'))
    })
}

fn is_numeric(s: &str) -> bool {
    !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit())
}

fn compare_prerelease(left: &str, right: &str) -> Ordering {
    let l: Vec<&str> = left.split('.').collect();
    let r: Vec<&str> = right.split('.').collect();
    for (a, b) in l.iter().zip(&r) {
        let ord = match (is_numeric(a), is_numeric(b)) {
            // Numeric identifiers compare by length, then bytes (no overflow for long numbers).
            (true, true) => a.len().cmp(&b.len()).then_with(|| a.cmp(b)),
            (true, false) => Ordering::Less,
            (false, true) => Ordering::Greater,
            (false, false) => a.cmp(b),
        };
        if ord != Ordering::Equal {
            return ord;
        }
    }
    l.len().cmp(&r.len())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::Deserialize;

    #[derive(Deserialize)]
    struct Oracle {
        parse: Vec<ParseCase>,
        compare: Vec<CompareCase>,
    }

    #[derive(Deserialize)]
    struct ParseCase {
        input: String,
        canonical: String,
        major: i64,
        minor: i64,
        patch: i64,
        pre: String,
        error: String,
    }

    #[derive(Deserialize)]
    struct CompareCase {
        left: String,
        right: String,
        expected: i32,
    }

    fn oracle() -> Oracle {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../../testdata/oracle/semver.json"
        );
        serde_json::from_str(&std::fs::read_to_string(path).expect("read oracle"))
            .expect("parse oracle")
    }

    #[test]
    fn oracle_parse_cases() {
        let o = oracle();
        assert!(!o.parse.is_empty());
        for c in &o.parse {
            match parse_version(&c.input) {
                Ok(v) => {
                    assert_eq!(c.error, "", "{}: Go failed, Rust parsed", c.input);
                    assert_eq!(v.to_string(), c.canonical, "{}", c.input);
                    assert_eq!(
                        (v.major, v.minor, v.patch, v.pre.as_str()),
                        (c.major, c.minor, c.patch, c.pre.as_str())
                    );
                }
                Err(e) => assert_eq!(e.to_string(), c.error, "{}", c.input),
            }
        }
    }

    #[test]
    fn oracle_compare_cases() {
        let o = oracle();
        assert!(!o.compare.is_empty());
        for c in &o.compare {
            let l = parse_version(&c.left).unwrap();
            let r = parse_version(&c.right).unwrap();
            assert_eq!(l.compare(&r), c.expected, "{} vs {}", c.left, c.right);
        }
    }

    #[test]
    fn prerelease_order() {
        let ordered = [
            "v1.0.0-alpha",
            "v1.0.0-alpha.1",
            "v1.0.0-alpha.beta",
            "v1.0.0-beta",
            "v1.0.0-beta.2",
            "v1.0.0-beta.11",
            "v1.0.0-rc.1",
            "v1.0.0",
        ];
        for w in ordered.windows(2) {
            assert!(
                parse_version(w[0]).unwrap() < parse_version(w[1]).unwrap(),
                "{} < {}",
                w[0],
                w[1]
            );
        }
        for bad in ["1.2", "v01.2.3", "1.2.3-01", "1.2.3+build"] {
            assert!(parse_version(bad).is_err(), "{bad}");
        }
    }
}
