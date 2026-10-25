//! Console output, color, and JSON error output (Go `Printer`).

use std::io::{self, IsTerminal, Read, Write};
use std::sync::{Arc, Mutex};

use crate::error::parts;

/// Sets the behavior of a [`Printer`].
#[derive(Debug, Clone, Copy, Default)]
pub struct Options {
    /// Switch errors to JSON and silence status messages.
    pub json: bool,
    /// Turn off ANSI color.
    pub no_color: bool,
    /// Forbid prompts.
    pub non_interactive: bool,
    /// Turn on `debug` output.
    pub verbose: bool,
}

/// A clonable in-memory writer. Tests use it to read what a [`Printer`] wrote.
#[derive(Debug, Clone, Default)]
pub struct SharedBuffer(Arc<Mutex<Vec<u8>>>);

impl SharedBuffer {
    /// Create an empty buffer.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// The bytes written so far, as text (lossy).
    #[must_use]
    pub fn contents(&self) -> String {
        String::from_utf8_lossy(
            &self
                .0
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner),
        )
        .into_owned()
    }
}

impl Write for SharedBuffer {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.0
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .extend_from_slice(buf);
        Ok(buf.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[derive(serde::Serialize)]
struct Envelope<'a> {
    error: &'a crate::error::Parts,
}

type Sink = Mutex<Box<dyn Write + Send>>;

/// Writes user-facing output.
pub struct Printer {
    // Kept so a caller can read prompt input through the printer. The Go Printer holds it too.
    #[allow(dead_code, reason = "reserved for stdin readers")]
    input: Mutex<Box<dyn Read + Send>>,
    out: Sink,
    err: Sink,
    tty: [bool; 3],
    opts: Options,
}

impl std::fmt::Debug for Printer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Printer")
            .field("tty", &self.tty)
            .field("opts", &self.opts)
            .finish_non_exhaustive()
    }
}

fn lock(sink: &Sink) -> std::sync::MutexGuard<'_, Box<dyn Write + Send>> {
    sink.lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner)
}

/// Go `json.Marshal` escapes `<`, `>`, `&`, U+2028, and U+2029. Do the same for byte parity.
pub fn go_escape(json: &str) -> String {
    let mut s = String::with_capacity(json.len());
    for c in json.chars() {
        match c {
            '<' => s.push_str("\\u003c"),
            '>' => s.push_str("\\u003e"),
            '&' => s.push_str("\\u0026"),
            '\u{2028}' => s.push_str("\\u2028"),
            '\u{2029}' => s.push_str("\\u2029"),
            _ => s.push(c),
        }
    }
    s
}

impl Printer {
    /// Create a printer over any reader and writers. None of them counts as a terminal.
    pub fn new(
        input: impl Read + Send + 'static,
        out: impl Write + Send + 'static,
        err: impl Write + Send + 'static,
        opts: Options,
    ) -> Self {
        Self {
            input: Mutex::new(Box::new(input)),
            out: Mutex::new(Box::new(out)),
            err: Mutex::new(Box::new(err)),
            tty: [false; 3],
            opts,
        }
    }

    /// Create a printer over the process streams. It detects terminals.
    #[must_use]
    pub fn stdio(opts: Options) -> Self {
        let mut p = Self::new(io::stdin(), io::stdout(), io::stderr(), opts);
        p.tty = [
            io::stdin().is_terminal(),
            io::stdout().is_terminal(),
            io::stderr().is_terminal(),
        ];
        p
    }

    /// Override the terminal flags for stdin, stdout, and stderr (for tests).
    #[must_use]
    pub fn with_tty(mut self, stdin: bool, stdout: bool, stderr: bool) -> Self {
        self.tty = [stdin, stdout, stderr];
        self
    }

    /// The options of this printer.
    #[must_use]
    pub fn options(&self) -> Options {
        self.opts
    }

    /// Write a line to out.
    pub fn println(&self, text: &str) {
        let _ = writeln!(lock(&self.out), "{text}");
    }

