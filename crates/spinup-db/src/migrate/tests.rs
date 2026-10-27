use super::*;
use spinup_exec::{
    fake::Fake,
    runner::{ExecError, Output, Sinks},
};
use std::sync::Mutex;

pub(crate) fn project() -> tempfile::TempDir {
    let dir = tempfile::tempdir().unwrap();
    std::fs::create_dir_all(dir.path().join("api/migrations")).unwrap();
    std::fs::write(dir.path().join("spinup.toml"), "[app]\nname='todo'\n").unwrap();
    std::fs::write(
        dir.path().join("api/.env"),
        "DATABASE_URL=postgres://dotenv-secret\n",
    )
    .unwrap();
    dir
}

fn metadata(fake: &Fake, root: &Path) {
    fake.expect("cargo", ["metadata", "--format-version", "1", "--locked"])
        .returns(serde_json::json!({"packages":[{"name":"spinup-kit", "manifest_path":root.join("kit/Cargo.toml")}]}).to_string(), "", 0);
}

struct ConfigRunner {
    fake: Fake,
    configs: Mutex<Vec<String>>,
}
impl Runner for ConfigRunner {
    fn run(&self, cmd: &Cmd, cancel: &Cancellation, sinks: Sinks<'_>) -> Result<Output, ExecError> {
        if let Some(index) = cmd.args.iter().position(|a| a == "--config") {
            self.configs
                .lock()
                .unwrap()
                .push(std::fs::read_to_string(&cmd.args[index + 1]).unwrap());
        }
        self.fake.run(cmd, cancel, sinks)
    }
}

#[test]
fn up_and_status_run_kit_first_with_a_live_separate_config() {
    for sub in [Sub::Up, Sub::Status] {
        let dir = project();
        let runner = ConfigRunner {
            fake: Fake::new(),
            configs: Mutex::new(Vec::new()),
        };
        metadata(&runner.fake, dir.path());
        let verb = if sub == Sub::Up { "run" } else { "info" };
        runner
            .fake
            .expect("sqlx", ["migrate", verb, "--source"])
            .returns("kit output\n", "", 0);
        runner
            .fake
            .expect("sqlx", ["migrate", verb, "--source", "migrations"])
            .returns("app output\n", "", 0);
        let lookup =
            |key: &str| (key == "DATABASE_URL").then(|| "postgres://injected-secret".into());
        let mut out = Vec::new();
        run(
            &mut Options {
                dir: dir.path().into(),
                lookup: Some(&lookup),
                out: Some(&mut out),
                ..Options::default()
            },
            sub,
            &runner,
            &Cancellation::new(),
        )
        .unwrap();
        runner.fake.assert_done().unwrap();
        assert_eq!(out, b"kit output\napp output\n");
        assert_eq!(
            runner.configs.into_inner().unwrap(),
            ["[migrate]\ntable-name = \"_spinup_kit_migrations\"\n"]
        );
        let calls = runner.fake.calls();
        assert_eq!(
            calls[1].args[3],
            dir.path().join("kit/migrations").to_string_lossy()
        );
        assert!(!Path::new(&calls[1].args[5]).exists());
        for cmd in &calls {
            assert_eq!(cmd.dir.as_deref(), Some(dir.path().join("api").as_path()));
            assert!(!cmd.args.iter().any(|arg| arg == "--offline"));
            assert!(!cmd.to_string().contains("secret"));
        }
        for cmd in &calls[1..] {
            if sub == Sub::Status {
                assert!(cmd.env.contains(&("NO_COLOR".into(), "1".into())));
            }
            assert!(
                cmd.env
                    .contains(&("DATABASE_URL".into(), "postgres://injected-secret".into()))
            );
            assert_eq!(cmd.redact, ["postgres://injected-secret"]);
        }
    }
}

