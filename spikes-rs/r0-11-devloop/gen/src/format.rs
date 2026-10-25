//! The three formatting plans of R0.10.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use anyhow::{Context, Result, bail};

/// How the generator formats a rendered Rust file.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Plan {
    /// (A) The templates are written rustfmt-stable. Render does no
    /// formatting.
    Stable,
    /// (B) Parse the output with `syn` and print it with `prettyplease`.
    PrettyPlease,
    /// (C) Pipe the output through `rustfmt --edition 2024` in the app
    /// directory, so the app `rustfmt.toml` applies.
    Rustfmt,
}

impl Plan {
    /// All plans, in the order of the result file.
    pub const ALL: [Plan; 3] = [Plan::Stable, Plan::PrettyPlease, Plan::Rustfmt];

    /// The short name of the plan for flags and tables.
    pub fn name(self) -> &'static str {
        match self {
            Plan::Stable => "stable",
            Plan::PrettyPlease => "prettyplease",
            Plan::Rustfmt => "rustfmt",
        }
    }

    /// Parses a plan name.
    pub fn parse(s: &str) -> Result<Self> {
        Self::ALL
            .into_iter()
            .find(|p| p.name() == s)
            .with_context(|| {
                format!("unknown format plan {s:?}\nTry: --format stable, prettyplease, or rustfmt")
            })
    }
}

/// Formats one rendered Rust file with the plan. `dir` is the directory that
/// rustfmt runs in (the app root).
pub fn format(plan: Plan, dir: &Path, path: &str, source: &str) -> Result<String> {
    match plan {
        Plan::Stable => Ok(source.to_owned()),
        Plan::PrettyPlease => {
            let file = syn::parse_file(source).with_context(|| format!("parse {path}"))?;
            Ok(prettyplease::unparse(&file))
        }
        Plan::Rustfmt => rustfmt(dir, source).with_context(|| format!("rustfmt {path}")),
    }
}

/// Formats every planned Rust file with the plan. Plan C starts one
/// `rustfmt` process for all files ([`rustfmt_batch`]).
pub fn format_files(plan: Plan, dir: &Path, files: &mut [crate::File]) -> Result<()> {
    let rust: Vec<&mut crate::File> = files
        .iter_mut()
        .filter(|f| f.path.ends_with(".rs"))
        .collect();
    if plan == Plan::Rustfmt {
        let sources: Vec<(&str, &str)> = rust
            .iter()
            .map(|f| (f.path.as_str(), f.contents.as_str()))
            .collect();
        let out = rustfmt_batch(dir, &sources)?;
        for (f, text) in rust.into_iter().zip(out) {
            f.contents = text;
        }
        return Ok(());
    }
    for f in rust {
        f.contents = format(plan, dir, &f.path, &f.contents)?;
    }
    Ok(())
}

/// The rustfmt configuration that `cargo fmt` would use for files under
/// `dir`: the first `rustfmt.toml` or `.rustfmt.toml` in `dir` or above.
fn config_file(dir: &Path) -> Option<PathBuf> {
    let dir = dir.canonicalize().ok()?;
    dir.ancestors().find_map(|d| {
        ["rustfmt.toml", ".rustfmt.toml"]
            .into_iter()
            .map(|n| d.join(n))
            .find(|p| p.is_file())
    })
}

/// Formats several files with one `rustfmt --edition 2024` process. It
/// writes them at their relative paths to a staging directory, formats them
/// in place, and reads them back. The app is not touched. The app
/// `rustfmt.toml` applies through `--config-path`.
///
/// rustfmt follows `mod x;` declarations and fails on a missing child file
/// (`skip_children` is unstable). So each declared child that the stage
/// lacks gets an empty stub file.
pub fn rustfmt_batch(dir: &Path, files: &[(&str, &str)]) -> Result<Vec<String>> {
    if files.is_empty() {
        return Ok(Vec::new());
    }
    let stage = TempDir::new("rustfmt")?;
    let paths: Vec<PathBuf> = files.iter().map(|(p, _)| stage.path().join(p)).collect();
    for (p, (_, source)) in paths.iter().zip(files) {
        std::fs::create_dir_all(p.parent().expect("parent"))?;
        std::fs::write(p, source)?;
    }
    for (p, (_, source)) in paths.iter().zip(files) {
        for child in declared_children(p, source) {
            if !child.exists() {
                std::fs::create_dir_all(child.parent().expect("parent"))?;
                std::fs::write(&child, "")?;
            }
        }
    }
    let mut cmd = Command::new("rustfmt");
    cmd.args(["--edition", "2024"]);
    if let Some(config) = config_file(dir) {
        cmd.arg("--config-path").arg(config);
    }
    let out = cmd
        .args(&paths)
        .stdin(Stdio::null())
        .output()
        .context("start rustfmt\nTry: rustup component add rustfmt")?;
    if !out.status.success() {
        bail!(
            "rustfmt failed: {}\nTry: fix the template, then run the generator again",
            String::from_utf8_lossy(&out.stderr).trim()
        );
    }
    paths
        .iter()
        .map(|p| Ok(std::fs::read_to_string(p)?))
        .collect()
}

/// The files of the `mod x;` declarations in a file at `path`.
fn declared_children(path: &Path, source: &str) -> Vec<PathBuf> {
    let parent = path.parent().expect("parent");
    let stem = path.file_stem().and_then(|s| s.to_str()).unwrap_or("");
    let base = if ["mod", "lib", "main"].contains(&stem) {
        parent.to_path_buf()
    } else {
        parent.join(stem)
    };
    source
        .lines()
        .filter_map(|l| {
            let l = l.trim();
            let l = l.strip_prefix("pub ").unwrap_or(l);
            let name = l.strip_prefix("mod ")?.strip_suffix(';')?;
            Some(base.join(format!("{name}.rs")))
        })
        .collect()
}

/// Runs `rustfmt --edition 2024` on the source through stdin and stdout.
/// `cargo fmt` passes the same edition from `Cargo.toml`, and both read
/// `rustfmt.toml` from the directory up, so the bytes are equal.
pub fn rustfmt(dir: &Path, source: &str) -> Result<String> {
    let mut child = Command::new("rustfmt")
        .args(["--edition", "2024"])
        .current_dir(dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .context("start rustfmt\nTry: rustup component add rustfmt")?;
    let mut stdin = child.stdin.take().expect("piped stdin");
    let input = source.to_owned();
    let writer = std::thread::spawn(move || stdin.write_all(input.as_bytes()));
    let out = child.wait_with_output()?;
    writer.join().expect("writer thread")?;
    if !out.status.success() {
        bail!(
            "rustfmt failed: {}\nTry: fix the template, then run the generator again",
            String::from_utf8_lossy(&out.stderr).trim()
        );
    }
    Ok(String::from_utf8(out.stdout)?)
}

/// A temporary directory that is removed on drop (the staging directory of
/// plan C).
struct TempDir(std::path::PathBuf);

impl TempDir {
    fn new(label: &str) -> Result<Self> {
        static N: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let n = N.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let dir = std::env::temp_dir().join(format!("r0-11-{label}-{}-{n}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir)?;
        Ok(Self(dir))
    }

    fn path(&self) -> &std::path::Path {
        &self.0
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}
