//! The web half of the embed mode must match the Go golden tree (Go
//! `internal/templates/golden_web_embed_test.go`). `rust-filter.toml` drops the `.go` files:
//! R1 does not run go/format.

use anyhow::Result;

#[test]
fn golden_web_embed() -> Result<()> {
    let Some(files) = spinup_testutil::golden::run("web-embed")? else {
        return Ok(());
    };
    for name in [
        "web/src/hooks.server.ts",
        "web/src/routes/+layout.server.ts",
        "web/src/routes/healthz/+server.ts",
        "deploy/Dockerfile.web",
    ] {
        assert!(files.get(name).is_none(), "embed app includes {name}");
    }
    for name in [
        "deploy/compose.yml",
        "deploy/Caddyfile",
        "deploy/environments/production.toml",
        "deploy/environments/staging.toml",
    ] {
        let data = crate::golden_deploy::read(&files, name)?;
        for absent in [
            "WEB_IMAGE",
            "upstream.caddy web",
            "[resources.web]",
            "web = 2",
        ] {
            assert!(!data.contains(absent), "{name} includes {absent}");
        }
    }
    Ok(())
}
