use super::*;
use spinup_exec::fake::Fake;

fn project(mode: &str, manager: &str) -> tempfile::TempDir {
    let dir = tempfile::tempdir().unwrap();
    std::fs::create_dir(dir.path().join("api")).unwrap();
    std::fs::write(dir.path().join("spinup.toml"), "[app]\nname = 'todo'\n").unwrap();
    std::fs::write(
        dir.path().join("api/rust-toolchain.toml"),
        "[toolchain]\nchannel = '1.99.1'\n",
    )
    .unwrap();
    let lock = format!(
        r#"{{"schema":3,"answers":{{"frontend_mode":"{mode}","package_manager":"{manager}"}}}}"#
    );
    std::fs::write(dir.path().join("spinup.lock"), lock).unwrap();
    dir
}

fn artifact(dir: &Path, fake: &Fake) {
    let file = dir.join("shared-target/release/api");
    std::fs::create_dir_all(file.parent().unwrap()).unwrap();
    std::fs::write(&file, "binary").unwrap();
    let json = serde_json::json!({"reason":"compiler-artifact","target":{"name":"api","kind":["bin"]},"executable":file});
    fake.expect(
        "cargo",
        [
            "build",
            "--release",
            "--locked",
            "--message-format=json-render-diagnostics",
        ],
    )
    .returns(format!("{json}\n"), "", 0);
}

#[test]
fn local_binary_and_environment() {
    let dir = project("node", "bun");
    let fake = Fake::new();
    artifact(dir.path(), &fake);
    let mut options = Options {
        dir: dir.path().join("api"),
        tag: "v1".into(),
        git_sha: "abc".into(),
        ..Options::default()
    };
    local(&Cancellation::new(), &mut options, &fake).unwrap();
    assert_eq!(
        std::fs::read(dir.path().join("api/bin/api")).unwrap(),
        b"binary"
    );
    let calls = fake.calls();
    assert_eq!(calls.len(), 1);
    assert_eq!(calls[0].dir, Some(dir.path().join("api")));
    assert_eq!(
        calls[0].env,
        [
            ("SPINUP_APP_VERSION".into(), "v1".into()),
            ("SPINUP_APP_GIT_SHA".into(), "abc".into()),
            ("SPINUP_APP_RUST".into(), "1.99.1".into())
        ]
    );
    fake.assert_done().unwrap();
}

#[test]
fn embed_and_dry_run() {
    for manager in ["bun", "pnpm"] {
        for dry in [false, true] {
            let dir = project("embed", manager);
            let dest = dir.path().join("api/webdist");
            std::fs::create_dir_all(&dest).unwrap();
            std::fs::write(dest.join("old"), "stale").unwrap();
            std::fs::create_dir_all(dir.path().join("web/build/assets")).unwrap();
            std::fs::write(dir.path().join("web/build/assets/main.js"), "fresh").unwrap();
            let fake = Fake::new();
            if !dry {
                let tool = Tool::new(manager, "");
                for cmd in [tool.install(true, false), tool.run("build", &[])] {
                    let cmd = tool.in_web(dir.path(), cmd);
                    fake.expect(manager, cmd.args);
                }
                artifact(dir.path(), &fake);
            }
            let mut out = Vec::new();
            let mut options = Options {
                dir: dir.path().into(),
                target: "all".into(),
                dry_run: dry,
                out: Some(&mut out),
                ..Options::default()
            };
            local(&Cancellation::new(), &mut options, &fake).unwrap();
            if dry {
                assert!(dest.join("old").exists());
                assert!(!dir.path().join("api/bin").exists());
                assert!(fake.calls().is_empty());
                let text = String::from_utf8(out).unwrap();
                assert!(text.contains("cargo build --release --locked"));
                assert!(text.contains("api/webdist"));
            } else {
                assert!(!dest.join("old").exists());
                assert_eq!(
                    std::fs::read(dest.join("assets/main.js")).unwrap(),
                    b"fresh"
                );
                assert_eq!(fake.calls().len(), 3);
            }
            fake.assert_done().unwrap();
        }
    }
}

