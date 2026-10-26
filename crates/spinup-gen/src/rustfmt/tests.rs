use super::*;
use spinup_exec::fake::Fake;
use spinup_exec::os_runner::OsRunner;
use spinup_exec::runner::{ExecError, Output, Sinks};
use spinup_vfs::plan::File;

fn plan(files: &[(&str, &str)]) -> Plan {
    Plan {
        files: files
            .iter()
            .map(|(path, source)| File {
                path: (*path).into(),
                data: source.as_bytes().to_vec(),
                class: "managed".into(),
                ..File::default()
            })
            .collect(),
    }
}

#[test]
fn no_rust_files_run_no_tool() {
    let fake = Fake::new();
    let mut plan = plan(&[("web/a.ts", "no change")]);
    assert_eq!(
        format(
            Path::new("missing-root"),
            &mut plan,
            &fake,
            &Cancellation::new()
        )
        .unwrap(),
        None
    );
    assert!(fake.calls().is_empty());
}

struct Inspect<'a> {
    fake: &'a Fake,
    root: &'a Path,
    config: Option<&'a str>,
}

impl Runner for Inspect<'_> {
    fn run(&self, cmd: &Cmd, cancel: &Cancellation, sinks: Sinks<'_>) -> Result<Output, ExecError> {
        if cmd.args.first().map(String::as_str) == Some("--edition") {
            assert_eq!(&cmd.args[..2], ["--edition", "2024"]);
            assert_eq!(
                cmd.dir.as_deref(),
                Some(self.root.canonicalize().unwrap().join("api").as_path())
            );
            let offset = if let Some(config) = self.config {
                assert_eq!(cmd.args[2], "--config-path");
                assert_eq!(
                    Path::new(&cmd.args[3]),
                    self.root.canonicalize().unwrap().join("api").join(config)
                );
                4
            } else {
                2
            };
            let paths: Vec<_> = cmd.args[offset..].iter().map(Path::new).collect();
            assert_eq!(paths.len(), 3);
            let stage = paths[0].ancestors().nth(3).unwrap();
            for placeholder in [
                "api/src/child.rs",
                "api/src/outer/nested.rs",
                "api/src/special_tests.rs",
                "api/src/feature/child.rs",
            ] {
                assert!(stage.join(placeholder).is_file(), "{placeholder} missing");
            }
            assert_eq!(
                std::fs::read_to_string(stage.join("api/src/planned.rs")).unwrap(),
                "pub fn child( ){}\n"
            );
            assert!(!paths.iter().any(|p| p.starts_with(self.root)));
        }
        self.fake.run(cmd, cancel, sinks)
    }
}

#[test]
#[allow(
    clippy::disallowed_methods,
    reason = "test fixtures in temporary directories"
)]
fn stages_every_file_and_missing_modules_and_selects_config() {
    for config in [None, Some("rustfmt.toml"), Some(".rustfmt.toml")] {
        let root = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(root.path().join("api")).unwrap();
        if let Some(config) = config {
            std::fs::write(root.path().join("api").join(config), "").unwrap();
        }
        let fake = Fake::new();
        fake.expect("rustfmt", ["--edition", "2024"])
            .returns("", "", 0);
        fake.expect("rustfmt", ["--version"]).returns(
            "rustfmt 1.9.0-stable (abc 2026-01-01)\n",
            "",
            0,
        );
        let mut plan = plan(&[
            ("api/src/planned.rs", "pub fn child( ){}\n"),
            (
                "api/src/lib.rs",
                "mod child; mod planned; mod outer { mod nested; } #[cfg(test)] #[path = \"special_tests.rs\"] mod tests; mod feature;",
            ),
            ("api/src/feature.rs", "mod child;"),
        ]);
        let version = format(
            root.path(),
            &mut plan,
            &Inspect {
                fake: &fake,
                root: root.path(),
                config,
            },
            &Cancellation::new(),
        )
        .unwrap();
        assert_eq!(
            version.as_deref(),
            Some("rustfmt 1.9.0-stable (abc 2026-01-01)")
        );
        fake.assert_done().unwrap();
        assert!(!root.path().join("api/src").exists());
        let staged = fake.calls()[0].args.last().unwrap().clone();
        assert!(!Path::new(&staged).exists(), "staging directory leaked");
    }
}

#[test]
fn real_rustfmt_formats_planned_files_without_project_modules() {
    let root = tempfile::tempdir().unwrap();
    let mut plan = plan(&[
        (
            "api/src/lib.rs",
            "mod missing;\npub fn root( ){let s=\"mod imaginary;\";}\n",
        ),
        ("api/src/handler.rs", "pub fn hello( ){let n=1;}\n"),
    ]);
    let formatter = format(root.path(), &mut plan, &OsRunner, &Cancellation::new())
        .unwrap()
        .unwrap();
    assert!(formatter.starts_with("rustfmt "));
    assert!(
        String::from_utf8_lossy(&plan.files[1].data)
            .contains("pub fn hello() {\n    let n = 1;\n}")
    );
    assert!(!root.path().join("api").exists());
}

#[test]
fn failures_leave_the_plan_and_project_unchanged() {
    for fail_version in [false, true] {
        let root = tempfile::tempdir().unwrap();
        let fake = Fake::new();
        fake.expect("rustfmt", ["--edition"]).returns(
            "tool stdout",
            if fail_version {
                ""
            } else {
                "invalid Rust source"
            },
            i32::from(!fail_version),
        );
        if fail_version {
            fake.expect("rustfmt", ["--version"])
                .returns("garbage", "", 0);
        }
        let mut plan = plan(&[("api/src/lib.rs", "fn main( ){}")]);
        let original = plan.files[0].data.clone();
        let error = format(root.path(), &mut plan, &fake, &Cancellation::new())
            .unwrap_err()
            .to_string();
        assert!(error.contains("\nTry:"), "{error}");
        if !fail_version {
            assert!(error.contains("invalid Rust source"), "{error}");
        }
        assert_eq!(plan.files[0].data, original);
        assert!(!root.path().join("api").exists());
        fake.assert_done().unwrap();
    }
}

#[test]
fn module_scanner_handles_comments_literals_and_nested_directories() {
    let children = modules::children(
        Path::new("api/src/lib.rs"),
        r####"
        // mod ignored;
        /* /* mod ignored_again; */ mod still_ignored; */
        const S: &str = "mod string;";
        const R: &str = r###"mod raw;"###;
        fn f<'a>() { let c = '{'; let q = '\''; }
        mod actual;
        mod outer { pub mod inner { mod nested; } }
        #[path = "alternate.rs"] mod renamed;
    "####,
    );
    assert_eq!(
        children,
        [
            "api/src/actual.rs",
            "api/src/outer/inner/nested.rs",
            "api/src/alternate.rs"
        ]
        .map(PathBuf::from)
    );
    assert!(safe_child(Path::new("api/src/../../../outside.rs")).is_err());
    assert_eq!(
        safe_child(Path::new("api/src/../shared.rs")).unwrap(),
        Path::new("api/shared.rs")
    );
}
