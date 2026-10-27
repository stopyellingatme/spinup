//! The generator runner: dry run, diff, force, and lock records (Go `internal/gen/run`).
//!
//! [`run`] loads `spinup.lock`, calls the generator, and validates its plan and commands.
//! Then it previews the plan (`--dry-run`, `--diff`), or it applies the plan through
//! `spinup-vfs`, records each written file in the lock, runs the external commands, and runs
//! the post-steps. The runner refuses to overwrite a file with different content unless
//! `--force` is set, the generator owns the file, or the generator marks it as an edit.

pub(crate) mod commands;
mod json;
pub(crate) mod steps;
#[cfg(test)]
mod tests;

use std::io::Write;
use std::path::Path;

use anyhow::anyhow;
use clap::ArgMatches;
use spinup_exec::runner::{Cancellation, Runner};
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_vfs::diff::ChangeKind;
use spinup_vfs::plan::{File, Plan};

use crate::contract::{Command, Generator, Input, PostStep};

pub use steps::{POST_STEPS, StepInput, StepRunner, step_name};

/// What the runner does and where its output goes.
pub struct Options<'a> {
    /// The project root (the directory that holds `spinup.lock`).
    pub root: &'a Path,
    /// List the formatted file actions without applying the plan or running post-steps.
    pub dry_run: bool,
    /// Print formatted diffs without applying the plan or running post-steps.
    pub diff: bool,
    /// Allow changes to existing files with different content.
    pub force: bool,
    /// Print the result as one JSON object.
    pub json: bool,
    /// The output of the runner, and the output of the external commands without `json`.
    pub out: &'a mut (dyn Write + Send),
    /// The output of the external commands with `json`. `None` is the standard error of the
    /// process (Go `os.Stderr`).
    pub command_out: Option<&'a mut (dyn Write + Send)>,
    /// Runs the external commands and the post-steps.
    pub runner: &'a dyn Runner,
    /// The cancel signal of the CLI.
    pub cancel: &'a Cancellation,
    /// The post-step runners. [`Options::new`] sets the [`POST_STEPS`] slice.
    pub post_steps: &'a [StepRunner],
    /// The resolved database URL. Tests can supply this value without process changes.
    pub database_url: Option<String>,
    // The constructor stays infallible for the CLI. The runner reports resolver errors.
    database_url_error: Option<anyhow::Error>,
}

impl<'a> Options<'a> {
    /// Returns options that apply the plan, with the registered post-step runners.
    pub fn new(
        root: &'a Path,
        out: &'a mut (dyn Write + Send),
        runner: &'a dyn Runner,
        cancel: &'a Cancellation,
    ) -> Self {
        let (database_url, database_url_error) =
            match spinup_project::env::database_url_from_process(root) {
                Ok(url) => (url, None),
                Err(error) => (None, Some(error)),
            };
        Self {
            database_url,
            database_url_error,
            root,
            dry_run: false,
            diff: false,
            force: false,
            json: false,
            out,
            command_out: None,
            runner,
            cancel,
            post_steps: POST_STEPS.static_slice(),
        }
    }
}

/// The action for one planned file.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Action {
    /// The file does not exist.
    Create,
    /// The file exists with different content.
    Update,
    /// The file exists with the same content. A permission difference alone is unchanged.
    Unchanged,
}

impl Action {
    /// The Go name of the action.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Create => "create",
            Self::Update => "update",
            Self::Unchanged => "unchanged",
        }
    }
}

impl std::fmt::Display for Action {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

/// One planned file and its action.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FileResult {
    /// The path, relative to the project root.
    pub path: String,
    /// The action.
    pub action: Action,
    /// The file class.
    pub class: Class,
}

/// The files, the ordered post-steps, the commands, and the suggested next commands.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RunResult {
    /// Every planned file, in path order.
    pub files: Vec<FileResult>,
    /// Each active post-step once, in run order. Previews retain every requested step.
    pub post_steps: Vec<PostStep>,
    /// The suggested commands of the generator.
    pub next: Vec<String>,
    /// The external commands. They run after the write and before the post-steps.
    pub commands: Vec<Command>,
}

