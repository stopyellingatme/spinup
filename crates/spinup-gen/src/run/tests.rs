//! The runner tests (ports of Go `run_test.go` and `commands_test.go`).

use std::cell::RefCell;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};

use clap::ArgMatches;
use spinup_exec::fake::Fake;
use spinup_exec::runner::{Cancellation, ExecError, Output as ExecOutput};
use spinup_manifest::lock::{Class, FileEntry, Lock};
use spinup_vfs::plan::{File, Plan};

use super::*;
use crate::contract::{Command, Generator, Input, Output, PostStep};

/// A temporary project root with an empty lock. Drop removes it.
struct Root(PathBuf);

impl Root {
    #[allow(
        clippy::disallowed_methods,
        reason = "test setup creates the temporary project root"
    )]
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "spinup-gen-run-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        let _ = std::fs::remove_dir_all(&path);
        std::fs::create_dir_all(&path).unwrap();
        let root = Self(path.canonicalize().unwrap());
        Lock::new().save(root.lock_path()).unwrap();
        root
    }

    fn path(&self) -> &Path {
        &self.0
    }

    fn lock_path(&self) -> PathBuf {
        self.0.join("spinup.lock")
    }

    fn lock(&self) -> Lock {
        Lock::load(self.lock_path()).unwrap()
    }

    /// Writes a fixture file through the vfs apply step.
    fn put(&self, path: &str, data: &str) {
        let mut plan = Plan::new();
        plan.add(File {
            path: path.into(),
            data: data.as_bytes().to_vec(),
            ..File::default()
        })
        .unwrap();
        spinup_vfs::apply::apply(self.path(), &plan, Default::default()).unwrap();
    }

    fn read(&self, path: &str) -> Option<Vec<u8>> {
        std::fs::read(self.0.join(path)).ok()
    }

    fn track(&self, path: &str, entry: FileEntry) {
        let mut lock = self.lock();
        lock.set_file(path, entry).unwrap();
        lock.save(self.lock_path()).unwrap();
    }
}

impl Drop for Root {
    #[allow(
        clippy::disallowed_methods,
        reason = "test cleanup removes the temporary project root"
    )]
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

thread_local! {
    /// The output of the generators below. Each test sets it on its own thread.
    static OUTPUT: RefCell<anyhow::Result<Output>> = RefCell::new(Ok(Output::default()));
    /// The input that the generator got.
    static SEEN: RefCell<Option<(PathBuf, Vec<String>, bool)>> = const { RefCell::new(None) };
}

fn planned(input: &Input<'_>) -> anyhow::Result<Output> {
    SEEN.with(|seen| {
        *seen.borrow_mut() = Some((
            input.root.to_path_buf(),
            input.args.to_vec(),
            input.matches.get_flag("tag"),
        ));
    });
    OUTPUT.with(|o| match &*o.borrow() {
        Ok(output) => Ok(output.clone()),
        Err(e) => Err(anyhow::anyhow!("{e:#}")),
    })
}

static EXAMPLE: Generator = Generator {
    name: "example",
    aliases: &[],
    short: "",
    usage: "",
    args: std::convert::identity,
    run: planned,
};

static ALERT: Generator = Generator {
    name: "alert",
    ..EXAMPLE
};

fn set_output(output: anyhow::Result<Output>) {
    OUTPUT.with(|o| *o.borrow_mut() = output);
}

fn file(path: &str, data: &str, class: &str, mode: u32) -> File {
    File {
        path: path.into(),
        data: data.as_bytes().to_vec(),
        class: class.into(),
        mode,
        ..File::default()
    }
}

fn plan_output(files: Vec<File>) -> Output {
    Output {
        plan: Plan { files },
        next: vec!["spinup test".into()],
        ..Output::default()
    }
}

fn matches() -> ArgMatches {
    clap::Command::new("example")
        .arg(
            clap::Arg::new("tag")
                .long("tag")
                .action(clap::ArgAction::SetTrue),
        )
        .get_matches_from(["example", "--tag"])
}

#[derive(Default, Clone, Copy)]
struct Flags {
    dry: bool,
    diff: bool,
    force: bool,
    json: bool,
}

