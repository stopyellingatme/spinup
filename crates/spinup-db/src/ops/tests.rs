use super::*;
use spinup_exec::{fake::Fake, runner::Stdin};

fn project(config: &str) -> tempfile::TempDir {
    let dir = tempfile::tempdir().unwrap();
    std::fs::create_dir_all(dir.path().join("api/migrations")).unwrap();
    std::fs::write(
        dir.path().join("spinup.toml"),
        format!("[app]\nname='todo'\n{config}"),
    )
    .unwrap();
    std::fs::write(
        dir.path().join("api/.env"),
        "DATABASE_URL=postgres://test\n",
    )
    .unwrap();
    for file in ["00001_init.up.sql", "00002_next.up.sql"] {
        std::fs::write(dir.path().join("api/migrations").join(file), "").unwrap();
    }
    dir
}

#[test]
fn new_writes_both_files_and_dry_run_does_not_write() {
    let dir = project("");
    let mut o = Options {
        dir: dir.path().into(),
        dry_run: true,
        ..Options::default()
    };
    new(&mut o, "add_widgets").unwrap();
    assert!(
        !dir.path()
            .join("api/migrations/00003_add_widgets.up.sql")
            .exists()
    );
    o.dry_run = false;
    new(&mut o, "add_widgets").unwrap();
    for suffix in ["up", "down"] {
        assert!(
            dir.path()
                .join(format!("api/migrations/00003_add_widgets.{suffix}.sql"))
                .is_file()
        );
    }
    assert!(new(&mut o, "add_widgets").is_err());
}

#[test]
fn migration_guards_keep_protected_environment_rules() {
    for (yes, ci, name, allowed) in [
        (false, false, "", false),
        (true, false, "", false),
        (true, true, "", true),
        (false, false, "production", true),
        (true, true, "other", false),
    ] {
        let dir = project("[env.production]\nprotected=true\n");
        let fake = Fake::new();
        let mut o = Options {
            dir: dir.path().into(),
            i_know: true,
            dry_run: true,
            yes,
            ci,
            confirm_name: name.into(),
            ..Options::default()
        };
        assert_eq!(redo(&mut o, &fake, &Cancellation::new()).is_ok(), allowed);
        assert!(fake.calls().is_empty());
    }
}

#[test]
fn redo_restores_former_version_and_to_uses_current_info() {
    let dir = project("");
    let fake = Fake::new();
    fake.expect("sqlx", ["migrate", "info"])
        .returns("1/installed init\n2/pending next\n", "", 0);
    fake.expect("sqlx", ["migrate", "revert", "--source", "migrations"]);
    fake.expect(
        "sqlx",
        [
            "migrate",
            "run",
            "--source",
            "migrations",
            "--target-version",
            "1",
        ],
    );
    let mut o = Options {
        dir: dir.path().into(),
        i_know: true,
        lookup: Some(&|_| None),
        ..Options::default()
    };
    redo(&mut o, &fake, &Cancellation::new()).unwrap();
    fake.expect("sqlx", ["migrate", "info"])
        .returns("1/pending init\n2/pending next\n", "", 0);
    fake.expect(
        "sqlx",
        [
            "migrate",
            "run",
            "--source",
            "migrations",
            "--target-version",
            "1",
        ],
    );
    o.i_know = false;
    to(&mut o, &fake, &Cancellation::new(), "1").unwrap();
    fake.expect("sqlx", ["migrate", "info"])
        .returns("1/installed init\n2/installed next\n", "", 0);
    let error = to(&mut o, &fake, &Cancellation::new(), "1").unwrap_err();
    assert!(
        spinup_ui::error::parts(&error)
            .try_hint
            .ends_with("--i-know")
    );
    o.i_know = true;
    fake.expect("sqlx", ["migrate", "info"])
        .returns("1/installed init\n2/installed next\n", "", 0);
    fake.expect(
        "sqlx",
        [
            "migrate",
            "revert",
            "--source",
            "migrations",
            "--target-version",
            "0",
        ],
    );
    to(&mut o, &fake, &Cancellation::new(), "0").unwrap();
    for version in ["-1", "bad", "9223372036854775808"] {
        assert!(to(&mut o, &fake, &Cancellation::new(), version).is_err());
    }
    assert!(
        fake.calls()
            .iter()
            .all(|cmd| !cmd.args.iter().any(|arg| arg == "--config"))
    );
    fake.assert_done().unwrap();
}

