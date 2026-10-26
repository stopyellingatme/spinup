use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_gen::contract::{Input, Output};
use spinup_manifest::lock::Lock;

fn generate(root: &std::path::Path, args: &[&str]) -> anyhow::Result<Output> {
    let generator = spinup_gen::contract::GENERATORS
        .iter()
        .find(|g| g.name == "openapi")
        .expect("the generator must be registered");
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "jobs".into(), "mail".into()];
    (generator.run)(&Input {
        root,
        lock: &lock,
        args: &args.iter().map(|s| (*s).into()).collect::<Vec<_>>(),
        matches: &clap::Command::new("openapi").get_matches_from(["openapi"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
}

#[test]
fn registers_and_plans_without_writing() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &[]).unwrap();
    assert_eq!(
        output.post_steps,
        [spinup_gen::contract::PostStep::OpenApiEmit]
    );
    assert_eq!(std::fs::read_dir(root.path()).unwrap().count(), 0);
}

#[test]
fn openapi_takes_no_arguments() {
    let root = tempfile::tempdir().unwrap();
    let error = generate(root.path(), &["extra"]).unwrap_err().to_string();
    assert!(
        error.contains("expected no positional arguments")
            && error.ends_with("Try: spinup g openapi")
    );
}

const DOCUMENT: &str = "openapi: 3.1.0\ninfo:\n  title: Test\n  version: 1.0.0\npaths: {}\n";

#[test]
fn emitter_needs_no_environment_and_tracks_the_seed() {
    let root = tempfile::tempdir().unwrap();
    Lock::new().save(root.path().join("spinup.lock")).unwrap();
    let fake = Fake::new();
    for _ in 0..2 {
        fake.expect("cargo", ["run", "--locked", "--quiet", "--", "openapi"])
            .returns(DOCUMENT, "", 0);
        (super::EMIT.run)(&spinup_gen::run::StepInput {
            root: root.path(),
            files: &[],
            runner: &fake,
            cancel: &Cancellation::new(),
        })
        .unwrap();
        assert_eq!(
            std::fs::read(root.path().join(super::BUNDLE)).unwrap(),
            DOCUMENT.as_bytes()
        );
        let lock = Lock::load(root.path().join("spinup.lock")).unwrap();
        let entry = &lock.files[super::BUNDLE];
        assert_eq!(entry.class, spinup_manifest::lock::Class::Seed);
        assert_eq!(entry.hash, spinup_manifest::hash::hash(DOCUMENT.as_bytes()));
        assert_eq!(entry.template, "gen/openapi");
    }
    fake.assert_done().unwrap();
    for call in fake.calls() {
        assert_eq!(call.dir, Some(root.path().join("api")));
        assert!(!call.env.iter().any(|(key, _)| key == "DATABASE_URL"));
    }
}

#[test]
fn emitter_failure_preserves_the_bundle_and_lock() {
    let root = tempfile::tempdir().unwrap();
    let mut plan = spinup_vfs::plan::Plan::new();
    plan.add(spinup_vfs::plan::File {
        path: super::BUNDLE.into(),
        data: b"old bundle\n".to_vec(),
        ..Default::default()
    })
    .unwrap();
    spinup_vfs::apply::apply(root.path(), &plan, Default::default()).unwrap();
    Lock::new().save(root.path().join("spinup.lock")).unwrap();
    let before = std::fs::read(root.path().join("spinup.lock")).unwrap();
    for (stdout, stderr, code, hint) in [
        ("partial", "build failed", 1, "spinup g openapi"),
        (
            "",
            spinup_project::env::SQLX_NO_CACHE,
            1,
            "Try: spinup db prepare",
        ),
        ("invalid: [yaml", "", 0, "Try:"),
        ("", "", 0, "Try:"),
    ] {
        let fake = Fake::new();
        fake.expect("cargo", ["run"]).returns(stdout, stderr, code);
        let error = (super::EMIT.run)(&spinup_gen::run::StepInput {
            root: root.path(),
            files: &[],
            runner: &fake,
            cancel: &Cancellation::new(),
        })
        .unwrap_err();
        assert!(format!("{error:#}").contains(hint), "{error:#}");
        assert_eq!(
            std::fs::read(root.path().join(super::BUNDLE)).unwrap(),
            b"old bundle\n"
        );
        assert_eq!(
            std::fs::read(root.path().join("spinup.lock")).unwrap(),
            before
        );
        fake.assert_done().unwrap();
    }
}

#[test]
fn registered_emit_failure_keeps_the_prepare_hint() {
    let root = tempfile::tempdir().unwrap();
    Lock::new().save(root.path().join("spinup.lock")).unwrap();
    let fake = Fake::new();
    fake.expect("cargo", ["run"])
        .returns("", spinup_project::env::SQLX_NO_CACHE, 1);
    let error = spinup_gen::poststeps::run_steps(
        &[spinup_gen::contract::PostStep::OpenApiEmit],
        &spinup_gen::run::StepInput {
            root: root.path(),
            files: &[],
            runner: &fake,
            cancel: &Cancellation::new(),
        },
    )
    .unwrap_err()
    .to_string();
    assert!(
        error.contains(spinup_project::env::SQLX_PREPARE_TRY),
        "{error}"
    );
}
