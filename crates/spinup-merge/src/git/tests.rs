use super::*;
use crate::contract::{Input, MAX_SIZE, Merged, Merger};
use spinup_exec::fake::Fake;
use spinup_exec::os_runner::OsRunner;
use spinup_exec::runner::{Cancellation, Cmd, ExecError, Output, Runner, Sinks, StopReason};
use std::path::Path;

fn input<'a>(path: &'a str, base: &'a [u8], ours: &'a [u8], theirs: &'a [u8]) -> Input<'a> {
    Input {
        path,
        base,
        ours,
        theirs,
    }
}

#[test]
fn real_git_merges_go_cases() {
    let base = "package demo\n\nfunc main() {\n\tfmt.Println(\"old\")\n}\n";
    let mine = base.replace("old", "mine");
    let new = base.replace("old", "new");
    for (ours, theirs, expected) in [
        (base, base, base),
        (base, new.as_str(), new.as_str()),
        (mine.as_str(), base, mine.as_str()),
        (mine.as_str(), mine.as_str(), mine.as_str()),
    ] {
        let got = new_git(&OsRunner)
            .merge(
                &input(
                    "main.go",
                    base.as_bytes(),
                    ours.as_bytes(),
                    theirs.as_bytes(),
                ),
                &Cancellation::new(),
            )
            .unwrap();
        assert_eq!(
            got,
            Merged {
                data: expected.as_bytes().to_vec(),
                conflicted: false
            }
        );
    }

    for (path, base, ours, theirs, expected) in [
        (
            "api/main.go",
            "package api\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n",
            "package api\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n\nfunc greet() {\n\tfmt.Println(\"hi\")\n}\n",
            "package api\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc main() {\n\tfmt.Println(strings.ToUpper(\"hello\"))\n}\n",
            "package api\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc main() {\n\tfmt.Println(strings.ToUpper(\"hello\"))\n}\n\nfunc greet() {\n\tfmt.Println(\"hi\")\n}\n",
        ),
        (
            "web/package.json",
            "{\n  \"name\": \"web\",\n  \"scripts\": {\n    \"check\": \"svelte-check\"\n  }\n}\n",
            "{\n  \"name\": \"web\",\n  \"private\": true,\n  \"scripts\": {\n    \"check\": \"svelte-check\"\n  }\n}\n",
            "{\n  \"name\": \"web\",\n  \"scripts\": {\n    \"check\": \"svelte-check\",\n    \"build\": \"vite build\"\n  }\n}\n",
            "{\n  \"name\": \"web\",\n  \"private\": true,\n  \"scripts\": {\n    \"check\": \"svelte-check\",\n    \"build\": \"vite build\"\n  }\n}\n",
        ),
        (
            "web/src/App.svelte",
            "<script>\n  let name = 'world';\n</script>\n\n<h1>Hello {name}</h1>\n",
            "<script>\n  let name = 'spinup';\n</script>\n\n<h1>Hello {name}</h1>\n",
            "<script>\n  let name = 'world';\n</script>\n\n<main><h1>Hello {name}</h1></main>\n",
            "<script>\n  let name = 'spinup';\n</script>\n\n<main><h1>Hello {name}</h1></main>\n",
        ),
        (
            "README.md",
            "title\nfirst\nsecond\nthird\nlast\n",
            "title\nours\nsecond\nthird\nlast\n",
            "title\nfirst\nsecond\nthird\ntheirs\n",
            "title\nours\nsecond\nthird\ntheirs\n",
        ),
        ("new.txt", "", "same\n", "same\n", "same\n"),
        (
            "notes.txt",
            "keep\ndelete\nlast\n",
            "keep\ndelete\nlast\n",
            "keep\nlast\n",
            "keep\nlast\n",
        ),
        ("empty.txt", "", "", "", ""),
    ] {
        let got = new_git(&OsRunner)
            .merge(
                &input(path, base.as_bytes(), ours.as_bytes(), theirs.as_bytes()),
                &Cancellation::new(),
            )
            .unwrap();
        assert_eq!(
            got,
            Merged {
                data: expected.as_bytes().to_vec(),
                conflicted: false
            },
            "{path}"
        );
    }
}

