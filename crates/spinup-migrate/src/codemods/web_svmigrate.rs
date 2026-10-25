//! Codemod 0001: `sv migrate` on a SvelteKit major bump (Go `0001_web_svmigrate.go`).
//!
//! When the step moves `@sveltejs/kit` to a new major version, the codemod runs
//! `pnpm dlx sv@<pin> migrate sveltekit-<major>` in `web/` (or `bunx` for a bun app).
//! The sv migrations change only code that still uses the old API, so a second run on a
//! migrated tree changes nothing.

use std::time::Duration;

use anyhow::anyhow;
use spinup_exec::runner::Cmd;
use spinup_project::webtool::Tool;

use crate::contract::{Change, Ctx, MIGRATIONS, Migration, Stage};

/// The ID of the codemod (Go `SvMigrateID`).
pub const SV_MIGRATE_ID: &str = "0001_web_svmigrate";

/// The time limit of one sv migration, which can download the pinned CLI.
const SV_MIGRATE_TIMEOUT: Duration = Duration::from_secs(10 * 60);

/// The registered codemod.
// SAFETY: linkme places the item in its own link section. This is the registration contract of ADR 0020 §5.
#[allow(unsafe_code)]
#[linkme::distributed_slice(MIGRATIONS)]
pub static WEB_SVMIGRATE: Migration = Migration {
    id: SV_MIGRATE_ID,
    since: "0.2.0",
    desc: "Run sv migrate when SvelteKit moves to a new major version",
    stage: Stage::After,
    plan,
    apply,
};

/// A planned `sv migrate` run.
struct SvStep {
    /// The old `@sveltejs/kit` pin.
    from: String,
    /// The new `@sveltejs/kit` pin.
    to: String,
    /// The new SvelteKit major version.
    major: i64,
    /// The pinned sv CLI version.
    sv_version: String,
}

impl SvStep {
    /// Returns the command of the selected package manager.
    fn command(&self, c: &Ctx<'_>) -> Cmd {
        tool(c).dlx(
            &format!("sv@{}", self.sv_version),
            &["migrate", &format!("sveltekit-{}", self.major)],
        )
    }
}

/// Selects the package manager. The answers win over the lock, as in Go. Empty answers
/// fall back to the lock.
fn tool(c: &Ctx<'_>) -> Tool {
    if c.answers.is_empty() {
        return Tool::from_lock(Some(c.lock));
    }
    let answer = |key: &str| {
        c.answers
            .get(key)
            .and_then(serde_json::Value::as_str)
            .unwrap_or("")
    };
    Tool::new(answer("package_manager"), answer("api_client"))
}

/// Returns the planned run, or `None` when the step needs no migration.
fn step(c: &Ctx<'_>) -> anyhow::Result<Option<SvStep>> {
    let Some(old) = c.old_stack else {
        return Ok(None);
    };
    match std::fs::metadata(c.root.join("web").join("package.json")) {
        // An API-only app has no web/ to migrate.
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => {
            return Err(anyhow!(
                "check web/package.json: {e}\nTry: make the web directory readable"
            ));
        }
        Ok(_) => {}
    }
    let Some(from) = old.npm.get("@sveltejs/kit") else {
        return Ok(None); // The old version had no web pack.
    };
    let to = c.new_stack.npm_version("@sveltejs/kit").map_err(|e| {
        anyhow!(
            "read the new @sveltejs/kit pin: {e}\nTry: use a template version with a complete stackversions.yaml"
        )
    })?;
    let from_major = npm_major(from)?;
    let to_major = npm_major(to)?;
    if to_major <= from_major {
        return Ok(None);
    }
    let sv = c.new_stack.npm_version("sv").map_err(|e| {
        anyhow!(
            "read the sv pin: {e}\nTry: use a template version that pins sv in stackversions.yaml"
        )
    })?;
    Ok(Some(SvStep {
        from: from.clone(),
        to: to.to_string(),
        major: to_major,
        sv_version: sv.to_string(),
    }))
}

