//! The Rust CI workflows preserve the action pins and strict shell execution.

use crate::golden_deploy::{read, run_tool, tool_failure, write_tree};
use anyhow::Result;
use spinup_exec::runner::Cmd;
use spinup_vfs::tree::Tree;

#[test]
fn golden_ci() -> Result<()> {
    for (name, web, push, count) in [
        ("api-only", false, false, 3),
        ("full", true, false, 6),
        ("deploy-on-push", true, true, 6),
    ] {
        let Some(tree) = spinup_testutil::golden::run(&format!("ci/{name}"))? else {
            continue;
        };
        assert_eq!(tree.iter().count(), count);
        check_workflows(&tree)?;
        let ci = read(&tree, ".github/workflows/ci.yml")?;
        assert_eq!(ci.contains("\n  web:\n"), web);
        for want in [
            "cargo fmt --check",
            "cargo clippy --locked --all-targets -- -D warnings",
            "cargo test --locked",
            "--features postgres,rustls,sqlx-toml",
            "cargo sqlx prepare --check --workspace -- --all-targets",
            "SQLX_OFFLINE=false",
            "cargo run --locked -- migrate up",
            "cargo run --locked -- openapi",
            "diff -u openapi/bundle.gen.yaml",
            "hashFiles('api/Cargo.lock', 'api/rust-toolchain.toml')",
            "api/target",
        ] {
            assert!(ci.contains(want), "CI lacks {want}");
        }
        let sqlx = &spinup_stack::load::embedded().cargo_tools["sqlx-cli"];
        assert!(ci.contains(&format!("cargo install sqlx-cli --version {sqlx}")));
        for workflow in ["ci", "upgrade-check"] {
            let text = read(&tree, &format!(".github/workflows/{workflow}.yml"))?;
            assert!(text.contains("rustup toolchain install"));
            assert!(text.contains("api/rust-toolchain.toml"));
            assert!(!text.contains("setup-go") && !text.contains("go.mod"));
        }
        if tree.get(".github/workflows/deploy.yml").is_some() {
            let deploy = read(&tree, ".github/workflows/deploy.yml")?;
            assert_eq!(deploy.contains("\n  push:\n"), push);
            assert!(deploy.contains("rustup toolchain install"));
            assert!(deploy.contains("api/rust-toolchain.toml"));
            assert!(!deploy.contains("setup-go"));
        }
    }
    Ok(())
}

fn check_workflows(tree: &Tree) -> Result<()> {
    let root = tempfile::tempdir()?;
    write_tree(tree, root.path())?;
    let mut workflows = Vec::new();
    for (path, _) in tree.iter() {
        let text = read(tree, path)?;
        let doc: yaml_serde::Value = yaml_serde::from_str(&text)?;
        check_steps(&doc);
        if path.starts_with(".github/workflows/") {
            assert_eq!(doc["permissions"]["contents"].as_str(), Some("read"));
            assert_eq!(doc["permissions"].as_mapping().unwrap().len(), 1);
            for job in doc["jobs"].as_mapping().unwrap().values() {
                assert!(job["timeout-minutes"].as_u64().unwrap() > 0);
            }
            workflows.push(path.to_owned());
        }
    }
    let output = run_tool(Cmd::new("actionlint").args(workflows).dir(root.path()))
        .map_err(|error| anyhow::anyhow!("actionlint: {}", tool_failure(&error)))?;
    assert!(output.stdout.is_empty() && output.stderr.is_empty());
    Ok(())
}

fn check_steps(value: &yaml_serde::Value) {
    match value {
        yaml_serde::Value::Mapping(map) => {
            for (key, value) in map {
                if key.as_str() == Some("uses") {
                    let action = value.as_str().unwrap();
                    if !action.starts_with("./") {
                        let (name, sha) = action.rsplit_once('@').unwrap();
                        assert_eq!(sha.len(), 40, "unpinned action {action}");
                        assert!(
                            sha.bytes()
                                .all(|c| c.is_ascii_hexdigit() && !c.is_ascii_uppercase())
                        );
                        let stack = spinup_stack::load::embedded();
                        let pin = stack.action(name).unwrap();
                        assert_eq!(sha, pin.sha);
                    }
                }
                if key.as_str() == Some("run")
                    && let Some(script) = value.as_str()
                {
                    assert!(script.starts_with("set -euo pipefail\n"));
                }
                check_steps(value);
            }
        }
        yaml_serde::Value::Sequence(items) => {
            for item in items {
                check_steps(item);
            }
        }
        _ => {}
    }
}

#[test]
fn golden_ci_web_scaffold() -> Result<()> {
    for name in ["auth", "no-auth"] {
        let Some(tree) = spinup_testutil::golden::run(&format!("ci/web-scaffold/{name}"))? else {
            continue;
        };
        let vite = read(&tree, "web/vite.config.js")?;
        assert!(vite.contains("exclude: ['e2e/**', ...configDefaults.exclude]"));
        let smoke = read(&tree, "web/e2e/smoke.spec.ts")?;
        if name == "auth" {
            assert!(smoke.contains("banner.getByTestId('user-email')"));
            assert!(
                smoke.contains("page.getByRole('menuitem', { name: 'Sign out', exact: true })")
            );
        }
    }
    Ok(())
}