#[test]
fn real_git_returns_exact_diff3_conflicts() {
    for (path, base, ours, theirs, expected) in [
        (
            "deploy/config.yml",
            "service:\n  port: 8080\n",
            "service:\n  port: 8081\n",
            "service:\n  port: 9090\n",
            "service:\n<<<<<<< deploy/config.yml (yours)\n  port: 8081\n||||||| deploy/config.yml (base)\n  port: 8080\n=======\n  port: 9090\n>>>>>>> deploy/config.yml (new template)\n",
        ),
        (
            "new.txt",
            "",
            "ours\n",
            "theirs\n",
            "<<<<<<< new.txt (yours)\nours\n||||||| new.txt (base)\n=======\ntheirs\n>>>>>>> new.txt (new template)\n",
        ),
    ] {
        let got = new_git(&OsRunner)
            .merge(
                &input(path, base.as_bytes(), ours.as_bytes(), theirs.as_bytes()),
                &Cancellation::new(),
            )
            .unwrap();
        assert!(got.conflicted);
        assert_eq!(got.data, expected.as_bytes());
    }
}

#[test]
fn rejects_unsafe_input_before_running_git() {
    let fake = Fake::new();
    for (bad, why) in [
        (vec![0], "contains a NUL byte"),
        (vec![b'x'; MAX_SIZE + 1], "is larger than 1048576 bytes"),
    ] {
        for part in 0..3 {
            let mut parts: [&[u8]; 3] = [b""; 3];
            parts[part] = &bad;
            let error = new_git(&fake)
                .merge(
                    &input("a.txt", parts[0], parts[1], parts[2]),
                    &Cancellation::new(),
                )
                .unwrap_err()
                .to_string();
            assert!(error.contains(why), "{error}");
            assert!(error.contains(["base", "ours", "theirs"][part]), "{error}");
            assert!(error.lines().last().unwrap().starts_with("Try:"));
        }
    }
    assert!(fake.calls().is_empty());
}

#[test]
fn returns_conflicts_and_reports_runner_failures() {
    for code in [0, 1, 2, 127, 128, 255, -1] {
        let fake = Fake::new();
        fake.expect("git", ["merge-file"])
            .returns("merged\n", "failure details", code);
        let got = new_git(&fake).merge(&input("a.txt", b"", b"", b""), &Cancellation::new());
        fake.assert_done().unwrap();
        if (0..=127).contains(&code) {
            assert_eq!(
                got.unwrap(),
                Merged {
                    data: b"merged\n".to_vec(),
                    conflicted: code != 0
                }
            );
        } else {
            let error = got.unwrap_err().to_string();
            assert!(
                error.contains("a.txt") && error.contains("failure details"),
                "{error}"
            );
            assert!(error.ends_with("Try: install git 2.44.0 or later and retry"));
        }
    }
    let fake = Fake::new();
    fake.expect("git", ["merge-file"])
        .fail(ExecError::NotFound { name: "git".into() });
    let error = new_git(&fake)
        .merge(&input("a.txt", b"", b"", b""), &Cancellation::new())
        .unwrap_err()
        .to_string();
    assert!(error.contains("was not found"), "{error}");
    assert!(error.ends_with("Try: install git 2.44.0 or later and retry"));
    fake.assert_done().unwrap();
}

struct InspectRunner;

impl Runner for InspectRunner {
    fn run(&self, cmd: &Cmd, _: &Cancellation, _: Sinks<'_>) -> Result<Output, ExecError> {
        assert_eq!(cmd.program, "git");
        assert_eq!(
            &cmd.args[..10],
            [
                "merge-file",
                "-p",
                "--diff3",
                "--diff-algorithm=histogram",
                "-L",
                "api/main.go (yours)",
                "-L",
                "api/main.go (base)",
                "-L",
                "api/main.go (new template)",
            ]
        );
        assert_eq!(cmd.args.len(), 13);
        for (path, name, bytes) in [
            (&cmd.args[10], "ours", b"ours\n".as_slice()),
            (&cmd.args[11], "base", b"base\n".as_slice()),
            (&cmd.args[12], "theirs", b"theirs\n".as_slice()),
        ] {
            assert_eq!(Path::new(path).file_name().unwrap(), name);
            assert_eq!(std::fs::read(path).unwrap(), bytes);
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                assert_eq!(
                    std::fs::metadata(path).unwrap().permissions().mode() & 0o777,
                    0o600
                );
            }
        }
        Ok(Output {
            stdout: b"merged\n".to_vec(),
            ..Output::default()
        })
    }
}