/// Validates the plan of `generator` and previews or applies it in `options.root`.
///
/// `args` are the positional arguments after the generator name. `matches` are the parsed
/// arguments of the generator subcommand.
///
/// # Errors
/// Returns an error with a `Try:` line when the lock, the generator, the plan, a conflict,
/// the write, a command, a post-step, or the output fails. Files that the runner wrote stay
/// on disk when a later command or post-step fails, and the lock records them.
pub fn run(
    generator: &Generator,
    args: &[String],
    matches: &ArgMatches,
    options: Options<'_>,
) -> anyhow::Result<RunResult> {
    let Options {
        root,
        dry_run,
        diff,
        force,
        json,
        out,
        command_out,
        runner,
        cancel,
        post_steps,
        database_url,
        database_url_error,
    } = options;
    if let Some(error) = database_url_error {
        return Err(error);
    }
    let lock_path = root.join("spinup.lock");
    let mut lock = Lock::load(&lock_path)?;
    let output = (generator.run)(&Input {
        root,
        lock: &lock,
        args,
        matches,
        runner,
        cancel,
    })
    .map_err(|e| {
        with_hint(
            &format!("generator {:?} failed", generator.name),
            &e,
            &format!("spinup g {} --help", generator.name),
        )
    })?;
    let mut plan = validate_plan(&output.plan)?;
    let formatter = crate::rustfmt::format(root, &mut plan, runner, cancel)?;
    let commands = commands::validate(generator.name, &output.commands)?;
    let changes = spinup_vfs::diff::diff(root, &plan).map_err(|e| {
        with_hint(
            "compare generator files",
            &e,
            "check the project paths and permissions",
        )
    })?;
    let mut result = RunResult {
        files: Vec::with_capacity(changes.len()),
        post_steps: steps::ordered(&output.post_steps),
        next: output.next.clone(),
        commands,
    };
    let mut writes = Plan::new();
    let mut conflicts = Vec::new();
    for (file, change) in plan.files.iter().zip(&changes) {
        // Identical content is a no-op, also with a permission difference.
        let action = match change.kind {
            ChangeKind::Create => Action::Create,
            ChangeKind::Update => Action::Update,
            ChangeKind::Unchanged | ChangeKind::ModeOnly | ChangeKind::Remove => Action::Unchanged,
        };
        result.files.push(FileResult {
            path: file.path.clone(),
            action,
            class: parse_class(&file.class).unwrap_or(Class::Managed),
        });
        if action == Action::Unchanged {
            continue;
        }
        if action == Action::Update
            && !force
            && !generator_owned(root, file, &lock)
            && !output.edits.contains(&file.path)
        {
            conflicts.push(file.path.clone());
        }
        writes.files.push(file.clone());
    }
    if !conflicts.is_empty() {
        return Err(anyhow!(
            "generator {:?} would overwrite existing files with different content:\n  {}\nTry: pass --force, or remove the files",
            generator.name,
            conflicts.join("\n  ")
        ));
    }
    if dry_run || diff {
        if json {
            json::print(out, &result)?;
            return Ok(result);
        }
        for (file, change) in result.files.iter().zip(&changes) {
            let written = if diff {
                out.write_all(&change.unified_diff)
            } else {
                writeln!(
                    out,
                    "{} {} ({})",
                    file.action,
                    file.path,
                    class_name(file.class)
                )
            };
            written.map_err(|e| output_error("write generator preview", &e))?;
        }
        commands::preview(out, &result.commands)?;
        return Ok(result);
    }
    if !writes.files.is_empty() {
        spinup_vfs::apply::apply(root, &writes, spinup_vfs::apply::ApplyOptions::default())?;
        for file in &writes.files {
            let mode = match file.mode & 0o777 {
                0 => 0o644,
                mode => mode,
            };
            let template = match lock.files.get(&file.path) {
                Some(entry) if output.edits.contains(&file.path) => entry.template.clone(),
                _ => format!("gen/{}", generator.name),
            };
            let entry = FileEntry {
                class: parse_class(&file.class).unwrap_or(Class::Managed),
                template,
                hash: spinup_manifest::hash::hash(&file.data),
                mode: format!("{mode:04o}"),
            };
            lock.set_file(&file.path, entry)?;
        }
    }
    let formatter_changed = formatter
        .as_ref()
        .is_some_and(|value| *value != lock.formatter);
    if let Some(formatter) = formatter {
        lock.formatter = formatter;
    }
    if !writes.files.is_empty() || formatter_changed {
        lock.save(&lock_path)?;
    }
    let mut stderr = std::io::stderr();
    let command_sink: &mut (dyn Write + Send) = match (json, command_out) {
        (false, _) => &mut *out,
        (true, Some(command_out)) => command_out,
        (true, None) => &mut stderr,
    };
    commands::run_all(root, &result.commands, runner, cancel, command_sink)?;
    let skipped = if database_url.is_none() {
        let skip_client = result.post_steps.contains(&PostStep::OpenApiEmit);
        let skipped: Vec<_> = result
            .post_steps
            .iter()
            .copied()
            .filter(|step| {
                matches!(
                    step,
                    PostStep::Migrate
                        | PostStep::SqlxPrepare
                        | PostStep::Build
                        | PostStep::OpenApiEmit
                ) || (*step == PostStep::Client && skip_client)
            })
            .collect();
        result.post_steps.retain(|step| !skipped.contains(step));
        if !skipped.is_empty() {
            for next in ["spinup db prepare", "spinup g openapi"] {
                if !result.next.iter().any(|s| s == next) {
                    result.next.push(next.into());
                }
            }
        }
        skipped
    } else {
        Vec::new()
    };
    if !skipped.is_empty() {
        writeln!(
            command_sink,
            "Skipped post-steps: DATABASE_URL is not set ({})",
            skipped
                .iter()
                .map(|step| step_name(*step))
                .collect::<Vec<_>>()
                .join(", ")
        )
        .map_err(|e| output_error("write skipped post-steps", &e))?;
    }
    let paths = plan.paths();
    crate::poststeps::run_registered(
        &result.post_steps,
        &StepInput {
            root,
            files: &paths,
            runner,
            cancel,
        },
        post_steps,
    )?;
    if json {
        json::print(out, &result)?;
        return Ok(result);
    }
    let count = |action| result.files.iter().filter(|f| f.action == action).count();
    writeln!(
        out,
        "Created {}, updated {} files",
        count(Action::Create),
        count(Action::Update)
    )
    .map_err(|e| output_error("write generator summary", &e))?;
    for next in &result.next {
        writeln!(out, "Next: {next}")
            .map_err(|e| output_error("write generator next command", &e))?;
    }
    Ok(result)
}