/// Runs `generator` and returns the result and the output text.
fn run_with(
    generator: &Generator,
    root: &Root,
    flags: Flags,
    fake: &Fake,
    steps: &[StepRunner],
) -> (anyhow::Result<RunResult>, String, String) {
    let mut out = Vec::new();
    let mut command_out = Vec::new();
    let cancel = Cancellation::new();
    let mut options = Options::new(root.path(), &mut out, fake, &cancel);
    options.dry_run = flags.dry;
    options.diff = flags.diff;
    options.force = flags.force;
    options.json = flags.json;
    options.command_out = Some(&mut command_out);
    options.post_steps = steps;
    let result = run(generator, &["arg".to_string()], &matches(), options);
    (
        result,
        String::from_utf8(out).unwrap(),
        String::from_utf8(command_out).unwrap(),
    )
}

fn run_example(root: &Root, flags: Flags) -> (anyhow::Result<RunResult>, String) {
    let fake = Fake::new();
    let (result, out, _) = run_with(&EXAMPLE, root, flags, &fake, &[]);
    fake.assert_done().unwrap();
    (result, out)
}

fn err_text(result: anyhow::Result<RunResult>) -> String {
    format!("{:#}", result.unwrap_err())
}

#[test]
fn actions_preview_apply_and_json() {
    for (name, dry, diff, json) in [
        ("apply", false, false, false),
        ("dry", true, false, false),
        ("diff", false, true, false),
        ("json", false, false, true),
        ("json-preview", true, false, true),
    ] {
        let root = Root::new();
        root.put("api/existing.txt", "old\n");
        root.put("api/same.txt", "same\n");
        let before = root.read("spinup.lock").unwrap();
        set_output(Ok(plan_output(vec![
            file("api/new.txt", "new\n", "scaffold", 0o755),
            file("api/existing.txt", "updated\n", "seed", 0),
            file("api/same.txt", "same\n", "managed", 0o755),
        ])));
        let flags = Flags {
            dry,
            diff,
            force: true,
            json,
        };
        let (result, out) = run_example(&root, flags);
        let result = result.unwrap();
        let actions: Vec<_> = result.files.iter().map(|f| f.action).collect();
        assert_eq!(
            actions,
            [Action::Update, Action::Create, Action::Unchanged],
            "{name}"
        );
        if json {
            assert_eq!(
                out,
                "{\"files\":[{\"path\":\"api/existing.txt\",\"action\":\"update\",\"class\":\"seed\"},{\"path\":\"api/new.txt\",\"action\":\"create\",\"class\":\"scaffold\"},{\"path\":\"api/same.txt\",\"action\":\"unchanged\",\"class\":\"managed\"}],\"post_steps\":[],\"next\":[\"spinup test\"]}\n",
                "{name}"
            );
        } else if diff {
            assert!(
                out.contains("--- a/api/existing.txt\n+++ b/api/existing.txt"),
                "{out}"
            );
            assert!(out.contains("+updated"), "{out}");
        } else if dry {
            assert!(out.contains("create api/new.txt (scaffold)\n"), "{out}");
            assert!(out.contains("update api/existing.txt (seed)\n"), "{out}");
            assert!(out.contains("unchanged api/same.txt (managed)\n"), "{out}");
        } else {
            assert!(
                out.contains("Created 1, updated 1 files\nNext: spinup test\n"),
                "{out}"
            );
        }
        if dry || diff {
            assert_eq!(root.read("spinup.lock").unwrap(), before, "{name}");
            assert!(root.read("api/new.txt").is_none(), "{name}");
            assert_eq!(root.read("api/existing.txt").unwrap(), b"old\n");
        } else {
            let lock = root.lock();
            let entry = &lock.files["api/new.txt"];
            assert_eq!(entry.class, Class::Scaffold);
            assert_eq!(entry.template, "gen/example");
            assert_eq!(entry.hash, spinup_manifest::hash::hash(b"new\n"));
            assert_eq!(entry.mode, "0755");
            assert_eq!(lock.files["api/existing.txt"].mode, "0644");
            assert_eq!(lock.files.len(), 2, "the identical file was recorded");
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                let mode = std::fs::metadata(root.path().join("api/same.txt"))
                    .unwrap()
                    .permissions()
                    .mode();
                assert_eq!(mode & 0o777, 0o644, "identical content changed the mode");
            }
        }
    }
}

