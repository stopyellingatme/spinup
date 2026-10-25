//! Parse and evaluate pack conditions with Go-compatible errors.

use std::collections::BTreeMap;

use serde::{Deserialize, Deserializer};

/// A condition that failed to parse or evaluate.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{message}")]
pub struct Error {
    message: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Op {
    Bool,
    Not,
    Equal(String),
    NotEqual(String),
    Contains(String),
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct Predicate {
    key: String,
    op: Op,
}

/// A disjunction of conjunctions. Evaluation checks every predicate.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Condition {
    source: String,
    groups: Vec<Vec<Predicate>>,
}

// Accept the JSON answer map without requiring a second public answer format.
// The generic deserializer also accepts TOML values in tests and other callers.
#[derive(Deserialize)]
#[serde(untagged)]
enum Answer {
    Bool(bool),
    String(String),
    List(Vec<Answer>),
    Other(serde::de::IgnoredAny),
}

impl Condition {
    /// Compiles keys, comparisons, membership, negation, `&&`, and `||`.
    pub fn parse(source: &str) -> Result<Self, Error> {
        let mut condition = Self {
            source: source.into(),
            groups: Vec::new(),
        };
        let mut rest = source.trim();
        if rest.is_empty() {
            return Ok(condition);
        }
        let mut group = Vec::new();
        loop {
            let mut op = Op::Bool;
            if let Some(tail) = rest.strip_prefix('!') {
                op = Op::Not;
                rest = tail.trim();
            }
            let n = rest
                .bytes()
                .enumerate()
                .take_while(|(i, b)| {
                    *b == b'_' || b.is_ascii_alphabetic() || (*i > 0 && b.is_ascii_digit())
                })
                .count();
            if n == 0 {
                return Err(invalid(source));
            }
            let key = rest[..n].to_string();
            rest = rest[n..].trim();
            for operator in ["==", "!=", "contains"] {
                if let Some(tail) = rest.strip_prefix(operator) {
                    if op == Op::Not
                        || (operator == "contains"
                            && tail
                                .as_bytes()
                                .first()
                                .is_some_and(|b| !char::from(*b).is_whitespace()))
                    {
                        return Err(invalid(source));
                    }
                    let Some(tail) = tail.trim().strip_prefix('\'') else {
                        return Err(invalid(source));
                    };
                    let Some(end) = tail.find('\'') else {
                        return Err(invalid(source));
                    };
                    let value = tail[..end].to_string();
                    op = match operator {
                        "==" => Op::Equal(value),
                        "!=" => Op::NotEqual(value),
                        _ => Op::Contains(value),
                    };
                    rest = tail[end + 1..].trim();
                    break;
                }
            }
            group.push(Predicate { key, op });
            if rest.is_empty() {
                condition.groups.push(group);
                return Ok(condition);
            }
            if let Some(tail) = rest.strip_prefix("&&") {
                rest = tail.trim();
            } else if let Some(tail) = rest.strip_prefix("||") {
                condition.groups.push(std::mem::take(&mut group));
                rest = tail.trim();
            } else {
                return Err(invalid(source));
            }
        }
    }

    /// Evaluates all predicates, including branches whose result is already known.
    /// Accepts `&serde_json::Map<String, Value>` directly through serde deserializers.
    pub fn matches<'de, D>(
        &self,
        answers: impl IntoIterator<Item = (&'de String, D)>,
    ) -> Result<bool, Error>
    where
        D: Deserializer<'de>,
    {
        if self.groups.is_empty() {
            return Ok(true);
        }
        let answers: BTreeMap<_, _> = answers
            .into_iter()
            .map(|(key, value)| {
                (
                    key.as_str(),
                    Answer::deserialize(value).unwrap_or(Answer::Other(serde::de::IgnoredAny)),
                )
            })
            .collect();
        let mut result = false;
        for group in &self.groups {
            let mut conjunction = true;
            for predicate in group {
                let matched = predicate.matches(&answers).map_err(|reason| Error {
                    message: format!(
                        "evaluate when {}: {reason}\nTry: supply answers with the declared types",
                        quote(&self.source)
                    ),
                })?;
                conjunction &= matched;
            }
            result |= conjunction;
        }
        Ok(result)
    }

    pub(crate) fn source(&self) -> &str {
        &self.source
    }
}

impl Predicate {
    fn matches(&self, answers: &BTreeMap<&str, Answer>) -> Result<bool, String> {
        let key = quote(&self.key);
        let value = answers
            .get(self.key.as_str())
            .ok_or_else(|| format!("unknown answer key {key}"))?;
        match &self.op {
            Op::Bool | Op::Not => match value {
                Answer::Bool(value) => Ok(*value ^ (self.op == Op::Not)),
                _ => Err(format!("answer {key} must be a bool")),
            },
            Op::Equal(expected) | Op::NotEqual(expected) => match value {
                Answer::String(value) => {
                    Ok((value == expected) ^ matches!(self.op, Op::NotEqual(_)))
                }
                _ => Err(format!("answer {key} must be a string")),
            },
            Op::Contains(expected) => match value {
                Answer::List(values) => Ok(values
                    .iter()
                    .any(|v| matches!(v, Answer::String(s) if s == expected))),
                _ => Err(format!("answer {key} must be a string slice")),
            },
        }
    }
}

fn invalid(source: &str) -> Error {
    Error {
        message: format!(
            "invalid when expression {}\nTry: use keys, single-quoted values, &&, and ||",
            quote(source)
        ),
    }
}

pub(crate) fn quote(value: &str) -> String {
    let mut result = String::from("\"");
    for c in value.chars() {
        match c {
            '\\' => result.push_str("\\\\"),
            '"' => result.push_str("\\\""),
            '\n' => result.push_str("\\n"),
            '\r' => result.push_str("\\r"),
            '\t' => result.push_str("\\t"),
            '\x07' => result.push_str("\\a"),
            '\x08' => result.push_str("\\b"),
            '\x0c' => result.push_str("\\f"),
            '\x0b' => result.push_str("\\v"),
            c if c.is_ascii_control() => result.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() => result.push_str(&format!("\\u{:04x}", c as u32)),
            c => result.push(c),
        }
    }
    result.push('"');
    result
}

#[cfg(test)]
mod tests {
    use serde::Deserialize;

    #[derive(Deserialize)]
    struct Case {
        expression: String,
        answers: Option<serde_json::Map<String, serde_json::Value>>,
        expected: bool,
        error: String,
    }

    #[test]
    fn oracle_when_cases() {
        let cases: Vec<Case> =
            serde_json::from_str(include_str!("../../../testdata/oracle/when.json")).unwrap();
        for case in cases {
            let answers = case.answers.unwrap_or_default();
            let result = super::Condition::parse(&case.expression)
                .and_then(|condition| condition.matches(&answers));
            match result {
                Ok(actual) => {
                    assert!(case.error.is_empty(), "{}: {}", case.expression, case.error);
                    assert_eq!(actual, case.expected, "{}", case.expression);
                }
                Err(error) => assert_eq!(error.to_string(), case.error, "{}", case.expression),
            }
        }
    }
}
