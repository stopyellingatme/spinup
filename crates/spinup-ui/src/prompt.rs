//! The `Prompter` trait and its terminal, scripted, and non-interactive implementations.
//!
//! Every question has a flag name. With no terminal, a prompter never asks. It fails and names the flag.

use std::collections::{HashMap, VecDeque};

use crate::error::Error;

/// Ask the user for a value. Every method takes the `flag` that gives the same value without a prompt.
pub trait Prompter {
    /// Pick one option. Return its index.
    ///
    /// # Errors
    /// Returns a user-facing error when the prompter cannot ask or the user cancels.
    fn select(
        &mut self,
        flag: &str,
        message: &str,
        options: &[String],
        default: Option<usize>,
    ) -> Result<usize, Error>;

    /// Pick any number of options. Return the indexes in list order.
    ///
    /// # Errors
    /// Returns a user-facing error when the prompter cannot ask or the user cancels.
    fn multi_select(
        &mut self,
        flag: &str,
        message: &str,
        options: &[String],
        defaults: &[usize],
    ) -> Result<Vec<usize>, Error>;

    /// Ask a yes or no question.
    ///
    /// # Errors
    /// Returns a user-facing error when the prompter cannot ask or the user cancels.
    fn confirm(&mut self, flag: &str, message: &str, default: bool) -> Result<bool, Error>;

    /// Ask for a line of text.
    ///
    /// # Errors
    /// Returns a user-facing error when the prompter cannot ask or the user cancels.
    fn input(&mut self, flag: &str, message: &str, default: Option<&str>) -> Result<String, Error>;

    /// Ask for a secret. The terminal does not echo it.
    ///
    /// # Errors
    /// Returns a user-facing error when the prompter cannot ask or the user cancels.
    fn password(&mut self, flag: &str, message: &str) -> Result<String, Error>;
}

/// The error for a question that has no terminal to ask on. It names the missing flag.
#[must_use]
pub fn missing_flag(flag: &str, message: &str) -> Error {
    Error::new(format!("Missing value for --{flag}"))
        .why(format!(
            "cannot ask \"{message}\" because no terminal is available"
        ))
        .try_hint(format!("Pass --{flag} <value>."))
        .code(crate::exit::USAGE)
}

fn canceled(flag: &str) -> Error {
    Error::new("Prompt canceled")
        .why("the user stopped the prompt")
        .try_hint(format!("Run the command again, or pass --{flag} <value>."))
}

fn failed(flag: &str, err: &inquire::InquireError) -> Error {
    use inquire::InquireError as E;
    match err {
        E::OperationCanceled | E::OperationInterrupted => canceled(flag),
        E::NotTTY => missing_flag(flag, "a value"),
        other => Error::new("Prompt failed")
            .why(other.to_string())
            .try_hint(format!("Pass --{flag} <value>.")),
    }
}

/// Asks on the terminal with `inquire`.
#[derive(Debug, Default, Clone, Copy)]
pub struct TerminalPrompter;

impl Prompter for TerminalPrompter {
    fn select(
        &mut self,
        flag: &str,
        message: &str,
        options: &[String],
        default: Option<usize>,
    ) -> Result<usize, Error> {
        let mut p = inquire::Select::new(message, options.to_vec());
        if let Some(d) = default {
            p = p.with_starting_cursor(d);
        }
        p.raw_prompt()
            .map(|o| o.index)
            .map_err(|e| failed(flag, &e))
    }

    fn multi_select(
        &mut self,
        flag: &str,
        message: &str,
        options: &[String],
        defaults: &[usize],
    ) -> Result<Vec<usize>, Error> {
        inquire::MultiSelect::new(message, options.to_vec())
            .with_default(defaults)
            .raw_prompt()
            .map(|v| v.into_iter().map(|o| o.index).collect())
            .map_err(|e| failed(flag, &e))
    }

    fn confirm(&mut self, flag: &str, message: &str, default: bool) -> Result<bool, Error> {
        inquire::Confirm::new(message)
            .with_default(default)
            .prompt()
            .map_err(|e| failed(flag, &e))
    }

    fn input(&mut self, flag: &str, message: &str, default: Option<&str>) -> Result<String, Error> {
        let mut p = inquire::Text::new(message);
        if let Some(d) = default {
            p = p.with_default(d);
        }
        p.prompt().map_err(|e| failed(flag, &e))
    }

    fn password(&mut self, flag: &str, message: &str) -> Result<String, Error> {
        inquire::Password::new(message)
            .without_confirmation()
            .prompt()
            .map_err(|e| failed(flag, &e))
    }
}

/// Never asks. Every question fails with an error that names the flag.
#[derive(Debug, Default, Clone, Copy)]
pub struct NoPrompter;

impl Prompter for NoPrompter {
    fn select(
        &mut self,
        flag: &str,
        message: &str,
        _: &[String],
        _: Option<usize>,
    ) -> Result<usize, Error> {
        Err(missing_flag(flag, message))
    }

    fn multi_select(
        &mut self,
        flag: &str,
        message: &str,
        _: &[String],
        _: &[usize],
    ) -> Result<Vec<usize>, Error> {
        Err(missing_flag(flag, message))
    }