#[test]
fn overwrite_rules() {
    for (name, class, template, force, allow, edited) in [
        ("untracked", "managed", "", false, false, false),
        ("scaffold", "scaffold", "gen/example", false, false, false),
        (
            "pack-owned untouched",
            "managed",
            "api/file.tmpl",
            false,
            true,
            false,
        ),
        (
            "pack-owned edited",
            "managed",
            "api/file.tmpl",
            false,
            false,
            true,
        ),
        (
            "generator-owned",
            "managed",
            "gen/other",
            false,
            true,
            false,
        ),
        ("force", "scaffold", "", true, true, false),
    ] {
        let root = Root::new();
        root.put("api/file.txt", "old");
        if !template.is_empty() {
            let locked = if edited {
                "what the template wrote"
            } else {
                "old"
            };
            root.track(
                "api/file.txt",
                FileEntry {
                    class: Class::Managed,
                    template: template.into(),
                    hash: spinup_manifest::hash::hash(locked.as_bytes()),
                    mode: "0644".into(),
                },
            );
        }
        set_output(Ok(plan_output(vec![file("api/file.txt", "new", class, 0)])));
        let (result, _) = run_example(
            &root,
            Flags {
                force,
                ..Flags::default()
            },
        );
        if allow {
            result.unwrap();
            assert_eq!(root.read("api/file.txt").unwrap(), b"new", "{name}");
        } else {
            let err = err_text(result);
            assert!(err.contains("api/file.txt"), "{name}: {err}");
            assert!(
                err.ends_with("Try: pass --force, or remove the files"),
                "{name}: {err}"
            );
            assert_eq!(root.read("api/file.txt").unwrap(), b"old", "{name}");
        }
    }
}

#[test]
fn invalid_plans() {
    let cases: Vec<(&str, Vec<File>)> = vec![
        ("escape", vec![file("../escape", "", "managed", 0)]),
        ("absolute", vec![file("/escape", "", "managed", 0)]),
        ("unclean", vec![file("api//x", "", "managed", 0)]),
        ("no-class", vec![file("api/x", "", "", 0)]),
        ("unknown-class", vec![file("api/x", "", "other", 0)]),
        (
            "duplicate",
            vec![
                file("api/x", "", "managed", 0),
                file("api/X", "", "managed", 0),
            ],
        ),
        ("lock", vec![file("spinup.lock", "", "managed", 0)]),
        (
            "delete",
            vec![File {
                delete: true,
                ..file("api/x", "", "managed", 0)
            }],
        ),
    ];
    for (name, files) in cases {
        let root = Root::new();
        set_output(Ok(plan_output(files)));
        let (result, _) = run_example(
            &root,
            Flags {
                force: true,
                ..Flags::default()
            },
        );
        let err = err_text(result);
        assert!(err.contains("Try:"), "{name}: {err}");
    }
}

#[cfg(unix)]
#[test]
fn rejects_a_symlink_outside_the_root() {
    let root = Root::new();
    let outside = Root::new();
    std::os::unix::fs::symlink(outside.path(), root.path().join("api")).unwrap();
    set_output(Ok(plan_output(vec![file("api/x", "x", "managed", 0)])));
    let (result, _) = run_example(
        &root,
        Flags {
            force: true,
            ..Flags::default()
        },
    );
    assert!(result.is_err());
    assert!(outside.read("x").is_none());
}

#[test]
fn passes_the_input() {
    let root = Root::new();
    set_output(Ok(Output::default()));
    let (result, out) = run_example(&root, Flags::default());
    result.unwrap();
    assert_eq!(out, "Created 0, updated 0 files\n");
    let seen = SEEN.with(|s| s.borrow_mut().take()).unwrap();
    assert_eq!(
        seen,
        (root.path().to_path_buf(), vec!["arg".to_string()], true)
    );
}

