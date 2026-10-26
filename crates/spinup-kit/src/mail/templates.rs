//! Mail templates with minijinja (Go `kit/mail/templates.go`).

use std::collections::BTreeMap;
use std::sync::Arc;

use minijinja::{AutoEscape, Environment, UndefinedBehavior, Value};
use serde::Serialize;

use super::Message;

/// A set of template files by name, such as `welcome.txt.tmpl` (Go `fs.FS` of
/// `mail.Render`). Apps embed the files of `api/templates/mail/` with `include_str!`.
/// Clones share the files.
///
/// ```
/// use spinup_kit::mail::Templates;
///
/// let templates = Templates::new([
///     ("welcome.subject.tmpl", "Welcome"),
///     ("welcome.txt.tmpl", "Hello {{ name }}"),
///     ("welcome.html.tmpl", "<p>Hello {{ name }}</p>"),
/// ]);
/// assert!(templates.get("welcome.txt.tmpl").is_some());
/// ```
#[derive(Clone, Default, PartialEq, Eq)]
pub struct Templates(Arc<BTreeMap<String, String>>);

impl std::fmt::Debug for Templates {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_list().entries(self.0.keys()).finish()
    }
}

impl Templates {
    /// Returns a set of `(file name, source)` pairs.
    pub fn new<I, N, S>(files: I) -> Self
    where
        I: IntoIterator<Item = (N, S)>,
        N: Into<String>,
        S: Into<String>,
    {
        Self(Arc::new(
            files
                .into_iter()
                .map(|(n, s)| (n.into(), s.into()))
                .collect(),
        ))
    }

    /// The source of the file `name`.
    pub fn get(&self, name: &str) -> Option<&str> {
        self.0.get(name).map(String::as_str)
    }
}

/// Converts JSON data into a template value. The kit does not turn on the `serde` feature
/// of minijinja, so the data goes through `serde_json`.
fn to_value(v: serde_json::Value) -> Value {
    match v {
        serde_json::Value::Null => Value::from(()),
        serde_json::Value::Bool(b) => Value::from(b),
        serde_json::Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                Value::from(i)
            } else if let Some(u) = n.as_u64() {
                Value::from(u)
            } else {
                Value::from(n.as_f64().unwrap_or_default())
            }
        }
        serde_json::Value::String(s) => Value::from(s),
        serde_json::Value::Array(a) => Value::from(a.into_iter().map(to_value).collect::<Vec<_>>()),
        serde_json::Value::Object(o) => Value::from(
            o.into_iter()
                .map(|(k, v)| (k, to_value(v)))
                .collect::<BTreeMap<String, Value>>(),
        ),
    }
}

fn valid_name(name: &str) -> bool {
    !name.is_empty() && name != "." && name != ".." && !name.contains(['/', '\\', '\0'])
}

/// Renders `<name>.subject.tmpl`, `<name>.txt.tmpl`, and `<name>.html.tmpl` with `data`
/// into a message from `from` to `to` (Go `mail.Render`). The HTML template escapes
/// values. The text templates do not. A missing value is an error. The subject is
/// trimmed. The message must pass [`Message::validate`].
///
/// ```
/// use spinup_kit::mail::{Templates, render};
///
/// let templates = Templates::new([
///     ("welcome.subject.tmpl", "Hello {{ name }}\n"),
///     ("welcome.txt.tmpl", "Hello {{ name }}"),
///     ("welcome.html.tmpl", "<p>Hello {{ name }}</p>"),
/// ]);
/// let data = serde_json::json!({"name": "<b>Ann</b>"});
/// let m = render(&templates, "welcome", "app@example.com", "ann@example.com", &data).unwrap();
/// assert_eq!(m.subject, "Hello <b>Ann</b>");
/// assert_eq!(m.html, "<p>Hello &lt;b&gt;Ann&lt;&#x2f;b&gt;</p>");
/// ```
///
/// # Errors
///
/// A name with a path, a missing template, a template that does not render, or an
/// invalid message. The text has a `Try:` line.
pub fn render(
    templates: &Templates,
    name: &str,
    from: &str,
    to: &str,
    data: &impl Serialize,
) -> anyhow::Result<Message> {
    if !valid_name(name) {
        anyhow::bail!(
            "mail: cannot render template: invalid name\nTry: use a template base name without a path"
        );
    }
    let ctx = serde_json::to_value(data).map(to_value).map_err(|_| {
        anyhow::anyhow!(
            "mail: cannot render template {name}: the data does not encode\nTry: check the template data fields"
        )
    })?;
    let mut env = Environment::new();
    env.set_undefined_behavior(UndefinedBehavior::Strict);
    env.set_auto_escape_callback(|file| {
        if file.ends_with(".html.tmpl") {
            AutoEscape::Html
        } else {
            AutoEscape::None
        }
    });
    // Go text/template keeps the final newline of a template.
    if let Ok(syntax) = minijinja::syntax::SyntaxConfig::builder()
        .keep_trailing_newline(true)
        .build()
    {
        env.set_syntax(syntax);
    }
    let mut parts = Vec::with_capacity(3);
    for suffix in ["subject", "txt", "html"] {
        let file = format!("{name}.{suffix}.tmpl");
        let Some(source) = templates.get(&file) else {
            anyhow::bail!(
                "mail: cannot read template {file}: the file does not exist\nTry: embed all three templates"
            );
        };
        let out = env
            .render_named_str(&file, source, ctx.clone())
            .map_err(|_| {
                anyhow::anyhow!(
                    "mail: cannot render template {file}\nTry: check the template syntax and data fields"
                )
            })?;
        parts.push(out);
    }
    let html = parts.pop().unwrap_or_default();
    let text = parts.pop().unwrap_or_default();
    let subject = parts.pop().unwrap_or_default().trim().to_owned();
    let m = Message {
        from: from.to_owned(),
        to: to.to_owned(),
        subject,
        text,
        html,
    };
    m.validate()?;
    Ok(m)
}
