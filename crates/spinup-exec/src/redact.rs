//! Ordered secret replacement for captured output, live output, and command text.

use crate::runner::{Cmd, ExecError, Output};
use std::fmt;

/// Replaces complete secret matches without changing other bytes.
pub fn redact_bytes(bytes: &[u8], secrets: &[String]) -> Vec<u8> {
    Redactor::new(secrets).push(bytes, true)
}

pub(crate) fn redact_string(value: &str, secrets: &[String]) -> String {
    String::from_utf8_lossy(&redact_bytes(value.as_bytes(), secrets)).into_owned()
}

pub(crate) fn redact_error(error: ExecError, secrets: &[String]) -> ExecError {
    let output = |output: &mut Output| {
        output.stdout = redact_bytes(&output.stdout, secrets);
        output.stderr = redact_bytes(&output.stderr, secrets);
    };
    match error {
        ExecError::NotFound { name } => ExecError::NotFound {
            name: redact_string(&name, secrets),
        },
        ExecError::Exit(mut error) => {
            error.cmd = redact_string(&error.cmd, secrets);
            error.stderr_tail = redact_string(&error.stderr_tail, secrets);
            output(&mut error.output);
            ExecError::Exit(error)
        }
        ExecError::Stopped {
            cmd,
            reason,
            output: mut bytes,
        } => {
            output(&mut bytes);
            ExecError::Stopped {
                cmd: redact_string(&cmd, secrets),
                reason,
                output: bytes,
            }
        }
        ExecError::Io { cmd, source } => {
            let message = source.to_string();
            let redacted = redact_string(&message, secrets);
            let source = if message == redacted {
                source
            } else {
                std::io::Error::new(source.kind(), redacted)
            };
            ExecError::Io {
                cmd: redact_string(&cmd, secrets),
                source,
            }
        }
    }
}

impl fmt::Display for Cmd {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        for (index, part) in std::iter::once(&self.program).chain(&self.args).enumerate() {
            if index > 0 {
                f.write_str(" ")?;
            }
            let part = redact_string(part, &self.redact);
            if !part.is_empty()
                && part
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || b"_@%+=:,./-".contains(&byte))
            {
                f.write_str(&part)?;
            } else {
                write!(f, "'{}'", part.replace('\'', "'\"'\"'"))?;
            }
        }
        Ok(())
    }
}

/// Applies ordered secret replacements across arbitrary byte chunks.
pub struct Redactor {
    stages: Vec<Stage>,
}

impl Redactor {
    /// Creates replacement stages, with the longest secrets first.
    pub fn new(secrets: &[String]) -> Self {
        let mut secrets = secrets.to_vec();
        secrets.sort_by_key(|s| std::cmp::Reverse(s.len()));
        Self {
            stages: secrets
                .into_iter()
                .filter(|s| !s.is_empty())
                .map(|s| Stage {
                    secret: s.into_bytes(),
                    pending: Vec::new(),
                })
                .collect(),
        }
    }

    /// Returns safe bytes. Set `final_chunk` at EOF to flush an unmatched suffix.
    pub fn push(&mut self, data: &[u8], final_chunk: bool) -> Vec<u8> {
        self.stages
            .iter_mut()
            .fold(data.to_vec(), |data, stage| stage.push(&data, final_chunk))
    }
}

struct Stage {
    secret: Vec<u8>,
    pending: Vec<u8>,
}

impl Stage {
    fn push(&mut self, data: &[u8], final_chunk: bool) -> Vec<u8> {
        self.pending.extend_from_slice(data);
        let mut matched = 0;
        while let Some(index) = self.pending[matched..]
            .windows(self.secret.len())
            .position(|s| s == self.secret)
        {
            matched += index + self.secret.len();
        }
        let tail = &self.pending[matched..];
        let retain = if final_chunk {
            0
        } else {
            (1..self.secret.len())
                .rev()
                .find(|&n| tail.ends_with(&self.secret[..n]))
                .unwrap_or(0)
        };
        let limit = self.pending.len() - retain;
        let mut output = Vec::new();
        let mut index = 0;
        while index < limit {
            if self.pending[index..limit].starts_with(&self.secret) {
                output.extend_from_slice(b"***");
                index += self.secret.len();
            } else {
                output.push(self.pending[index]);
                index += 1;
            }
        }
        self.pending.drain(..limit);
        output
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::runner::Cmd;

    #[test]
    fn every_split_matches_ordered_replacement() {
        let cases = [
            (vec!["ab", "bcd"], "xabz\nxabcd"),
            (vec!["abab"], "xababab"),
            (vec!["sec", "secret"], "secret sec secret"),
            (vec!["ab", "bc"], "abcabc"),
            (vec!["aaaa"], "xaaaaaaaayaaa"),
            (vec!["secret"], "secret sec"),
            (vec!["", "secret", "secret"], "secret secret"),
            (vec!["***", "secret"], "secret ***"),
            (vec!["密码", "密"], "密码 密 密码"),
            (vec![], "unchanged\n"),
            (vec!["secret"], ""),
        ];
        for (secrets, input) in cases {
            let secrets: Vec<String> = secrets.into_iter().map(String::from).collect();
            let expected = redact_bytes(input.as_bytes(), &secrets);
            for split in 0..=input.len() {
                let mut redactor = Redactor::new(&secrets);
                let mut output = redactor.push(&input.as_bytes()[..split], false);
                output.extend(redactor.push(&input.as_bytes()[split..], true));
                assert_eq!(output, expected, "{input:?}, split {split}");
            }
            let mut redactor = Redactor::new(&secrets);
            let mut output = Vec::new();
            for byte in input.as_bytes() {
                output.extend(redactor.push(&[*byte], false));
            }
            output.extend(redactor.push(&[], true));
            assert_eq!(output, expected);
        }
    }

    #[test]
    fn incomplete_secret_stays_buffered_until_eof() {
        let mut redactor = Redactor::new(&["secret".into()]);
        assert_eq!(redactor.push(b"before sec", false), b"before ");
        assert_eq!(redactor.push(b"ret after sec", false), b"*** after ");
        assert_eq!(redactor.push(&[], true), b"sec");
    }

    #[test]
    fn binary_bytes_survive_redaction() {
        assert_eq!(
            redact_bytes(b"\xffsecret\x00", &["secret".into()]),
            b"\xff***\x00"
        );
    }

    #[test]
    fn command_display_redacts_before_shell_quoting() {
        let mut cmd =
            Cmd::new("tool").args(["safe_@%+=:,./-", "a value", "it's", "", "密码", "secret"]);
        cmd.redact = vec!["secret".into()];
        assert_eq!(
            cmd.to_string(),
            "tool safe_@%+=:,./- 'a value' 'it'\"'\"'s' '' '密码' '***'"
        );
    }
}