#[test]
fn invalid_inputs_run_nothing() {
    let dir = project("node", "pnpm");
    for target in ["web", "unknown"] {
        let fake = Fake::new();
        let mut o = Options {
            dir: dir.path().into(),
            target: target.into(),
            ..Options::default()
        };
        assert!(
            local(&Cancellation::new(), &mut o, &fake)
                .unwrap_err()
                .to_string()
                .contains("Try:")
        );
        assert!(fake.calls().is_empty());
    }
    std::fs::write(dir.path().join("spinup.lock"), "invalid JSON").unwrap();
    let fake = Fake::new();
    let mut o = Options {
        dir: dir.path().into(),
        ..Options::default()
    };
    let text = local(&Cancellation::new(), &mut o, &fake)
        .unwrap_err()
        .to_string();
    assert!(text.contains("spinup.lock"));
    assert!(text.contains("Try:"));
    assert!(fake.calls().is_empty());
}

#[test]
fn failures_and_sqlx_hint() {
    for (stdout, stderr, code, expected) in [
        ("", "build failed", 1, "build failed"),
        (SQLX_NO_CACHE, "", 1, SQLX_PREPARE_TRY),
        ("", SQLX_NO_CACHE, 1, SQLX_PREPARE_TRY),
        ("", "", 0, "executable"),
        ("not json", "", 0, "JSON"),
    ] {
        let dir = project("node", "pnpm");
        let fake = Fake::new();
        fake.expect("cargo", ["build"])
            .returns(stdout, stderr, code);
        let mut o = Options {
            dir: dir.path().into(),
            ..Options::default()
        };
        let error = local(&Cancellation::new(), &mut o, &fake)
            .unwrap_err()
            .to_string();
        assert!(error.contains(expected), "{error}");
        assert!(error.contains("Try:"));
        fake.assert_done().unwrap();
    }
}

#[test]
fn missing_tools_web_failures_and_missing_assets_stop_build() {
    for failure in ["tool", "install", "web", "assets"] {
        let dir = project("embed", "pnpm");
        let fake = Fake::new();
        let install = fake.expect("pnpm", ["--dir", "web", "install"]);
        match failure {
            "tool" => {
                install.fail(ExecError::NotFound {
                    name: "pnpm".into(),
                });
            }
            "install" => {
                install.returns("", "install failed", 1);
            }
            _ => {
                let build = fake.expect("pnpm", ["--dir", "web", "build"]);
                if failure == "web" {
                    build.returns("", "web failed", 1);
                }
            }
        }
        let mut o = Options {
            dir: dir.path().into(),
            ..Options::default()
        };
        let error = local(&Cancellation::new(), &mut o, &fake)
            .unwrap_err()
            .to_string();
        assert!(
            error.contains(if failure == "tool" {
                "Try: install pnpm"
            } else {
                "Try:"
            }),
            "{error}"
        );
        assert!(fake.calls().iter().all(|c| c.program != "cargo"));
        fake.assert_done().unwrap();
    }
}

#[test]
fn absent_lock_diagnostics_and_executable_permissions() {
    let dir = project("node", "pnpm");
    std::fs::remove_file(dir.path().join("spinup.lock")).unwrap();
    let binary = dir.path().join("artifact");
    std::fs::write(&binary, "binary").unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&binary, std::fs::Permissions::from_mode(0o755)).unwrap();
    }
    let fake = Fake::new();
    let messages = [
        serde_json::json!({"reason":"compiler-message","message":{"rendered":"a compiler warning\n"}}),
        serde_json::json!({"reason":"compiler-artifact","target":{"name":"dependency","kind":["bin"]},"executable":"wrong"}),
        serde_json::json!({"reason":"compiler-artifact","target":{"name":"api","kind":["bin"]},"executable":binary}),
        serde_json::json!({"reason":"build-finished","success":true}),
    ].map(|value| value.to_string()).join("\n");
    fake.expect("cargo", ["build"])
        .returns(messages, "cargo diagnostic\n", 0);
    let mut diagnostics = Vec::new();
    let mut o = Options {
        dir: dir.path().into(),
        err_out: Some(&mut diagnostics),
        ..Options::default()
    };
    local(&Cancellation::new(), &mut o, &fake).unwrap();
    assert_eq!(
        String::from_utf8(diagnostics).unwrap(),
        "cargo diagnostic\na compiler warning\n"
    );
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            std::fs::metadata(dir.path().join("api/bin/api"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o755
        );
    }
    fake.assert_done().unwrap();
}
