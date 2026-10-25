//! Process exit codes (Go `internal/ui` `ExitCode`).

use crate::error::find_ui_error;

/// Success.
pub const OK: i32 = 0;
/// A general failure.
pub const ERROR: i32 = 1;
/// A bad command, flag, or argument count.
pub const USAGE: i32 = 2;
/// An operation stopped with merge conflicts.
pub const CONFLICTS: i32 = 3;
/// The project differs from the expected state.
pub const DRIFT: i32 = 4;
/// Another spinup process holds the lock.
pub const LOCKED: i32 = 5;

/// Map an error to a process exit code.
///
/// A [`crate::error::Error`] in the chain gives its code, or [`ERROR`] when the code is zero.
/// Any other error gives [`ERROR`].
#[must_use]
pub fn exit_code(err: &anyhow::Error) -> i32 {
    match find_ui_error(err) {
        Some(ue) if ue.code != 0 => ue.code,
        _ => ERROR,
    }
}

/// Map a result to a process exit code. `Ok` gives [`OK`].
#[must_use]
pub fn exit_code_of<T>(result: &Result<T, anyhow::Error>) -> i32 {
    match result {
        Ok(_) => OK,
        Err(err) => exit_code(err),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::Error;
    use anyhow::Context;

    #[test]
    fn codes_match_go() {
        let plain = anyhow::anyhow!("x");
        assert_eq!(exit_code(&plain), ERROR);
        assert_eq!(exit_code(&Error::new("x").into()), ERROR);
        for code in [USAGE, CONFLICTS, DRIFT, LOCKED] {
            assert_eq!(exit_code(&Error::new("x").code(code).into()), code);
        }
        let wrapped: anyhow::Error = Err::<(), _>(Error::new("x").code(DRIFT))
            .context("w")
            .unwrap_err();
        assert_eq!(exit_code(&wrapped), DRIFT);
        assert_eq!(exit_code_of(&Ok::<(), anyhow::Error>(())), OK);
        assert_eq!(exit_code_of(&Err::<(), _>(plain)), ERROR);
    }
}
