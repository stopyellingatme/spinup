use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_gen::contract::{Input, Output};
use spinup_manifest::lock::Lock;

fn generate(root: &std::path::Path, args: &[&str]) -> anyhow::Result<Output> {
    let generator = spinup_gen::contract::GENERATORS
        .iter()
        .find(|g| g.name == "handler")
        .expect("the generator must be registered");
    let mut fixture = spinup_vfs::plan::Plan::new();
    fixture.add(spinup_vfs::plan::File {
        path: "api/Cargo.toml".into(),
        data: b"[package]\nname = \"api\"\n".to_vec(),
        ..Default::default()
    })?;
    spinup_vfs::apply::apply(root, &fixture, Default::default())?;
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "jobs".into(), "mail".into()];
    (generator.run)(&Input {
        root,
        lock: &lock,
        args: &args.iter().map(|s| (*s).into()).collect::<Vec<_>>(),
        matches: &clap::Command::new("handler").get_matches_from(["handler"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
}

#[test]
fn registers_and_plans_without_writing() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["HealthReport"]).unwrap();
    assert!(
        output
            .plan
            .get("api/src/handlers/health_report.rs")
            .is_some()
    );
    assert!(!root.path().join("api/src").exists());
}

#[test]
fn handler_routes_and_tests_use_the_shared_router() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["HealthReport"]).unwrap();
    assert_eq!(
        output.plan.paths(),
        [
            "api/src/handlers/health_report.rs",
            "api/src/handlers/health_report_tests.rs",
            "api/src/handlers/mod.rs",
            "api/src/models/mod.rs",
            "api/src/routes_gen.rs"
        ]
    );
    let handler = String::from_utf8(
        output
            .plan
            .get("api/src/handlers/health_report.rs")
            .unwrap()
            .data,
    )
    .unwrap();
    assert!(handler.contains("pub async fn handle() -> Json<Value>"));
    assert!(handler.contains("OpenApiRouter<kit::db::PgPool>"));
    assert!(handler.contains("#[path = \"health_report_tests.rs\"]"));
    let routes = String::from_utf8(output.plan.get("api/src/routes_gen.rs").unwrap().data).unwrap();
    assert!(routes.contains("router.merge(handlers::health_report::routes())"));
    let test = String::from_utf8(
        output
            .plan
            .get("api/src/handlers/health_report_tests.rs")
            .unwrap()
            .data,
    )
    .unwrap();
    assert!(test.contains("kit::testkit::http::send("));
    assert!(test.contains("/api/health_report"));
    assert_eq!(
        generate(root.path(), &["health-report"]).unwrap().plan,
        output.plan
    );
}

#[test]
fn handler_refusals_include_retry_hints() {
    let root = tempfile::tempdir().unwrap();
    for args in [
        vec![],
        vec!["a", "b"],
        vec!["9lives"],
        vec!["main"],
        vec!["mod"],
        vec!["../escape"],
        vec!["_ping"],
        vec!["ping_"],
        vec!["ping__pong"],
    ] {
        assert!(
            generate(root.path(), &args)
                .unwrap_err()
                .to_string()
                .contains("\nTry:")
        );
    }
    let input = Input {
        root: root.path(),
        lock: &Lock::new(),
        args: &["ping".into()],
        matches: &clap::Command::new("handler").get_matches_from(["handler"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    };
    let other = tempfile::tempdir().unwrap();
    let error = (super::HANDLER.run)(&Input {
        root: other.path(),
        ..input
    })
    .unwrap_err();
    assert!(error.to_string().contains("api/Cargo.toml"));
}
