//! Questions and their flags.
//!
use anyhow::Result;
use serde_json::{Map, Value};
use spinup_packs::loader::{Pack, Question};
use spinup_ui::prompt::Prompter;

use crate::select::Options;

pub use spinup_render::answers::questions;

/// Completes explicit flags and defaults. The caller establishes terminal availability.
/// With `interactive` false, this function never calls the prompter.
pub fn complete_answers(
    options: &Options,
    questions: &[Question],
    mut prompter: Option<&mut dyn Prompter>,
) -> Result<(Map<String, Value>, Vec<String>)> {
    let mut answers = options.answers.clone();
    answers.insert("app_name".into(), options.name.clone().into());
    let mut warnings = Vec::new();
    for q in questions {
        if answers.contains_key(&q.key) {
            continue;
        }
        let mut value = if q.has_default() {
            Some(if q.r#type == "bool" {
                Value::Bool(q.default == "true")
            } else {
                q.default.clone().into()
            })
        } else {
            None
        };
        if q.key == "module" {
            value = Some(format!("example.com/{}", options.name).into());
        }
        if options.interactive {
            let prompter = prompter.as_deref_mut().ok_or_else(|| {
                anyhow::anyhow!(
                    "cannot prompt for {:?}: no prompter is available\nTry: pass --{}",
                    q.key,
                    q.key.replace('_', "-")
                )
            })?;
            value = Some(spinup_render::answers::ask_question(
                prompter,
                q,
                value.as_ref(),
            )?);
        }
        if q.key == "module"
            && value.as_ref().and_then(Value::as_str)
                == Some(&format!("example.com/{}", options.name))
        {
            warnings.push(format!(
                "Using example.com/{} as the Go module. Pass --module to choose your module path.",
                options.name
            ));
        }
        if let Some(value) = value {
            answers.insert(q.key.clone(), value);
        }
    }
    let pack = Pack {
        questions: questions.to_vec(),
        ..Pack::default()
    };
    let answers = spinup_render::answers::validate_answers(&[pack], &answers).map_err(|error| {
        if error.to_string().contains("validate answer \"app_name\"") {
            anyhow::anyhow!("invalid app_name {:?}: the name does not match the pack pattern\nTry: pass an application name such as todo", options.name)
        } else { error }
    })?;
    Ok((answers, warnings))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use spinup_ui::prompt::{Answer, ScriptedPrompter};

    #[test]
    fn explicit_flags_override_prompts_and_defaults() {
        let questions = vec![
            Question {
                key: "module".into(),
                r#type: "string".into(),
                ..Question::default()
            },
            Question {
                key: "enabled".into(),
                r#type: "bool".into(),
                default: "true".into(),
                ..Question::default()
            },
            Question {
                key: "mode".into(),
                r#type: "enum".into(),
                choices: vec!["full".into(), "lite".into()],
                default: "full".into(),
                ..Question::default()
            },
        ];
        for interactive in [false, true] {
            let options = Options {
                name: "todo".into(),
                interactive,
                answers: json!({"module":"example.com/todo"})
                    .as_object()
                    .unwrap()
                    .clone(),
                ..Options::default()
            };
            let mut prompt = ScriptedPrompter::new()
                .answer("enabled", Answer::Bool(false))
                .answer("mode", Answer::Index(1));
            let (answers, _) = complete_answers(&options, &questions, Some(&mut prompt)).unwrap();
            assert_eq!(answers["module"], "example.com/todo");
            assert_eq!(answers["enabled"], !interactive);
            assert_eq!(answers["mode"], if interactive { "lite" } else { "full" });
            if interactive {
                assert_eq!(prompt.asked(), ["enabled", "mode"]);
            } else {
                assert!(prompt.asked().is_empty());
            }
        }
    }

    #[test]
    fn required_flags_fail_without_a_terminal_and_do_not_prompt() {
        let q = Question {
            key: "required_value".into(),
            r#type: "string".into(),
            ..Question::default()
        };
        let mut prompt = ScriptedPrompter::new();
        let error = complete_answers(
            &Options {
                name: "todo".into(),
                ..Options::default()
            },
            std::slice::from_ref(&q),
            Some(&mut prompt),
        )
        .unwrap_err()
        .to_string();
        assert!(error.contains("--required-value") && error.contains("Try:"));
        assert!(prompt.asked().is_empty());
        let options = Options {
            name: "todo".into(),
            interactive: true,
            ..Options::default()
        };
        assert!(
            complete_answers(&options, &[q], None)
                .unwrap_err()
                .to_string()
                .contains("no prompter")
        );
    }

    #[test]
    fn shared_questions_have_one_definition() {
        let mut pack = Pack {
            questions: vec![Question {
                key: "name".into(),
                r#type: "string".into(),
                default: "demo".into(),
                ..Question::default()
            }],
            ..Pack::default()
        };
        assert_eq!(questions(&[pack.clone(), pack.clone()]).unwrap().len(), 1);
        let first = pack.clone();
        pack.questions[0].default = "other".into();
        assert!(
            questions(&[first, pack])
                .unwrap_err()
                .to_string()
                .contains("conflicting question")
        );
    }
}
