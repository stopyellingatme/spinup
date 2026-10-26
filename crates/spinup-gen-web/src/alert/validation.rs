//! Validate duration bounds and staged alert rules.
use anyhow::{Context, Result, bail};
use spinup_exec::runner::Cmd;
use spinup_gen::contract::Input;
use std::{
    fs::OpenOptions,
    io::Write,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::Duration,
};

pub(super) fn valid_duration(value: &str) -> bool {
    if value == "0" {
        return true;
    }
    if value.is_empty() {
        return false;
    }
    let units = [
        ("y", 365_u64 * 24 * 60 * 60 * 1_000_000_000),
        ("w", 7 * 24 * 60 * 60 * 1_000_000_000),
        ("d", 24 * 60 * 60 * 1_000_000_000),
        ("h", 60 * 60 * 1_000_000_000),
        ("m", 60 * 1_000_000_000),
        ("s", 1_000_000_000),
        ("ms", 1_000_000),
    ];
    let mut rest = value;
    let mut total = 0_u64;
    for (unit, scale) in units {
        let n = rest.bytes().take_while(u8::is_ascii_digit).count();
        if n == 0 {
            continue;
        }
        let suffix = &rest[n..];
        if !suffix.starts_with(unit) || (unit == "m" && suffix.starts_with("ms")) {
            continue;
        }
        let Ok(number) = rest[..n].parse::<u64>() else {
            return false;
        };
        let Some(sum) = number
            .checked_mul(scale)
            .and_then(|n| total.checked_add(n))
            .filter(|n| *n <= i64::MAX as u64)
        else {
            return false;
        };
        total = sum;
        rest = &suffix[unit.len()..];
    }
    rest.is_empty()
}

struct Stage(PathBuf);
impl Drop for Stage {
    #[allow(
        clippy::disallowed_methods,
        reason = "Remove the temporary validation copy, never a project file."
    )]
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}
impl Stage {
    #[allow(
        clippy::disallowed_methods,
        reason = "Stage a private validation copy outside the project, as required by the alert planner."
    )]
    fn create(data: &[u8]) -> Result<Self> {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        for _ in 0..100 {
            let path = std::env::temp_dir().join(format!(
                "spinup-alert-{}-{}.yml",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            let mut options = OpenOptions::new();
            options.write(true).create_new(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                options.mode(0o600);
            }
            match options.open(&path) {
                Ok(mut file) => {
                    let stage = Self(path);
                    file.write_all(data).context("write temporary alert rules failed\nTry: check the temporary directory permissions")?;
                    return Ok(stage);
                }
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => continue,
                Err(e) => {
                    return Err(anyhow::anyhow!(
                        "create temporary alert rules: {e}\nTry: check the temporary directory permissions"
                    ));
                }
            }
        }
        bail!(
            "create temporary alert rules failed: no unused temporary path\nTry: check the temporary directory permissions"
        )
    }
}
pub(super) fn validate(input: &Input<'_>, tool: &Path, data: &[u8]) -> Result<()> {
    let stage = Stage::create(data)?;
    let mut cmd =
        Cmd::new(tool.to_string_lossy()).args(["check", "rules", &stage.0.to_string_lossy()]);
    cmd.timeout = Some(Duration::from_secs(30));
    let result = input.runner.output(&cmd, input.cancel).map_err(|e| {
        let text = e.output().map(|o| format!("{}{}", String::from_utf8_lossy(&o.stdout), String::from_utf8_lossy(&o.stderr))).unwrap_or_default();
        anyhow::anyhow!("validate monitoring/alerts/custom.yml before writing: {e}\n{text}\nTry: correct --expr, --for, or the existing alert rules")
    })?;
    if result.exit_code != 0 {
        bail!(
            "validate monitoring/alerts/custom.yml before writing: promtool exited with code {}\n{}{}\nTry: correct --expr, --for, or the existing alert rules",
            result.exit_code,
            String::from_utf8_lossy(&result.stdout),
            String::from_utf8_lossy(&result.stderr)
        );
    }
    Ok(())
}
