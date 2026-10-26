use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_gen::contract::{Input, Output};
use spinup_manifest::lock::Lock;

fn generate(root: &std::path::Path, args: &[&str]) -> anyhow::Result<Output> {
    let generator = spinup_gen::contract::GENERATORS
        .iter()
        .find(|g| g.name == "job")
        .expect("the generator must be registered");
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "jobs".into(), "mail".into()];
    (generator.run)(&Input {
        root,
        lock: &lock,
        args: &args.iter().map(|s| (*s).into()).collect::<Vec<_>>(),
        matches: &clap::Command::new("job").get_matches_from(["job"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
}

#[test]
fn registers_and_plans_without_writing() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["SendDigest"]).unwrap();
    assert!(output.plan.get("api/src/jobs/send_digest.rs").is_some());
    assert_eq!(std::fs::read_dir(root.path()).unwrap().count(), 0);
}

#[test]
fn names_keep_go_case_rules_and_reject_reserved_names() {
    for value in ["send_digest", "send-digest", "SendDigest", "sendDigest"] {
        let name = super::parse_name(value).unwrap();
        assert_eq!(name.snake, "send_digest");
        assert_eq!(name.pascal, "SendDigest");
    }
    assert_eq!(
        super::parse_name("process_payments").unwrap().snake,
        "process_payments"
    );
    for value in [
        "",
        "2fast",
        "../escape",
        "send digest",
        "register",
        "Deps",
        "mod",
        "self",
        "async",
        "load_test",
        "routes_gen",
        "éclair",
        "sync_linux",
        "sync_arm64",
        "sync_linux_arm64",
    ] {
        let error = super::parse_name(value).unwrap_err().to_string();
        assert!(error.contains("\nTry:"), "{error}");
    }
    assert!(super::parse_name(&"a".repeat(51)).is_err());
}

fn fixture(root: &std::path::Path, names: &[&str]) {
    let mut plan = spinup_vfs::plan::Plan::new();
    for name in names {
        plan.add(spinup_vfs::plan::File {
            path: format!("api/src/jobs/{name}"),
            data: b"not parsed as Rust".to_vec(),
            ..Default::default()
        })
        .unwrap();
    }
    spinup_vfs::apply::apply(root, &plan, Default::default()).unwrap();
}

#[test]
fn scan_uses_files_and_keeps_registration_sorted() {
    let root = tempfile::tempdir().unwrap();
    fixture(
        root.path(),
        &[
            "zebra.rs",
            "archive.rs",
            "mod.rs",
            "fake_tests.rs",
            "archive_test.rs",
            "README.md",
        ],
    );
    let output = generate(root.path(), &["send_digest"]).unwrap();
    let modules = String::from_utf8(output.plan.get(super::REGISTER_FILE).unwrap().data).unwrap();
    assert!(
        modules.contains("pub mod archive;\npub mod send_digest;\npub mod zebra;"),
        "{modules}"
    );
    assert!(modules.contains("    archive::register(worker, pool)?;\n    send_digest::register(worker, pool)?;\n    zebra::register(worker, pool)?;"));
    assert!(!modules.contains("fake"));
    assert_eq!(
        output.plan.get(super::REGISTER_FILE).unwrap().class,
        "managed"
    );
    assert_eq!(
        generate(root.path(), &["sendDigest"]).unwrap().plan,
        output.plan
    );
}

#[test]
fn pascal_collision_uses_file_names() {
    let root = tempfile::tempdir().unwrap();
    fixture(root.path(), &["send_2fa.rs"]);
    let error = generate(root.path(), &["send2fa"]).unwrap_err().to_string();
    assert!(
        error.contains("Send2fa") && error.ends_with("Try: use a different job name"),
        "{error}"
    );
    // Regeneration of the same path remains subject to the runner conflict check.
    generate(root.path(), &["send_2fa"]).unwrap();
}

#[test]
fn zero_jobs_bytes_are_the_pack_contract() {
    let source = super::module_source(&Default::default()).unwrap();
    assert_eq!(
        source,
        "//! The application jobs. spinup owns this file.\n\n/// Registers each application job in name order.\npub fn register(worker: &mut kit::jobs::Worker, pool: &kit::db::PgPool) -> anyhow::Result<()> {\n    let _ = (worker, pool);\n    Ok(())\n}\n"
    );
}

#[test]
fn job_requires_the_module_and_exactly_one_argument() {
    let root = tempfile::tempdir().unwrap();
    for args in [vec![], vec!["a", "b"]] {
        assert!(
            generate(root.path(), &args)
                .unwrap_err()
                .to_string()
                .contains("expected one job name")
        );
    }
    let args = vec!["send_digest".into()];
    let error = (super::JOB.run)(&Input {
        root: root.path(),
        lock: &Lock::new(),
        args: &args,
        matches: &clap::Command::new("job").get_matches_from(["job"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
    .unwrap_err();
    assert!(error.to_string().ends_with("Try: spinup module add jobs"));
}

#[test]
fn generated_rust_compiles_with_the_app_dependencies() {
    use spinup_exec::{
        os_runner::OsRunner,
        runner::{Cmd, Runner},
    };
    use spinup_vfs::plan::{File, Plan};

    let root = tempfile::tempdir().unwrap();
    let repo = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .canonicalize()
        .unwrap();
    let environment = spinup_render::env::environment(Some(spinup_stack::load::embedded()));
    let manifest = std::fs::read_to_string(
        repo.join("crates/spinup-packs/overlay/api/files/api/Cargo.toml.tmpl"),
    )
    .unwrap();
    let context = serde_json::json!({"data": {"app_name": "generator_probe", "modules": ["jobs", "mail"], "frontend_mode": "separate", "Stack": {"Rust": {"Edition": "2024", "Min": "1.99"}}}});
    let mut manifest = environment
        .render_str(&manifest, minijinja::value::Serde(&context))
        .unwrap();
    manifest.push_str(&format!(
        "\n[patch.crates-io]\nspinup-kit = {{ path = {} }}\n",
        serde_json::to_string(&repo.join("crates/spinup-kit").to_string_lossy()).unwrap()
    ));
    let mut fixture = Plan::new();
    let lock =
        std::fs::read(repo.join("crates/spinup-packs/overlay/api/files/api/Cargo.lock")).unwrap();
    for (path, data) in [
        ("api/Cargo.toml", manifest.into_bytes()),
        ("api/Cargo.lock", lock),
        ("api/src/lib.rs", b"pub mod handlers;\npub mod jobs;\npub mod mail;\npub mod models;\npub mod routes_gen;\n".to_vec()),
        ("api/src/mail/mailers.rs", b"pub struct Mailers { pub from: String, pub mailer: kit::mail::Capture }\nimpl Mailers { pub async fn send(&self, message: &kit::mail::Message) -> anyhow::Result<()> { kit::mail::Mailer::send(&self.mailer, message).await } }\n".to_vec()),
        ("api/src/mail/worker.rs", Vec::new()),
    ] {
        fixture.add(File { path: path.into(), data, ..File::default() }).unwrap();
    }
    spinup_vfs::apply::apply(root.path(), &fixture, Default::default()).unwrap();
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "jobs".into(), "mail".into()];
    let fake = Fake::new();
    let cancel = Cancellation::new();
    for (kind, args) in [
        ("handler", vec!["HealthReport"]),
        ("job", vec!["SendDigest"]),
        (
            "mailer",
            vec![
                "receipt",
                "name:string",
                "sent_at:time",
                "due:date?",
                "key:uuid",
                "price:decimal",
                "count:int",
                "payload:json",
            ],
        ),
    ] {
        let generator = spinup_gen::contract::GENERATORS
            .iter()
            .find(|g| g.name == kind)
            .unwrap();
        let matches = clap::Command::new("probe").get_matches_from(["probe"]);
        let args = args.into_iter().map(String::from).collect::<Vec<_>>();
        let mut output = (generator.run)(&Input {
            root: root.path(),
            lock: &lock,
            args: &args,
            matches: &matches,
            runner: &fake,
            cancel: &cancel,
        })
        .unwrap();
        spinup_gen::rustfmt::format(root.path(), &mut output.plan, &OsRunner, &cancel).unwrap();
        spinup_vfs::apply::apply(root.path(), &output.plan, Default::default()).unwrap();
    }
    let command = Cmd::new("cargo")
        .args([
            "check",
            "--offline",
            "--locked",
            "--all-targets",
            "--no-default-features",
            "--features",
            "jobs,mail",
        ])
        .dir(root.path().join("api"))
        .env("SQLX_OFFLINE", "true");
    let output = OsRunner.output(&command, &cancel);
    if let Err(error) = output {
        let stderr = error
            .output()
            .map(|output| String::from_utf8_lossy(&output.stderr).into_owned())
            .unwrap_or_default();
        panic!("generated Rust must compile: {error}\n{stderr}");
    }
}