    /// Write text to out, with no newline.
    pub fn print(&self, text: &str) {
        let _ = write!(lock(&self.out), "{text}");
    }

    /// Write an informational line to out. It is silent in JSON mode.
    pub fn info(&self, text: &str) {
        if !self.opts.json {
            self.println(text);
        }
    }

    /// Write a success line to out. It is silent in JSON mode.
    pub fn success(&self, text: &str) {
        if !self.opts.json {
            let mark = self.paint(1, "32", "✓ ");
            self.println(&format!("{mark}{text}"));
        }
    }

    /// Write a warning line to err.
    pub fn warn(&self, text: &str) {
        let label = self.paint(2, "33", "Warning: ");
        let _ = writeln!(lock(&self.err), "{label}{text}");
    }

    /// Write a line to err when `verbose` is set.
    pub fn debug(&self, text: &str) {
        if self.opts.verbose {
            let _ = writeln!(lock(&self.err), "{text}");
        }
    }

    /// Write `value` as indented JSON (2 spaces), plus a newline, to out.
    ///
    /// # Errors
    /// Returns an error when `value` cannot serialize or the write fails.
    pub fn json<T: serde::Serialize>(&self, value: &T) -> anyhow::Result<()> {
        let text = go_escape(&serde_json::to_string_pretty(value)?);
        writeln!(lock(&self.out), "{text}")?;
        Ok(())
    }

    /// Write `err` to err. See [`crate::error::parts`] for the rules.
    pub fn render_error(&self, err: &anyhow::Error) {
        let p = parts(err);
        if self.opts.json {
            if let Ok(text) = serde_json::to_string(&Envelope { error: &p }) {
                let _ = writeln!(lock(&self.err), "{}", go_escape(&text));
            }
            return;
        }
        let mut w = lock(&self.err);
        let label = self.paint_for(self.color_for(2), "31;1", "Error:");
        let _ = writeln!(w, "{label} {}", p.what);
        if !p.why.is_empty() {
            let _ = writeln!(w, "  {}", p.why);
        }
        if !p.try_hint.is_empty() {
            let _ = writeln!(w, "  Try: {}", p.try_hint);
        }
    }

    /// Report whether prompts are allowed: stdin and stdout are terminals,
    /// `non_interactive` is false, and `CI` is not set.
    #[must_use]
    pub fn interactive(&self) -> bool {
        let ci = std::env::var_os("CI").is_some_and(|v| !v.is_empty());
        interactive_from(self.opts, ci, self.tty[0], self.tty[1])
    }

    /// Report whether the printer writes ANSI color to out.
    #[must_use]
    pub fn color(&self) -> bool {
        self.color_for(1)
    }

    fn color_for(&self, stream: usize) -> bool {
        color_from(
            self.opts,
            std::env::var_os("NO_COLOR").is_some(),
            self.tty[stream],
        )
    }

    fn paint(&self, stream: usize, code: &str, text: &str) -> String {
        self.paint_for(self.color_for(stream), code, text)
    }

    fn paint_for(&self, on: bool, code: &str, text: &str) -> String {
        if on {
            format!("\x1b[{code}m{text}\x1b[0m")
        } else {
            text.to_string()
        }
    }
}

/// The interactive rule, with the environment and terminals given.
#[must_use]
pub fn interactive_from(opts: Options, ci_set: bool, stdin_tty: bool, stdout_tty: bool) -> bool {
    !(opts.non_interactive || ci_set) && stdin_tty && stdout_tty
}