#[test]
fn local_operations_keep_compose_streams_and_atomic_dump() {
    let dir = project("");
    std::fs::create_dir_all(dir.path().join("api/seeds")).unwrap();
    for (name, body) in [
        ("02_b.sql", "second"),
        ("01_a.sql", "first"),
        ("notes.txt", "ignored"),
    ] {
        std::fs::write(dir.path().join("api/seeds").join(name), body).unwrap();
    }
    let archive = dir.path().join("in.dump");
    std::fs::write(&archive, "PGDMP-input").unwrap();
    let fake = Fake::new();
    fake.expect(
        "docker",
        ["compose", "-f", COMPOSE_FILE, "exec", "postgres", "psql"],
    );
    fake.expect(
        "docker",
        [
            "compose",
            "-f",
            COMPOSE_FILE,
            "exec",
            "-T",
            "postgres",
            "pg_dump",
        ],
    )
    .returns("PGDMP-output", "", 0);
    fake.expect(
        "docker",
        [
            "compose",
            "-f",
            COMPOSE_FILE,
            "exec",
            "-T",
            "postgres",
            "pg_restore",
        ],
    );
    fake.expect(
        "docker",
        [
            "compose",
            "-f",
            COMPOSE_FILE,
            "exec",
            "-T",
            "postgres",
            "psql",
        ],
    );
    fake.expect(
        "docker",
        [
            "compose",
            "-f",
            COMPOSE_FILE,
            "exec",
            "-T",
            "postgres",
            "psql",
        ],
    );
    let mut o = Options {
        dir: dir.path().into(),
        yes: true,
        ..Options::default()
    };
    let cancel = Cancellation::new();
    psql(&mut o, &fake, &cancel).unwrap();
    dump(&mut o, &fake, &cancel, "").unwrap();
    restore(&mut o, &fake, &cancel, archive.to_str().unwrap()).unwrap();
    seed(&mut o, &fake, &cancel).unwrap();
    let calls = fake.calls();
    assert!(calls[0].interactive);
    assert_eq!(calls[0].stdin, Stdin::Inherit);
    assert_eq!(calls[2].stdin, Stdin::Bytes(b"PGDMP-input".to_vec()));
    assert_eq!(calls[3].stdin, Stdin::Bytes(b"first".to_vec()));
    assert_eq!(calls[4].stdin, Stdin::Bytes(b"second".to_vec()));
    assert_eq!(
        calls[3].args[7..],
        ["-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1", "-f", "-"]
    );
    assert_eq!(
        std::fs::read(dir.path().join(DEFAULT_DUMP_FILE)).unwrap(),
        b"PGDMP-output"
    );
    fake.expect("docker", ["compose"])
        .returns("partial", "failed", 1);
    assert!(dump(&mut o, &fake, &cancel, "").is_err());
    assert_eq!(
        std::fs::read(dir.path().join(DEFAULT_DUMP_FILE)).unwrap(),
        b"PGDMP-output"
    );
    assert_eq!(
        std::fs::read_dir(dir.path().join(".spinup/dumps"))
            .unwrap()
            .count(),
        1
    );
    fake.assert_done().unwrap();
}

#[test]
fn local_guards_and_prompts_work_without_a_terminal() {
    let dir = project("[env.dev]\nprotected=true\n");
    let fake = Fake::new();
    let mut o = Options {
        dir: dir.path().into(),
        dry_run: true,
        env: "production".into(),
        yes: true,
        ..Options::default()
    };
    assert!(psql(&mut o, &fake, &Cancellation::new()).is_err());
    o.env = "dev".into();
    assert!(seed(&mut o, &fake, &Cancellation::new()).is_err());
    o.env.clear();
    o.yes = false;
    assert!(reset(&mut o, &fake, &Cancellation::new()).is_err());
    for answer in ["y\n", "YES\n", "n\n", ""] {
        let mut input = answer.as_bytes();
        let mut out = Vec::new();
        let result = reset(
            &mut Options {
                dir: dir.path().into(),
                interactive: true,
                dry_run: true,
                input: Some(&mut input),
                out: Some(&mut out),
                ..Options::default()
            },
            &fake,
            &Cancellation::new(),
        );
        assert_eq!(result.is_ok(), matches!(answer, "y\n" | "YES\n"));
        assert!(String::from_utf8(out).unwrap().contains("[y/N]"));
    }
    assert!(fake.calls().is_empty());
}

