//! Helpers that the tests and the `compare` binary share: temporary
//! directories, the base app, the reference resources, and the rustfmt
//! check.

use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU32, Ordering};

use anyhow::Result;

use crate::{File, Plan, Report, apply, format, plan_args};

/// The spike workspace (`spikes-rs/r0-10-generator`).
pub fn workspace() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("workspace")
        .to_path_buf()
}

/// The two reference resources of R0.10, in generation order.
pub const REFERENCE: &[&[&str]] = &[
    &[
        "todo",
        "title:string",
        "done:bool",
        "due:date?",
        "priority:enum:low|normal|high",
    ],
    &["note", "title:string", "body:text?"],
];

/// A directory under the system temporary directory, removed on drop.
pub struct TempDir(PathBuf);

impl TempDir {
    /// Creates a new empty directory with a unique name.
    pub fn new(label: &str) -> Result<Self> {
        static N: AtomicU32 = AtomicU32::new(0);
        let n = N.fetch_add(1, Ordering::Relaxed);
        let dir = std::env::temp_dir().join(format!("r0-10-{label}-{}-{n}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir)?;
        Ok(Self(dir))
    }

    /// The path of the directory.
    pub fn path(&self) -> &Path {
        &self.0
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

/// Copies the app files that the app template writes (not the generator)
/// from the committed `todo-api` into `dst`.
pub fn copy_base(dst: &Path) -> Result<()> {
    let src = workspace().join("todo-api");
    fs::create_dir_all(dst.join("src"))?;
    fs::create_dir_all(dst.join("migrations"))?;
    for rel in ["Cargo.toml", "src/lib.rs", "src/main.rs"] {
        fs::copy(src.join(rel), dst.join(rel))?;
    }
    Ok(())
}

/// Plans and applies each resource in order. It returns the last plan and
/// one report per resource.
pub fn generate(app: &Path, resources: &[&[&str]], plan: Plan) -> Result<Vec<Report>> {
    let mut reports = Vec::new();
    for args in resources {
        let files = plan_one(app, args, plan)?;
        reports.push(apply(app, &files, false)?);
    }
    Ok(reports)
}

/// Plans one resource from its arguments.
pub fn plan_one(app: &Path, args: &[&str], plan: Plan) -> Result<Vec<File>> {
    let args: Vec<String> = args.iter().map(|s| (*s).to_owned()).collect();
    plan_args(app, &args, plan)
}

/// Returns the planned Rust files whose bytes rustfmt would change.
pub fn not_rustfmt_clean(app: &Path, files: &[File]) -> Result<Vec<String>> {
    let mut out = Vec::new();
    for f in files.iter().filter(|f| f.path.ends_with(".rs")) {
        if format::rustfmt(app, &f.contents)? != f.contents {
            out.push(f.path.clone());
        }
    }
    Ok(out)
}
