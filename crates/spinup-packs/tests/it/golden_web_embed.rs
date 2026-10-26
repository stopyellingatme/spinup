//! The Rust embed files and the static frontend deployment.

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
    let dockerfile = crate::golden_deploy::read(&files, "deploy/Dockerfile.api")?;
    assert!(dockerfile.contains("COPY --from=web-build /web/build ./webdist"));
    assert!(files.contains("api/build.rs"));
    assert!(files.contains("api/webdist/.gitignore"));
    assert!(!files.contains("api/internal/webdist/.gitignore"));
    Ok(())
}

#[test]
fn golden_web_embed_build_table() -> Result<()> {
    use spinup_exec::runner::Cmd;

    if !spinup_testutil::golden::selected("web-embed") {
        return Ok(());
    }
    let tree = crate::golden_deploy::render(
        &["base", "api"],
        serde_json::json!({"app_name": "todo", "module": "example.com/todo", "frontend_mode": "embed"}),
    )?;
    let temp = tempfile::tempdir()?;
    let app = temp.path().join("app");
    let out = temp.path().join("out");
    std::fs::create_dir_all(&app)?;
    std::fs::create_dir_all(&out)?;
    let script = temp.path().join("build.rs");
    std::fs::write(&script, &tree.get("api/build.rs").unwrap().data)?;
    let binary = temp
        .path()
        .join(format!("webdist-build{}", std::env::consts::EXE_SUFFIX));
    crate::golden_deploy::run_tool(Cmd::new("rustc").args([
        "--edition",
        "2024",
        script.to_str().unwrap(),
        "-o",
        binary.to_str().unwrap(),
    ]))?;
    let command = Cmd::new(binary.to_str().unwrap())
        .env("CARGO_MANIFEST_DIR", app.to_str().unwrap())
        .env("OUT_DIR", out.to_str().unwrap());
    let table = out.join("webdist.rs");
    crate::golden_deploy::run_tool(command.clone())?;
    let empty = std::fs::read(&table)?;
    std::fs::create_dir(app.join("webdist"))?;
    std::fs::write(app.join("webdist/.gitignore"), "*\n")?;
    crate::golden_deploy::run_tool(command.clone())?;
    assert_eq!(
        std::fs::read(&table)?,
        empty,
        "missing and empty assets differ"
    );
    assert_eq!(
        String::from_utf8(empty)?,
        "static WEB_FILES: kit::spa::EmbeddedFiles = &[\n];\n"
    );

    std::fs::create_dir_all(app.join("webdist/_app/immutable"))?;
    std::fs::write(app.join("webdist/_app/immutable/z.js"), "asset")?;
    std::fs::write(app.join("webdist/index.html"), "index")?;
    std::fs::write(app.join("webdist/.secret"), "hidden")?;
    crate::golden_deploy::run_tool(command.clone())?;
    let first = std::fs::read_to_string(&table)?;
    crate::golden_deploy::run_tool(command)?;
    assert_eq!(std::fs::read_to_string(&table)?, first);
    assert!(!first.contains(".secret"));
    assert!(first.find("_app/immutable/z.js") < first.find("index.html"));
    // Compile the table with real assets. This also checks include_bytes paths on Windows.
    let proof = temp.path().join("proof.rs");
    std::fs::write(
        &proof,
        format!(
            "mod kit {{ pub mod spa {{ pub type EmbeddedFiles = &'static [(&'static str, &'static [u8])]; }} }}\n{first}\nfn main() {{ assert_eq!(WEB_FILES.len(), 2); assert_eq!(WEB_FILES[1].1, b\"index\"); }}\n"
        ),
    )?;
    let proof_binary = temp
        .path()
        .join(format!("proof{}", std::env::consts::EXE_SUFFIX));
    crate::golden_deploy::run_tool(Cmd::new("rustc").args([
        "--edition",
        "2024",
        proof.to_str().unwrap(),
        "-o",
        proof_binary.to_str().unwrap(),
    ]))?;
    crate::golden_deploy::run_tool(Cmd::new(proof_binary.to_str().unwrap()))?;
    Ok(())
}