#[test]
fn dry_run_prints_two_lines_and_runs_nothing() {
    let dir = project();
    let fake = Fake::new();
    let mut out = Vec::new();
    up(
        &mut Options {
            dir: dir.path().into(),
            dry_run: true,
            out: Some(&mut out),
            ..Options::default()
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "sqlx migrate run --source '<kit>/migrations' --config '<tmp>/kit-migrations.toml'\nsqlx migrate run --source migrations\n"
    );
    assert!(fake.calls().is_empty());
}

#[test]
fn down_requires_acknowledgement_and_only_reverts_app() {
    let dir = project();
    let fake = Fake::new();
    let mut o = Options {
        dir: dir.path().into(),
        ..Options::default()
    };
    assert!(run(&mut o, Sub::Down, &fake, &Cancellation::new()).is_err());
    assert!(fake.calls().is_empty());
    o.i_know = true;
    fake.expect("sqlx", ["migrate", "revert", "--source", "migrations"]);
    run(&mut o, Sub::Down, &fake, &Cancellation::new()).unwrap();
    fake.assert_done().unwrap();
}

#[test]
fn failed_kit_migration_stops_before_app_and_redacts_secrets() {
    let dir = project();
    let fake = Fake::new();
    metadata(&fake, dir.path());
    fake.expect("sqlx", ["migrate", "run"])
        .returns("", "failure postgres://dotenv-secret", 7);
    let mut err = Vec::new();
    let error = up(
        &mut Options {
            dir: dir.path().into(),
            lookup: Some(&|_| None),
            err_out: Some(&mut err),
            ..Options::default()
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap_err();
    assert_eq!(spinup_ui::error::parts(&error).code, 7);
    assert!(!String::from_utf8(err).unwrap().contains("dotenv-secret"));
    assert_eq!(fake.calls().len(), 2);
    fake.assert_done().unwrap();
}

#[test]
fn missing_sqlx_or_config_feature_has_the_install_hint() {
    for missing in [true, false] {
        let dir = project();
        let fake = Fake::new();
        metadata(&fake, dir.path());
        let next = fake.expect("sqlx", ["migrate", "run"]);
        if missing {
            next.fail(ExecError::NotFound {
                name: "sqlx".into(),
            });
        } else {
            next.returns("", "unexpected argument '--config' found", 2);
        }
        let error = up(
            &mut Options {
                dir: dir.path().into(),
                lookup: Some(&|_| None),
                ..Options::default()
            },
            &fake,
            &Cancellation::new(),
        )
        .unwrap_err();
        assert_eq!(
            spinup_ui::error::parts(&error).try_hint,
            crate::ops::SQLX_INSTALL_TRY
        );
    }
}

#[test]
fn prepare_migrates_then_checks_all_targets_with_online_sqlx() {
    let dir = project();
    let fake = Fake::new();
    metadata(&fake, dir.path());
    fake.expect("sqlx", ["migrate", "run"]);
    fake.expect("sqlx", ["migrate", "run"]);
    fake.expect(
        "cargo",
        ["sqlx", "prepare", "--workspace", "--", "--all-targets"],
    );
    crate::prepare::run(
        &mut Options {
            dir: dir.path().into(),
            lookup: Some(&|_| None),
            ..Options::default()
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap();
    let calls = fake.calls();
    assert!(
        calls[3]
            .env
            .contains(&("SQLX_OFFLINE".into(), "false".into()))
    );
    assert!(
        calls[3]
            .env
            .contains(&("DATABASE_URL".into(), "postgres://dotenv-secret".into()))
    );
    fake.assert_done().unwrap();
}

#[test]
fn metadata_errors_and_missing_database_have_hints() {
    for bytes in [
        "not json",
        r#"{"packages":[]}"#,
        r#"{"packages":[{"name":"spinup-kit","manifest_path":"Cargo.toml"}]}"#,
    ] {
        let dir = project();
        let fake = Fake::new();
        fake.expect("cargo", ["metadata"]).returns(bytes, "", 0);
        let error = up(
            &mut Options {
                dir: dir.path().into(),
                ..Options::default()
            },
            &fake,
            &Cancellation::new(),
        )
        .unwrap_err();
        assert!(!spinup_ui::error::parts(&error).try_hint.is_empty());
    }
    let dir = project();
    std::fs::remove_file(dir.path().join("api/.env")).unwrap();
    let fake = Fake::new();
    let error = run(
        &mut Options {
            dir: dir.path().into(),
            i_know: true,
            lookup: Some(&|_| None),
            ..Options::default()
        },
        Sub::Down,
        &fake,
        &Cancellation::new(),
    )
    .unwrap_err();
    assert!(
        spinup_ui::error::parts(&error)
            .why
            .contains("DATABASE_URL is not set")
    );
    assert!(fake.calls().is_empty());
}
