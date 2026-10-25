//! Typed confirmation for protected environments.

use std::io::{BufRead, BufReader, Read, Write};

use crate::config::{Env, Error};

/// The flags, terminal status, and streams for a protected operation.
/// The caller detects terminal status before it sets `interactive`.
#[derive(Default)]
pub struct Confirm<'a> {
    /// The input stream for a typed confirmation.
    pub input: Option<&'a mut dyn Read>,
    /// The output stream for the prompt.
    pub output: Option<&'a mut dyn Write>,
    /// Whether the caller has an interactive terminal.
    pub interactive: bool,
    /// The value of `--yes`.
    pub yes: bool,
    /// Whether CI is active.
    pub ci: bool,
    /// The value of `--confirm-env`.
    pub name: &'a str,
}

/// Confirm a protected operation. Unprotected environments require no confirmation.
/// An explicit name takes priority over approval through CI.
pub fn confirm_protected(env: &Env, action: &str, confirm: Confirm<'_>) -> Result<(), Error> {
    if !env.protected {
        return Ok(());
    }
    let hint = format!("pass --confirm-env {}", env.name);
    if !confirm.name.is_empty() {
        if confirm.name != env.name {
            return Err(Error::new(
                format!(
                    "cannot {action} in protected environment {:?}: --confirm-env has the value {:?}.",
                    env.name, confirm.name
                ),
                hint,
            ));
        }
        return Ok(());
    }
    if confirm.yes && confirm.ci {
        return Ok(());
    }
    if !confirm.interactive {
        return Err(Error::new(
            format!("cannot {action} in protected environment {:?}.", env.name),
            format!("{hint}, re-run in a terminal, or pass --yes in CI"),
        ));
    }
    let input = confirm
        .input
        .ok_or_else(|| Error::new("read confirmation: input is nil", &hint))?;
    let output = confirm
        .output
        .ok_or_else(|| Error::new("write confirmation: output is nil", &hint))?;
    write!(output, "{action} targets the protected environment {:?} ({}). Type the environment name to continue:", env.name, env.host)
        .and_then(|()| output.flush())
        .map_err(|e| Error::new(format!("write confirmation: {e}"), &hint))?;
    let mut answer = String::new();
    BufReader::new(input)
        .read_line(&mut answer)
        .map_err(|e| Error::new(format!("read confirmation: {e}"), &hint))?;
    let answer = answer.strip_suffix('\n').unwrap_or(&answer);
    let answer = answer.strip_suffix('\r').unwrap_or(answer);
    if answer != env.name {
        return Err(Error::new("confirmation did not match", hint));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn flags_and_ci_confirmation() {
        for protected in [false, true] {
            let env = Env {
                name: "prod".into(),
                protected,
                ..Env::default()
            };
            for name in ["", "prod", "dev"] {
                for yes in [false, true] {
                    for ci in [false, true] {
                        let mut output = Vec::new();
                        let result = confirm_protected(
                            &env,
                            "deploy",
                            Confirm {
                                name,
                                yes,
                                ci,
                                output: Some(&mut output),
                                ..Confirm::default()
                            },
                        );
                        let want = !protected || name == "prod" || (name.is_empty() && yes && ci);
                        assert_eq!(result.is_ok(), want, "{protected} {name} {yes} {ci}");
                        if let Err(err) = result {
                            assert!(err.to_string().contains("Try: pass --confirm-env prod"));
                        }
                        assert!(output.is_empty());
                    }
                }
            }
        }
    }

    #[test]
    fn typed_confirmation_preserves_whitespace_and_accepts_eof_or_crlf() {
        let env = Env {
            name: "prod".into(),
            host: "example.test".into(),
            protected: true,
            ..Env::default()
        };
        for answer in [
            "prod\n", "prod\r\n", "prod", "dev\n", " prod\n", "prod \n", "",
        ] {
            let mut input = answer.as_bytes();
            let mut output = Vec::new();
            let result = confirm_protected(
                &env,
                "deploy",
                Confirm {
                    interactive: true,
                    input: Some(&mut input),
                    output: Some(&mut output),
                    ..Confirm::default()
                },
            );
            assert_eq!(
                result.is_ok(),
                matches!(answer, "prod\n" | "prod\r\n" | "prod")
            );
            assert_eq!(
                String::from_utf8(output).unwrap(),
                "deploy targets the protected environment \"prod\" (example.test). Type the environment name to continue:"
            );
        }
    }

    #[test]
    fn missing_streams_fail_with_hints() {
        let env = Env {
            name: "prod".into(),
            protected: true,
            ..Env::default()
        };
        let err = confirm_protected(
            &env,
            "deploy",
            Confirm {
                interactive: true,
                ..Confirm::default()
            },
        )
        .unwrap_err();
        assert!(
            err.to_string()
                .contains("read confirmation: input is nil\nTry:")
        );
        let mut input = "prod".as_bytes();
        let err = confirm_protected(
            &env,
            "deploy",
            Confirm {
                interactive: true,
                input: Some(&mut input),
                ..Confirm::default()
            },
        )
        .unwrap_err();
        assert!(
            err.to_string()
                .contains("write confirmation: output is nil\nTry:")
        );
    }
}
