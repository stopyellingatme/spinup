//! The generator picker through `Prompter` (Go `internal/gen/run/picker.go` and the picker
//! part of `internal/cli/generate.go`).
//!
//! `spinup g` without a kind asks for the kind and then for the argument line. Without a
//! terminal, the CLI never prompts: it prints [`listing`] and returns [`no_terminal`].

use std::fmt::Write as _;

use spinup_ui::prompt::Prompter;

use crate::contract::Generator;

/// The prompt flag of the kind question. The kind is the first positional argument.
pub const KIND_FLAG: &str = "kind";

/// The prompt flag of the argument line question.
pub const ARGS_FLAG: &str = "args";

/// Asks for a generator kind and its argument line, and splits the line at white space.
///
/// The returned generator is one of `kinds`.
///
/// # Errors
/// Returns an error with a `Try:` line when `kinds` is empty or when the prompt fails.
pub fn pick<'a>(
    kinds: &[&'a Generator],
    prompter: &mut dyn Prompter,
) -> anyhow::Result<(&'a Generator, Vec<String>)> {
    if kinds.is_empty() {
        anyhow::bail!(
            "cannot select a generator: none are registered\nTry: use a spinup build with registered generators"
        );
    }
    let options: Vec<String> = kinds
        .iter()
        .map(|g| format!("{} — {}", g.name, g.short))
        .collect();
    let index = prompter
        .select(KIND_FLAG, "Generator kind", &options, None)
        .map_err(prompt_error)?;
    let Some(generator) = kinds.get(index).copied() else {
        anyhow::bail!(
            "cannot select generator {index}: the kind is not registered\nTry: spinup g <kind> --help"
        );
    };
    let mut title = String::from("Arguments and flags");
    if !generator.usage.is_empty() {
        let _ = write!(title, " ({})", generator.usage);
    }
    let line = prompter
        .input(ARGS_FLAG, &title, None)
        .map_err(prompt_error)?;
    Ok((
        generator,
        line.split_whitespace().map(str::to_string).collect(),
    ))
}

/// The prompt error, with the `Try:` line of the generate command. The prompter hint names
/// a flag, but the kind and its arguments are positional.
fn prompt_error(err: spinup_ui::error::Error) -> anyhow::Error {
    anyhow::anyhow!("read generator selection: {err}\nTry: spinup g <kind> [args...]")
}

/// The list of kinds that `spinup g` prints when it cannot prompt.
pub fn listing(kinds: &[&Generator]) -> String {
    let mut text = String::from("Available generators:\n");
    for g in kinds {
        let _ = writeln!(text, "  {}\t{}", g.name, g.short);
    }
    if kinds.is_empty() {
        text.push_str("  (none registered)\n");
    }
    text
}

/// The usage error of `spinup g` without a kind and without a terminal.
pub fn no_terminal() -> spinup_ui::error::Error {
    spinup_ui::error::Error::new("Cannot select a generator")
        .why("a terminal is required for the picker")
        .try_hint("spinup g <kind> --help")
        .code(spinup_ui::exit::USAGE)
}

/// The usage error for a kind that no generator has.
pub fn unknown_kind(kind: &str) -> spinup_ui::error::Error {
    spinup_ui::error::Error::new("Cannot generate files")
        .why(format!("generator {kind:?} is not registered"))
        .try_hint("spinup g <kind> --help")
        .code(spinup_ui::exit::USAGE)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::contract::{Input, Output};
    use spinup_ui::prompt::{Answer, NoPrompter, ScriptedPrompter};

    fn run(_: &Input<'_>) -> anyhow::Result<Output> {
        Ok(Output::default())
    }

    static EXAMPLE: Generator = Generator {
        name: "example",
        aliases: &["e"],
        short: "Example generator",
        usage: "<name> [fields...]",
        args: std::convert::identity,
        run,
    };
    static OTHER: Generator = Generator {
        name: "other",
        aliases: &[],
        short: "Other generator",
        usage: "",
        args: std::convert::identity,
        run,
    };

    #[test]
    fn picks_a_kind_and_splits_the_line() {
        let mut prompter = ScriptedPrompter::new()
            .answer(KIND_FLAG, Answer::Index(0))
            .answer(
                ARGS_FLAG,
                Answer::Text("  todo title:string\t--tag selected ".into()),
            );
        let (g, args) = pick(&[&EXAMPLE, &OTHER], &mut prompter).unwrap();
        assert_eq!(g.name, "example");
        assert_eq!(args, ["todo", "title:string", "--tag", "selected"]);
        assert_eq!(prompter.asked(), [KIND_FLAG, ARGS_FLAG]);
    }

    #[test]
    fn empty_line_gives_no_arguments() {
        let mut prompter = ScriptedPrompter::new()
            .answer(KIND_FLAG, Answer::Index(1))
            .answer(ARGS_FLAG, Answer::Text(String::new()));
        let (g, args) = pick(&[&EXAMPLE, &OTHER], &mut prompter).unwrap();
        assert_eq!(g.name, "other");
        assert!(args.is_empty());
    }

    #[test]
    fn errors_have_one_try_line() {
        let err = pick(&[], &mut NoPrompter).unwrap_err();
        assert!(format!("{err:#}").ends_with("Try: use a spinup build with registered generators"));
        for mut prompter in [
            ScriptedPrompter::new(),
            ScriptedPrompter::new().answer(KIND_FLAG, Answer::Index(0)),
            ScriptedPrompter::new().answer(KIND_FLAG, Answer::Index(9)),
        ] {
            let err = format!("{:#}", pick(&[&EXAMPLE], &mut prompter).unwrap_err());
            assert!(err.starts_with("read generator selection: "), "{err}");
            assert!(err.ends_with("\nTry: spinup g <kind> [args...]"), "{err}");
            assert_eq!(err.matches("Try:").count(), 1, "{err}");
        }
    }

    #[test]
    fn listing_and_usage_errors() {
        assert_eq!(
            listing(&[&EXAMPLE, &OTHER]),
            "Available generators:\n  example\tExample generator\n  other\tOther generator\n"
        );
        assert_eq!(listing(&[]), "Available generators:\n  (none registered)\n");
        let err = no_terminal();
        assert_eq!(err.try_hint, "spinup g <kind> --help");
        assert_eq!(err.code, spinup_ui::exit::USAGE);
        let err = unknown_kind("nope");
        assert_eq!(
            err.to_string(),
            "Cannot generate files: generator \"nope\" is not registered"
        );
        assert_eq!(err.code, spinup_ui::exit::USAGE);
    }
}
