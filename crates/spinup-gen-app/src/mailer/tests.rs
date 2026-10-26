use spinup_exec::{fake::Fake, runner::Cancellation};
use spinup_gen::contract::{Input, Output};
use spinup_manifest::lock::Lock;

fn generate(root: &std::path::Path, args: &[&str]) -> anyhow::Result<Output> {
    let generator = spinup_gen::contract::GENERATORS
        .iter()
        .find(|g| g.name == "mailer")
        .expect("the generator must be registered");
    let mut lock = Lock::new();
    lock.packs = vec!["api".into(), "jobs".into(), "mail".into()];
    (generator.run)(&Input {
        root,
        lock: &lock,
        args: &args.iter().map(|s| (*s).into()).collect::<Vec<_>>(),
        matches: &clap::Command::new("mailer").get_matches_from(["mailer"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
}

#[test]
fn registers_and_plans_without_writing() {
    let root = tempfile::tempdir().unwrap();
    let output = generate(root.path(), &["welcome", "name:string"]).unwrap();
    assert!(output.plan.get("api/src/mail/welcome.rs").is_some());
    assert_eq!(std::fs::read_dir(root.path()).unwrap().count(), 0);
}

#[test]
fn typed_mailer_and_seed_templates_are_deterministic() {
    let root = tempfile::tempdir().unwrap();
    let args = [
        "receipt",
        "sent_at:time",
        "due:date?",
        "key:uuid",
        "price:decimal",
        "count:int",
        "payload:json",
    ];
    let output = generate(root.path(), &args).unwrap();
    assert_eq!(output.plan.files.len(), 5);
    assert_eq!(generate(root.path(), &args).unwrap().plan, output.plan);
    let source =
        String::from_utf8(output.plan.get("api/src/mail/receipt.rs").unwrap().data).unwrap();
    for text in [
        "pub struct ReceiptData",
        "pub sent_at: time::OffsetDateTime",
        "pub due: Option<time::Date>",
        "pub key: uuid::Uuid",
        "pub price: String",
        "pub payload: serde_json::Value",
        "pub async fn send_receipt",
        "kit::mail::render",
        "self.send(&message).await",
        "../../templates/mail/receipt.txt.tmpl",
    ] {
        assert!(source.contains(text), "missing {text}:\n{source}");
    }
    for file in &output.plan.files {
        assert_eq!(
            file.class,
            if file.path.ends_with(".rs") {
                "managed"
            } else {
                "seed"
            }
        );
    }
    let text = output
        .plan
        .get("api/templates/mail/receipt.txt.tmpl")
        .unwrap();
    assert!(
        String::from_utf8(text.data)
            .unwrap()
            .contains("{{ sent_at }}")
    );
}

#[test]
fn mailer_refusals_include_retry_hints() {
    let root = tempfile::tempdir().unwrap();
    for args in [
        vec![],
        vec!["../escape"],
        vec!["mailers"],
        vec!["password_reset"],
        vec!["welcome", "name:string", "name:int"],
        vec!["welcome", "send_2fa:string", "send2fa:int"],
    ] {
        let error = generate(root.path(), &args).unwrap_err().to_string();
        assert!(error.contains("\nTry:"), "{error}");
    }
    let error = (super::MAILER.run)(&Input {
        root: root.path(),
        lock: &Lock::new(),
        args: &["welcome".into()],
        matches: &clap::Command::new("mailer").get_matches_from(["mailer"]),
        runner: &Fake::new(),
        cancel: &Cancellation::new(),
    })
    .unwrap_err();
    assert!(error.to_string().ends_with("Try: spinup module add mail"));
}

#[test]
fn mailer_scans_files_and_rejects_pascal_collisions() {
    let root = tempfile::tempdir().unwrap();
    let mut fixture = spinup_vfs::plan::Plan::new();
    for name in [
        "mailers",
        "worker",
        "send_2fa",
        "archive",
        "fake_tests",
        "mod",
    ] {
        fixture
            .add(spinup_vfs::plan::File {
                path: format!("api/src/mail/{name}.rs"),
                data: b"not parsed as Rust".to_vec(),
                ..Default::default()
            })
            .unwrap();
    }
    spinup_vfs::apply::apply(root.path(), &fixture, Default::default()).unwrap();
    let output = generate(root.path(), &["welcome"]).unwrap();
    let modules = String::from_utf8(output.plan.get(super::MODULE_FILE).unwrap().data).unwrap();
    assert!(
        modules.contains("pub mod archive;\npub mod send_2fa;\npub mod welcome;"),
        "{modules}"
    );
    assert!(!modules.contains("fake_tests"));
    let error = generate(root.path(), &["send2fa"]).unwrap_err().to_string();
    assert!(
        error.contains("Send2fa") && error.ends_with("Try: use a different mailer name"),
        "{error}"
    );
}

#[test]
fn zero_mailer_bytes_are_the_pack_contract() {
    assert_eq!(
        super::module_source(&Default::default()).unwrap(),
        "//! The application mailers. spinup owns this file.\n\nmod mailers;\n#[cfg(feature = \"jobs\")]\nmod worker;\n\npub use mailers::Mailers;\n"
    );
}
