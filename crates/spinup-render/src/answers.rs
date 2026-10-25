//! Default completion and answer validation.

use anyhow::{Result, bail};
use regex::Regex;
use serde_json::{Map, Value};
use spinup_packs::loader::{Pack, Question};
use std::collections::BTreeMap;

/// Copies answers, completes defaults, and validates shared question declarations.
pub fn validate_answers(
    packs: &[Pack],
    answers: &Map<String, Value>,
) -> Result<Map<String, Value>> {
    let mut completed = answers.clone();
    for key in answers.keys() {
        if matches!(key.as_str(), "Stack" | "TemplateVersion") {
            bail!(
                "validate answer {key:?}: reserved template key\nTry: use a different answer key"
            );
        }
    }
    let mut seen: BTreeMap<&str, &Question> = BTreeMap::new();
    for pack in packs {
        for q in &pack.questions {
            q.validate()?;
            if let Some(previous) = seen.insert(&q.key, q)
                && !same_question(previous, q)
            {
                return Err(answer_error(
                    q,
                    "conflicting question definitions across packs",
                ));
            }
            let value = match completed.get(&q.key) {
                Some(value) => value.clone(),
                None if q.has_default() => default_value(q).unwrap(),
                None => return Err(answer_error(q, "required answer is missing")),
            };
            completed.insert(q.key.clone(), validate_value(q, value)?);
        }
    }
    Ok(completed)
}

fn same_question(a: &Question, b: &Question) -> bool {
    a.key == b.key
        && a.prompt == b.prompt
        && a.r#type == b.r#type
        && a.default == b.default
        && a.has_default() == b.has_default()
        && a.pattern == b.pattern
        && a.choices == b.choices
}

/// Returns the declared default with the question's answer type.
pub fn default_value(q: &Question) -> Option<Value> {
    q.has_default().then(|| {
        if q.r#type == "bool" {
            Value::Bool(q.default == "true")
        } else {
            Value::String(q.default.clone())
        }
    })
}

fn validate_value(q: &Question, value: Value) -> Result<Value> {
    if q.key == "modules" && q.r#type == "string" {
        let values = match value {
            Value::String(text) => text
                .split(',')
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .map(|s| Value::String(s.into()))
                .collect::<Vec<_>>(),
            Value::Array(values) => values,
            _ => {
                return Err(answer_error(
                    q,
                    "expected a string slice or comma-separated string",
                ));
            }
        };
        for value in &values {
            check_pattern(
                q,
                value
                    .as_str()
                    .ok_or_else(|| answer_error(q, "expected a string slice"))?,
            )?;
        }
        return Ok(Value::Array(values));
    }
    match q.r#type.as_str() {
        "bool" if !value.is_boolean() => return Err(answer_error(q, "expected a bool")),
        "string" | "enum" => {
            let text = value
                .as_str()
                .ok_or_else(|| answer_error(q, "expected a string"))?;
            if q.r#type == "enum" && !q.choices.iter().any(|s| s == text) {
                return Err(answer_error(q, "value is not one of the enum choices"));
            }
            check_pattern(q, text)?;
        }
        _ => {}
    }
    Ok(value)
}

fn check_pattern(q: &Question, text: &str) -> Result<()> {
    if !q.pattern.is_empty() {
        let pattern = Regex::new(&q.pattern)
            .map_err(|e| answer_error(q, &format!("invalid pattern: {e}")))?;
        if !pattern.is_match(text) {
            return Err(answer_error(q, "value does not match the pattern"));
        }
    }
    Ok(())
}

pub(crate) fn answer_error(q: &Question, reason: &str) -> anyhow::Error {
    anyhow::anyhow!(
        "validate answer {:?}: {reason}\nTry: supply a valid --{} value",
        q.key,
        q.key.replace('_', "-")
    )
}

/// Returns shared questions once and rejects conflicting declarations.
pub fn questions(packs: &[Pack]) -> Result<Vec<Question>> {
    let mut result: Vec<Question> = Vec::new();
    for pack in packs {
        for q in &pack.questions {
            q.validate()?;
            if let Some(previous) = result.iter().find(|previous| previous.key == q.key) {
                if !same_question(previous, q) {
                    return Err(answer_error(
                        q,
                        "conflicting question definitions across packs",
                    ));
                }
            } else {
                result.push(q.clone());
            }
        }
    }
    Ok(result)
}

/// Asks through the UI interface. The caller must establish terminal availability.
pub fn ask_question(
    prompter: &mut dyn spinup_ui::prompt::Prompter,
    q: &Question,
    default: Option<&Value>,
) -> Result<Value> {
    let flag = q.key.replace('_', "-");
    match q.r#type.as_str() {
        "bool" => Ok(Value::Bool(prompter.confirm(
            &flag,
            &q.prompt,
            default.and_then(Value::as_bool).unwrap_or(false),
        )?)),
        "enum" => {
            let index = default
                .and_then(Value::as_str)
                .and_then(|value| q.choices.iter().position(|s| s == value));
            let selected = prompter.select(&flag, &q.prompt, &q.choices, index)?;
            q.choices
                .get(selected)
                .cloned()
                .map(Value::String)
                .ok_or_else(|| answer_error(q, "selected choice is out of range"))
        }
        _ => Ok(Value::String(prompter.input(
            &flag,
            &q.prompt,
            default.and_then(Value::as_str),
        )?)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use spinup_packs::{embed::embedded, loader::load};

    #[test]
    fn defaults_types_and_modules() {
        let pack = load(&embedded().unwrap(), "_sample").unwrap();
        assert_eq!(validate_answers(std::slice::from_ref(&pack), &Map::new()).unwrap(), json!({"app_name":"demo", "db":"postgres", "modules":[], "auth":false, "greeting":"Hello"}).as_object().unwrap().clone());
        for (answers, failure) in [
            (json!({"app_name":true}), "app_name"),
            (json!({"app_name":"Bad!"}), "--app-name"),
            (json!({"auth":"false"}), "auth"),
            (json!({"db":"sqlite"}), "db"),
            (json!({"modules":[2]}), "modules"),
            (json!({"Stack":null}), "reserved"),
            (json!({"TemplateVersion":"x"}), "reserved"),
        ] {
            let error = validate_answers(std::slice::from_ref(&pack), answers.as_object().unwrap())
                .unwrap_err()
                .to_string();
            assert!(
                error.contains(failure) && error.contains("\nTry:"),
                "{error}"
            );
        }
        for value in [json!("jobs, mail, ,"), json!(["jobs", "mail"])] {
            let answers = json!({"modules":value}).as_object().unwrap().clone();
            let mut completed = validate_answers(std::slice::from_ref(&pack), &answers).unwrap();
            assert_eq!(completed["modules"], json!(["jobs", "mail"]));
            completed["modules"][0] = json!("changed");
            assert_ne!(completed["modules"], answers["modules"]);
        }
    }

    #[test]
    fn shared_conflicting_and_required_questions() {
        let mut pack = Pack {
            questions: vec![Question {
                key: "app_name".into(),
                r#type: "string".into(),
                ..Question::default()
            }],
            ..Pack::default()
        };
        assert!(
            validate_answers(std::slice::from_ref(&pack), &Map::new())
                .unwrap_err()
                .to_string()
                .contains("--app-name")
        );
        pack.questions[0].default = "demo".into();
        assert!(validate_answers(&[pack.clone(), pack.clone()], &Map::new()).is_ok());
        let mut other = pack.clone();
        other.questions[0].default = "other".into();
        assert!(
            validate_answers(&[pack, other], &Map::new())
                .unwrap_err()
                .to_string()
                .contains("conflicting")
        );
    }
}