/// The color rule, with the environment and terminal given.
#[must_use]
pub fn color_from(opts: Options, no_color_env: bool, tty: bool) -> bool {
    !opts.no_color && !no_color_env && tty
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::Error;

    fn printer(opts: Options) -> (Printer, SharedBuffer, SharedBuffer) {
        let (out, err) = (SharedBuffer::new(), SharedBuffer::new());
        (
            Printer::new(io::empty(), out.clone(), err.clone(), opts),
            out,
            err,
        )
    }

    fn render(opts: Options, err: &anyhow::Error) -> String {
        let (p, out, e) = printer(opts);
        p.render_error(err);
        assert_eq!(out.contents(), "", "stdout must stay empty");
        e.contents()
    }

    #[test]
    fn render_error_cases() {
        let no_color = Options {
            no_color: true,
            ..Options::default()
        };
        let full: anyhow::Error = Error::new("Cannot read file")
            .why("it is missing")
            .try_hint("spinup doctor")
            .into();
        assert_eq!(
            render(no_color, &full),
            "Error: Cannot read file\n  it is missing\n  Try: spinup doctor\n"
        );
        assert_eq!(
            render(no_color, &Error::new("Failed").into()),
            "Error: Failed\n"
        );
        assert_eq!(render(no_color, &anyhow::anyhow!("boom")), "Error: boom\n");
        let json: anyhow::Error = Error::new("w").why("y").try_hint("t").code(3).into();
        assert_eq!(
            render(
                Options {
                    json: true,
                    ..Options::default()
                },
                &json
            ),
            "{\"error\":{\"what\":\"w\",\"why\":\"y\",\"try\":\"t\",\"code\":3}}\n"
        );
    }

    #[test]
    fn render_error_shows_one_try() {
        let o = Options {
            no_color: true,
            ..Options::default()
        };
        let nested = anyhow::anyhow!("outer: inner\nTry: inner hint\nTry: outer hint");
        assert_eq!(
            render(o, &nested),
            "Error: outer: inner\n  Try: outer hint\n"
        );
        let ui: anyhow::Error = Error::new("Upgrade failed")
            .why("step: x\nTry: low\nTry: mid")
            .try_hint("top")
            .into();
        assert_eq!(
            render(o, &ui),
            "Error: Upgrade failed\n  step: x\n  Try: top\n"
        );
        let no_own: anyhow::Error = Error::new("Failed").why("x\nTry: only").into();
        assert_eq!(render(o, &no_own), "Error: Failed\n  x\n  Try: only\n");
    }

    #[test]
    fn json_and_messages() {
        let (p, out, _) = printer(Options::default());
        p.json(&serde_json::json!({"a": 1})).unwrap();
        assert_eq!(out.contents(), "{\n  \"a\": 1\n}\n");
        let (p, out, err) = printer(Options::default());
        p.println("x y");
        p.print("2\n");
        p.info("info z");
        p.success("ok");
        p.warn("careful");
        p.debug("hidden");
        assert_eq!(out.contents(), "x y\n2\ninfo z\n✓ ok\n");
        assert_eq!(err.contents(), "Warning: careful\n");
    }

    #[test]
    fn json_escapes_like_go() {
        let (p, out, _) = printer(Options::default());
        p.json(&serde_json::json!({"a": "<x>&"})).unwrap();
        assert_eq!(
            out.contents(),
            "{\n  \"a\": \"\\u003cx\\u003e\\u0026\"\n}\n"
        );
    }

    #[test]
    fn json_mode_silences_status() {
        let (p, out, _) = printer(Options {
            json: true,
            ..Options::default()
        });
        p.info("a");
        p.success("b");
        assert_eq!(out.contents(), "");
    }

    #[test]
    fn verbose_debug() {
        let (p, _, err) = printer(Options {
            verbose: true,
            ..Options::default()
        });
        p.debug("d");
        assert_eq!(err.contents(), "d\n");
    }

    #[test]
    fn interactive_and_color_rules() {
        let o = Options::default();
        assert!(interactive_from(o, false, true, true));
        assert!(!interactive_from(o, true, true, true));
        assert!(!interactive_from(o, false, false, true));
        assert!(!interactive_from(
            Options {
                non_interactive: true,
                ..o
            },
            false,
            true,
            true
        ));
        assert!(color_from(o, false, true));
        assert!(!color_from(o, true, true));
        assert!(!color_from(
            Options {
                no_color: true,
                ..o
            },
            false,
            true
        ));
        let (p, _, _) = printer(o);
        assert!(!p.interactive() && !p.color());
    }
}
