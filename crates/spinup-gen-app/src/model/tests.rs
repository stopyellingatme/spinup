use super::*;
use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_manifest::lock::Lock;

fn generate(root: &std::path::Path, values: &[&str], force: bool) -> Result<Output> {
    let mut command = clap::Command::new("model").arg(
        clap::Arg::new("force")
            .long("force")
            .action(clap::ArgAction::SetTrue),
    );
    command = (MODEL.args)(command);
    let flags = if force {
        vec!["model", "--force"]
    } else {
        vec!["model"]
    };
    let matches = command.try_get_matches_from(flags)?;
    run(&Input {
        root,
        lock: &Lock::new(),
        args: &values
            .iter()
            .map(|value| (*value).into())
            .collect::<Vec<_>>(),
        matches: &matches,
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
}

#[test]
fn model_has_queries_and_migrations_without_handlers() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["tag", "name:string"], false).unwrap();
    assert!(output.plan.get("api/src/models/tag.rs").is_some());
    assert!(output.plan.get("api/src/handlers/tags.rs").is_none());
    assert!(
        output
            .plan
            .paths()
            .iter()
            .any(|p| p.ends_with("_create_tags.up.sql"))
    );
    let modules = output.plan.get("api/src/models/mod.rs").unwrap();
    assert!(
        String::from_utf8(modules.data)
            .unwrap()
            .contains("pub mod tag;")
    );
    assert_eq!(
        output.post_steps,
        [
            PostStep::Migrate,
            PostStep::SqlxPrepare,
            PostStep::Build,
            PostStep::OpenApiEmit
        ]
    );
    assert_eq!(MODEL.name, "model");
}

#[test]
fn model_refusals_have_retry_hints() {
    let root = tempfile::tempdir().unwrap();
    for values in [
        vec![],
        vec!["tag"],
        vec!["tag", "name"],
        vec!["tag", "owner:ref:member"],
    ] {
        let error = generate(root.path(), &values, false).unwrap_err();
        assert!(error.to_string().contains("\nTry:"), "{error}");
    }
}

#[test]
fn force_reuses_the_migration_pair() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["tag", "name:string"], false).unwrap();
    spinup_vfs::apply::apply(root.path(), &output.plan, Default::default()).unwrap();
    let error = generate(root.path(), &["tag", "name:string"], false).unwrap_err();
    assert!(error.to_string().contains("already exists"));
    assert!(error.to_string().contains("\nTry:"));
    let forced = generate(root.path(), &["tag", "name:string"], true).unwrap();
    assert_eq!(forced.plan, output.plan);
    assert!(
        spinup_gen::contract::GENERATORS
            .iter()
            .any(|g| g.name == "model")
    );
}
