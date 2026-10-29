use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_gen::contract::{Input, Output};
use spinup_manifest::lock::Lock;

fn generate(root: &std::path::Path, args: &[&str]) -> anyhow::Result<Output> {
    let generator = spinup_gen::contract::GENERATORS
        .iter()
        .find(|g| g.name == "migration")
        .expect("the generator must be registered");
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "jobs".into(), "mail".into()];
    (generator.run)(&Input {
        root,
        lock: &lock,
        args: &args.iter().map(|s| (*s).into()).collect::<Vec<_>>(),
        matches: &clap::Command::new("migration").get_matches_from(["migration"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
}

#[test]
fn registers_and_plans_without_writing() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["add_index"]).unwrap();
    assert_eq!(
        output.plan.paths(),
        [
            "api/migrations/00001_add_index.down.sql",
            "api/migrations/00001_add_index.up.sql"
        ]
    );
    assert_eq!(std::fs::read_dir(root.path()).unwrap().count(), 0);
}

#[test]
fn numbering_pairs_seed_bytes_and_refusals() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["add_index"]).unwrap();
    assert_eq!(
        output
            .plan
            .files
            .iter()
            .map(|f| f.class.as_str())
            .collect::<Vec<_>>(),
        ["seed", "seed"]
    );
    assert!(output.post_steps.is_empty());
    assert_eq!(
        output
            .plan
            .get("api/migrations/00001_add_index.up.sql")
            .unwrap()
            .data,
        b"-- Write the SQL for this migration here.\n"
    );
    assert_eq!(
        output
            .plan
            .get("api/migrations/00001_add_index.down.sql")
            .unwrap()
            .data,
        b"-- Write the SQL that reverses the up migration here.\n"
    );
    spinup_vfs::apply::apply(root.path(), &output.plan, Default::default()).unwrap();
    assert!(generate(root.path(), &["other"]).unwrap().plan.paths()[0].contains("00002_other"));
    for args in [
        vec![],
        vec!["one", "two"],
        vec!["../escape"],
        vec!["AddIndex"],
        vec!["add_index"],
    ] {
        let error = generate(root.path(), &args).unwrap_err().to_string();
        assert!(error.contains("\nTry:"), "{error}");
    }
}

fn database_fixture(root: &std::path::Path) {
    let mut plan = spinup_vfs::plan::Plan::new();
    for (path, source) in [
        ("spinup.toml", "[app]\nname='todo'\n"),
        ("api/.env", "DATABASE_URL=postgres://fixture-secret\n"),
    ] {
        plan.add(spinup_vfs::plan::File {
            path: path.into(),
            data: source.as_bytes().to_vec(),
            ..Default::default()
        })
        .unwrap();
    }
    spinup_vfs::apply::apply(root, &plan, Default::default()).unwrap();
}

#[test]
fn database_steps_use_the_shared_implementation() {
    use spinup_gen::{contract::PostStep, run::StepInput};
    for step in [PostStep::Migrate, PostStep::SqlxPrepare] {
        let root = tempfile::tempdir().unwrap();
        database_fixture(root.path());
        let fake = Fake::new();
        fake.expect("cargo", ["metadata", "--format-version", "1", "--locked"])
            .returns(serde_json::json!({"packages": [{"name": "spinup-kit", "manifest_path": root.path().join("kit/Cargo.toml")}]}).to_string(), "", 0);
        fake.expect("sqlx", ["migrate", "run", "--source"])
            .returns("kit migrated", "", 0);
        fake.expect("sqlx", ["migrate", "run", "--source", "migrations"])
            .returns("app migrated", "", 0);
        if step == PostStep::SqlxPrepare {
            fake.expect(
                "cargo",
                ["sqlx", "prepare", "--workspace", "--", "--all-targets"],
            )
            .returns("", "", 0);
        }
        spinup_gen::poststeps::run_steps(
            &[step],
            &StepInput {
                root: root.path(),
                files: &[],
                runner: &fake,
                cancel: &Cancellation::new(),
            },
        )
        .unwrap();
        fake.assert_done().unwrap();
        let calls = fake.calls();
        for call in &calls {
            assert_eq!(call.dir.as_deref(), Some(root.path().join("api").as_path()));
            assert!(!call.to_string().contains("fixture-secret"));
        }
        assert!(std::path::Path::new(&calls[1].args[3]).ends_with("kit/migrations"));
        assert!(calls[1].args.contains(&"--config".into()));
        if step == PostStep::SqlxPrepare {
            assert!(
                calls
                    .last()
                    .unwrap()
                    .env
                    .contains(&("SQLX_OFFLINE".into(), "false".into()))
            );
        }
    }
}

#[test]
fn database_step_failure_has_the_retry_command() {
    let root = tempfile::tempdir().unwrap();
    database_fixture(root.path());
    let fake = Fake::new();
    fake.expect("cargo", ["metadata"])
        .returns("", "metadata failed", 1);
    let error = (super::MIGRATE.run)(&spinup_gen::run::StepInput {
        root: root.path(),
        files: &[],
        runner: &fake,
        cancel: &Cancellation::new(),
    })
    .unwrap_err();
    assert!(error.to_string().contains("\nTry:"), "{error}");
    fake.assert_done().unwrap();
}
