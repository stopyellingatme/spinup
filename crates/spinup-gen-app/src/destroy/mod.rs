//! Safe removal of unchanged generator files, with module and navigation updates.

#[cfg(test)]
mod tests;

use std::{
    io::Write,
    path::{Path, PathBuf},
    sync::Mutex,
};

use anyhow::{Context, Result, bail};
use spinup_exec::runner::{Cancellation, Runner};
use spinup_gen::{contract::PostStep, fields::Name, run::StepInput};
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_vfs::plan::{File, Plan as FilePlan};

/// Selects the generated item and the output destination.
#[derive(Default)]
pub struct Options<'a> {
    /// The project root.
    pub root: PathBuf,
    /// The generator kind: resource, model, or handler.
    pub kind: String,
    /// The generated item name.
    pub name: String,
    /// Preview changes without writing or running tools.
    pub dry_run: bool,
    /// Receives file actions and migration suggestions. None discards output.
    pub out: Option<&'a Mutex<dyn Write + Send>>,
}

/// Safe file changes, retained paths, and empty directories.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Plan {
    /// Safe deletions and the rebuilt shared files.
    pub files: FilePlan,
    /// Changed, untracked, or linked files that must remain.
    pub kept: Vec<String>,
    /// The suggested drop migration. Existing migrations always remain.
    pub next: String,
    /// Directories left empty by the plan, deepest first.
    pub dirs: Vec<String>,
}

/// Compares generated paths with the lock without changing the project.
/// Rejects unsupported kinds and unsafe paths with a retry hint.
pub fn build(options: &Options<'_>, lock: &Lock) -> Result<Plan> {
    let mut result = Plan::default();
    let paths = match options.kind.as_str() {
        "resource" | "model" => {
            let name = Name::parse(&options.name)?;
            result.next = format!("spinup db new drop_{}", name.plural_snake());
            if options.kind == "model" {
                vec![crate::resource_core::paths::model_path(&name)]
            } else {
                crate::resource_core::paths(&name)?
            }
        }
        "handler" => {
            let name = crate::handler::name(&options.name)?;
            vec![
                format!("api/src/handlers/{}.rs", name.snake),
                format!("api/src/handlers/{}_tests.rs", name.snake),
            ]
        }
        kind => bail!(
            "cannot destroy {kind:?}: unsupported kind\nTry: spinup destroy resource|model|handler <name>"
        ),
    };
    for path in paths {
        if spinup_vfs::path::clean_path(&path)? != path || path.starts_with("api/migrations/") {
            bail!("cannot destroy unsafe generator path {path:?}\nTry: check the generator paths");
        }
        match has_link(&options.root, &path) {
            Ok(true) => {
                result.kept.push(path);
                continue;
            }
            Ok(false) => {}
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => continue,
            Err(error) => {
                return Err(error)
                    .context("cannot inspect generated files\nTry: check the project permissions");
            }
        }
        let data = std::fs::read(options.root.join(&path))
            .with_context(|| format!("cannot read {path}\nTry: check the file permissions"))?;
        let Some(entry) = lock
            .files
            .get(&path)
            .filter(|e| e.hash == spinup_manifest::hash::hash(&data))
        else {
            result.kept.push(path);
            continue;
        };
        result.files.add(File {
            path,
            class: class_name(entry.class).into(),
            delete: true,
            ..File::default()
        })?;
    }
    let deleted = deleted_paths(&result.files);
    if !deleted.is_empty() {
        if options.kind == "resource" {
            result.files = spinup_gen_web::nav::plan_nav(&options.root, result.files, &deleted)?;
        }
        result.files = crate::resource_core::modules::plan_module_lists(
            &options.root,
            result.files,
            &deleted,
        )?;
        result.dirs = spinup_vfs::apply::emptied_dirs(&options.root, &deleted)?;
        // Shared files can keep a directory that the deletion-only scan considered empty.
        result.dirs.retain(|dir| {
            !result
                .files
                .files
                .iter()
                .any(|f| !f.delete && f.path.starts_with(&format!("{dir}/")))
        });
    }
    result.files = result.files.sorted();
    result.kept.sort();
    Ok(result)
}