#[test]
#[allow(clippy::disallowed_methods, reason = "the test removes the lock")]
fn errors_have_a_try_line() {
    // The lock is missing.
    let root = Root::new();
    std::fs::remove_file(root.lock_path()).unwrap();
    set_output(Ok(Output::default()));
    let err = err_text(run_example(&root, Flags::default()).0);
    assert!(err.contains("spinup.lock") && err.contains("Try:"), "{err}");

    // The generator fails without a hint, and with a hint.
    let root = Root::new();
    set_output(Err(anyhow::anyhow!("bad argument")));
    let err = err_text(run_example(&root, Flags::default()).0);
    assert_eq!(
        err,
        "generator \"example\" failed: bad argument\nTry: spinup g example --help"
    );
    set_output(Err(anyhow::anyhow!("bad argument\nTry: pass a name")));
    let err = err_text(run_example(&root, Flags::default()).0);
    assert_eq!(
        err,
        "generator \"example\" failed: bad argument\nTry: pass a name"
    );
}

#[test]
fn identical_files_change_nothing() {
    let root = Root::new();
    root.put("api/same.txt", "same");
    let before = root.read("spinup.lock").unwrap();
    set_output(Ok(plan_output(vec![file(
        "api/same.txt",
        "same",
        "scaffold",
        0,
    )])));
    let (result, out) = run_example(&root, Flags::default());
    assert_eq!(result.unwrap().files[0].action, Action::Unchanged);
    assert!(out.contains("Created 0, updated 0 files"), "{out}");
    assert_eq!(root.read("spinup.lock").unwrap(), before);
}

#[test]
fn lists_all_conflicts_and_writes_nothing() {
    let root = Root::new();
    root.put("api/a", "old");
    root.put("api/z", "old");
    set_output(Ok(plan_output(vec![
        file("api/z", "new", "scaffold", 0),
        file("api/a", "new", "seed", 0),
        file("api/new", "", "managed", 0),
    ])));
    let err = err_text(run_example(&root, Flags::default()).0);
    assert!(err.contains("api/a\n  api/z"), "{err}");
    assert!(
        root.read("api/new").is_none(),
        "a conflict wrote part of the plan"
    );
}

struct Failing;