fn plan(c: &mut Ctx<'_>) -> anyhow::Result<Vec<Change>> {
    let Some(step) = step(c)? else {
        return Ok(Vec::new());
    };
    Ok(vec![Change {
        path: "web".into(),
        desc: format!(
            "SvelteKit {} to {}: run {}",
            step.from,
            step.to,
            step.command(c)
        ),
    }])
}

fn apply(c: &mut Ctx<'_>) -> anyhow::Result<()> {
    let Some(step) = step(c)? else {
        return Ok(());
    };
    let mut cmd = step.command(c);
    cmd.dir = Some(c.root.join("web"));
    cmd.timeout = Some(SV_MIGRATE_TIMEOUT);
    c.runner.output(&cmd, c.cancel).map_err(|e| {
        anyhow!(
            "run {cmd} in web/: {e}\nTry: run the command by hand, then run spinup upgrade --continue"
        )
    })?;
    Ok(())
}

/// Returns the major version of an exact npm pin such as `3.0.1`.
fn npm_major(pin: &str) -> anyhow::Result<i64> {
    let trimmed = pin.trim_start_matches(['^', '~', 'v', '=']);
    let head = trimmed.split_once('.').map_or(trimmed, |(head, _)| head);
    match head.parse::<i64>() {
        Ok(major) if major >= 0 => Ok(major),
        _ => Err(anyhow!(
            "parse npm version {pin:?}: not a semantic version\nTry: pin @sveltejs/kit to an exact version in stackversions.yaml"
        )),
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::fs;
    use std::path::Path;
    use std::sync::Mutex;

    use spinup_exec::fake::Fake;
    use spinup_exec::runner::{Cancellation, ExecError, Output, Runner, Sinks};
    use spinup_manifest::lock::Lock;
    use spinup_stack::stack::Stack;

    use super::*;
    use crate::registry;
    use crate::testkit::{self, Options};

    const KIT2_CONFIG: &str = "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter() } };\n";
    const KIT2_VITE: &str = "import { sveltekit } from '@sveltejs/kit/vite';\nexport default { plugins: [sveltekit()] };\n";
    const KIT3_VITE: &str = "import adapter from '@sveltejs/adapter-node';\nimport { sveltekit } from '@sveltejs/kit/vite';\nexport default { plugins: [sveltekit({ adapter: adapter() })] };\n";
    const WEB_PACKAGE: &str = "{\"name\": \"todo-web\"}\n";

    /// Imitates `sv migrate sveltekit-3`: it moves `svelte.config.js` into
    /// `vite.config.js`. Like the real tool, it changes nothing in a tree without Kit 2 code.
    #[derive(Default)]
    struct FakeSv {
        calls: Mutex<Vec<Cmd>>,
        fail: bool,
    }

    impl Runner for FakeSv {
        fn run(&self, cmd: &Cmd, _: &Cancellation, _: Sinks<'_>) -> Result<Output, ExecError> {
            self.calls.lock().unwrap().push(cmd.clone());
            let io = |e: std::io::Error| ExecError::Io {
                cmd: cmd.to_string(),
                source: e,
            };
            if self.fail {
                return Err(io(std::io::Error::other("exit status 1")));
            }
            let dir = cmd.dir.clone().unwrap();
            let config = dir.join("svelte.config.js");
            if !config.exists() {
                return Ok(Output::default());
            }
            fs::write(dir.join("vite.config.js"), KIT3_VITE).map_err(io)?;
            fs::remove_file(config).map_err(io)?;
            Ok(Output::default())
        }
    }

    fn stack(kit: &str) -> Stack {
        Stack {
            npm: BTreeMap::from([
                ("@sveltejs/kit".into(), kit.into()),
                ("sv".into(), "1.1.1".into()),
            ]),
            ..Stack::default()
        }
    }

    fn write_tree(files: &[(&str, &str)]) -> tempfile::TempDir {
        let root = tempfile::tempdir().unwrap();
        for (path, data) in files {
            let target = root.path().join(path);
            fs::create_dir_all(target.parent().unwrap()).unwrap();
            fs::write(target, data).unwrap();
        }
        root
    }

    fn kit2_tree() -> tempfile::TempDir {
        write_tree(&[
            ("web/package.json", WEB_PACKAGE),
            ("web/svelte.config.js", KIT2_CONFIG),
            ("web/vite.config.js", KIT2_VITE),
        ])
    }

    /// Calls `f` with a context on `root`.
    fn with_ctx<R>(
        root: &Path,
        runner: &dyn Runner,
        old: Option<&Stack>,
        new: &Stack,
        answers: serde_json::Map<String, serde_json::Value>,
        f: impl FnOnce(&mut Ctx<'_>) -> R,
    ) -> R {
        let lock = Lock::default();
        let cancel = Cancellation::new();
        let mut answers = answers;
        let mut ctx = Ctx {
            root,
            from: "0.1.0",
            to: "0.2.0",
            answers: &mut answers,
            lock: &lock,
            runner,
            cancel: &cancel,
            old_stack: old,
            new_stack: new,
        };
        f(&mut ctx)
    }

    #[test]
    fn major_bump() {
        let before = kit2_tree();
        let after = write_tree(&[
            ("web/package.json", WEB_PACKAGE),
            ("web/vite.config.js", KIT3_VITE),
        ]);
        let runner = FakeSv::default();
        let (old, new) = (stack("2.49.0"), stack("3.0.1"));
        testkit::run(
            &WEB_SVMIGRATE,
            before.path(),
            after.path(),
            Options {
                runner: Some(&runner),
                old_stack: Some(&old),
                new_stack: Some(&new),
            },
        );
        let calls = runner.calls.lock().unwrap();
        assert_eq!(calls.len(), 2, "apply runs twice");
        for call in calls.iter() {
            assert_eq!(call.program, "pnpm");
            assert_eq!(call.args, ["dlx", "sv@1.1.1", "migrate", "sveltekit-3"]);
            assert_eq!(call.dir.as_deref().unwrap().file_name().unwrap(), "web");
            assert_eq!(call.timeout, Some(SV_MIGRATE_TIMEOUT));
        }
    }

    #[test]
    fn noop_cases() {
        let empty = Stack::default();
        type Case<'a> = (&'a str, Option<Stack>, Stack, &'a [(&'a str, &'a str)]);
        let cases: [Case<'_>; 4] = [
            ("unknown old version", None, stack("3.0.1"), &[]),
            ("same major", Some(stack("3.0.1")), stack("3.4.0"), &[]),
            (
                "old version without web",
                Some(empty.clone()),
                stack("3.0.1"),
                &[],
            ),
            (
                "api-only app",
                Some(stack("2.49.0")),
                stack("3.0.1"),
                &[("api/Cargo.toml", "[package]\nname = \"todo\"\n")],
            ),
        ];
        for (name, old, new, files) in cases {
            let root = if files.is_empty() {
                kit2_tree()
            } else {
                write_tree(files)
            };
            // The harness reports every subprocess call.
            let violations = testkit::check(
                &WEB_SVMIGRATE,
                root.path(),
                root.path(),
                Options {
                    runner: None,
                    old_stack: old.as_ref(),
                    new_stack: Some(&new),
                },
            );
            assert!(violations.is_empty(), "{name}: {violations:?}");
        }
    }

    #[test]
    fn registered() {
        let found = registry::all()
            .into_iter()
            .find(|m| m.id == SV_MIGRATE_ID)
            .expect("codemod 0001 is registered");
        assert_eq!(found.stage, Stage::After);
        assert_eq!(found.since, "0.2.0");
        // The harness gives no old stack, so the codemod plans and runs nothing.
        let root = kit2_tree();
        testkit::run(found, root.path(), root.path(), Options::default());
        // With both stacks in the context, it plans the sv run.
        let (old, new) = (stack("2.49.0"), stack("3.0.1"));
        let fake = Fake::new();
        let changes = with_ctx(
            root.path(),
            &fake,
            Some(&old),
            &new,
            serde_json::Map::new(),
            |c| (found.plan)(c),
        )
        .unwrap();
        assert_eq!(
            changes,
            [Change {
                path: "web".into(),
                desc: "SvelteKit 2.49.0 to 3.0.1: run pnpm dlx sv@1.1.1 migrate sveltekit-3".into(),
            }]
        );
        let selected = registry::pending("0.1.0", "0.2.0", &[], Stage::After).unwrap();
        assert!(selected.iter().any(|m| m.id == SV_MIGRATE_ID));
        let applied = [SV_MIGRATE_ID.to_string()];
        let selected = registry::pending("0.1.0", "0.2.0", &applied, Stage::After).unwrap();
        assert!(selected.iter().all(|m| m.id != SV_MIGRATE_ID));
    }

    #[test]
    fn errors() {
        let (old, new) = (stack("2.49.0"), stack("3.0.1"));
        let none = serde_json::Map::new;
        let root = kit2_tree();
        let runner = FakeSv {
            fail: true,
            ..FakeSv::default()
        };
        let err = with_ctx(root.path(), &runner, Some(&old), &new, none(), apply).unwrap_err();
        let text = err.to_string();
        assert!(
            text.contains("sv@1.1.1 migrate sveltekit-3")
                && text.ends_with(
                    "\nTry: run the command by hand, then run spinup upgrade --continue"
                ),
            "{text}"
        );

        let bad = stack("next");
        let fake = Fake::new();
        let err = with_ctx(root.path(), &fake, Some(&bad), &new, none(), plan).unwrap_err();
        assert!(err.to_string().contains("\nTry: "), "{err}");

        let mut no_sv = stack("3.0.1");
        no_sv.npm.remove("sv");
        let err = with_ctx(root.path(), &fake, Some(&old), &no_sv, none(), plan).unwrap_err();
        assert!(err.to_string().contains("sv pin"), "{err}");

        let mut no_kit = stack("3.0.1");
        no_kit.npm.remove("@sveltejs/kit");
        let err = with_ctx(root.path(), &fake, Some(&old), &no_kit, none(), plan).unwrap_err();
        assert!(err.to_string().contains("@sveltejs/kit pin"), "{err}");
        assert!(fake.calls().is_empty());
    }

    #[test]
    fn bun() {
        let root = kit2_tree();
        let fake = Fake::new();
        fake.expect("bunx", ["sv@1.1.1", "migrate", "sveltekit-3"]);
        let (old, new) = (stack("2.49.0"), stack("3.0.1"));
        let mut answers = serde_json::Map::new();
        answers.insert("package_manager".into(), "bun".into());
        with_ctx(root.path(), &fake, Some(&old), &new, answers, |c| {
            let changes = plan(c).unwrap();
            assert_eq!(changes.len(), 1);
            assert!(changes[0].desc.contains("bunx sv@1.1.1"), "{changes:?}");
            apply(c).unwrap();
        });
        fake.assert_done().unwrap();
    }

    #[test]
    fn bun_from_lock() {
        let lock =
            spinup_manifest::json::parse(br#"{"schema":3,"answers":{"package_manager":"bun"}}"#)
                .unwrap();
        let cancel = Cancellation::new();
        let fake = Fake::new();
        let new = Stack::default();
        let mut answers = serde_json::Map::new();
        let ctx = Ctx {
            root: Path::new("."),
            from: "0.1.0",
            to: "0.2.0",
            answers: &mut answers,
            lock: &lock,
            runner: &fake,
            cancel: &cancel,
            old_stack: None,
            new_stack: &new,
        };
        assert_eq!(tool(&ctx).name(), "bun");
    }

    #[test]
    fn majors() {
        for (pin, want) in [("3.0.1", Some(3)), ("^2.49.0", Some(2)), ("v10", Some(10))] {
            assert_eq!(npm_major(pin).ok(), want, "{pin}");
        }
        for pin in ["next", "", "-1.0.0", "x.1"] {
            assert!(npm_major(pin).is_err(), "{pin}");
        }
    }
}