#[test]
fn reset_runs_compose_then_kit_then_app_and_allows_local_alias() {
    let dir = project("");
    let fake = Fake::new();
    fake.expect(
        "docker",
        [
            "compose",
            "-f",
            COMPOSE_FILE,
            "exec",
            "-T",
            "postgres",
            "psql",
            "-U",
            "app",
            "-d",
            "app",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;",
        ],
    );
    fake.expect("cargo", ["metadata"]).returns(serde_json::json!({"packages":[{"name":"spinup-kit","manifest_path":dir.path().join("kit/Cargo.toml")}]}).to_string(), "", 0);
    fake.expect("sqlx", ["migrate", "run", "--source"]);
    fake.expect("sqlx", ["migrate", "run", "--source", "migrations"]);
    reset(
        &mut Options {
            dir: dir.path().into(),
            env: "local".into(),
            yes: true,
            lookup: Some(&|_| None),
            ..Options::default()
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap();
    fake.assert_done().unwrap();
}

#[test]
fn doctor_reads_sqlx_pending_output_and_keeps_version_policy() {
    for (version, failed, warning) in [
        ("170006", false, false),
        ("140015", false, true),
        ("130000", true, false),
        ("190000", true, false),
    ] {
        let dir = project("");
        let fake = Fake::new();
        fake.expect("docker", ["compose"]).returns(version, "", 0);
        fake.expect("cargo", ["metadata"]).returns(serde_json::json!({"packages":[{"name":"spinup-kit","manifest_path":dir.path().join("kit/Cargo.toml")}]}).to_string(), "", 0);
        fake.expect("sqlx", ["migrate", "info"])
            .returns("1/installed kit init\n", "", 0);
        fake.expect("sqlx", ["migrate", "info"]).returns(
            "1/installed init\n2/pending next\n",
            "",
            0,
        );
        let (mut out, mut err) = (Vec::new(), Vec::new());
        let result = doctor(
            &mut Options {
                dir: dir.path().into(),
                lookup: Some(&|_| None),
                out: Some(&mut out),
                err_out: Some(&mut err),
                ..Options::default()
            },
            &fake,
            &Cancellation::new(),
        );
        assert_eq!(result.is_err(), failed);
        let err = String::from_utf8(err).unwrap();
        assert!(err.contains("1 pending migrations: 2 next"));
        assert_eq!(err.contains("reaches its end of life"), warning);
        fake.assert_done().unwrap();
    }
    for version in ["", "abc", "96", "-10000"] {
        assert!(check_server_version(version).is_err());
    }
}

#[test]
fn malformed_migration_status_never_causes_redo_to_succeed() {
    let dir = project("");
    let fake = Fake::new();
    fake.expect("sqlx", ["migrate", "info"])
        .returns("1/unknown init\n", "", 0);
    let error = redo(
        &mut Options {
            dir: dir.path().into(),
            i_know: true,
            lookup: Some(&|_| None),
            ..Options::default()
        },
        &fake,
        &Cancellation::new(),
    )
    .unwrap_err();
    assert!(
        spinup_ui::error::parts(&error)
            .why
            .contains("did not print migration versions")
    );
    assert_eq!(fake.calls().len(), 1);
    fake.assert_done().unwrap();
}

#[test]
fn docker_errors_keep_go_hints_and_exit_codes() {
    let dir = project("");
    let fake = Fake::new();
    fake.expect("docker", ["compose"])
        .fail(ExecError::NotFound {
            name: "docker".into(),
        });
    let mut o = Options {
        dir: dir.path().into(),
        ..Options::default()
    };
    let error = psql(&mut o, &fake, &Cancellation::new()).unwrap_err();
    let parts = spinup_ui::error::parts(&error);
    assert_eq!(parts.what, "Cannot open psql");
    assert_eq!(parts.why, "the docker command was not found");
    assert_eq!(
        parts.try_hint,
        format!("install Docker, then start the database: {START_DB}")
    );
    fake.expect("docker", ["compose"])
        .returns("", "not running", 4);
    let error = psql(&mut o, &fake, &Cancellation::new()).unwrap_err();
    let parts = spinup_ui::error::parts(&error);
    assert_eq!(parts.code, 4);
    assert!(parts.try_hint.ends_with(START_DB));
    fake.assert_done().unwrap();
}

#[test]
fn to_zero_with_nothing_applied_is_a_no_op() {
    let dir = project("");
    let fake = Fake::new();
    fake.expect("sqlx", ["migrate", "info"])
        .returns("1/pending init\n2/pending next\n", "", 0);
    let mut out = Vec::new();
    {
        let mut o = Options {
            dir: dir.path().into(),
            i_know: true,
            out: Some(&mut out),
            lookup: Some(&|_| None),
            ..Options::default()
        };
        to(&mut o, &fake, &Cancellation::new(), "0").unwrap();
        // Without --i-know it is still a no-op: nothing rolls back.
        o.i_know = false;
        fake.expect("sqlx", ["migrate", "info"])
            .returns("1/pending init\n2/pending next\n", "", 0);
        to(&mut o, &fake, &Cancellation::new(), "0").unwrap();
    }
    fake.assert_done().unwrap();
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "Nothing to roll back: no app migration is applied\n".repeat(2)
    );

    // A project with no migration file needs no info query.
    let dir = project("");
    for file in ["00001_init.up.sql", "00002_next.up.sql"] {
        std::fs::remove_file(dir.path().join("api/migrations").join(file)).unwrap();
    }
    let mut out = Vec::new();
    let mut o = Options {
        dir: dir.path().into(),
        out: Some(&mut out),
        lookup: Some(&|_| None),
        ..Options::default()
    };
    to(&mut o, &fake, &Cancellation::new(), "0").unwrap();
    drop(o);
    assert!(fake.calls().len() == 2, "{:?}", fake.calls());
    assert_eq!(
        String::from_utf8(out).unwrap(),
        "Nothing to roll back: no app migration is applied\n"
    );
}

/// A database of its own on the server of `DATABASE_URL`, dropped at the end.
struct OwnDatabase {
    admin: String,
    name: String,
    url: String,
}

impl OwnDatabase {
    fn create(admin: &str, label: &str) -> Self {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let name = format!("spinup_db_{label}_{}_{nanos}", std::process::id());
        let (base, query) = admin
            .split_once('?')
            .map_or((admin, String::new()), |(b, q)| (b, format!("?{q}")));
        let (server, _) = base.rsplit_once('/').expect("DATABASE_URL has a path");
        let url = format!("{server}/{name}{query}");
        let db = Self {
            admin: admin.into(),
            name,
            url,
        };
        run_psql(admin, &format!("CREATE DATABASE {}", db.name));
        db
    }
}

fn run_psql(url: &str, sql: &str) {
    let cmd = Cmd::new("psql").args([url, "-q", "-v", "ON_ERROR_STOP=1", "-c", sql]);
    spinup_exec::os_runner::OsRunner
        .run(&cmd, &Cancellation::new(), Sinks::none())
        .unwrap_or_else(|error| panic!("psql {sql:?}: {error}"));
}

impl Drop for OwnDatabase {
    fn drop(&mut self) {
        let sql = format!("DROP DATABASE IF EXISTS {} WITH (FORCE)", self.name);
        let cmd = Cmd::new("psql").args([self.admin.as_str(), "-q", "-c", sql.as_str()]);
        let _ = spinup_exec::os_runner::OsRunner.run(&cmd, &Cancellation::new(), Sinks::none());
    }
}

#[test]
fn pg_to_zero_with_nothing_applied_is_a_no_op() {
    let Ok(admin) = std::env::var("DATABASE_URL") else {
        return;
    };
    let db = OwnDatabase::create(&admin, "to_zero");
    let dir = project("");
    let migrations = dir.path().join("api/migrations");
    for file in ["00001_init.up.sql", "00002_next.up.sql"] {
        std::fs::remove_file(migrations.join(file)).unwrap();
    }
    for (file, sql) in [
        (
            "00001_init.up.sql",
            "CREATE TABLE widgets (id int PRIMARY KEY);\n",
        ),
        ("00001_init.down.sql", "DROP TABLE widgets;\n"),
    ] {
        std::fs::write(migrations.join(file), sql).unwrap();
    }
    let url = db.url.clone();
    let lookup = move |key: &str| (key == "DATABASE_URL").then(|| url.clone());
    let runner = spinup_exec::os_runner::OsRunner;
    let cancel = Cancellation::new();
    let run = |version: &str| -> String {
        let mut out = Vec::new();
        let mut err = Vec::new();
        let mut o = Options {
            dir: dir.path().into(),
            i_know: true,
            out: Some(&mut out),
            err_out: Some(&mut err),
            lookup: Some(&lookup),
            ..Options::default()
        };
        let result = to(&mut o, &runner, &cancel, version);
        drop(o);
        let err = String::from_utf8_lossy(&err).into_owned();
        result.unwrap_or_else(|error| panic!("db to {version}: {error:#}\n{err}"));
        String::from_utf8(out).unwrap()
    };
    // Nothing is applied: version 0 is a no-op that says so.
    assert_eq!(
        run("0"),
        "Nothing to roll back: no app migration is applied\n"
    );
    // Apply, roll everything back, and ask again.
    run("1");
    run_psql(&db.url, "SELECT 1 FROM widgets");
    run("0");
    assert_eq!(
        run("0"),
        "Nothing to roll back: no app migration is applied\n"
    );
}