impl std::io::Write for Failing {
    fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
        Err(std::io::Error::other("output closed"))
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn output_failures_have_a_try_line() {
    for (dry, diff, json) in [
        (false, false, false),
        (true, false, false),
        (false, true, false),
        (false, false, true),
    ] {
        let root = Root::new();
        set_output(Ok(plan_output(vec![file("api/new", "new", "scaffold", 0)])));
        let fake = Fake::new();
        let cancel = Cancellation::new();
        let mut out = Failing;
        let mut options = Options::new(root.path(), &mut out, &fake, &cancel);
        options.dry_run = dry;
        options.diff = diff;
        options.json = json;
        let err = format!("{:#}", run(&EXAMPLE, &[], &matches(), options).unwrap_err());
        assert!(
            err.contains("output closed") && err.ends_with("Try: check the output destination"),
            "{err}"
        );
    }
}

#[test]
fn edits_update_a_seed_file_and_keep_the_template() {
    let root = Root::new();
    let path = "monitoring/alerts/custom.yml";
    let template = "monitoring/files/monitoring/alerts/custom.yml";
    root.put(path, "groups: []\n");
    root.track(
        path,
        FileEntry {
            class: Class::Seed,
            template: template.into(),
            hash: spinup_manifest::hash::hash(b"groups: []\n"),
            mode: "0644".into(),
        },
    );
    let edited = file(path, "groups:\n  - name: custom\n", "seed", 0);
    set_output(Ok(Output {
        edits: vec![path.into()],
        ..plan_output(vec![edited.clone()])
    }));
    let fake = Fake::new();
    run_with(&ALERT, &root, Flags::default(), &fake, &[])
        .0
        .unwrap();
    let entry = root.lock().files[path].clone();
    assert_eq!(entry.template, template);
    assert_eq!(entry.class, Class::Seed);
    // Without the edit mark, the same update is a conflict.
    set_output(Ok(plan_output(vec![file(
        path,
        "groups:\n  - name: other\n",
        "seed",
        0,
    )])));
    assert!(run_example(&root, Flags::default()).0.is_err());
}

// Post-steps.

fn tool(input: &StepInput<'_>, program: &str) -> anyhow::Result<()> {
    let cmd = spinup_exec::runner::Cmd::new(program)
        .args(input.files.iter().cloned())
        .dir(input.root.join("api"));
    input.runner.output(&cmd, input.cancel)?;
    Ok(())
}

fn step(step: PostStep, run: fn(&StepInput<'_>) -> anyhow::Result<()>) -> StepRunner {
    StepRunner {
        step,
        run,
        retry: |files| format!("run tool {} in api/ by hand", files.join(" ")),
    }
}

fn step_runners() -> Vec<StepRunner> {
    vec![
        step(PostStep::Client, |i| tool(i, "client-tool")),
        step(PostStep::Migrate, |i| tool(i, "migrate-tool")),
        step(PostStep::SqlxPrepare, |i| tool(i, "sqlx-tool")),
    ]
}

fn step_output() -> Output {
    Output {
        post_steps: vec![
            PostStep::Client,
            PostStep::SqlxPrepare,
            PostStep::Migrate,
            PostStep::SqlxPrepare,
        ],
        ..plan_output(vec![file("api/x.rs", "fn x() {}\n", "scaffold", 0)])
    }
}

#[test]
fn post_steps_run_once_in_order() {
    for (name, dry, diff, fail) in [
        ("order", false, false, false),
        ("dry", true, false, false),
        ("diff", false, true, false),
        ("failure", false, false, true),
    ] {
        let root = Root::new();
        let fake = Fake::new();
        if !dry && !diff {
            fake.expect("migrate-tool", ["api/x.rs"]).returns("", "", 0);
            fake.expect("sqlx-tool", ["api/x.rs"]).returns(
                "tool stdout",
                "tool stderr",
                i32::from(fail),
            );
            if !fail {
                fake.expect("client-tool", ["api/x.rs"]).returns("", "", 0);
            }
        }
        set_output(Ok(step_output()));
        let flags = Flags {
            dry,
            diff,
            ..Flags::default()
        };
        let (result, _, _) = run_with(&EXAMPLE, &root, flags, &fake, &step_runners());
        if fail {
            let err = err_text(result);
            assert!(
                err.starts_with("generator post-step \"sqlx-prepare\" failed: "),
                "{err}"
            );
            assert!(err.contains("tool stderr"), "{err}");
            assert!(
                err.ends_with("\nTry: run tool api/x.rs in api/ by hand"),
                "{err}"
            );
            assert_eq!(err.matches("Try:").count(), 1, "{err}");
            assert!(
                root.read("api/x.rs").is_some(),
                "a failed step removed files"
            );
            assert_eq!(root.lock().files.len(), 1);
        } else {
            let result = result.unwrap();
            assert_eq!(
                result.post_steps,
                [PostStep::Migrate, PostStep::SqlxPrepare, PostStep::Client],
                "{name}"
            );
        }
        for call in fake.calls() {
            assert_eq!(call.dir, Some(root.path().join("api")), "{name}");
        }
        fake.assert_done().unwrap();
    }
}

#[test]
fn post_steps_in_json() {
    let root = Root::new();
    set_output(Ok(step_output()));
    let fake = Fake::new();
    let flags = Flags {
        dry: true,
        json: true,
        ..Flags::default()
    };
    let (_, out, _) = run_with(&EXAMPLE, &root, flags, &fake, &step_runners());
    assert!(
        out.contains("\"post_steps\":[\"migrate\",\"sqlx-prepare\",\"client\"]"),
        "{out}"
    );
}

#[test]
fn a_missing_post_step_runner_keeps_the_files() {
    let root = Root::new();
    set_output(Ok(Output {
        post_steps: vec![PostStep::Build],
        ..plan_output(vec![file("api/new.txt", "new", "scaffold", 0)])
    }));
    let fake = Fake::new();
    let (result, _, _) = run_with(&EXAMPLE, &root, Flags::default(), &fake, &step_runners());
    let err = err_text(result);
    assert_eq!(
        err,
        "cannot run generator post-step \"build\": no runner is registered\nTry: register the build post-step runner, then repeat the generator"
    );
    assert_eq!(root.read("api/new.txt").unwrap(), b"new");
    assert_eq!(root.lock().files["api/new.txt"].template, "gen/example");
    // Two runners for one step are an error too.
    let twice = [
        step(PostStep::Build, |_| Ok(())),
        step(PostStep::Build, |_| Ok(())),
    ];
    let (result, _, _) = run_with(&EXAMPLE, &root, Flags::default(), &fake, &twice);
    assert!(err_text(result).contains("two runners are registered"));
    assert!(fake.calls().is_empty());
}

#[test]
fn the_registered_post_steps_are_unique() {
    for s in [
        PostStep::Migrate,
        PostStep::SqlxPrepare,
        PostStep::Build,
        PostStep::OpenApiEmit,
        PostStep::Client,
    ] {
        assert!(POST_STEPS.iter().filter(|r| r.step == s).count() <= 1);
    }
}

// External commands.

fn add_dialog() -> Vec<Command> {
    vec![
        Command {
            dir: "web".into(),
            argv: [
                "pnpm",
                "dlx",
                "shadcn-svelte@1.7.0",
                "add",
                "dialog",
                "--yes",
            ]
            .map(String::from)
            .to_vec(),
        },
        Command {
            dir: String::new(),
            argv: vec!["echo".into(), "two words".into()],
        },
    ]
}

fn command_output(commands: Vec<Command>, files: Vec<File>) -> Output {
    Output {
        commands,
        post_steps: vec![PostStep::SqlxPrepare],
        ..plan_output(files)
    }
}

#[test]
fn commands_run_after_the_write_and_before_the_post_steps() {
    let root = Root::new();
    let fake = Fake::new();
    fake.expect(
        "pnpm",
        ["dlx", "shadcn-svelte@1.7.0", "add", "dialog", "--yes"],
    )
    .returns("added dialog\n", "", 0);
    fake.expect("echo", ["two words"]).returns("", "", 0);
    fake.expect("sqlx-tool", ["web/a.txt"]).returns("", "", 0);
    set_output(Ok(command_output(
        add_dialog(),
        vec![file("web/a.txt", "a\n", "scaffold", 0)],
    )));
    let (result, out, command_out) =
        run_with(&EXAMPLE, &root, Flags::default(), &fake, &step_runners());
    let result = result.unwrap();
    fake.assert_done().unwrap();
    let dirs: Vec<_> = fake.calls().into_iter().map(|c| c.dir.unwrap()).collect();
    assert_eq!(
        dirs,
        [
            root.path().join("web"),
            root.path().to_path_buf(),
            root.path().join("api")
        ]
    );
    assert!(out.contains("added dialog"), "{out}");
    assert!(out.contains("Created 1, updated 0 files"), "{out}");
    assert!(command_out.is_empty(), "{command_out}");
    let mut want = add_dialog();
    want[1].dir = ".".into();
    assert_eq!(result.commands, want);
}

#[test]
fn commands_write_to_the_command_output_with_json() {
    let root = Root::new();
    let fake = Fake::new();
    fake.expect("pnpm", ["dlx"])
        .returns("added dialog\n", "warn\n", 0);
    set_output(Ok(Output {
        commands: add_dialog()[..1].to_vec(),
        ..Output::default()
    }));
    let flags = Flags {
        json: true,
        ..Flags::default()
    };
    let (result, out, command_out) = run_with(&EXAMPLE, &root, flags, &fake, &[]);
    result.unwrap();
    assert_eq!(command_out, "added dialog\nwarn\n");
    assert_eq!(
        out,
        "{\"files\":[],\"post_steps\":[],\"next\":[],\"commands\":[{\"Dir\":\"web\",\"Argv\":[\"pnpm\",\"dlx\",\"shadcn-svelte@1.7.0\",\"add\",\"dialog\",\"--yes\"]}]}\n"
    );
}

#[test]
fn commands_preview() {
    for (dry, diff, json) in [
        (true, false, false),
        (false, true, false),
        (true, false, true),
    ] {
        let root = Root::new();
        let fake = Fake::new();
        set_output(Ok(command_output(add_dialog(), Vec::new())));
        let flags = Flags {
            dry,
            diff,
            json,
            ..Flags::default()
        };
        let (result, out, _) = run_with(&EXAMPLE, &root, flags, &fake, &step_runners());
        result.unwrap();
        assert!(fake.calls().is_empty(), "a preview ran {:?}", fake.calls());
        if json {
            assert!(
                out.contains("\"commands\":[{\"Dir\":\"web\",") && out.contains("{\"Dir\":\".\","),
                "{out}"
            );
        } else {
            assert_eq!(
                out,
                "run pnpm dlx shadcn-svelte@1.7.0 add dialog --yes (in web)\nrun echo 'two words' (in .)\n"
            );
        }
    }
}

type FailureCase = (&'static str, fn(&Fake), &'static str);

#[test]
fn a_command_failure_keeps_the_files() {
    let cases: Vec<FailureCase> = vec![
        (
            "exit code",
            |f| {
                f.expect("pnpm", Vec::<String>::new())
                    .returns("", "registry unreachable", 1);
            },
            "registry unreachable",
        ),
        (
            "not found",
            |f| {
                f.expect("pnpm", Vec::<String>::new())
                    .fail(ExecError::NotFound {
                        name: "pnpm".into(),
                    });
            },
            "command \"pnpm\" was not found.",
        ),
        (
            "other",
            |f| {
                f.expect("pnpm", Vec::<String>::new()).fail(ExecError::Io {
                    cmd: "pnpm".into(),
                    source: std::io::Error::other("boom"),
                });
            },
            "boom",
        ),
    ];
    for (name, expect, want) in cases {
        let root = Root::new();
        let fake = Fake::new();
        expect(&fake);
        set_output(Ok(command_output(
            add_dialog()[..1].to_vec(),
            vec![file("web/a.txt", "a\n", "scaffold", 0)],
        )));
        let (result, _, _) = run_with(&EXAMPLE, &root, Flags::default(), &fake, &step_runners());
        let err = err_text(result);
        assert!(err.contains(want), "{name}: {err}");
        assert_eq!(err.matches("Try:").count(), 1, "{name}: {err}");
        assert!(
            err.contains("Try: correct the problem, then run pnpm dlx shadcn-svelte@1.7.0 add dialog --yes in web by hand"),
            "{name}: {err}"
        );
        assert!(root.read("web/a.txt").is_some(), "{name}");
        assert_eq!(root.lock().files.len(), 1, "{name}");
        // The post-steps did not run.
        fake.assert_done().unwrap();
        assert_eq!(fake.calls().len(), 1, "{name}");
    }
}

#[test]
fn a_nonzero_exit_without_an_error_fails() {
    struct ExitOne;
    impl spinup_exec::runner::Runner for ExitOne {
        fn run(
            &self,
            _: &spinup_exec::runner::Cmd,
            _: &Cancellation,
            _: spinup_exec::runner::Sinks<'_>,
        ) -> Result<ExecOutput, ExecError> {
            Ok(ExecOutput {
                exit_code: 3,
                ..ExecOutput::default()
            })
        }
    }
    let err = commands::run_all(
        Path::new("/project"),
        &add_dialog()[..1],
        &ExitOne,
        &Cancellation::new(),
        &mut Vec::new(),
    )
    .unwrap_err();
    assert!(format!("{err:#}").contains("failed in web: exit code 3\nTry: "));
}

#[test]
fn invalid_commands() {
    for (dir, argv) in [
        ("../web", vec!["pnpm"]),
        ("/web", vec!["pnpm"]),
        ("web/", vec!["pnpm"]),
        ("web", vec![]),
        ("web", vec![""]),
    ] {
        let root = Root::new();
        let fake = Fake::new();
        set_output(Ok(command_output(
            vec![Command {
                dir: dir.into(),
                argv: argv.into_iter().map(String::from).collect(),
            }],
            Vec::new(),
        )));
        let (result, _, _) = run_with(&EXAMPLE, &root, Flags::default(), &fake, &step_runners());
        let err = err_text(result);
        assert!(
            err.ends_with("Try: repair the generator commands"),
            "{dir:?}: {err}"
        );
        assert!(fake.calls().is_empty());
    }
}

#[test]
fn reason_drops_try_lines() {
    assert_eq!(
        commands::reason("command x failed\nTry: inspect the output"),
        "command x failed"
    );
    assert_eq!(
        commands::reason("command \"pnpm\" was not found. Try: install pnpm"),
        "command \"pnpm\" was not found."
    );
}