#[test]
fn runs_expected_command_and_removes_temporary_files() {
    struct RecordRunner(Fake);
    impl Runner for RecordRunner {
        fn run(
            &self,
            cmd: &Cmd,
            cancel: &Cancellation,
            sinks: Sinks<'_>,
        ) -> Result<Output, ExecError> {
            InspectRunner.run(cmd, cancel, sinks)?;
            self.0.output(cmd, cancel)
        }
    }
    for code in [0, 1, 128] {
        let runner = RecordRunner(Fake::new());
        runner
            .0
            .expect("git", ["merge-file"])
            .returns("merged\n", "", code);
        let result = new_git(&runner).merge(
            &input("api/main.go", b"base\n", b"ours\n", b"theirs\n"),
            &Cancellation::new(),
        );
        assert_eq!(result.is_ok(), code < 128);
        for path in &runner.0.calls()[0].args[10..] {
            assert!(!Path::new(path).exists(), "{path} remains");
            assert!(!Path::new(path).parent().unwrap().exists());
        }
        runner.0.assert_done().unwrap();
    }
}

#[test]
fn check_git_version_matches_go_cases() {
    for (output, expected) in [
        ("git version 2.56.0\n", Ok("2.56.0")),
        (
            "git version 2.39.5 (Apple Git-154)\n",
            Err("found 2.39.5, but requires 2.44.0"),
        ),
        ("git version 2.44.0.windows.1\n", Ok("2.44.0")),
        (
            "git version 2.43.9\n",
            Err("found 2.43.9, but requires 2.44.0"),
        ),
        ("unknown\n", Err("cannot parse git version")),
        ("git version 2.44.0-rc1\n", Err("cannot parse git version")),
        ("git version 02.44.0\n", Err("invalid numeric identifier")),
        (
            "git version 9999999999999999999999.0.0\n",
            Err("invalid numeric identifier"),
        ),
        ("notice\ngit version 2.44.0\n", Ok("2.44.0")),
    ] {
        let fake = Fake::new();
        fake.expect("git", ["--version"]).returns(output, "", 0);
        let got = check_git_version(&fake, &Cancellation::new());
        match expected {
            Ok(expected) => assert_eq!(got.unwrap(), expected),
            Err(expected) => {
                let error = got.unwrap_err().to_string();
                assert!(error.contains(expected), "{error}");
                assert!(error.ends_with("Try: install git 2.44 or later"));
            }
        }
        fake.assert_done().unwrap();
    }
    let fake = Fake::new();
    fake.expect("git", ["--version"])
        .fail(ExecError::NotFound { name: "git".into() });
    let error = check_git_version(&fake, &Cancellation::new())
        .unwrap_err()
        .to_string();
    assert!(error.contains("check git version:"));
    assert!(error.ends_with("Try: install git 2.44 or later"));
    fake.assert_done().unwrap();
}

#[test]
fn real_git_version_is_supported() {
    assert!(
        !check_git_version(&OsRunner, &Cancellation::new())
            .unwrap()
            .is_empty()
    );
}

#[test]
fn cancellation_is_an_error_even_with_partial_conflict_output() {
    let fake = Fake::new();
    fake.expect("git", ["merge-file"]).fail(ExecError::Stopped {
        cmd: "git merge-file".into(),
        reason: StopReason::Cancelled,
        output: Box::new(Output {
            stdout: b"partial".to_vec(),
            exit_code: 1,
            ..Output::default()
        }),
    });
    let error = new_git(&fake)
        .merge(&input("a.txt", b"", b"", b""), &Cancellation::new())
        .unwrap_err()
        .to_string();
    assert!(error.contains("context canceled"), "{error}");
    fake.assert_done().unwrap();
}

#[test]
fn real_git_accepts_the_size_limit() {
    let mut data = vec![b'x'; MAX_SIZE];
    data[MAX_SIZE - 1] = b'\n';
    let got = new_git(&OsRunner)
        .merge(
            &input("large.txt", &data, &data, &data),
            &Cancellation::new(),
        )
        .unwrap();
    assert!(!got.conflicted);
    assert_eq!(got.data, data);
}

#[test]
fn forwards_cancellation_to_the_os_runner() {
    let cancel = Cancellation::new();
    cancel.cancel();
    let error = new_git(&OsRunner)
        .merge(&input("a.txt", b"", b"", b""), &cancel)
        .unwrap_err()
        .to_string();
    assert!(error.contains("context canceled"), "{error}");
    let error = check_git_version(&OsRunner, &cancel)
        .unwrap_err()
        .to_string();
    assert!(error.contains("context canceled"), "{error}");
}
