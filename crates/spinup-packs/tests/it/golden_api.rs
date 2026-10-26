//! The Rust API pack and the database service pins.

use anyhow::Result;
use serde_json::json;

#[test]
fn golden_api() -> Result<()> {
    if !spinup_testutil::golden::selected("api") {
        return Ok(());
    }
    let tree = crate::golden_deploy::render(
        &["base", "api"],
        json!({"app_name": "todo", "module": "example.com/todo"}),
    )?;
    for name in ["api/src/main.rs", "api/src/lib.rs", "api/src/routes.rs"] {
        assert!(tree.contains(name), "missing {name}");
    }
    for (path, _) in tree.iter() {
        assert!(!path.ends_with(".go"), "Go output remains: {path}");
    }
    for name in [
        "api/go.mod",
        "api/go.sum",
        "api/tools.mod",
        "api/tools.sum",
        "api/.air.toml",
        "api/sqlc.yaml",
        "api/oapi-codegen.yaml",
        "api/build.rs",
    ] {
        assert!(
            !tree.contains(name),
            "obsolete or conditional output: {name}"
        );
    }
    let dockerfile = crate::golden_deploy::read(&tree, "deploy/Dockerfile.api")?;
    assert!(!dockerfile.contains("golang:"));
    assert!(!dockerfile.contains("AS web-build"));
    for required in [
        "FROM scratch AS kit",
        "AS chef",
        "AS planner",
        "AS build",
        "/opt/kit",
        "SQLX_OFFLINE=true",
        "cargo chef cook --release --locked",
        "USER nonroot",
        "ENTRYPOINT [\"/api\"]",
    ] {
        assert!(
            dockerfile.contains(required),
            "Docker contract is missing {required}"
        );
    }
    spinup_testutil::golden::run("api")?;
    Ok(())
}

#[test]
fn golden_api_postgres_pins() -> Result<()> {
    if !spinup_testutil::golden::selected("api") {
        return Ok(());
    }
    for version in ["18", "17", "16"] {
        let tree = crate::golden_deploy::render(
            &["base", "api"],
            json!({"app_name": "todo", "module": "example.com/todo", "db_version": version}),
        )?;
        let compose = crate::golden_deploy::read(&tree, "deploy/compose.dev.yml")?;
        let key = if version == "18" {
            "postgres".into()
        } else {
            format!("postgres-{version}")
        };
        let image = spinup_stack::load::embedded().image(&key)?;
        assert!(compose.contains(&format!("image: {}", image.reference())));
        assert!(spinup_doctor::composelint::lint("deploy/compose.dev.yml", &compose)?.is_empty());
    }
    Ok(())
}

#[test]
fn golden_api_feature_wiring_is_rustfmt_stable() -> Result<()> {
    use spinup_exec::runner::{Cmd, Stdin};

    if !spinup_testutil::golden::selected("api") {
        return Ok(());
    }
    for modules in [
        "",
        "auth",
        "auth,mail",
        "jobs",
        "mail",
        "jobs,mail",
        "auth,jobs,mail",
    ] {
        for frontend in ["node", "embed"] {
            let tree = crate::golden_deploy::render(
                &["base", "api"],
                json!({"app_name": "todo", "module": "example.com/todo", "modules": modules, "frontend_mode": frontend, "auth_oidc": true}),
            )?;
            for path in ["api/src/main.rs", "api/src/lib.rs", "api/src/openapi.rs"] {
                let source = &tree.get(path).unwrap().data;
                let mut command = Cmd::new("rustfmt").args([
                    "--edition",
                    "2024",
                    "--config",
                    "skip_children=true",
                    "--emit",
                    "stdout",
                ]);
                command.stdin = Stdin::Bytes(source.to_vec());
                let formatted = crate::golden_deploy::run_tool(command)?;
                let source = String::from_utf8_lossy(source);
                let formatted = String::from_utf8_lossy(&formatted.stdout);
                assert!(
                    source == formatted,
                    "{modules} {frontend} {path}\n{}",
                    similar::TextDiff::from_lines(source.as_ref(), formatted.as_ref())
                        .unified_diff()
                );
            }
        }
    }
    Ok(())
}