/// Checks each planned file: a clean path that is not the lock, a valid class, and no
/// delete. Returns the plan sorted by path.
fn validate_plan(plan: &Plan) -> anyhow::Result<Plan> {
    let mut validated = Plan::new();
    for file in &plan.files {
        let clean = spinup_vfs::path::clean_path(&file.path)?;
        if clean != file.path
            || clean == "spinup.lock"
            || file.delete
            || parse_class(&file.class).is_none()
        {
            return Err(anyhow!(
                "invalid generator file {:?}: require a clean path, a valid class, and file content\nTry: repair the generator plan",
                file.path
            ));
        }
        validated.add(file.clone())?;
    }
    Ok(validated.sorted())
}

/// Reports whether a generator may update an existing file without `--force`: the plan
/// marks it managed, and either a generator wrote it last (lock template `gen/...`) or it is
/// a managed pack file that the user never edited (the disk hash equals the lock hash),
/// such as the routes file of a new app (ADR 0007 §1).
fn generator_owned(root: &Path, file: &File, lock: &Lock) -> bool {
    let Some(entry) = lock.files.get(&file.path) else {
        return false;
    };
    if parse_class(&file.class) != Some(Class::Managed) || entry.class != Class::Managed {
        return false;
    }
    if entry.template.starts_with("gen/") {
        return true;
    }
    let disk = file
        .path
        .split('/')
        .fold(root.to_path_buf(), |p, part| p.join(part));
    std::fs::read(disk).is_ok_and(|data| spinup_manifest::hash::hash(&data) == entry.hash)
}

/// Parses the class name of a plan file.
fn parse_class(name: &str) -> Option<Class> {
    match name {
        "managed" => Some(Class::Managed),
        "scaffold" => Some(Class::Scaffold),
        "seed" => Some(Class::Seed),
        _ => None,
    }
}

/// The lock name of a class.
fn class_name(class: Class) -> &'static str {
    match class {
        Class::Managed => "managed",
        Class::Scaffold => "scaffold",
        Class::Seed => "seed",
    }
}

/// Joins `what` and the error text with `: `. Adds the `Try:` hint when the text has none
/// (Go `withHint`).
fn with_hint(what: &str, err: &anyhow::Error, hint: &str) -> anyhow::Error {
    let text = format!("{err:#}");
    if text.contains("Try:") {
        anyhow!("{what}: {text}")
    } else {
        anyhow!("{what}: {text}\nTry: {hint}")
    }
}

/// The error of a failed write to the output.
fn output_error(what: &str, err: &std::io::Error) -> anyhow::Error {
    anyhow!("{what}: {err}\nTry: check the output destination")
}