/// Previews or applies safe deletions, saves the lock, and runs post-steps.
/// Rechecks the plan before apply. A failure never authorizes changed-file deletion.
pub fn run(options: &Options<'_>, runner: &dyn Runner, cancel: &Cancellation) -> Result<()> {
    let lock_path = options.root.join("spinup.lock");
    let mut lock = Lock::load(&lock_path)?;
    let mut plan = build(options, &lock)?;
    for path in &plan.kept {
        output(
            options,
            &format!("keep {path} (changed or untracked)\nTry: delete it by hand, or keep it\n"),
        )?;
    }
    for file in &plan.files.files {
        let action = if file.delete { "remove" } else { "update" };
        let prefix = if options.dry_run { "would " } else { "" };
        output(options, &format!("{prefix}{action} {}\n", file.path))?;
    }
    for dir in &plan.dirs {
        let prefix = if options.dry_run { "would " } else { "" };
        output(options, &format!("{prefix}remove {dir}/\n"))?;
    }
    if !options.dry_run && !plan.files.files.is_empty() {
        let checked = build(options, &lock)?;
        if checked != plan {
            bail!(
                "cannot destroy generated files: files changed during destruction\nTry: inspect the files, then repeat spinup destroy"
            );
        }
        let formatter =
            spinup_gen::rustfmt::format(&options.root, &mut plan.files, runner, cancel)?;
        // Formatting can run a tool. Recheck file hashes after that tool returns.
        if build(options, &lock)? != checked {
            bail!(
                "cannot destroy generated files: files changed during formatting\nTry: inspect the files, then repeat spinup destroy"
            );
        }
        spinup_vfs::apply::apply(&options.root, &plan.files, Default::default())?;
        spinup_vfs::apply::remove_dirs(&options.root, &plan.dirs)?;
        for file in &plan.files.files {
            if file.delete {
                lock.files.remove(&file.path);
            } else {
                lock.set_file(
                    &file.path,
                    FileEntry {
                        class: Class::Managed,
                        template: "gen/resource".into(),
                        hash: spinup_manifest::hash::hash(&file.data),
                        mode: format!(
                            "{:04o}",
                            if file.mode == 0 {
                                0o644
                            } else {
                                file.mode & 0o777
                            }
                        ),
                    },
                )?;
            }
        }
        if let Some(formatter) = formatter {
            lock.formatter = formatter;
        }
        lock.save(lock_path)?;
        post_steps(options, &lock, &plan.files.paths(), runner, cancel)?;
    }
    if !plan.next.is_empty() {
        output(options, &format!("Next: {}\n", plan.next))?;
    }
    Ok(())
}

fn post_steps(
    options: &Options<'_>,
    lock: &Lock,
    files: &[String],
    runner: &dyn Runner,
    cancel: &Cancellation,
) -> Result<()> {
    let mut steps = vec![
        PostStep::SqlxPrepare,
        PostStep::Build,
        PostStep::OpenApiEmit,
    ];
    if lock.packs.iter().any(|p| p == "web") {
        steps.push(PostStep::Client);
    }
    if spinup_project::env::database_url_from_process(&options.root)?.is_none() {
        output(
            options,
            &format!(
                "Skipped post-steps: DATABASE_URL is not set ({})\nNext: spinup db prepare\nNext: spinup g openapi\n",
                steps
                    .iter()
                    .map(|s| spinup_gen::run::step_name(*s))
                    .collect::<Vec<_>>()
                    .join(", ")
            ),
        )?;
        return Ok(());
    }
    spinup_gen::poststeps::run_steps(
        &steps,
        &StepInput {
            root: &options.root,
            files,
            runner,
            cancel,
        },
    )
}

fn deleted_paths(plan: &FilePlan) -> Vec<String> {
    plan.files
        .iter()
        .filter(|f| f.delete)
        .map(|f| f.path.clone())
        .collect()
}

fn has_link(root: &Path, path: &str) -> std::io::Result<bool> {
    let mut current = root.to_path_buf();
    for part in path.split('/') {
        current.push(part);
        if std::fs::symlink_metadata(&current)?
            .file_type()
            .is_symlink()
        {
            return Ok(true);
        }
    }
    Ok(false)
}

fn class_name(class: Class) -> &'static str {
    match class {
        Class::Managed => "managed",
        Class::Scaffold => "scaffold",
        Class::Seed => "seed",
    }
}

fn output(options: &Options<'_>, text: &str) -> Result<()> {
    if let Some(out) = options.out {
        out.lock().map_err(|_| anyhow::anyhow!("cannot write destruction output: output lock is poisoned\nTry: check the output destination"))?
            .write_all(text.as_bytes()).context("cannot write destruction output\nTry: check the output destination")?;
    }
    Ok(())
}
