//! An ordered, thread-safe runner for deterministic tests.

use crate::os_runner::{exit_result, io_error};
use crate::redact::{redact_bytes, redact_error};
use crate::runner::{Cancellation, Cmd, ExecError, Output, Runner, Sinks};
use std::collections::VecDeque;
use std::io;
use std::sync::{Arc, Mutex};

/// A scripted runner. Clones share expectations and recorded calls.
#[derive(Default, Clone)]
pub struct Fake(Arc<Mutex<State>>);

#[derive(Default)]
struct State {
    expectations: VecDeque<Arc<Mutex<Response>>>,
    calls: Vec<Cmd>,
}

struct Response {
    program: String,
    prefix: Vec<String>,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
    code: i32,
    error: Option<ExecError>,
}

impl Fake {
    /// Creates an empty runner.
    pub fn new() -> Self {
        Self::default()
    }

    /// Appends an expected program and argument prefix.
    pub fn expect<I, S>(&self, program: impl Into<String>, prefix: I) -> Expectation
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        let response = Arc::new(Mutex::new(Response {
            program: program.into(),
            prefix: prefix.into_iter().map(Into::into).collect(),
            stdout: Vec::new(),
            stderr: Vec::new(),
            code: 0,
            error: None,
        }));
        self.0
            .lock()
            .unwrap()
            .expectations
            .push_back(response.clone());
        Expectation(response)
    }

    /// Returns independent copies of every invocation, including unexpected calls.
    pub fn calls(&self) -> Vec<Cmd> {
        self.0.lock().unwrap().calls.clone()
    }

    /// Reports unused expectations. An unexpected call does not consume an expectation.
    pub fn assert_done(&self) -> Result<(), ExecError> {
        let state = self.0.lock().unwrap();
        if let Some(next) = state.expectations.front() {
            let next = next.lock().unwrap();
            return Err(io_error(
                &Cmd::new(&next.program),
                io::Error::other(format!(
                    "fake has {} unused expectation(s); next command is {}",
                    state.expectations.len(),
                    next.program
                )),
            ));
        }
        Ok(())
    }
}

/// Configures one expected invocation.
pub struct Expectation(Arc<Mutex<Response>>);

impl Expectation {
    /// Supplies output bytes and an exit code. Clears a scripted error.
    pub fn returns(self, stdout: impl AsRef<[u8]>, stderr: impl AsRef<[u8]>, code: i32) -> Self {
        {
            let mut response = self.0.lock().unwrap();
            response.stdout = stdout.as_ref().to_vec();
            response.stderr = stderr.as_ref().to_vec();
            response.code = code;
            response.error = None;
        }
        self
    }

    /// Supplies a typed error instead of the exit-code result.
    pub fn fail(self, error: ExecError) -> Self {
        self.0.lock().unwrap().error = Some(error);
        self
    }
}

impl Runner for Fake {
    fn run(&self, cmd: &Cmd, _: &Cancellation, mut sinks: Sinks<'_>) -> Result<Output, ExecError> {
        let mut state = self.0.lock().unwrap();
        state.calls.push(cmd.clone());
        let unexpected = || io_error(cmd, io::Error::other(format!("unexpected command: {cmd}")));
        let Some(next) = state.expectations.front().cloned() else {
            return Err(unexpected());
        };
        let mut response = next.lock().unwrap();
        if response.program != cmd.program || !cmd.args.starts_with(&response.prefix) {
            return Err(unexpected());
        }
        state.expectations.pop_front();
        let output = Output {
            stdout: redact_bytes(&response.stdout, &cmd.redact),
            stderr: redact_bytes(&response.stderr, &cmd.redact),
            exit_code: response.code,
            ..Output::default()
        };
        for (sink, bytes) in [
            (&mut sinks.stdout, &output.stdout),
            (&mut sinks.stderr, &output.stderr),
        ] {
            if let Some(sink) = sink
                && !bytes.is_empty()
            {
                let _ = sink.write_all(bytes);
                let _ = sink.flush();
            }
        }
        if let Some(error) = response.error.take() {
            return Err(redact_error(error, &cmd.redact));
        }
        exit_result(cmd, output)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::runner::*;

    #[test]
    fn ordering_prefixes_errors_and_call_copies() {
        let fake = Fake::default();
        fake.expect("first", ["arg"]).returns("one", "", 0);
        fake.expect("second", [] as [&str; 0]).returns("", "bad", 5);
        assert!(
            fake.output(&Cmd::new("second"), &Cancellation::new())
                .is_err()
        );
        assert!(fake.assert_done().is_err());
        let first = fake
            .output(
                &Cmd::new("first").args(["arg", "extra"]),
                &Cancellation::new(),
            )
            .unwrap();
        assert_eq!(first.stdout, b"one");
        let error = fake
            .output(&Cmd::new("second"), &Cancellation::new())
            .unwrap_err();
        assert_eq!(error.output().unwrap().exit_code, 5);
        assert!(matches!(error, ExecError::Exit(_)));
        fake.assert_done().unwrap();
        let mut calls = fake.calls();
        assert_eq!(calls.len(), 3);
        calls[1].args.clear();
        assert_eq!(fake.calls()[1].args, ["arg", "extra"]);
        assert!(
            fake.output(&Cmd::new("third"), &Cancellation::new())
                .is_err()
        );
    }

    #[test]
    fn streams_and_captures_redacted_bytes() {
        let fake = Fake::default();
        fake.expect("tool", [] as [&str; 0])
            .returns("secret!", "secret?", 7);
        let mut cmd = Cmd::new("tool");
        cmd.redact = vec!["secret".into()];
        let mut stdout = Vec::new();
        let mut stderr = Vec::new();
        let error = fake
            .run(
                &cmd,
                &Cancellation::new(),
                Sinks {
                    stdout: Some(&mut stdout),
                    stderr: Some(&mut stderr),
                },
            )
            .unwrap_err();
        assert_eq!(stdout, b"***!");
        assert_eq!(stderr, b"***?");
        assert_eq!(error.output().unwrap().stdout, stdout);
        assert!(!error.to_string().contains("secret"));
    }

    #[test]
    fn scripted_errors_redact_their_source_and_output() {
        let fake = Fake::new();
        fake.expect("tool", [] as [&str; 0]).fail(ExecError::Io {
            cmd: "secret command".into(),
            source: io::Error::other("secret reason"),
        });
        let mut cmd = Cmd::new("tool");
        cmd.redact = vec!["secret".into()];
        let error = fake.output(&cmd, &Cancellation::new()).unwrap_err();
        assert!(!error.to_string().contains("secret"));
        fake.expect("tool", [] as [&str; 0])
            .fail(ExecError::Stopped {
                cmd: "secret command".into(),
                reason: StopReason::Cancelled,
                output: Box::new(Output {
                    stdout: b"secret".to_vec(),
                    ..Output::default()
                }),
            });
        let error = fake.output(&cmd, &Cancellation::new()).unwrap_err();
        assert!(!error.to_string().contains("secret"));
        assert_eq!(error.output().unwrap().stdout, b"***");
    }

    #[test]
    fn scripted_failure_is_typed() {
        let fake = Fake::default();
        fake.expect("tool", [] as [&str; 0])
            .fail(ExecError::NotFound {
                name: "missing".into(),
            });
        assert!(
            matches!(fake.output(&Cmd::new("tool"), &Cancellation::new()), Err(ExecError::NotFound { name }) if name == "missing")
        );
        fake.assert_done().unwrap();
    }
}
