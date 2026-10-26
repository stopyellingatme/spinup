//! The Rust web variants cover frontend modes, package managers, and API clients.

use crate::golden_deploy::read;
use anyhow::{Context, Result};

#[test]
fn golden_web_variants() -> Result<()> {
    for mode in ["node", "embed"] {
        for pm in ["pnpm", "bun"] {
            for api in ["openapi-fetch", "heyapi"] {
                check_variant(mode, pm, api)
                    .with_context(|| format!("variant {mode}/{pm}/{api}"))?;
            }
        }
    }
    Ok(())
}

fn check_variant(mode: &str, pm: &str, api: &str) -> Result<()> {
    let Some(tree) = spinup_testutil::golden::run(&format!("web-variants/{mode}-{pm}-{api}"))?
    else {
        return Ok(());
    };
    let (lockfile, absent) = if pm == "bun" {
        ("web/bun.lock", "web/pnpm-lock.yaml")
    } else {
        ("web/pnpm-lock.yaml", "web/bun.lock")
    };
    assert!(tree.get(lockfile).is_some(), "the render has no {lockfile}");
    assert!(tree.get(absent).is_none(), "unexpected lockfile {absent}");
    let bun = spinup_stack::load::embedded()
        .npm
        .get("bun")
        .context("the stack has no npm pin for bun")?;
    for path in [".github/workflows/ci.yml", ".github/workflows/e2e.yml"] {
        let data = read(&tree, path)?;
        assert!(
            pm != "bun"
                || (data.contains(&format!("npm install --global bun@{bun}"))
                    && !data.contains("pnpm")),
            "bun workflow {path}:\n{data}"
        );
    }
    let e2e = read(&tree, ".github/workflows/e2e.yml")?;
    for expected in [
        "api/rust-toolchain.toml",
        "rustup toolchain install",
        "api/Cargo.lock",
        "SQLX_OFFLINE: \"true\"",
        "cargo run --locked -- migrate up",
        "PORT=8080 cargo run --locked)",
    ] {
        assert!(e2e.contains(expected), "e2e workflow lacks {expected:?}");
    }
    for absent in ["setup-go", "GOTOOLCHAIN", " go run", "-tags embedweb"] {
        assert!(!e2e.contains(absent), "e2e workflow includes {absent:?}");
    }
    let client = read(&tree, "web/src/lib/api/client.ts")?;
    assert!(
        api != "heyapi" || client.contains("./client/client.gen"),
        "heyapi wrapper does not import the generated client"
    );
    if mode == "node" && pm == "bun" {
        let data = read(&tree, "deploy/Dockerfile.web")?;
        assert!(
            !data.contains("pnpm") && data.contains("bun.lock"),
            "bun Dockerfile:\n{data}"
        );
    }
    Ok(())
}
