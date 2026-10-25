//! The kit notes parser for `MIGRATION.json` (Go `internal/migrate/notes.go`).

use std::collections::BTreeSet;

use serde::{Deserialize, Serialize};

/// The kit changes in `MIGRATION.json`.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct KitNotes {
    /// The format version of the notes. Only schema 1 is supported.
    pub schema: i64,
    /// The kit version that the notes describe.
    pub version: String,
    /// The changes, in presentation order.
    pub steps: Vec<KitStep>,
    /// The behavior versions that an app can opt in to with `behavior_version`.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub behaviors: Vec<KitBehavior>,
}

/// One opt-in behavior version (`docs/policy/behavior-version.md`).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct KitBehavior {
    /// The `behavior_version` value that turns the behavior on. It is 2 or more.
    pub version: i64,
    /// What changes for an app that opts in.
    pub desc: String,
}

/// A codemod, an inline fix, or a manual change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct KitStep {
    /// The ID of the step in the notes.
    pub id: String,
    /// The change, for the user.
    pub desc: String,
    /// `codemod`, `fix-inline`, or `manual`.
    pub kind: String,
    /// The registered migration of a codemod step.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub migration: String,
}

/// Kit notes that do not parse or that are not valid.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("parse MIGRATION.json: {0}\nTry: provide schema 1 kit notes with valid steps")]
pub struct NotesError(String);

/// Decodes schema 1 kit notes (Go `ParseKitNotes`). Unknown fields are errors.
///
/// # Errors
/// Returns [`NotesError`] when the data is not one JSON object, when it has an unknown
/// field, or when a step or a behavior is not valid.
pub fn parse_kit_notes(data: &[u8]) -> Result<KitNotes, NotesError> {
    let fail = |reason: String| NotesError(reason);
    let notes: Option<KitNotes> = serde_json::from_slice(data).map_err(|e| fail(e.to_string()))?;
    let notes = notes.ok_or_else(|| fail("expected a JSON object".into()))?;
    if notes.schema != 1 {
        return Err(fail(format!("unsupported schema {}", notes.schema)));
    }
    for step in &notes.steps {
        match step.kind.as_str() {
            "codemod" if step.migration.trim().is_empty() => {
                return Err(fail(format!("codemod step {:?} has no migration", step.id)));
            }
            "codemod" | "fix-inline" | "manual" => {}
            kind => {
                return Err(fail(format!(
                    "step {:?} has unknown kind {kind:?}",
                    step.id
                )));
            }
        }
    }
    let mut seen = BTreeSet::new();
    for behavior in &notes.behaviors {
        let version = behavior.version;
        if version < 2 {
            return Err(fail(format!("behavior version {version} is below 2")));
        }
        if seen.contains(&version) {
            return Err(fail(format!("behavior version {version} is listed twice")));
        }
        if behavior.desc.trim().is_empty() {
            return Err(fail(format!(
                "behavior version {version} has no description"
            )));
        }
        seen.insert(version);
    }
    Ok(notes)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_cases() {
        for (name, data, valid) in [
            (
                "valid",
                r#"{"schema":1,"version":"0.2.0","steps":[{"id":"a","desc":"rename","kind":"codemod","migration":"a"},{"kind":"fix-inline"},{"kind":"manual"}]}"#,
                true,
            ),
            ("schema", r#"{"schema":2}"#, false),
            ("unknown", r#"{"schema":1,"extra":true}"#, false),
            (
                "step unknown",
                r#"{"schema":1,"steps":[{"kind":"manual","extra":1}]}"#,
                false,
            ),
            ("kind", r#"{"schema":1,"steps":[{"kind":"other"}]}"#, false),
            (
                "migration",
                r#"{"schema":1,"steps":[{"kind":"codemod"}]}"#,
                false,
            ),
            ("trailing", r#"{"schema":1} {}"#, false),
            ("null", "null", false),
            ("syntax", "{", false),
            (
                "behaviors",
                r#"{"schema":1,"behaviors":[{"version":2,"desc":"strict"},{"version":3,"desc":"more"}]}"#,
                true,
            ),
            (
                "behavior low",
                r#"{"schema":1,"behaviors":[{"version":1,"desc":"x"}]}"#,
                false,
            ),
            (
                "behavior twice",
                r#"{"schema":1,"behaviors":[{"version":2,"desc":"x"},{"version":2,"desc":"y"}]}"#,
                false,
            ),
            (
                "behavior desc",
                r#"{"schema":1,"behaviors":[{"version":2,"desc":" "}]}"#,
                false,
            ),
        ] {
            let result = parse_kit_notes(data.as_bytes());
            assert_eq!(result.is_ok(), valid, "{name}: {result:?}");
            if let Err(e) = result {
                let text = e.to_string();
                assert!(text.starts_with("parse MIGRATION.json: "), "{text}");
                assert!(text.ends_with("\nTry: provide schema 1 kit notes with valid steps"));
            }
        }
    }

    #[test]
    fn fields_decode() {
        let notes = parse_kit_notes(
            br#"{"schema":1,"version":"0.2.0","steps":[{"id":"a","desc":"rename","kind":"codemod","migration":"0001_x"}]}"#,
        )
        .unwrap();
        assert_eq!(notes.version, "0.2.0");
        assert_eq!(
            notes.steps,
            [KitStep {
                id: "a".into(),
                desc: "rename".into(),
                kind: "codemod".into(),
                migration: "0001_x".into(),
            }]
        );
        assert!(notes.behaviors.is_empty());
        let text = serde_json::to_string(&notes).unwrap();
        assert!(!text.contains("behaviors"), "{text}");
    }
}
