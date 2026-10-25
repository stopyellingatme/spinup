//! Executable lookup with the command directory and environment.

use crate::redact::redact_string;
use crate::runner::{Cmd, ExecError};
use std::ffi::OsString;
use std::path::{Path, PathBuf};

/// Finds a program in the parent environment.
pub fn look_path(program: &str) -> Result<PathBuf, ExecError> {
    resolve(&Cmd::new(program))
}

/// Finds a program with the directory and `PATH` specified by `cmd`.
pub fn resolve(cmd: &Cmd) -> Result<PathBuf, ExecError> {
    let missing = || ExecError::NotFound {
        name: redact_string(&cmd.program, &cmd.redact),
    };
    let directory = std::env::current_dir()
        .map_err(|source| ExecError::Io {
            cmd: cmd.to_string(),
            source,
        })?
        .join(cmd.dir.as_deref().unwrap_or_else(|| Path::new("")));
    let absolute = |path: &Path| {
        if path.is_absolute() {
            path.to_path_buf()
        } else {
            directory.join(path)
        }
    };
    if cmd.program.contains('/') || (cfg!(windows) && cmd.program.contains('\\')) {
        return executable(&absolute(Path::new(&cmd.program)), cmd).ok_or_else(missing);
    }
    let path = environment(cmd, "PATH").unwrap_or_default();
    if path.is_empty() {
        return Err(missing());
    }
    let parent_lookup = cmd.dir.is_none()
        && !cmd.replace_env
        && !cmd.env.iter().any(|(key, _)| {
            if cfg!(windows) {
                key.eq_ignore_ascii_case("PATH")
            } else {
                key == "PATH"
            }
        });
    for entry in std::env::split_paths(&path) {
        // Go LookPath skips empty entries and rejects implicit relative executables.
        // An explicit command PATH or directory permits relative entries.
        if parent_lookup && entry.as_os_str().is_empty() && cfg!(unix) {
            continue;
        }
        if let Some(path) = executable(&absolute(&entry.join(&cmd.program)), cmd) {
            if parent_lookup && !entry.is_absolute() {
                return Err(missing());
            }
            return Ok(path);
        }
    }
    Err(missing())
}

pub(crate) fn environment(cmd: &Cmd, name: &str) -> Option<OsString> {
    cmd.env
        .iter()
        .rev()
        .find(|(key, _)| {
            if cfg!(windows) {
                key.eq_ignore_ascii_case(name)
            } else {
                key == name
            }
        })
        .map(|(_, value)| OsString::from(value))
        .or_else(|| {
            if cmd.replace_env {
                None
            } else {
                std::env::var_os(name)
            }
        })
}

#[cfg(unix)]
fn executable(path: &Path, _: &Cmd) -> Option<PathBuf> {
    use std::os::unix::fs::PermissionsExt;
    let metadata = path.metadata().ok()?;
    (metadata.is_file() && metadata.permissions().mode() & 0o111 != 0).then(|| path.to_path_buf())
}

#[cfg(windows)]
fn executable(path: &Path, cmd: &Cmd) -> Option<PathBuf> {
    let extensions = environment(cmd, "PATHEXT").unwrap_or_else(|| ".COM;.EXE;.BAT;.CMD".into());
    let extensions: Vec<_> = extensions
        .to_string_lossy()
        .split(';')
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
        .collect();
    if path.extension().is_some_and(|extension| {
        extensions.iter().any(|value| {
            value
                .trim_start_matches('.')
                .eq_ignore_ascii_case(&extension.to_string_lossy())
        })
    }) && path.is_file()
    {
        return Some(path.to_path_buf());
    }
    extensions
        .iter()
        .map(|extension| {
            let mut value = path.as_os_str().to_os_string();
            value.push(extension);
            PathBuf::from(value)
        })
        .find(|candidate| candidate.is_file())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn replacement_environment_controls_lookup() {
        let binary = std::env::current_exe().unwrap();
        let mut cmd = Cmd::new(binary.file_name().unwrap().to_str().unwrap());
        cmd.replace_env = true;
        assert!(matches!(resolve(&cmd), Err(ExecError::NotFound { .. })));
        cmd = cmd
            .env("PATH", "")
            .env("PATH", binary.parent().unwrap().to_str().unwrap());
        assert_eq!(resolve(&cmd).unwrap(), binary);
        cmd = cmd.env("PATH", "");
        assert!(resolve(&cmd).is_err());
    }

    #[test]
    fn missing_program_has_guidance_and_no_secret() {
        let mut cmd = Cmd::new("spinup-missing-secret-program");
        cmd.redact = vec!["secret".into()];
        let error = resolve(&cmd).unwrap_err().to_string();
        assert!(error.contains("Try: install"));
        assert!(!error.contains("secret"));
    }

    #[cfg(unix)]
    #[test]
    fn relative_executable_and_path_use_command_directory() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let bin = dir.path().join("bin");
        std::fs::create_dir(&bin).unwrap();
        let path = bin.join("tool");
        std::fs::write(&path, "#!/bin/sh\nexit 0\n").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o700)).unwrap();
        assert_eq!(
            resolve(&Cmd::new("./bin/tool").dir(dir.path())).unwrap(),
            dir.path().join("./bin/tool")
        );
        assert_eq!(
            resolve(&Cmd::new("tool").dir(dir.path()).env("PATH", "bin")).unwrap(),
            path
        );
        assert!(resolve(&Cmd::new("tool").dir(dir.path()).env("PATH", "")).is_err());
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600)).unwrap();
        assert!(resolve(&Cmd::new("./bin/tool").dir(dir.path())).is_err());
        assert!(resolve(&Cmd::new("./bin").dir(dir.path())).is_err());
    }
}
