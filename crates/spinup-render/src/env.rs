//! Strict minijinja configuration for converted packs.
use minijinja::{AutoEscape, Environment, Error, UndefinedBehavior};
use spinup_stack::stack::Stack;

/// Creates an environment with strict answers, explicit delimiters, and Go helpers.
pub fn environment(stack: Option<&Stack>) -> Environment<'static> {
    let mut env = Environment::new();
    env.set_syntax(
        minijinja::syntax::SyntaxConfig::builder()
            .variable_delimiters("[[", "]]")
            .block_delimiters("[%", "%]")
            .comment_delimiters("[#", "#]")
            .keep_trailing_newline(true)
            .build()
            .unwrap(),
    );
    env.set_undefined_behavior(UndefinedBehavior::Strict);
    env.set_auto_escape_callback(|_| AutoEscape::None);
    env.set_formatter(|out, state, value| {
        if value.is_undefined() {
            return minijinja::escape_formatter(out, state, value);
        }
        let text = if value.is_none() {
            "<no value>".to_owned()
        } else {
            crate::functions::go_display(value)?
        };
        out.write_str(&text).map_err(Error::from)
    });
    crate::functions::register(&mut env, stack);
    env
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    #[test]
    fn converted_templates_compile() {
        let tree = spinup_packs::embed::embedded().unwrap();
        let env = super::environment(Some(spinup_stack::load::embedded()));
        for (path, file) in tree.iter().filter(|(path, _)| path.ends_with(".tmpl")) {
            env.template_from_named_str(path, std::str::from_utf8(&file.data).unwrap())
                .unwrap_or_else(|e| panic!("{path}: {e}"));
        }
        for name in spinup_packs::loader::list(&tree).unwrap() {
            let pack = spinup_packs::loader::load(&tree, &name).unwrap();
            for file in pack.files {
                env.template_from_named_str(&file.source, &file.path)
                    .unwrap();
            }
        }
    }

    #[test]
    fn syntax_strictness_and_newlines() {
        let env = super::environment(None);
        assert_eq!(
            env.render_str(
                "[# comment #][% if yes %][[ text ]][% endif %]\n",
                minijinja::value::Serde(json!({"yes": true, "text": "<&>"}))
            )
            .unwrap(),
            "<&>\n"
        );
        for source in [
            "[[ missing ]]",
            "[% if missing %]x[% endif %]",
            "[[ default('x', missing) ]]",
            "[[ has('x', missing) ]]",
            "[[ go_printf('%v', missing) ]]",
        ] {
            assert!(
                env.render_str(source, minijinja::value::Serde(json!({})))
                    .is_err(),
                "{source}"
            );
        }
    }
}
