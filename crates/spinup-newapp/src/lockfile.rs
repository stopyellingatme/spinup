//! The first lock and application of a new app.

use crate::select::{Options, ResultPlan, check_target, creation_error};
use anyhow::{Result, bail};
use serde_json::{Map, Value};
use spinup_exec::runner::{Cancellation, Runner};
use spinup_manifest::lock::{Class, FileEntry, KitRef, Lock};
use spinup_stack::stack::Stack;
use spinup_ui::printer::Printer;
use spinup_vfs::plan::{File, Plan};
use std::path::{Path, PathBuf};

/// Records all render inputs and the hashes of the final planned bytes.
pub fn new_lock(
    cli_version: &str,
    version: &str,
    packs: &[String],
    answers: &Map<String, Value>,
    plan: &Plan,
    stack: &Stack,
) -> Result<Lock> {
    let mut lock = Lock {
        cli_version: if cli_version.is_empty() {
            "dev"
        } else {
            cli_version
        }
        .into(),
        template_version: version.into(),
        min_cli: version.into(),
        kit: KitRef {
            crate_name: crate::kit::KIT_MODULE.into(),
            version: "v0.1.0".into(),
        },
        behavior_version: 1,
        engine: "minijinja".into(),
        packs: packs.to_vec(),
        ..Lock::new()
    };
    for (key, value) in answers {
        lock.answers
            .insert(key.clone(), serde_json::value::to_raw_value(value)?);
    }
    for (key, image) in &stack.images {
        lock.stack_versions.insert(key.clone(), image.reference());
    }
    for file in &plan.files {
        let class = match file.class.as_str() {
            "managed" => Class::Managed,
            "scaffold" => Class::Scaffold,
            "seed" => Class::Seed,
            other => bail!(
                "cannot lock {:?}: invalid file class {other:?}\nTry: use managed, scaffold, or seed",
                file.path
            ),
        };
        lock.set_file(
            &file.path,
            FileEntry {
                class,
                template: file.template.clone(),
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
    lock.validate()?;
    Ok(lock)
}

/// Writes the plan, local environment, and lock. Git failures produce warnings.
pub fn apply(
    result: &ResultPlan,
    options: &Options,
    runner: &dyn Runner,
    cancel: &Cancellation,
    printer: &Printer,
) -> Result<()> {
    for warning in &result.warnings {
        printer.warn(warning);
    }
    if let Some(path) = &result.kit_replace {
        printer.info(&format!("Note: using local kit at {}", path.display()));
    }
    if options.dry_run {
        if !options.json {
            for file in &result.plan.sorted().files {
                printer.println(&format!("{} ({})", file.path, file.class));
            }
            printer.println("spinup.lock (CLI-owned)");
            printer.println(".env (local)");
        }
    } else {
        check_target(&result.dir)?;
        result.lock.validate()?;
        // VFS creates the app directory as a parent of the planned files.
        // Use the nearest existing ancestor, including when --dir has missing parents.
        let root = existing_ancestor(&result.dir)?;
        let relative = result.dir.strip_prefix(&root).map_err(creation_error)?;
        let mut plan = Plan::new();
        for mut file in result.plan.files.clone() {
            file.path = portable(&relative.join(&file.path))?;
            plan.add(file)?;
        }
        spinup_vfs::apply::apply(&root, &plan, Default::default()).map_err(creation_error)?;
        copy_environment(&result.dir)?;
        result.lock.save(result.dir.join("spinup.lock"))?;
        if !options.no_git {
            crate::git::initialize_git(&result.dir, &options.version, runner, cancel, printer);
        }
    }
    if options.json {
        printer.json(&serde_json::json!({"dir":result.dir,"packs":result.packs,"files":result.plan.files.len()})).map_err(|e| anyhow::anyhow!("cannot write creation result: {e}\nTry: check the output stream"))?;
    } else if !options.dry_run {
        printer.println(&format!(
            "Created {} ({} files) from packs {}",
            options.name,
            result.plan.files.len(),
            result.packs.join(", ")
        ));
        printer.print(&format!("\nNext:\n  cd {}\n  spinup dev\n", options.name));
    }
    Ok(())
}

fn existing_ancestor(path: &Path) -> Result<PathBuf> {
    for ancestor in path.ancestors() {
        match std::fs::metadata(ancestor) {
            Ok(meta) if meta.is_dir() => return Ok(ancestor.to_path_buf()),
            Ok(_) => bail!(
                "Cannot create the application: parent {ancestor:?} is not a directory\nTry: choose a directory for --dir"
            ),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(creation_error(e)),
        }
    }
    bail!(
        "Cannot create the application: no parent directory exists\nTry: choose an existing directory for --dir"
    )
}

fn portable(path: &Path) -> Result<String> {
    path.iter()
        .map(|part| {
            part.to_str()
                .ok_or_else(|| creation_error("target path is not UTF-8"))
        })
        .collect::<Result<Vec<_>>>()
        .map(|parts| parts.join("/"))
}

/// Copies `.env.example` into an untracked mode 0600 `.env` through VFS.
/// An existing `.env` remains unchanged.
pub fn copy_environment(root: &Path) -> Result<()> {
    match std::fs::symlink_metadata(root.join(".env")) {
        Ok(_) => return Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(creation_error(e)),
    }
    let data = std::fs::read(root.join(".env.example")).map_err(creation_error)?;
    let mut plan = Plan::new();
    plan.add(File {
        path: ".env".into(),
        data,
        mode: 0o600,
        ..File::default()
    })?;
    spinup_vfs::apply::apply(root, &plan, Default::default()).map_err(creation_error)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use spinup_exec::{fake::Fake, os_runner::OsRunner, runner::Cmd};
    use spinup_ui::printer::{Options as PrintOptions, SharedBuffer};

    fn options(parent: &Path) -> Options {
        Options {
            name: "todo".into(),
            dir: parent.into(),
            api_only: true,
            no_git: true,
            ..Options::default()
        }
    }

    fn printer() -> (Printer, SharedBuffer) {
        let out = SharedBuffer::new();
        (
            Printer::new(
                std::io::empty(),
                out.clone(),
                out.clone(),
                PrintOptions::default(),
            ),
            out,
        )
    }

    #[test]
    fn apply_creates_nested_app_environment_and_lock() {
        let parent = tempfile::tempdir().unwrap();
        let options = options(&parent.path().join("missing/parent"));
        let result = crate::select::generate(&options, None).unwrap();
        let (printer, out) = printer();
        apply(
            &result,
            &options,
            &Fake::new(),
            &Cancellation::new(),
            &printer,
        )
        .unwrap();
        let lock = Lock::load(result.dir.join("spinup.lock")).unwrap();
        assert_eq!(lock.marshal().unwrap(), result.lock.marshal().unwrap());
        for (path, entry) in &lock.files {
            let data = std::fs::read(result.dir.join(path)).unwrap();
            assert_eq!(spinup_manifest::hash::hash(&data), entry.hash, "{path}");
        }
        assert_eq!(
            std::fs::read(result.dir.join(".env")).unwrap(),
            std::fs::read(result.dir.join(".env.example")).unwrap()
        );
        assert!(!lock.files.contains_key(".env"));
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                std::fs::metadata(result.dir.join(".env"))
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777,
                0o600
            );
        }
        assert!(out.contents().contains("Created todo (") && out.contents().contains("Next:"));
        assert!(crate::select::generate(&options, None).is_err());
        assert!(
            apply(
                &result,
                &options,
                &Fake::new(),
                &Cancellation::new(),
                &printer
            )
            .is_err()
        );
    }

    #[test]
    fn dry_run_and_json_do_not_create_the_app() {
        let parent = tempfile::tempdir().unwrap();
        for json in [false, true] {
            let mut options = options(parent.path());
            options.dry_run = true;
            options.json = json;
            let result = crate::select::generate(&options, None).unwrap();
            let (printer, out) = printer();
            let fake = Fake::new();
            apply(&result, &options, &fake, &Cancellation::new(), &printer).unwrap();
            assert!(!result.dir.exists());
            assert!(fake.calls().is_empty());
            if json {
                // Warning text is sent to stderr. Read the JSON from its opening brace.
                let text = out.contents();
                let data: Value = serde_json::from_str(&text[text.find('{').unwrap()..]).unwrap();
                assert_eq!(data["files"], result.plan.files.len());
                assert_eq!(data["packs"], serde_json::json!(result.packs));
            } else {
                assert!(out.contents().contains("spinup.lock (CLI-owned)"));
            }
        }
    }

    #[test]
    fn failed_git_commit_leaves_the_app_and_warns() {
        let parent = tempfile::tempdir().unwrap();
        let mut options = options(parent.path());
        options.no_git = false;
        options.version = "v0.1.0".into();
        let result = crate::select::generate(&options, None).unwrap();
        let fake = Fake::new();
        fake.expect("git", ["init", "-b", "main"]);
        fake.expect("git", ["add", "-A"]);
        fake.expect("git", ["commit", "-m", "Initial commit from spinup v0.1.0"])
            .returns("", "no identity", 1);
        let (printer, out) = printer();
        apply(&result, &options, &fake, &Cancellation::new(), &printer).unwrap();
        fake.assert_done().unwrap();
        assert!(result.dir.join("spinup.lock").is_file());
        assert!(
            out.contents()
                .contains("Warning: Git initialization did not finish")
        );
        assert!(
            fake.calls()
                .iter()
                .all(|cmd| cmd.dir.as_ref() == Some(&result.dir))
        );
    }

    #[test]
    fn existing_environment_is_never_overwritten() {
        let root = tempfile::tempdir().unwrap();
        std::fs::write(root.path().join(".env"), b"DATABASE_URL=keep\n").unwrap();
        std::fs::write(root.path().join(".env.example"), b"DATABASE_URL=new\n").unwrap();
        copy_environment(root.path()).unwrap();
        assert_eq!(
            std::fs::read(root.path().join(".env")).unwrap(),
            b"DATABASE_URL=keep\n"
        );
    }

    #[test]
    fn real_git_initialization_creates_main_and_commits_the_lock() {
        let parent = tempfile::tempdir().unwrap();
        let options = options(parent.path());
        let result = crate::select::generate(&options, None).unwrap();
        let (printer, _) = printer();
        apply(
            &result,
            &options,
            &Fake::new(),
            &Cancellation::new(),
            &printer,
        )
        .unwrap();
        // Supply an identity on every command without changing process environment.
        struct IdentityRunner;
        impl Runner for IdentityRunner {
            fn run(
                &self,
                cmd: &Cmd,
                cancel: &Cancellation,
                sinks: spinup_exec::runner::Sinks<'_>,
            ) -> std::result::Result<spinup_exec::runner::Output, spinup_exec::runner::ExecError>
            {
                let cmd = cmd
                    .clone()
                    .env("GIT_AUTHOR_NAME", "spinup test")
                    .env("GIT_AUTHOR_EMAIL", "test@example.com")
                    .env("GIT_COMMITTER_NAME", "spinup test")
                    .env("GIT_COMMITTER_EMAIL", "test@example.com");
                OsRunner.run(&cmd, cancel, sinks)
            }
        }
        let cancel = Cancellation::new();
        crate::git::initialize_git(&result.dir, "", &IdentityRunner, &cancel, &printer);
        let output = OsRunner
            .output(
                &Cmd::new("git")
                    .args(["branch", "--show-current"])
                    .dir(&result.dir),
                &cancel,
            )
            .unwrap();
        assert_eq!(output.stdout, b"main\n");
        let output = OsRunner
            .output(
                &Cmd::new("git")
                    .args(["show", "HEAD:spinup.lock"])
                    .dir(&result.dir),
                &cancel,
            )
            .unwrap();
        assert_eq!(output.stdout, result.lock.marshal().unwrap());
        let output = OsRunner
            .output(
                &Cmd::new("git").args(["ls-files", ".env"]).dir(&result.dir),
                &cancel,
            )
            .unwrap();
        assert!(output.stdout.is_empty());
    }
}