    fn confirm(&mut self, flag: &str, message: &str, _: bool) -> Result<bool, Error> {
        Err(missing_flag(flag, message))
    }

    fn input(&mut self, flag: &str, message: &str, _: Option<&str>) -> Result<String, Error> {
        Err(missing_flag(flag, message))
    }

    fn password(&mut self, flag: &str, message: &str) -> Result<String, Error> {
        Err(missing_flag(flag, message))
    }
}

/// One scripted answer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Answer {
    /// An index for `select`.
    Index(usize),
    /// Indexes for `multi_select`.
    Indexes(Vec<usize>),
    /// A value for `confirm`.
    Bool(bool),
    /// Text for `input` and `password`.
    Text(String),
}

/// Answers from a script, in order for each flag. Tests use it. A question with no answer fails like [`NoPrompter`].
#[derive(Debug, Default, Clone)]
pub struct ScriptedPrompter {
    answers: HashMap<String, VecDeque<Answer>>,
    asked: Vec<String>,
}

impl ScriptedPrompter {
    /// Create an empty script.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Queue an answer for `flag`.
    #[must_use]
    pub fn answer(mut self, flag: &str, answer: Answer) -> Self {
        self.answers
            .entry(flag.to_string())
            .or_default()
            .push_back(answer);
        self
    }

    /// The flags that the code asked, in order.
    #[must_use]
    pub fn asked(&self) -> &[String] {
        &self.asked
    }

    fn next(&mut self, flag: &str, message: &str) -> Result<Answer, Error> {
        self.asked.push(flag.to_string());
        self.answers
            .get_mut(flag)
            .and_then(VecDeque::pop_front)
            .ok_or_else(|| missing_flag(flag, message))
    }

    fn mismatch(flag: &str, got: &Answer, want: &str) -> Error {
        Error::new(format!("Wrong scripted answer for --{flag}"))
            .why(format!("expected {want}, got {got:?}"))
    }
}

impl Prompter for ScriptedPrompter {
    fn select(
        &mut self,
        flag: &str,
        message: &str,
        options: &[String],
        _: Option<usize>,
    ) -> Result<usize, Error> {
        match self.next(flag, message)? {
            Answer::Index(i) if i < options.len() => Ok(i),
            other => Err(Self::mismatch(flag, &other, "an index in range")),
        }
    }

    fn multi_select(
        &mut self,
        flag: &str,
        message: &str,
        options: &[String],
        _: &[usize],
    ) -> Result<Vec<usize>, Error> {
        match self.next(flag, message)? {
            Answer::Indexes(v) if v.iter().all(|i| *i < options.len()) => Ok(v),
            other => Err(Self::mismatch(flag, &other, "indexes in range")),
        }
    }

    fn confirm(&mut self, flag: &str, message: &str, _: bool) -> Result<bool, Error> {
        match self.next(flag, message)? {
            Answer::Bool(b) => Ok(b),
            other => Err(Self::mismatch(flag, &other, "a bool")),
        }
    }

    fn input(&mut self, flag: &str, message: &str, _: Option<&str>) -> Result<String, Error> {
        match self.next(flag, message)? {
            Answer::Text(s) => Ok(s),
            other => Err(Self::mismatch(flag, &other, "text")),
        }
    }

    fn password(&mut self, flag: &str, message: &str) -> Result<String, Error> {
        self.input(flag, message, None)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn opts() -> Vec<String> {
        vec!["a".into(), "b".into()]
    }

    #[test]
    fn no_prompter_names_the_flag() {
        let e = NoPrompter.confirm("yes", "Go on?", false).unwrap_err();
        assert_eq!(e.what, "Missing value for --yes");
        assert_eq!(e.try_hint, "Pass --yes <value>.");
        assert_eq!(e.code, crate::exit::USAGE);
    }

    #[test]
    fn scripted_answers_in_order() {
        let mut p = ScriptedPrompter::new()
            .answer("pack", Answer::Index(1))
            .answer("name", Answer::Text("todo".into()))
            .answer("name", Answer::Text("two".into()))
            .answer("mods", Answer::Indexes(vec![0, 1]))
            .answer("ok", Answer::Bool(true));
        assert_eq!(p.select("pack", "Pack", &opts(), None).unwrap(), 1);
        assert_eq!(p.input("name", "Name", None).unwrap(), "todo");
        assert_eq!(p.password("name", "Secret").unwrap(), "two");
        assert_eq!(
            p.multi_select("mods", "Mods", &opts(), &[]).unwrap(),
            [0, 1]
        );
        assert!(p.confirm("ok", "Ok?", false).unwrap());
        assert!(p.input("name", "Name", None).is_err());
        assert_eq!(p.asked().len(), 6);
    }

    #[test]
    fn scripted_rejects_bad_answers() {
        let mut p = ScriptedPrompter::new()
            .answer("pack", Answer::Index(5))
            .answer("ok", Answer::Text("x".into()));
        assert!(p.select("pack", "Pack", &opts(), None).is_err());
        assert!(p.confirm("ok", "Ok?", true).is_err());
    }
}
