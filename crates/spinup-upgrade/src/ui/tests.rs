use super::*;
use spinup_exec::{os_runner::OsRunner, runner::Cancellation};
use spinup_manifest::lock::Lock;
use spinup_tplstore::source::Version;
use spinup_vfs::tree::Tree;
use std::sync::Arc;

const BUTTON: &str = "web/src/lib/components/ui/button/button.svelte";

fn version(name: &str, registry: &str, content: &str, class: &str, path: &str) -> Arc<Version> {
    let mut tree = Tree::new();
    let metadata = format!(
        "name='web'\n[[questions]]\nkey='shadcn_registry'\nprompt='Registry version'\ntype='string'\ndefault='{registry}'\n[[files]]\npath='{path}'\nsource='button.svelte'\nclass='{class}'\n"
    );
    tree.insert("web/pack.toml", metadata.into_bytes(), 0o644)
        .unwrap();
    tree.insert("web/button.svelte", content.as_bytes().to_vec(), 0o644)
        .unwrap();
    Arc::new(Version {
        version: name.into(),
        packs: Arc::new(tree),
        stack: Arc::default(),
        kit_migration: None,
    })
}

#[test]
fn run_ui_components_opt_in_merge_delete_and_conflict() {
    for (old, ours, new, enabled, want, conflict) in [
        ("old\n", Some("old\n"), "new\n", false, "old\n", false),
        ("old\n", Some("old\n"), "new\n", true, "new\n", false),
        ("old\n", Some("new\n"), "new\n", true, "new\n", false),
        ("old\n", None, "new\n", true, "", false),
        ("old\n", Some("user\n"), "new\n", true, "<<<<<<<", true),
        (
            "old\nline2\nline3\nold\n",
            Some("user\nline2\nline3\nold\n"),
            "old\nline2\nline3\nnew\n",
            true,
            "user\nline2\nline3\nnew\n",
            false,
        ),
    ] {
        let dir = tempfile::tempdir().unwrap();
        if let Some(ours) = ours {
            crate::plan::testing::write(dir.path(), BUTTON, ours, 0o644);
        }
        let mut lock = Lock::new();
        lock.packs = vec!["web".into()];
        lock.answers
            .insert("shadcn_registry".into(), to_raw_value("1.7.0").unwrap());
        let answers = crate::lock::answers(&lock).unwrap();
        let cancel = Cancellation::new();
        let mut output = Vec::new();
        let merger = spinup_merge::git::new_git(&OsRunner);
        let mut step = Step {
            from: version("0.1.0", "1.7.0", old, "seed", BUTTON),
            to: version("0.2.0", "1.8.0", new, "seed", BUTTON),
            worktree: dir.path().into(),
            lock,
            answers,
            plan: Some(Plan::default()),
            options: Default::default(),
            runner: &OsRunner,
            cancel: &cancel,
            out: &mut output,
            merger: &merger,
            conflicts: vec![],
        };
        run_ui(&mut step, enabled).unwrap();
        assert_eq!(!step.conflicts.is_empty(), conflict);
        assert_eq!(
            !step.plan.as_ref().unwrap().conflicts().is_empty(),
            conflict
        );
        if ours.is_none() {
            assert!(!dir.path().join(BUTTON).exists());
        } else {
            assert!(
                std::fs::read_to_string(dir.path().join(BUTTON))
                    .unwrap()
                    .contains(want)
            );
        }
        assert_eq!(
            crate::lock::answers(&step.lock).unwrap()["shadcn_registry"],
            if enabled { "1.8.0" } else { "1.7.0" }
        );
        if !enabled {
            assert!(String::from_utf8(output).unwrap().contains("--ui"));
        }
    }
}

#[test]
fn run_ui_new_tombstone_managed_and_outside_files() {
    for (absent, tombstone, managed, outside) in [
        (true, false, false, false),
        (true, true, false, false),
        (false, false, true, false),
        (false, false, false, true),
    ] {
        let path = if outside {
            "web/src/lib/api/schema.d.ts"
        } else {
            BUTTON
        };
        let dir = tempfile::tempdir().unwrap();
        let mut old = version(
            "0.1.0",
            "1.7.0",
            "old\n",
            if managed { "managed" } else { "seed" },
            path,
        );
        if absent {
            let tree = Arc::make_mut(&mut Arc::make_mut(&mut old).packs);
            tree.insert("web/pack.toml",b"name='web'\n[[questions]]\nkey='shadcn_registry'\nprompt='Registry'\ntype='string'\ndefault='1.7.0'\n".to_vec(),0o644).unwrap();
        } else {
            crate::plan::testing::write(dir.path(), path, "old\n", 0o644);
        }
        let mut lock = Lock::new();
        lock.packs = vec!["web".into()];
        lock.answers
            .insert("shadcn_registry".into(), to_raw_value("1.7.0").unwrap());
        if tombstone {
            lock.tombstones.push(path.into());
        }
        let answers = crate::lock::answers(&lock).unwrap();
        let cancel = Cancellation::new();
        let mut output = Vec::new();
        let merger = spinup_merge::git::new_git(&OsRunner);
        let mut step = Step {
            from: old,
            to: version("0.2.0", "1.8.0", "new\n", "seed", path),
            worktree: dir.path().into(),
            lock,
            answers,
            plan: Some(Plan::default()),
            options: Default::default(),
            runner: &OsRunner,
            cancel: &cancel,
            out: &mut output,
            merger: &merger,
            conflicts: vec![],
        };
        run_ui(&mut step, true).unwrap();
        if tombstone {
            assert!(!dir.path().join(path).exists());
        } else {
            assert_eq!(
                std::fs::read_to_string(dir.path().join(path)).unwrap(),
                if absent { "new\n" } else { "old\n" }
            );
        }
        if absent && !tombstone {
            assert_eq!(step.lock.files[path].hash, hash(b"new\n"));
            assert_eq!(step.lock.files[path].class, Class::Seed);
        }
    }
}
